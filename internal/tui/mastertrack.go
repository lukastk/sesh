package tui

// MASTER TRACKING — the persistent sidebar's cursor follows the cockpit.
//
// The sidebar drives the cockpit (selection-follow, Enter), but until now nothing drove
// the sidebar: `WithMasterCursor` resolved the master window's current thread ONCE, at
// startup, and never again. So every cockpit-side move — prefix+, / prefix+. , prefix+L,
// the mmt-* pickers, a command that creates a thread and jumps to it — switched the
// thread pane while the sidebar's `>` stayed wherever it was, which reads as the sidebar
// having lost track of you (Lukas, 2026-08-28).
//
// THE RULE THAT MAKES THIS SAFE: act on a CHANGE in what the cockpit is showing, never
// on "the cockpit disagrees with the cursor". The two are not the same, and the
// difference is the whole design:
//
//   - Following a change is what the user asked for: the cockpit moved, so the cursor
//     moves with it.
//   - "Disagrees with the cursor" would FIGHT the user. Arrowing the sidebar onto a row
//     the follow policy deliberately skips (a headless thread — a preview must never
//     revive) leaves the cockpit where it was, so a disagreement-driven tracker would
//     yank the cursor straight back on the next tick and browsing would be impossible.
//
// So lastMasterThread records what the cockpit was LAST OBSERVED showing, and only a
// resolve that differs from it moves anything. That also makes the sidebar's own
// follows self-cancelling: the cursor is already on the thread it navved to, so the
// resulting change resolves to a no-op move.
//
// CADENCE is a cheap local file stat plus a rare authoritative resolve. Every tick reads
// the nav bell (tmux.NavBellPath — a few bytes, no fork, no daemon call, no network);
// only a rung bell or an expired backstop countdown spends a real MarkerClientCurrent
// resolve, which costs two tmux calls on the target machine and, when the active master
// window is a REMOTE machine, a mesh round trip. That is what buys immediacy for the
// cockpit's own keys without putting a per-second cross-machine call on a process that
// runs all day.

import (
	"context"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	// masterTrackInterval is the bell-check cadence. It bounds how long the cursor can
	// lag a cockpit keypress, and costs one small file read.
	masterTrackInterval = 250 * time.Millisecond
	// masterTrackBackstopTicks is how many bell-checks pass before a resolve happens
	// anyway (12 x 250ms = 3s). It catches cockpit moves sesh never saw and so could
	// not ring for — a native prefix+n window switch, a pane selected by hand.
	masterTrackBackstopTicks = 12
)

// masterTrackTickMsg drives the bell check.
type masterTrackTickMsg struct{}

// masterCursorMsg carries a resolve of what the active master window is showing.
// ok=false means NO INFORMATION (no cockpit context, or the resolve failed) and must be
// ignored entirely — recording it as "the cockpit shows nothing" would make the next
// successful resolve look like a change and jump the cursor for no reason.
type masterCursorMsg struct {
	id string
	ok bool
}

// WithMasterTracking turns on cursor tracking against the nav bell at bellPath. Only
// meaningful in sidebar mode with a follow resolver — the resolver is what names the
// machine whose master window to read, and it is called LIVE because the traveling
// sidebar is swapped between windows under the same process.
func (m Model) WithMasterTracking(bellPath string) Model {
	m.masterTracking = true
	m.masterTrackBell = bellPath
	m.masterTrackCountdown = masterTrackBackstopTicks
	return m
}

// masterTrackTick schedules the next bell check.
func masterTrackTick() tea.Cmd {
	return tea.Tick(masterTrackInterval, func(time.Time) tea.Msg { return masterTrackTickMsg{} })
}

// masterTrackDue decides — purely, so the cadence is a truth table rather than a clock
// — whether this tick should spend an authoritative resolve, and returns the countdown
// to carry into the next tick. A rung bell resolves immediately AND restarts the
// backstop (the bell just gave us a fresh reading, so the backstop need not double up).
func masterTrackDue(bell, lastBell string, countdown int) (due bool, next int) {
	if bell != lastBell {
		return true, masterTrackBackstopTicks
	}
	if countdown <= 1 {
		return true, masterTrackBackstopTicks
	}
	return false, countdown - 1
}

// applyMasterCursor moves the cursor to the thread the cockpit is now showing. Reports
// whether anything changed (for tests and the debug log).
//
// A thread the CURRENT VIEW does not contain — one that is on hold, or archived while
// the sidebar sits on `active`, or excluded by an active filter — leaves the cursor
// exactly where it is (Lukas, 2026-08-28). Deliberately NOT the goto-uuid behaviour of
// escalating to a view that would show it: that is right for a command the user just
// typed, and wrong for an ambient tracker, which would silently retitle the list under
// someone who is reading it. Nor is a pending preselect armed: it would land minutes
// later, whenever the row happened to become visible.
func (m *Model) applyMasterCursor(msg masterCursorMsg) bool {
	if !msg.ok || msg.id == m.lastMasterThread {
		return false // no information, or the cockpit has not moved
	}
	m.lastMasterThread = msg.id
	if msg.id == "" {
		return false // the master window is on a plain-shell pane / has no client
	}
	if !m.positionCursorOn(msg.id) {
		return false // not in this view — leave the cursor alone
	}
	m.ensureCursorVisible()
	// The cockpit is ALREADY on this thread, so the selection must not be followed
	// back to it (the same bookkeeping navDoneMsg does after an Enter).
	m.lastFollowedID = msg.id
	return true
}

// resolveMasterTrack reads what the sidebar's CURRENT master window is showing, off the
// main loop. The machine comes from the live follow resolver, not a value pinned at
// construction: the traveling sidebar is swapped between master windows by a tmux hook,
// so the window it sits in — and therefore the machine to ask — changes underneath it.
func (m Model) resolveMasterTrack() tea.Cmd {
	c, origin, resolve := m.client, m.machine, m.followResolver
	return func() tea.Msg {
		if resolve == nil || c == nil {
			return masterCursorMsg{}
		}
		machine := resolve()
		if machine == "" {
			return masterCursorMsg{} // no readable cockpit context — no information
		}
		if machine == origin {
			machine = "" // local: the daemon reads its own marker
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, tid, _, err := c.TmuxMasterCurrent(ctx, origin, machine)
		if err != nil {
			return masterCursorMsg{} // a failed resolve is not "the cockpit shows nothing"
		}
		return masterCursorMsg{id: tid, ok: true}
	}
}

// readNavBell returns the current bell value, "" when it is absent or unreadable. "" is
// neither an error nor information: a cockpit that has never navved has nothing to
// report, and the tracker compares values rather than interpreting them. The path is
// passed in (tmux.NavBellPath, resolved by the caller) so the TUI keeps its deliberate
// independence from the tmux package.
func readNavBell(path string) string {
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// MasterTrackTick returns the tracking tick message, so a conformance claim can drive
// the tracker deterministically instead of sleeping through its real timer. That the
// timer is WIRED is covered separately (Init batches it in — see the unit tests); this
// seam only removes the wall-clock wait from the end-to-end proof.
func MasterTrackTick() tea.Msg { return masterTrackTickMsg{} }
