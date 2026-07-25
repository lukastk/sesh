// Package tui is the sesh live grid — a Bubble Tea app over the daemon's
// HTTP+JSON surface. It is a THIN renderer + action dispatcher: it owns no domain
// logic, and its ONLY source of state is the api.http-json client. By rule it
// imports internal/client and internal/api but never internal/store or daemon
// internals (enforced by a test), so "the grid renders real daemon state" and
// "actions really act" are testable claims, not vibes.
package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/client"
)

// refreshInterval is the poll cadence (poll-first, no SSE).
const refreshInterval = 3 * time.Second

// postActionReconcileDelay schedules ONE fast follow-up reconcile after a
// successful action. The immediate post-action fetch races the maintainer's
// snapshot republish (it ticks every ~300ms and the mesh/self view is read from
// THAT published snapshot, not live from the DB), so a structural change with no
// optimistic patch — notably `reparent` (incl. reparent-to-root on an empty
// prompt) — would otherwise not show until the next 3s poll. 500ms reliably
// outlasts one maintainer tick, so the change appears promptly.
const postActionReconcileDelay = 500 * time.Millisecond

// Model is the TUI state. Its rows come only from the daemon grid.
// View indexes into the model's view list: the three built-ins followed by
// the config-defined custom views ([[tui.views]]). Tab cycles; the current
// view's name is rendered in the title.
type View int

const (
	ViewActive   View = iota // (non-archived OR archived-but-headful) AND not on hold (the default)
	ViewHold                 // non-archived AND on hold (parked threads)
	ViewArchived             // archived only
	ViewAll                  // everything (archived + on-hold included)
	viewBuiltins             // = number of built-ins; custom views follow
)

// customView is one compiled [[tui.views]] entry.
type customView struct {
	name string
	pred Predicate
}

func (m Model) viewName() string { return m.viewNameAt(int(m.view)) }

// viewNameAt names the i-th view (built-ins then [[tui.views]] customs).
func (m Model) viewNameAt(i int) string {
	switch View(i) {
	case ViewActive:
		return "active"
	case ViewHold:
		return "on hold"
	case ViewArchived:
		return "archived"
	case ViewAll:
		return "all"
	}
	if c := i - int(viewBuiltins); c >= 0 && c < len(m.customViews) {
		return m.customViews[c].name
	}
	return "?"
}

func (m Model) viewCount() int { return int(viewBuiltins) + len(m.customViews) }

// builtinViewAdmits reports whether a row belongs in one of the built-in views.
// (Custom [[tui.views]] use their own predicate instead.) The default `active` view
// hides on-hold threads and hides archived threads UNLESS they are still headful (a
// live pane) — so an archived thread stays visible while its agent is running and
// drops out only once it goes headless. `on hold` shows the parked ones.
func builtinViewAdmits(view View, row api.ThreadRow) bool {
	switch view {
	case ViewActive:
		return (!row.Archived || row.Head == api.Headful) && !row.OnHold
	case ViewHold:
		return !row.Archived && row.OnHold
	case ViewArchived:
		return row.Archived
	case ViewAll:
		return true
	}
	return false
}

// WithViews installs the compiled custom views (see CompileViews).
func (m Model) WithViews(views []customView) Model {
	m.customViews = views
	return m
}

// ViewSpec is one [[tui.views]] entry as the caller hands it over.
type ViewSpec struct{ Name, Filter string }

// CompileViews compiles [[tui.views]] specs (loud on a broken name/filter).
func CompileViews(specs []ViewSpec) ([]customView, error) {
	out := make([]customView, 0, len(specs))
	for i, sp := range specs {
		if sp.Name == "" || sp.Filter == "" {
			return nil, fmt.Errorf("[tui.views] entry %d: name and filter are both required", i+1)
		}
		pred, err := CompilePredicate(sp.Filter)
		if err != nil {
			return nil, fmt.Errorf("[tui.views] %q: %w", sp.Name, err)
		}
		out = append(out, customView{name: sp.Name, pred: pred})
	}
	return out, nil
}

// promptKind says what the line-prompt input will do on submit.
type promptKind int

const (
	promptNone       promptKind = iota
	promptRename                // rename the selected thread
	promptTag                   // add a tag to the selected thread
	promptReparent              // set the selected thread's parent (paste a uuid; empty = root)
	promptHold                  // hold the selected thread until a date (YYYY-MM-DD; empty = clear)
	promptNewVirtual            // name a NEW virtual grouping thread (empty = cancel)
	promptNewDivider            // label a NEW divider (empty = an unlabeled divider; Esc cancels)
)

// confirmKind says which destructive action a y/n confirmation popup gates.
type confirmKind int

const (
	confirmNone   confirmKind = iota
	confirmDelete             // `d` — drop the record (archive is instant + undoable since H54)
)

type Model struct {
	client      *client.Client
	allMachines bool
	view        View
	showID      bool // `i` toggles an ID column (tid8) — the only id surface in the TUI
	// sidebar (`tui --sidebar`): persistent-pane mode — nav hands focus to the
	// sibling pane instead of quitting. See WithSidebar.
	sidebar bool
	// followResolver resolves — AT FOLLOW TIME — the machine whose work server
	// the sidebar's SIBLING pane currently shows (the cockpit window's machine).
	// When set, MOVING the selection follows: after the cursor rests
	// (followDebounce) the sibling pane navs to the selected thread while focus
	// STAYS in the sidebar — Enter is what commits focus. Follow never revives
	// (live headful threads only) and never switches master windows (the
	// resolver's machine only); nil = follow disabled, Enter-only. It is a
	// FUNCTION, not a cached value, because the TRAVELING sidebar is swapped
	// between master windows by a tmux hook — the sibling machine changes under
	// the same process, and a swap of same-sized panes raises no resize event
	// to re-cache on. A static spawner ($SESH_TUI_MASTER_MACHINE) uses a
	// constant resolver; the cockpit uses a live window-name read.
	followResolver func() string
	// followInFlight: a follow nav is currently running. Selection moves while
	// one runs don't queue navs — the completion handler re-arms for wherever
	// the cursor is THEN (fire-immediately + coalesce: single moves preview at
	// nav cost with no debounce; held arrows degrade to previewing the rows the
	// nav keeps catching up to). lastFollowedID dedups so landing on the
	// already-shown thread re-navs nothing.
	followInFlight bool
	lastFollowedID string
	// hideOffline (default true; `o` toggles, [tui] show_offline sets the default):
	// hide the last-known threads of a mesh machine that is currently unreachable.
	// Their owner can't be reached, so every action on them would hang on the routing
	// timeout (see requiresReachableOwner) and fail — hiding them by default keeps the
	// grid to threads you can actually act on. The OFFLINE footer line still shows the
	// machine exists (and how many threads are hidden), so nothing silently vanishes.
	hideOffline bool

	// Line-prompt input mode (rename / tag-add). While prompting, ordinary keys
	// edit the input; Enter submits, Esc cancels. promptCursor is the insertion
	// point within promptInput (0..len) so ←/→ can edit in place.
	prompting    promptKind
	promptInput  []rune
	promptCursor int
	promptRow    api.ThreadRow // the row the prompt acts on (captured at open)

	// preselectID: when set, the first successful fetch moves the cursor to this
	// thread (the `--cursor` start-on-current-thread affordance), then clears it.
	preselectID string

	// masterCursorMachine: when set (the master popup's prefix+s / F12), the TUI resolves
	// the thread the active master window is CURRENTLY showing and jumps the cursor to it.
	// The resolve is kicked CONCURRENTLY with the first fetch from Init (a one-shot, since
	// Init runs once), so the cursor is already correct by the time rows first render
	// instead of starting at the top and visibly jumping a beat later. It runs on that
	// machine's daemon (the work server it owns): a direct daemon-client call when the
	// active window is local (no `sesh` subprocess), routed via a subprocess when remote.
	masterCursorMachine string

	// uuidPopup: `y` shows the selected thread's FULL uuid in a popup; `c` inside
	// it copies to the system clipboard, any other key closes. note is the last
	// one-line status (e.g. "UUID copied"), shown dim under the title.
	uuidPopup bool
	note      string

	// confirming, when non-zero, means a y/n confirmation popup is open for a
	// destructive action (delete / archive); confirmRow is the row it acts on
	// (captured when the popup opened). `y` runs it, anything else cancels.
	confirming confirmKind
	confirmRow api.ThreadRow

	// reordering, when true, means MOVE MODE is active for the manual-ordering block:
	// ↑/↓ (or k/j) reposition the pinned row reorderID within the block, Enter/Esc
	// commit-and-exit. Entered with `m` on a top-level row (auto-pinning it to the top
	// first if it wasn't pinned). See handleReorderKey.
	reordering bool
	reorderID  string

	// actionErr is the last in-app ACTION error (reparent/delete/tag/…). Unlike
	// lastErr (fetch/daemon reachability), it PERSISTS across reconcile fetches so a
	// failed mutation stays on screen — it is cleared only by the next action. (A
	// reconcile fetch silently clearing it was the "no warning on a bad reparent" bug.)
	actionErr error

	// tagPopup: `T` opens a picker over the selected thread's tags; ↑/↓ (or j/k) move,
	// enter removes the highlighted tag (routed to the owner), esc/q closes. tagPopupRow
	// holds the tag list captured at open and is decremented optimistically as tags are
	// removed, so several can be stripped without reopening; the popup closes when the
	// last tag goes.
	tagPopup       bool
	tagPopupRow    api.ThreadRow
	tagPopupCursor int

	// customViews are the compiled [[tui.views]] entries (after the built-ins
	// in the Tab cycle).
	customViews []customView

	// Tree fold state: per-node overrides + the configured default (children
	// start collapsed unless [tui] expand_children / --expand).
	expanded      map[string]bool
	defaultExpand bool

	// Filter state (see filter.go): filtering = the prompt is being edited;
	// filter = the ACTIVE query (persists when applied via Esc); filterCaret =
	// rune index of the text caret; target = what the query matches.
	filtering   bool
	filter      string
	filterCaret int
	target      filterTarget
	// filterChildren: when filtering, whether to include CHILD threads (those with a
	// parent) in the results. Default false — a query searches only top-level threads;
	// ^k in the filter prompt toggles it (children of a tree you're navigating are
	// usually noise when you're searching by name).
	filterChildren bool

	// columns is the visible column set (validated names; see columns.go).
	// userHome powers the CWD column's ~-relative display; cwdLabeler, when set,
	// transforms it further ([[cwd_label]] rules — see WithCwdLabeler).
	columns    []string
	userHome   string
	cwdLabeler func(cwd string) string
	// colColors tints individual columns ([[tui.column_color]] + built-in defaults).
	// Applied to non-selected, non-highlighted cells only (see renderCells).
	colColors map[string]lipgloss.Style
	// glyphColors tints the gutter's attention glyphs ([[tui.glyph_color]] +
	// built-in defaults: busy ▶ / descendant ↓ bright green). Applied to
	// non-selected rows only, when the glyph is in its active state.
	glyphColors map[string]lipgloss.Style
	// maxColWidth caps each column at a maximum render width (the default, set via
	// New / [tui] max_column_widths). The `w` key toggles it per session: off, every
	// column grows to its content so clipped text (a long name/cwd) is fully visible.
	// colMax holds per-column [[tui.column_width]] overrides of the built-in caps.
	maxColWidth bool
	colMax      map[string]int

	// viewPicker: Tab opens a picker POPUP listing every view (built-ins +
	// [[tui.views]]) instead of blind-cycling — navigate with tab/↑/↓ (tab
	// preserves the old tap-tap rhythm: it opens preselecting the NEXT view, so
	// tab+enter ≡ the old single tab), Enter applies, Esc cancels, a mouse
	// click applies directly, the wheel moves the selection.
	viewPicker       bool
	viewPickerCursor int
	// helpPopup: `?` opens a full-screen takeover showing the complete keymap
	// (the bottom line only carries the "? keys" hint); helpOffset scrolls it
	// when the binding list overflows the terminal height.
	helpPopup  bool
	helpOffset int
	// archiveUndo is the U key's LIFO stack of this session's archives.
	archiveUndo []archiveUndoEntry
	// detailsPopup: `I` opens a full-screen takeover showing ALL of the selected
	// thread's fields (the record + live axes); detailsRow is captured when it opens.
	// Any of esc/q/enter closes it. It's read-only — nothing routes or shells out.
	detailsPopup bool
	detailsRow   api.ThreadRow

	// binaryPath + navEnv: how the nav action execs the `sesh tmux nav` primitive
	// (the TUI drives the primitive, it does not re-implement nav). Defaults to the
	// running sesh binary; tests override both.
	binaryPath string
	navEnv     []string

	// machine + tmuxSocket: this client's own identity + work socket, so Enter can use
	// in-client nav (no master) for a LOCAL thread when the TUI is running inside that
	// work socket's tmux. Empty (the test/default) => always use the master nav path.
	machine    string
	tmuxSocket string
	// tmux is this process's $TMUX (captured at construction so behavior is deterministic
	// and testable). "" => not in tmux (Enter attaches); basename == tmuxSocket => on the
	// work socket (Enter switches in place).
	tmux string
	// clientName is the tmux client this TUI is rendered on (resolved by the caller
	// BEFORE the TUI grabs the terminal, while stdin is still the tty — see runTUI).
	// In-client nav passes it as `--client` so the switch targets exactly this client:
	// a ttyless nav subprocess cannot resolve it itself (tmux's ambient "current
	// client" fallback picks an arbitrary client — observed live moving a master
	// window's attach instead of the invoker).
	clientName string

	// pending holds the in-flight optimistic mutations, one entry PER ACTION in
	// keypress order (rename/tag/notify/archive/hold/flag/stop/pin/delete). An
	// entry is recorded — and applied to the rows — at KEYPRESS, so every action
	// renders instantly on every machine; the action's subprocess then confirms
	// or fails it. Per-action identity (rowPatch.seq) is what makes the failure
	// path honest: a failed action reverts EXACTLY its own entry (the refetch
	// restores the server's truth) without disturbing sibling actions' patches
	// on the same thread. Entries are re-applied to matching rows after each
	// fetch and dropped once the server agrees — or at their deadline, LOUDLY,
	// so a mutation that silently didn't take SURFACES rather than sticking.
	pending []*rowPatch
	// patchSeq mints rowPatch.seq (monotonic per session; 0 = no patch).
	patchSeq int

	// tickStarted guards the one-time bootstrap of the poll timer (see meshMsg).
	tickStarted bool

	rows      []api.ThreadRow
	machines  []api.MachineView // per-machine freshness, for the staleness footer
	fetchedAt int64             // unix time the current data was fetched (for staleness age)
	cursor    int
	err       error
	lastErr   error
	width     int
	height    int
	// vOffset is the index of the first row in the vertical viewport; hOffset is the
	// number of leading data columns scrolled past (horizontal pan). Both are 0 when
	// nothing is clipped. Only active when width/height are known (a WindowSizeMsg has
	// arrived) — with them unset the whole grid renders unclipped.
	vOffset int
	hOffset int
	// Mouse-wheel sensitivity ([tui] mouse_scroll_v/h, default 1): how many wheel
	// notches move one row / pan one column. wheelAccV/H accumulate notches between
	// steps so a higher divisor dampens fast trackpad scrolling.
	scrollDivV, scrollDivH int
	wheelAccV, wheelAccH   int
	// Left-click row interaction: a press SELECTS the row; a second press on the SAME
	// row within doubleClickWindow ENTERS it (like the `enter` key). nowFn overrides the
	// clock in tests; nil = time.Now. See mouse.go.
	lastClickID string
	lastClickAt time.Time
	nowFn       func() time.Time

	// attachTarget, when set, means the TUI quit in order to ATTACH the terminal to a
	// thread (Enter from a plain shell, outside tmux). The caller reads PendingAttach
	// after Run() and execs the attach. "" = quit normally. attachThread carries the
	// thread id so the attach lands on the window holding its pane.
	attachTarget string
	attachThread string

	// Tickets view (K): a full-screen takeover listing the selected thread's tickets,
	// drilling into one ticket's fields + actions, editing a field in $EDITOR. See
	// tickets.go. ticketMode == ticketNone means the view is closed.
	ticketMode         ticketMode
	ticketThread       api.ThreadRow // the thread whose tickets are shown
	tickets            []api.Ticket
	ticketCursor       int    // selection in the list
	ticketDetail       int    // selection in the detail item list
	ticketErr          error  // loud ticket-view error (load/action)
	ticketStatusCursor int    // selection in the status picker
	ticketPickQuery    []rune // change-thread picker query (search by name/uuid)
	ticketPickCursor   int
	ticketNewInput     []rune // name buffer while creating a new ticket (n in the list)
	ticketFilter       string // status filter for the list (default "active"; "all" = no filter)
	ticketFilterCursor int    // selection in the Tab status-filter picker
	editor             string // editor for in-TUI field edits (--editor / [tui] editor / $EDITOR)
}

// New builds a model talking to the daemon at socketPath.
func New(socketPath string, allMachines bool) Model {
	bin, err := os.Executable()
	if err != nil {
		bin = "sesh"
	}
	home, _ := os.UserHomeDir()
	return Model{client: client.New(socketPath), allMachines: allMachines, binaryPath: bin,
		tmux: os.Getenv("TMUX"), columns: append([]string(nil), DefaultColumns...), userHome: home,
		scrollDivV: 1, scrollDivH: 1, hideOffline: true, maxColWidth: true}
}

// WithSidebar puts the TUI in SIDEBAR mode (`tui --sidebar`, issue #8): a
// persistent narrow pane beside the cockpit's attach pane. The one behavioural
// difference from the normal grid is that a successful nav does NOT quit — the
// sidebar stays and hands focus to its sibling pane (see navDoneMsg). Everything
// else (views, filter, actions, mouse) is the same TUI.
func (m Model) WithSidebar() Model {
	m.sidebar = true
	return m
}

// WithSidebarFollow enables selection-follow against a FIXED sibling machine
// (a spawner that pins it via $SESH_TUI_MASTER_MACHINE; tests/claims).
func (m Model) WithSidebarFollow(machine string) Model {
	return m.WithSidebarFollowResolver(func() string { return machine })
}

// WithSidebarFollowResolver enables selection-follow with a LIVE sibling-machine
// resolver, called at each follow attempt (the traveling sidebar — see
// followResolver).
func (m Model) WithSidebarFollowResolver(fn func() string) Model {
	m.followResolver = fn
	return m
}

// armFollow fires a selection-follow IMMEDIATELY when the selected row is
// eligible and no follow is already running (sidebar mode with a known sibling
// machine only — nil otherwise, so call sites can return it unconditionally).
// There is deliberately NO debounce: a single move previews at nav cost
// (~tens of ms locally); while a nav is in flight further moves return nil and
// the completion handler (followDoneMsg) calls armFollow again, coalescing a
// held-arrow burst into "preview wherever the cursor is when each nav lands"
// instead of queueing one nav per row. Each USER selection move arms;
// fetch-driven cursor moves (reanchor/preselect) deliberately do not.
func (m *Model) armFollow() tea.Cmd {
	if !m.sidebar || m.followResolver == nil || m.followInFlight {
		return nil
	}
	row, ok := m.followEligible()
	if !ok {
		return nil
	}
	m.followInFlight = true
	return m.followNav(row)
}

// followEligible reports whether the selected row is one the sidebar should
// follow to, applying the model-side follow policy: only a not-already-shown
// thread, only a LIVE headful session (a preview must never revive), and only
// a reachable owner. All skips are deliberate policy no-ops, not errors —
// Enter still takes the full path.
func (m Model) followEligible() (api.ThreadRow, bool) {
	if !m.sidebar || m.followResolver == nil {
		return api.ThreadRow{}, false
	}
	row, ok := m.Selected()
	if !ok || row.ID == m.lastFollowedID {
		return api.ThreadRow{}, false
	}
	if row.Head != api.Headful || row.SessionName == "" {
		return api.ThreadRow{}, false
	}
	if !m.machineReachable(row.Machine) {
		return api.ThreadRow{}, false
	}
	return row, true
}

// followDoneMsg reports a completed follow nav (success or failure). Its
// handler clears the in-flight latch and RE-ARMS — the coalescing half of the
// no-debounce design: if the selection moved while this nav ran, the next
// preview fires immediately for wherever the cursor is now.
type followDoneMsg struct {
	id  string
	err error
}

// followNav navs the cockpit's THREAD pane to row without touching focus, no
// revive (followEligible guarantees a live session). Two paths:
//
//   - FAST PATH — the row lives on the sidebar's CURRENT window's machine AND
//     that is the local machine: the whole nav reduces to the INNER switch on
//     the local work server, done as ONE warm daemon call (client.TmuxNav on
//     the unix socket — no `sesh` subprocess, no master-window select; the
//     client is already viewing this window). This is the "arrow between my
//     own machine's threads" case and lands in ~a tmux switch.
//   - SUBPROCESS PATH — anything else: the full `sesh tmux nav` master path
//     (window select + routed inner switch; never --in-client — an in-client
//     switch would move the whole view away from the sidebar). A row on
//     ANOTHER machine than the current window follows too — the nav switches
//     the master window and the traveling-sidebar hook brings the sidebar
//     along; the "follow" INTENT declared beforehand (declareSidebarIntent)
//     tells the hook to keep focus ON the sidebar so the user keeps arrowing.
//
// An unresolvable sibling machine ("") means no readable cockpit context: skip
// (a follow without a master to drive would only spray errors while arrowing).
func (m Model) followNav(row api.ThreadRow) tea.Cmd {
	bin, env, resolve := m.binaryPath, m.navEnv, m.followResolver
	client, localMachine := m.client, m.machine
	return func() tea.Msg {
		if resolve == nil {
			return followDoneMsg{}
		}
		wm := resolve()
		if wm == "" {
			return followDoneMsg{} // no cockpit context — deliberate no-op
		}
		if wm == row.Machine && row.Machine == localMachine && client != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := client.TmuxNav(ctx, api.NavRequest{Session: row.SessionName, Origin: localMachine, ThreadID: row.ID}); err != nil {
				return followDoneMsg{id: row.ID, err: fmt.Errorf("follow %s: %w", row.SessionName, err)}
			}
			return followDoneMsg{id: row.ID}
		}
		if wm != row.Machine {
			// The nav will switch master windows: tell the hook this is a
			// FOLLOW so it keeps focus on the traveling sidebar.
			declareSidebarIntent("follow")
		}
		target := row.Machine + ":" + row.SessionName
		cmd := exec.Command(bin, "tmux", "nav", "--to", target, "--thread", row.ID)
		cmd.Env = append(os.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			clearSidebarIntent() // the switch didn't happen — don't leave a stale intent
			return followDoneMsg{id: row.ID, err: fmt.Errorf("follow %s: %v: %s", target, err, strings.TrimSpace(string(out)))}
		}
		return followDoneMsg{id: row.ID}
	}
}

// declareSidebarIntent records — as a global option on the sidebar's own tmux
// server ($TMUX-resolved) — WHY the upcoming master window switch is happening:
// "follow" (keep focus on the traveling sidebar after the swap) or "enter"
// (focus the attach pane). The traveling-sidebar hook consumes-and-clears it;
// without an intent (a manual prefix+N switch) the hook leaves focus alone.
// Outside tmux this is a no-op; a failed set is best-effort (the hook then
// just leaves focus where tmux put it — the pre-intent behavior, not wrongness).
func declareSidebarIntent(intent string) {
	if os.Getenv("TMUX") == "" {
		return
	}
	exec.Command("tmux", "set-option", "-g", "@sesh-sidebar-intent", intent).Run() //nolint:errcheck — best-effort, see above
}

// clearSidebarIntent removes a declared-but-unconsumed intent (nav failed: no
// window switch happened, so the next MANUAL switch must not act on it).
func clearSidebarIntent() {
	if os.Getenv("TMUX") == "" {
		return
	}
	exec.Command("tmux", "set-option", "-gu", "@sesh-sidebar-intent").Run() //nolint:errcheck
}

// focusSiblingPane hands the tmux focus from the sidebar's pane to the OTHER pane
// of its window (the attach pane the nav just changed), via the sidebar's own
// inherited $TMUX/$TMUX_PANE (a plain `tmux` exec resolves both). A single-pane
// window (standalone testing) or a client that nav moved to ANOTHER window makes
// this a harmless no-op on our window — the select still leaves the attach pane
// active for the next visit. Only a real exec failure is surfaced.
func focusSiblingPane() tea.Cmd {
	return func() tea.Msg {
		if os.Getenv("TMUX") == "" {
			return nil // not in tmux (unit tests / odd launch): nothing to focus
		}
		cmd := exec.Command("tmux", "select-pane", "-t", ":.+")
		if out, err := cmd.CombinedOutput(); err != nil {
			return actionMsg{err: fmt.Errorf("sidebar focus handoff: %v: %s", err, strings.TrimSpace(string(out)))}
		}
		return nil
	}
}

// WithShowOffline sets whether an OFFLINE mesh machine's stale threads are shown by
// default ([tui] show_offline / --show-offline). Default (false) hides them; the `o`
// key toggles it per-session regardless.
func (m Model) WithShowOffline(show bool) Model {
	m.hideOffline = !show
	return m
}

// WithMouseScroll sets the wheel sensitivity divisors ([tui] mouse_scroll_v/h): how
// many notches move one row / pan one column. Values < 1 clamp to 1.
func (m Model) WithMouseScroll(v, h int) Model {
	if v < 1 {
		v = 1
	}
	if h < 1 {
		h = 1
	}
	m.scrollDivV, m.scrollDivH = v, h
	return m
}

// WithTmux overrides the captured $TMUX value (tests set it to drive the Enter path
// deterministically: "" => attach, a work-socket value => in-client, else master path).
func (m Model) WithTmux(tmux string) Model {
	m.tmux = tmux
	return m
}

// WithExec overrides how the nav action execs sesh (binary path + extra env) —
// used by tests so nav drives a sandbox's tmux/mesh config.
func (m Model) WithExec(binaryPath string, env []string) Model {
	m.binaryPath = binaryPath
	m.navEnv = env
	return m
}

// WithNavEnv sets extra env appended (last-wins) to every nav subprocess's
// environment — used to inject the daemon's SESH_* identity (home, machine,
// tmux/master sockets) so `sesh tmux nav` / `thread resume` resolve the right
// servers even when the TUI was launched without that env (a `zsh -fc` popup).
func (m Model) WithNavEnv(env []string) Model {
	m.navEnv = env
	return m
}

// PendingAttach reports whether the TUI quit to attach the terminal to a thread, and
// if so returns the argv to exec (`sesh tmux nav --to <target> --attach`). The caller
// (runTUI) execs it AFTER the TUI has exited and restored the terminal.
func (m Model) PendingAttach() ([]string, bool) {
	if m.attachTarget == "" {
		return nil, false
	}
	argv := []string{m.binaryPath, "tmux", "nav", "--to", m.attachTarget, "--attach"}
	if m.attachThread != "" {
		argv = append(argv, "--thread", m.attachThread)
	}
	return argv, true
}

// WithClient sets the tmux client this TUI renders on (for in-client nav's --client).
func (m Model) WithClient(name string) Model {
	m.clientName = name
	return m
}

// WithLocal sets this client's own machine + work socket, enabling in-client nav for
// a local thread when the TUI is inside that work socket's tmux.
func (m Model) WithLocal(machine, tmuxSocket string) Model {
	m.machine = machine
	m.tmuxSocket = tmuxSocket
	return m
}

// WithCwdLabeler sets the CWD-column display transform (the compiled
// [[cwd_label]] rules; errors are resolved by the CALLER — a labeler given here
// is total). Unset = ~-relative paths.
func (m Model) WithCwdLabeler(f func(string) string) Model {
	m.cwdLabeler = f
	return m
}

// WithColumnColors sets the per-column colour styles (already validated +
// defaults-merged via ResolveColumnColors). Unset = no per-column tint.
func (m Model) WithColumnColors(c map[string]lipgloss.Style) Model {
	m.colColors = c
	return m
}

// WithGlyphColors sets the gutter-glyph colour styles (already validated +
// defaults-merged via ResolveGlyphColors). Unset = no glyph tint.
func (m Model) WithGlyphColors(c map[string]lipgloss.Style) Model {
	m.glyphColors = c
	return m
}

// WithPreselect makes the first fetch place the cursor on the given thread id.
// If the thread exists but the current view filters it out (e.g. it is archived
// while the default view is `active`), the TUI escalates to the `all` view so the
// preselect can land — see the meshMsg handler.
func (m Model) WithPreselect(threadID string) Model {
	m.preselectID = threadID
	return m
}

// WithMasterCursor makes the TUI asynchronously resolve, then jump to, the thread
// the given machine's master window is currently showing (the master prefix+s
// affordance). machine is the active master window's machine. The resolve happens
// AFTER the first render, so startup is never blocked.
func (m Model) WithMasterCursor(machine string) Model {
	m.masterCursorMachine = machine
	return m
}

// meshMsg carries a freshly-fetched mesh view, already flattened to rows.
type meshMsg struct {
	rows      []api.ThreadRow
	machines  []api.MachineView
	fetchedAt int64
	err       error
	// preselectSeen reports whether a pending preselect target was present in the
	// UNFILTERED mesh (before the current view's filter dropped it). It distinguishes
	// "the thread is hidden by this view" (escalate to ViewAll) from "the thread isn't
	// published yet" (keep waiting) — see the meshMsg handler.
	preselectSeen bool
}

type tickMsg time.Time

// Init kicks off the first fetch. For the master prefix+s / F12 path it ALSO kicks the
// master-cursor resolve CONCURRENTLY (rather than waiting for the first meshMsg), so the
// resolve overlaps startup and the cursor lands on the current thread by the time rows
// first render — no visible jump. (The single poll timer is still started from the first
// meshMsg — see tickStarted. A non-master-cursor Init stays a lone fetch cmd, which the
// conformance harness drives directly via Init()().)
func (m Model) Init() tea.Cmd {
	if m.masterCursorMachine != "" {
		return tea.Batch(m.fetch(), m.resolveMasterCursor())
	}
	return m.fetch()
}

// fetch polls the daemon's merged mesh view (a LOCAL read of the cache — instant,
// offline-capable) and flattens it to sorted rows. Self-only unless --all-machines.
func (m Model) fetch() tea.Cmd {
	c, view, all, preselect, hideOffline := m.client, m.view, m.allMachines, m.preselectID, m.hideOffline
	var pred *Predicate
	if i := int(view - viewBuiltins); i >= 0 && i < len(m.customViews) {
		p := m.customViews[i].pred
		pred = &p
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		mesh, err := c.Mesh(ctx)
		if err != nil {
			return meshMsg{err: err}
		}
		rows, preselectSeen := flattenMeshRows(mesh.Machines, view, pred, all, hideOffline, preselect)
		return meshMsg{rows: rows, machines: mesh.Machines, fetchedAt: time.Now().Unix(), preselectSeen: preselectSeen}
	}
}

// flattenMeshRows flattens a mesh view into the sorted row set the grid renders,
// applying the machine-level and view-level filters. It is a pure function (no client,
// no clock) so the filtering — especially the offline-hide — is unit-testable without a
// daemon. all=false keeps self only; hideOffline drops an unreachable peer's last-known
// threads (self is never dropped). preselectSeen reports whether the preselect id is
// present in the mesh at all (before the view filter) — it drives the ViewAll auto-switch.
func flattenMeshRows(machines []api.MachineView, view View, pred *Predicate, all, hideOffline bool, preselect string) ([]api.ThreadRow, bool) {
	var rows []api.ThreadRow
	var preselectSeen bool
	for _, mv := range machines {
		if !all && !mv.Self {
			continue
		}
		// Hide a disconnected machine's last-known threads (the default): its owner is
		// unreachable, so they can't be entered or mutated. `o` shows them again; the
		// OFFLINE footer keeps them discoverable. Self is never hidden.
		if hideOffline && !mv.Self && !mv.Reachable {
			continue
		}
		for _, t := range mv.Threads {
			row := api.ThreadRow{Thread: t.Thread, Head: t.Head, Busy: t.Busy, Attachment: t.Attachment, TicketsOpen: t.TicketsOpen, TicketName: t.TicketName, TicketNeedsInput: t.TicketNeedsInput, CwdRel: t.CwdRel, OnHold: t.OnHold, OnHoldEffectiveUnix: t.OnHoldEffectiveUnix, StateAuthority: t.StateAuthority}
			if preselect != "" && t.ID == preselect {
				preselectSeen = true // present in the mesh, regardless of the view filter
			}
			if pred != nil {
				// A custom view sees EVERYTHING its predicate admits
				// (archived/on-hold included — `not archived`/`not onhold` is the user's call).
				if !pred.Eval(row) {
					continue
				}
			} else if !builtinViewAdmits(view, row) {
				continue
			}
			rows = append(rows, row)
		}
	}
	// Stable order (machine, then name, then id) so the cursor never jumps —
	// the maintainer's snapshot map iteration is unordered. The ARCHIVED view is
	// the exception: it orders by most-recently-archived first (archived_at DESC),
	// the order a user reaches for when reviewing what they just parked. Ties and
	// pre-37 records (archived_at=0) fall back to the stable order. The tree walk
	// (visibleMatches) inherits this as its sibling/root order.
	sort.Slice(rows, func(i, j int) bool {
		if view == ViewArchived && rows[i].ArchivedAtUnix != rows[j].ArchivedAtUnix {
			return rows[i].ArchivedAtUnix > rows[j].ArchivedAtUnix
		}
		if rows[i].Machine != rows[j].Machine {
			return rows[i].Machine < rows[j].Machine
		}
		if rows[i].Name != rows[j].Name {
			return rows[i].Name < rows[j].Name
		}
		return rows[i].ID < rows[j].ID
	})
	return rows, preselectSeen
}

func tick() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// Update handles messages (satisfies tea.Model). Tests type-assert the returned
// tea.Model back to Model to inspect the result.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ensureCursorVisible() // a resize can shrink the viewport under the cursor
		if m.hOffset > m.maxHOffset() {
			m.hOffset = m.maxHOffset()
		}
		return m, nil
	case tea.MouseMsg:
		// The mouse wheel moves the SELECTION between rows (up/down, like ↑/↓, viewport
		// following — works even when the grid fits the screen, unlike a viewport-only
		// scroll). Horizontal pan (like h/l) comes from a native wheel-left/right OR
		// Shift+vertical-wheel — the reliable cross-terminal path, since many terminals
		// don't emit horizontal wheel events. Sensitivity divisors ([tui] mouse_scroll_*)
		// dampen fast scrolling: a notch acts only every Nth event.
		// While the `?` popup is up, the wheel scrolls the keymap instead.
		if m.helpPopup {
			switch msg.Button {
			case tea.MouseButtonWheelUp:
				if m.helpOffset > 0 {
					m.helpOffset--
				}
			case tea.MouseButtonWheelDown:
				if m.helpOffset < m.helpMaxOffset() {
					m.helpOffset++
				}
			}
			return m, nil
		}
		// While the Tab view picker is up: the wheel moves its selection, a left
		// click on a view line applies it directly (click outside the list is a
		// no-op — the popup stays, esc dismisses).
		if m.viewPicker {
			n := m.viewCount()
			switch msg.Button {
			case tea.MouseButtonWheelUp:
				m.viewPickerCursor = (m.viewPickerCursor - 1 + n) % n
			case tea.MouseButtonWheelDown:
				m.viewPickerCursor = (m.viewPickerCursor + 1) % n
			case tea.MouseButtonLeft:
				if msg.Action == tea.MouseActionPress {
					if i, ok := m.viewPickerRowAtY(msg.Y); ok {
						return m.applyPickedView(i)
					}
				}
			}
			return m, nil
		}
		var wheelFollow tea.Cmd
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			if msg.Shift {
				m.wheelPanH(-1)
			} else {
				m.wheelMoveV(-1)
				wheelFollow = m.armFollow() // sidebar: the wheel moves the selection too
			}
		case tea.MouseButtonWheelDown:
			if msg.Shift {
				m.wheelPanH(1)
			} else {
				m.wheelMoveV(1)
				wheelFollow = m.armFollow()
			}
		case tea.MouseButtonWheelLeft:
			m.wheelPanH(-1)
		case tea.MouseButtonWheelRight:
			m.wheelPanH(1)
		case tea.MouseButtonLeft:
			// A left PRESS selects/enters a row or toggles a fold marker (see mouse.go).
			// Release/motion events are ignored so a drag doesn't fire actions.
			if msg.Action == tea.MouseActionPress {
				return m.handleLeftClick(msg)
			}
		}
		return m, wheelFollow
	case meshMsg:
		if msg.err != nil {
			m.lastErr = msg.err
		} else {
			// Anchor the selection to a SPECIFIC thread, not a row index: capture the
			// selected thread's id BEFORE swapping in the new rows, then move the cursor
			// back onto it below (reanchorCursor). Otherwise a row appearing/disappearing
			// above the cursor — a routine poll / mesh-sync change nobody asked for —
			// would silently shift the selection onto a DIFFERENT thread, the
			// archive/delete-the-wrong-row footgun.
			anchorID := m.selectedID()
			m.lastErr = nil
			m.rows = msg.rows
			m.machines = msg.machines
			m.fetchedAt = msg.fetchedAt
			m.applyPending(true) // reconcile: re-apply optimistic patches, GC satisfied/expired ones
			if m.preselectID != "" && m.positionCursorOn(m.preselectID) {
				m.preselectID = "" // landed — release it so the user can move freely
			} else if m.preselectID != "" && msg.preselectSeen && m.view != ViewAll {
				// Preselect couldn't land but the thread DOES exist in the mesh — it's just
				// hidden by the current view (e.g. an archived thread while the default view
				// is `active`). Escalate to ViewAll (a superset of every view) and refetch so
				// the cursor can land. Guarded on preselectSeen so a not-yet-published thread
				// doesn't trip it, and on view!=ViewAll so we escalate at most once.
				m.view = ViewAll
				return m, m.fetch()
			} else {
				// No explicit preselect jump is in flight — keep the cursor pinned to the
				// thread it was already on so a changed row set doesn't move the selection
				// out from under the user.
				m.reanchorCursor(anchorID)
			}
			m.ensureCursorVisible() // keep the viewport over the (re-anchored) cursor
		}
		// Bootstrap the SINGLE poll timer exactly once (on the first successful
		// fetch). Subsequent meshMsgs — including those from action/reconcile
		// fetches — do NOT re-arm, so the poll rate can't multiply. (The master-cursor
		// resolve is kicked from Init concurrently with this first fetch, not here.)
		if !m.tickStarted {
			m.tickStarted = true
			return m, tick()
		}
		return m, nil
	case preselectMsg:
		// The async master-cursor resolve landed: jump to the thread the master window
		// is showing. Route it through the persistent preselect so that if the row
		// isn't published yet, a later fetch still lands it (expanding a nested child's
		// ancestors). Empty id = nothing to preselect (no master client / plain shell).
		if msg.id != "" {
			if !m.positionCursorOn(msg.id) {
				m.preselectID = msg.id
				// Refetch NOW rather than waiting up to refreshInterval (3s) for the next
				// poll. The immediate fetch carries the preselect, so the meshMsg handler
				// can land it — or, if the thread is hidden by the current view (e.g. it's
				// archived while the view is `active`), escalate to ViewAll — without a
				// visible delay. This makes the master prefix+a path match the instant
				// `--cursor`/SESH_TUI_PANE path (whose preselect is set at construction).
				return m, m.fetch()
			}
		}
		return m, nil
	case tickMsg:
		return m, tea.Batch(m.fetch(), tick())
	case reconcileMsg:
		// A fast follow-up reconcile after a successful action (does NOT re-arm the
		// poll timer — it's a one-shot, scheduled by the actionMsg handler).
		return m, m.fetch()
	case actionMsg:
		m.note = ""
		if msg.err != nil {
			// Action errors live in actionErr, NOT lastErr: the reconcile fetch below
			// clears lastErr on success, which would instantly erase this warning.
			m.actionErr = msg.err
			// REVERT exactly this action's keypress-time optimism (per-action
			// identity): its entry is dropped and the refetch below restores the
			// server's truth. Sibling actions' patches are untouched.
			m.dropPatch(msg.seq)
		} else {
			m.actionErr = nil
			// The mutation is CONFIRMED. Its optimistic patch was applied at
			// KEYPRESS; re-stamp its deadline from NOW so the TTL bounds only the
			// read path's catch-up, not the action's own round trip.
			m.confirmPatch(msg.seq)
			if msg.undoArchive != nil {
				m.archiveUndo = append(m.archiveUndo, *msg.undoArchive)
				if len(m.archiveUndo) > archiveUndoCap {
					m.archiveUndo = m.archiveUndo[1:]
				}
				m.note = fmt.Sprintf("archived %q · U to undo", msg.undoArchive.name)
				// Keep the selection on the acted-on thread. A non-hiding change that
				// reorders the list (rename re-sorts by name) must not shift the cursor onto
				// a neighbour; reanchorCursor follows the row to its new slot. If the change
				// HID the row (archive/delete/hold leaves the view), the id isn't found and
				// it falls back to the positional slot — landing on the neighbour, which is
				// the wanted behaviour when the selected row itself disappears.
				m.reanchorCursor(msg.id)
				m.ensureCursorVisible()
			}
		}
		if msg.err == nil && msg.preselect != "" {
			m.preselectID = msg.preselect // land the cursor on the moved node once the refetch lands
		}
		if msg.err == nil && msg.expand != "" {
			if m.expanded == nil {
				m.expanded = map[string]bool{}
			}
			m.expanded[msg.expand] = true // keep the new parent open so the moved child is visible
		}
		if msg.err == nil {
			// Reconcile now AND once more shortly: the immediate fetch can outrun the
			// maintainer's snapshot republish, so a no-optimistic-patch structural
			// change (reparent, incl. reparent-to-root) would otherwise not show until
			// the next 3s poll. The delayed reconcile makes it appear promptly.
			return m, tea.Batch(m.fetch(), reconcileAfter(postActionReconcileDelay))
		}
		return m, m.fetch() // reconcile against the daemon
	case pinDoneMsg:
		// The optimistic pin patch is already applied (instant feel). Success
		// re-stamps its deadline from confirmation; a failure surfaces loudly,
		// reverts exactly that step's entry, then refetches the server's true order.
		if msg.err != nil {
			m.actionErr = msg.err
			m.dropPatch(msg.seq)
			return m, m.fetch()
		}
		m.confirmPatch(msg.seq)
		return m, nil
	case followDoneMsg:
		// A follow nav finished. Clear the latch, record what the cockpit now
		// shows, then RE-ARM: if the selection moved while this nav ran, the
		// next preview fires immediately for wherever the cursor is now (the
		// coalescing half of the no-debounce design; armFollow's eligibility
		// dedup makes this a no-op when the cursor didn't move).
		m.followInFlight = false
		if msg.err != nil {
			m.actionErr = msg.err
		}
		if msg.id != "" {
			// Recorded on FAILURE too: the coalesce below re-arms, and without
			// this a still-selected failing target would refire the same broken
			// nav forever (error -> re-arm -> eligible -> error ...). Recording
			// the ATTEMPT makes the dedup break the loop; moving away and back
			// retries deliberately.
			m.lastFollowedID = msg.id
		}
		cmd := m.armFollow()
		return m, cmd
	case navDoneMsg:
		// The selected thread is now on screen (the client switched under us).
		// SIDEBAR mode: the TUI is a persistent pane BESIDE the thread, not a
		// popup covering it — stay open, note the entry, and hand focus to the
		// sibling (attach) pane so the user lands typing at the agent.
		// Otherwise quit so the TUI (and the popup hosting it) gets out of the way.
		if m.sidebar {
			if msg.id != "" {
				m.lastFollowedID = msg.id // the cockpit now shows it — no follow needed
			}
			return m, focusSiblingPane()
		}
		return m, tea.Quit
	case attachMsg:
		// Quit so the terminal is restored, then runTUI execs the attach.
		m.attachTarget, m.attachThread = msg.target, msg.thread
		return m, tea.Quit
	case ticketsLoadedMsg:
		// Ignore a stale load (the user backed out / switched threads).
		if m.ticketMode == ticketNone || msg.thread.ID != m.ticketThread.ID {
			return m, nil
		}
		if msg.err != nil {
			m.ticketErr = msg.err
			return m, nil
		}
		m.ticketErr = nil
		m.tickets = msg.tickets
		if vis := len(m.visibleTickets()); m.ticketCursor >= vis {
			m.ticketCursor = max(0, vis-1)
		}
		return m, nil
	case ticketEditDoneMsg:
		return m, m.applyTicketEdit(msg)
	case ticketActionMsg:
		if msg.err != nil {
			m.ticketErr = msg.err
			return m, nil
		}
		m.ticketErr = nil
		m.note = msg.note
		if msg.reload != "" {
			return m, m.loadTickets(m.ticketThread)
		}
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// actionMsg is the result of an in-app mutation whose optimistic patch (if any)
// was already recorded and applied at KEYPRESS (see recordPatch). seq is that
// patch's per-action identity (0 = the action had no patch): success re-stamps
// its reconcile deadline, failure reverts exactly that entry.
type actionMsg struct {
	err       error
	id        string // the thread the mutation targeted
	seq       int    // the keypress-recorded patch this action owns (0 = none)
	preselect string // on success, move the cursor here on the next fetch (structural changes like reparent: no patch, the tree refetches and the moved node is re-selected)
	expand    string // on success, expand this node so a freshly-nested child stays visible even before the snapshot reflects the new parent (avoids the preselect/propagation race)
	// undoArchive: a CONFIRMED archive pushes its undo entry (the U key's LIFO
	// stack) — attached on success only, so a failed archive never yields a
	// bogus undo target.
	undoArchive *archiveUndoEntry
}

// archiveUndoEntry is one undoable archive. Archiving is instant (no confirm)
// since the flow is act-then-undo: U un-archives the most recent entry.
type archiveUndoEntry struct{ id, machine, name string }

// archiveUndoCap bounds the session's undo stack (oldest entries drop off).
const archiveUndoCap = 20

// reconcileMsg fires after postActionReconcileDelay to trigger one extra fetch (see
// reconcileAfter) so a server-side change that lagged the immediate post-action fetch
// shows promptly instead of waiting for the next 3s poll.
type reconcileMsg struct{}

// reconcileAfter schedules a single delayed reconcile fetch (via reconcileMsg).
func reconcileAfter(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return reconcileMsg{} })
}

// preselectMsg carries the result of the async master-cursor resolve (the thread the
// active master window is showing). id == "" means nothing to preselect (no master
// client, or its pane is a plain shell) — a legitimate no-op, never an error.
type preselectMsg struct{ id string }

// resolveMasterCursor resolves the thread the active master window is currently showing,
// off the main loop, returning a preselectMsg. Failures resolve to an empty preselect — a
// missing thread must never disrupt the TUI.
//
// Always a single LOCAL daemon-client call — no `sesh` subprocess. For a local active
// window machine is "" (the daemon reads its own marker). For a REMOTE active window the
// daemon routes the resolve to that peer over its own warm mesh connection (schema 36), so
// even the cross-machine case is a local unix call + a warm round trip rather than a cold
// fork+connect. (machine == origin is the local case, sent as "" so a pre-36 daemon — which
// would ignore the param — still resolves it correctly during a deploy skew.)
func (m Model) resolveMasterCursor() tea.Cmd {
	c, origin, machine := m.client, m.machine, m.masterCursorMachine
	if machine == origin {
		machine = ""
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, tid, _, err := c.TmuxMasterCurrent(ctx, origin, machine)
		if err != nil {
			return preselectMsg{} // resolve failed → no preselect (non-fatal)
		}
		return preselectMsg{id: tid}
	}
}

// positionCursorOn moves the cursor to the row for id, expanding a nested child's
// ancestors first so it's actually visible in the tree. Returns true if the row was
// found and the cursor placed; false if id isn't in the current rows yet (the caller
// keeps the pending preselect so a later fetch — once the maintainer has published it
// — lands it).
func (m *Model) positionCursorOn(id string) bool {
	if id == "" {
		return false
	}
	m.expandAncestors(id)
	for i, rm := range m.visibleMatches() {
		if rm.row.ID == id {
			m.cursor = i
			return true
		}
	}
	return false
}

// expandAncestors opens every ancestor of id so a collapsed child becomes visible.
func (m *Model) expandAncestors(id string) {
	byID := make(map[string]api.ThreadRow, len(m.rows))
	for _, r := range m.rows {
		byID[r.ID] = r
	}
	if m.expanded == nil {
		m.expanded = map[string]bool{}
	}
	cur, ok := byID[id]
	seen := map[string]bool{} // cycle guard
	for ok && cur.Parent != "" && !seen[cur.Parent] {
		seen[cur.Parent] = true
		m.expanded[cur.Parent] = true
		cur, ok = byID[cur.Parent]
	}
}

// selectedID returns the id of the thread currently under the cursor, or "" when
// nothing is selected (empty rows / cursor out of range).
func (m *Model) selectedID() string {
	if tr, ok := m.selectedTree(); ok {
		return tr.row.ID
	}
	return ""
}

// reanchorCursor pins the selection to a SPECIFIC thread across a change in the row
// set or sort order. If anchorID is still visible, the cursor moves to its (possibly
// new) index — so a row appearing/disappearing above it, or the selected row itself
// being renamed/reordered, never silently shifts the selection onto a DIFFERENT thread
// (the archive/delete-the-wrong-row footgun). If anchorID is gone from the view
// (archived/held/reparented away, or "" because nothing was selected), the cursor
// instead HOLDS its positional slot, clamped into range — landing on the neighbour
// rather than chasing the vanished row.
func (m *Model) reanchorCursor(anchorID string) {
	vis := m.visibleMatches()
	if anchorID != "" {
		for i, tr := range vis {
			if tr.row.ID == anchorID {
				m.cursor = i
				return
			}
		}
	}
	if m.cursor >= len(vis) {
		m.cursor = max(0, len(vis)-1)
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

// optimisticPatchTTL bounds how long an unsatisfied optimistic patch survives (from
// its confirmation) before it's dropped and the server's truth shows — LOUDLY, via
// actionErr, since a confirmed mutation the read path never reflected is a real
// problem (badly degraded mesh sync), not something to mask forever. It is a
// wall-clock deadline, NOT a fetch count: the read path catches up in TIME (owner
// republish ≤300ms + mesh pull ≤1s + poll ≤3s), while fetches arrive at a rate set
// by UI activity — counting fetches made a patch die faster the busier the UI was
// (each action fires 2 reconcile fetches, burning every OTHER pending patch), which
// is how archived rows resurfaced mid-reconcile. 15s comfortably outlasts a healthy
// catch-up while still surfacing genuine sync failure promptly.
const optimisticPatchTTL = 15 * time.Second

// rowPatch is ONE action's optimistic field overrides for one row (nil/empty =
// untouched). Entries live in Model.pending in keypress order; seq is the
// action's identity for confirm/revert.
type rowPatch struct {
	// id is the thread the patch overrides; seq its per-action identity
	// (minted by recordPatch); confirmed flips when the action's subprocess
	// reported success (drives the deadline-expiry wording — an unconfirmed
	// expiry is a slow/hung action, not a sync failure).
	id        string
	seq       int
	confirmed bool

	name         *string
	notify       *bool
	onHold       *bool
	archived     *bool
	flagged      *bool
	flagDisabled *bool
	head         *api.Head
	busy         *api.Busy
	addTags      []string
	removeTags   []string
	// pinSet, when true, overrides the row's manual-ordering key to pinOrder (which may
	// be nil = un-pinned). It re-sorts the row instantly (pinned block above the auto
	// block) — the optimism behind `p`/`u`/`m` while the mesh read path catches up.
	pinSet   bool
	pinOrder *float64
	// hide drops the row from the CURRENT view instantly (archive/unarchive/delete/
	// hold remove it from this view; the mesh read path lags the write). Unlike the
	// field overlays, it removes the row rather than editing it.
	hide bool
	// machine is the row's owning machine at action time. It decides how the row's
	// ABSENCE from a fetch reads: machine reporting reachable → the change landed
	// (or the row is gone) → patch satisfied; machine offline/missing → absence
	// proves nothing → the patch holds until deadline (never GC on missing data).
	machine string
	// desc names the action ("archive \"corkboard\"") for the deadline-expiry warning.
	desc string
	// deadline is when an unsatisfied patch gives up: dropped, server truth shown,
	// actionErr set. Stamped at confirmation time (routedVerb & friends).
	deadline time.Time
}

// merge folds o's overrides onto p (later action wins per field). Used only to
// build the READ-ONLY combined view of a thread's pending entries (pendingFor);
// the pending list itself keeps entries separate — that separation is what
// per-action revert relies on.
func (p *rowPatch) merge(o *rowPatch) {
	if o.name != nil {
		p.name = o.name
	}
	if o.notify != nil {
		p.notify = o.notify
	}
	if o.onHold != nil {
		p.onHold = o.onHold
	}
	if o.archived != nil {
		p.archived = o.archived
	}
	if o.flagged != nil {
		p.flagged = o.flagged
	}
	if o.flagDisabled != nil {
		p.flagDisabled = o.flagDisabled
	}
	if o.head != nil {
		p.head = o.head
	}
	if o.busy != nil {
		p.busy = o.busy
	}
	if o.hide {
		p.hide = true
	}
	if o.pinSet {
		p.pinSet, p.pinOrder = true, o.pinOrder // latest reorder wins
	}
	p.addTags = append(p.addTags, o.addTags...)
	p.removeTags = append(p.removeTags, o.removeTags...)
	if o.machine != "" {
		p.machine = o.machine // same row → same owner; latest stamp wins
	}
	if o.desc != "" {
		if p.desc != "" && p.desc != o.desc {
			p.desc = p.desc + "; " + o.desc
		} else {
			p.desc = o.desc
		}
	}
	if o.deadline.After(p.deadline) {
		p.deadline = o.deadline
	}
}

// supersededBy clears from p (an OLDER pending entry on the same thread) every
// field the NEWER patch o sets: the newer action's optimism replaces p's, and —
// critically — p's satisfied()/expiry obligation for those fields dies with it
// (else e.g. two quick notify toggles leave the first entry demanding a value
// the server will never show, expiring into a bogus loud sync warning).
// Opposite-direction tag edits cancel likewise. Returns true when the clearing
// left p SPENT: it had field proof and now has none — its hide (if any) rode on
// those fields (archive's hide dies when an unarchive lands on top), so the
// whole entry should be dropped. A from-birth pure hide (delete) shares no
// fields with anything and is never touched here.
func (p *rowPatch) supersededBy(o *rowPatch) bool {
	hadProof := p.hasFieldProof()
	if o.name != nil {
		p.name = nil
	}
	if o.notify != nil {
		p.notify = nil
	}
	if o.onHold != nil {
		p.onHold = nil
	}
	if o.archived != nil {
		p.archived = nil
	}
	if o.flagged != nil {
		p.flagged = nil
	}
	if o.flagDisabled != nil {
		p.flagDisabled = nil
	}
	if o.head != nil {
		p.head = nil
	}
	if o.busy != nil {
		p.busy = nil
	}
	if o.pinSet {
		p.pinSet, p.pinOrder = false, nil
	}
	for _, t := range o.addTags {
		p.removeTags = removeStr(p.removeTags, t)
	}
	for _, t := range o.removeTags {
		p.addTags = removeStr(p.addTags, t)
	}
	return hadProof && !p.hasFieldProof()
}

// hasFieldProof reports whether the patch asserts any server-provable field —
// the overrides satisfied() can check a present row against. A patch without
// field proof is either empty or a PURE hide (delete), which only the row's
// absence or the deadline can end.
func (p *rowPatch) hasFieldProof() bool {
	return p.name != nil || p.notify != nil || p.onHold != nil ||
		p.archived != nil || p.flagged != nil || p.flagDisabled != nil ||
		p.head != nil || p.busy != nil || p.pinSet ||
		len(p.addTags) > 0 || len(p.removeTags) > 0
}

func (p *rowPatch) apply(r *api.ThreadRow) {
	if p.name != nil {
		r.Name = *p.name
	}
	if p.notify != nil {
		r.Notify = *p.notify
	}
	if p.onHold != nil {
		r.OnHold = *p.onHold
	}
	if p.archived != nil {
		r.Archived = *p.archived
	}
	if p.flagged != nil {
		r.Flagged = *p.flagged
	}
	if p.flagDisabled != nil {
		r.FlagDisabled = *p.flagDisabled
	}
	if p.head != nil {
		r.Head = *p.head
	}
	if p.busy != nil {
		r.Busy = *p.busy
	}
	if p.pinSet {
		r.PinOrder = p.pinOrder
	}
	for _, t := range p.addTags {
		if !containsStr(r.Tags, t) {
			r.Tags = append(append([]string(nil), r.Tags...), t)
		}
	}
	for _, t := range p.removeTags {
		if containsStr(r.Tags, t) {
			r.Tags = removeStr(r.Tags, t)
		}
	}
}

// satisfied reports whether the server row already reflects every override.
// satisfied() is only consulted for a row still PRESENT in the fetch. A hide patch
// with field overrides (archive/hold set archived/onHold) is satisfied once the
// fields prove the change landed — the row can legitimately still be present in a
// view that admits its new state (e.g. `all` after archiving in `active`), and
// keeping the hide there wrongly blanked it. A PURE hide (delete: no field can
// prove it) is never satisfied by a present row — only the row's absence (see
// applyPending) or the deadline ends it.
func (p *rowPatch) satisfied(r api.ThreadRow) bool {
	if !p.hasFieldProof() {
		return false // empty or PURE hide (delete): only absence/deadline ends it
	}
	if p.name != nil && r.Name != *p.name {
		return false
	}
	if p.notify != nil && r.Notify != *p.notify {
		return false
	}
	if p.onHold != nil && r.OnHold != *p.onHold {
		return false
	}
	if p.archived != nil && r.Archived != *p.archived {
		return false
	}
	if p.flagged != nil && r.Flagged != *p.flagged {
		return false
	}
	if p.flagDisabled != nil && r.FlagDisabled != *p.flagDisabled {
		return false
	}
	if p.head != nil && r.Head != *p.head {
		return false
	}
	if p.busy != nil && r.Busy != *p.busy {
		return false
	}
	if p.pinSet && !samePinOrder(r.PinOrder, p.pinOrder) {
		return false
	}
	for _, t := range p.addTags {
		if !containsStr(r.Tags, t) {
			return false
		}
	}
	for _, t := range p.removeTags {
		if containsStr(r.Tags, t) {
			return false
		}
	}
	return true
}

// recordPatch is the KEYPRESS half of an optimistic action: it stamps the
// patch's identity + reconcile context (thread, machine, desc, deadline; the
// deadline is provisional — confirmPatch re-stamps it once the subprocess
// reports success, so the TTL bounds the read path, not the round trip),
// SUPERSEDES older entries' now-replaced fields, appends it to the pending
// list, and applies it so the change renders instantly. A hide patch removes
// the selected row, so the cursor re-anchors (falling to the neighbour when
// the selected row itself vanished — the wanted behaviour). Returns the
// action's seq for its actionMsg.
func (m *Model) recordPatch(row api.ThreadRow, p *rowPatch, verb string) int {
	m.patchSeq++
	p.id, p.seq = row.ID, m.patchSeq
	p.machine = row.Machine
	if p.desc == "" { // an action may pre-set a more precise label (e.g. "unarchive")
		p.desc = fmt.Sprintf("%s %q", verb, rowDisplayName(row))
	}
	p.deadline = time.Now().Add(optimisticPatchTTL)
	kept := m.pending[:0]
	for _, old := range m.pending {
		if old.id == row.ID && old.supersededBy(p) {
			continue // spent: every field it asserted is now this action's
		}
		kept = append(kept, old)
	}
	m.pending = append(kept, p)
	anchor := m.selectedID()
	m.applyPending(false) // instant feedback (no GC — the rows are still server-stale)
	m.reanchorCursor(anchor)
	m.ensureCursorVisible()
	return p.seq
}

// confirmPatch marks the action's entry confirmed and restarts its deadline —
// from confirmation the TTL measures only the read path's catch-up. A missing
// seq (0, or an entry already GC'd/superseded) is a no-op.
func (m *Model) confirmPatch(seq int) {
	for _, p := range m.pending {
		if p.seq == seq {
			p.confirmed = true
			p.deadline = time.Now().Add(optimisticPatchTTL)
			return
		}
	}
}

// dropPatch reverts EXACTLY one action's optimism (its entry is removed; the
// caller's refetch restores the server's truth for those fields). Sibling
// entries — other actions on the same thread — are untouched.
func (m *Model) dropPatch(seq int) {
	if seq == 0 {
		return
	}
	kept := m.pending[:0]
	for _, p := range m.pending {
		if p.seq != seq {
			kept = append(kept, p)
		}
	}
	m.pending = kept
}

// pendingFor is the combined view of a thread's pending entries (in keypress
// order; nil = none) — inspection/tests only, the list stays per-action.
func (m Model) pendingFor(id string) *rowPatch {
	var out *rowPatch
	for _, p := range m.pending {
		if p.id != id {
			continue
		}
		if out == nil {
			out = &rowPatch{id: id}
		}
		out.merge(p)
	}
	return out
}

// applyPending overlays the pending optimistic patches onto m.rows, in keypress
// order (a later action's override wins where they overlap — though recordPatch's
// supersede keeps same-thread entries field-disjoint). When gc is true (a
// reconcile after a fresh fetch), an entry the server has caught up to is
// dropped: a present row satisfying every override, or the row ABSENT while its
// owning machine is reporting reachable (it left this view / was deleted — the
// read path caught up). Absence with the machine offline or missing proves
// nothing and never GCs (never conclude from missing data). An unsatisfied entry
// survives until its wall-clock deadline; expiry surfaces the server's truth
// LOUDLY via actionErr — a confirmed write the mesh never reflected is a sync
// problem to report, not mask (an UNCONFIRMED expiry is a slow/hung action; its
// own actionMsg will still report the outcome). When gc is false (right after
// recording a patch), it only overlays — the freshly-read rows are still stale,
// so nothing should be GC'd yet.
func (m *Model) applyPending(gc bool) {
	if len(m.pending) == 0 {
		return
	}
	idx := map[string]int{}
	for i := range m.rows {
		idx[m.rows[i].ID] = i
	}
	reach := map[string]bool{}
	for _, mv := range m.machines {
		reach[mv.Machine] = mv.Self || mv.Reachable
	}
	now := time.Now()
	hidden := map[string]bool{}
	kept := m.pending[:0]
	for _, p := range m.pending {
		i, ok := idx[p.id]
		if gc {
			if !ok && reach[p.machine] {
				continue // row gone while its owner reports in — the change landed
			}
			if ok && p.satisfied(m.rows[i]) {
				continue
			}
			if now.After(p.deadline) {
				// Server never caught up — surface its truth, loudly.
				desc, owner := p.desc, p.machine
				if desc == "" {
					desc = "a change to " + p.id
				}
				if owner == "" {
					owner = "its owner"
				}
				if p.confirmed {
					m.actionErr = fmt.Errorf("%s: confirmed by %s but still not reflected in the mesh view after %s — sync may be degraded; showing the server's state", desc, owner, optimisticPatchTTL)
				} else {
					m.actionErr = fmt.Errorf("%s: no confirmation from %s after %s — still running; showing the server's state meanwhile", desc, owner, optimisticPatchTTL)
				}
				continue
			}
		}
		kept = append(kept, p)
		if !ok {
			continue
		}
		if p.hide {
			hidden[p.id] = true // drop it from the view until the read path catches up (or the deadline resurrects it)
		} else {
			p.apply(&m.rows[i])
		}
	}
	m.pending = kept
	if len(hidden) > 0 {
		keptRows := m.rows[:0]
		for _, r := range m.rows {
			if !hidden[r.ID] {
				keptRows = append(keptRows, r)
			}
		}
		m.rows = keptRows
	}
}

func containsStr(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// removeStr returns a copy of s with every occurrence of v dropped.
func removeStr(s []string, v string) []string {
	out := make([]string, 0, len(s))
	for _, x := range s {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

func sptr(s string) *string { return &s }
func bptr(b bool) *bool     { return &b }

// attachMsg asks the TUI to quit and have the caller attach the terminal to target
// (<machine>:<session>) — used when Enter is pressed outside tmux. thread carries the
// thread id so the attach lands on the WINDOW holding its pane.
type attachMsg struct{ target, thread string }

// navDoneMsg reports a successful ENTER nav: the user is where they asked to
// be, so the TUI quits — except in SIDEBAR mode, where the TUI is a persistent
// pane: it stays running and hands focus to its sibling (attach) pane instead,
// with no note (the switched pane IS the feedback — Lukas). (A FAILED nav
// stays an actionMsg with the error, keeping the TUI open either way; follow
// navs report followDoneMsg.) id feeds lastFollowedID.
type navDoneMsg struct {
	id string
}

// machineReachable reports whether `machine` was reachable at the last mesh read. This
// machine (self, or an empty machine id) is always reachable. A machine not present in
// the last mesh view is treated as reachable — we never block an action on missing data
// (the routed call surfaces its own loud error if it's genuinely wrong); every real row's
// machine is self or a mesh peer, so it is always present in practice.
// offlineRowLabel is a short human label for a thread in an error message: its name, or
// its tid8 when nameless.
func offlineRowLabel(row api.ThreadRow) string {
	if row.Name != "" {
		return row.Name
	}
	return tid8(row.ID)
}

func (m Model) machineReachable(machine string) bool {
	if machine == "" || machine == m.machine {
		return true
	}
	for _, mv := range m.machines {
		if mv.Machine == machine {
			return mv.Reachable
		}
	}
	return true
}

// requiresReachableOwner reports whether a normal-mode key triggers an action that
// mutates or enters a thread ON ITS OWNING MACHINE (and so routes over the mesh). Such
// an action on an OFFLINE machine's thread would shell out and hang on the 6–15s routing
// timeout before failing — so handleKey refuses it instantly instead. Navigation/read-only
// keys (movement, scroll, fold, filter, view cycle, uuid, id, copy, refresh) are exempt:
// they never touch the owner. A new owner-routed key MUST be added here (a test enforces
// that every routed action's key is covered — see TestRequiresReachableOwnerCoversActions).
func requiresReachableOwner(key string) bool {
	switch key {
	case "enter", // navSelected (resume/nav on the owner)
		"a",      // archive/unarchive
		"d",      // delete
		"x",      // stop
		"f",      // flag toggle (owner-routed setter)
		"F",      // fork (thread new --fork-from on the owner)
		"ctrl+f", // flag-gate toggle (owner-routed setter)
		"r",      // rename
		"t",      // tag add
		"T",      // tag remove
		"P",      // reparent
		"h",      // hold toggle
		"H",      // hold until date
		"n",      // notify on/off
		"v",      // new virtual group (created on the selected row's machine)
		"p",      // pin to top (routed pin on the owner)
		"u",      // unpin (routed pin --clear on the owner)
		"m",      // enter move mode (auto-pins on the owner)
		"D",      // new divider (created on the selected row's machine)
		"K":      // tickets view (loadTickets routes to the owner)
		return true
	}
	return false
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.ticketMode != ticketNone {
		return m.handleTicketKey(msg)
	}
	if m.confirming != confirmNone {
		return m.handleConfirmKey(msg)
	}
	if m.detailsPopup {
		return m.handleDetailsKey(msg)
	}
	if m.helpPopup {
		return m.handleHelpKey(msg)
	}
	if m.viewPicker {
		return m.handleViewPickerKey(msg)
	}
	if m.uuidPopup {
		return m.handleUUIDKey(msg)
	}
	if m.tagPopup {
		return m.handleTagPopupKey(msg)
	}
	if m.prompting != promptNone {
		return m.handlePromptKey(msg)
	}
	if m.filtering {
		return m.handleFilterKey(msg)
	}
	// Move mode (manual ordering): ↑/↓ reposition the pinned row, Enter/Esc exit.
	// Dispatched before the offline gate — entry (`m`) is already gated, and a routed
	// reorder that fails on an owner going offline mid-move surfaces loudly on its own.
	if m.reordering {
		return m.handleReorderKey(msg)
	}
	// Refuse an owner-routed action on an OFFLINE machine's thread instantly (loud, no
	// popup, no shell-out) instead of hanging on the routing timeout. Gating here — before
	// any confirm/prompt popup opens — means `a`/`d`/`r`/… don't even prompt for a thread
	// you can't reach.
	if requiresReachableOwner(msg.String()) {
		if row, ok := m.Selected(); ok && !m.machineReachable(row.Machine) {
			m.note = ""
			m.actionErr = fmt.Errorf("%s is offline — can't reach %q until it reconnects", row.Machine, offlineRowLabel(row))
			return m, nil
		}
	}
	switch msg.String() {
	// Esc quits from normal mode. (When a filter mode lands, Esc-in-filter will
	// apply/leave the filter first, v1-style — quitting stays a normal-mode-only Esc.)
	case "q", "esc", "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		m.moveCursor(-1) // wraps (fzf --cycle feel), over the VISIBLE (filtered) rows
		m.ensureCursorVisible()
		cmd := m.armFollow() // sidebar: preview the selection in the sibling pane once it rests
		return m, cmd
	case "down", "j":
		m.moveCursor(1)
		m.ensureCursorVisible()
		cmd := m.armFollow()
		return m, cmd
	case "ctrl+k":
		m.scrollRows(-m.halfPage()) // scroll the viewport up a half-page (cursor follows into view)
		cmd := m.armFollow()
		return m, cmd
	case "ctrl+j":
		m.scrollRows(m.halfPage())
		cmd := m.armFollow()
		return m, cmd
	case "/":
		m.filtering = true
		m.filterCaret = len([]rune(m.filter))
	// Fold/unfold the tree is on the ARROW keys; ^h/^l pan the columns horizontally so
	// clipped columns can be brought into view. (h/l moved to ^h/^l in 2026-06 to free
	// h/H for hold — Lukas; the pan pair stays symmetric.)
	case "right":
		m.foldSelected(true)
	case "left":
		m.foldSelected(false)
	case "ctrl+l":
		if m.hOffset < m.maxHOffset() {
			m.hOffset++
		}
	case "ctrl+h":
		if m.hOffset > 0 {
			m.hOffset--
		}
	case "h":
		// Toggle hold: a non-held thread is parked until the start of tomorrow (so it
		// returns to the active view tomorrow); a held thread is released now.
		// (Bound before returning: the helper records the keypress-time patch on m.)
		cmd := m.holdToggleSelected()
		return m, cmd
	case "H":
		// Hold until an explicit date: open a prompt (YYYY-MM-DD); blank clears the hold.
		if row, ok := m.Selected(); ok {
			pre := []rune(holdDatePrefill(row))
			m.prompting, m.promptRow, m.promptInput = promptHold, row, pre
			m.promptCursor = len(m.promptInput)
		}
	case "tab":
		// Open the VIEW PICKER preselecting the NEXT view — tab+enter reproduces
		// the old cycle in two taps, while the list shows where you're going.
		m.viewPicker = true
		m.viewPickerCursor = (int(m.view) + 1) % m.viewCount()
	case "i":
		m.showID = !m.showID
	case "w":
		// Toggle the per-column max-width cap. Off = every column grows to its
		// content, so a clipped name/cwd becomes fully readable. Re-clamp the
		// horizontal pan since the column widths (and so maxHOffset) just changed.
		m.maxColWidth = !m.maxColWidth
		if m.hOffset > m.maxHOffset() {
			m.hOffset = m.maxHOffset()
		}
	case "o":
		// Toggle whether OFFLINE machines' last-known threads are shown. Refetch so the
		// change lands immediately; reset the cursor since the visible row set shifts.
		m.hideOffline = !m.hideOffline
		m.cursor = 0
		return m, m.fetch()
	case "n":
		cmd := m.notifySelected()
		return m, cmd
	case "f":
		cmd := m.flagSelected()
		return m, cmd
	case "ctrl+f":
		cmd := m.flagGateSelected()
		return m, cmd
	case "?":
		m.helpPopup = true
	case "y":
		if _, ok := m.Selected(); ok {
			m.uuidPopup = true
		}
	case "I":
		// Thread details: a read-only full-screen takeover of every field of the
		// selected thread (the record + live axes). Local data — nothing routes.
		if row, ok := m.Selected(); ok {
			m.detailsPopup, m.detailsRow = true, row
		}
	case "R":
		return m, m.fetch()
	case "r":
		if row, ok := m.Selected(); ok {
			// Prefill with the current name, cursor at the end so you can edit in place.
			m.prompting, m.promptRow, m.promptInput = promptRename, row, []rune(row.Name)
			m.promptCursor = len(m.promptInput)
		}
	case "t":
		if row, ok := m.Selected(); ok {
			m.prompting, m.promptRow, m.promptInput, m.promptCursor = promptTag, row, nil, 0
		}
	case "T":
		if row, ok := m.Selected(); ok {
			if len(row.Tags) == 0 {
				m.note = "no tags to remove"
			} else {
				m.tagPopup, m.tagPopupRow, m.tagPopupCursor = true, row, 0
			}
		}
	case "P":
		if row, ok := m.Selected(); ok {
			m.prompting, m.promptRow, m.promptInput, m.promptCursor = promptReparent, row, nil, 0
		}
	case "v":
		// New VIRTUAL grouping thread (a root group; reparent children under it
		// with P). The selection is only a MACHINE carrier — the group is created
		// on the selected row's machine, because a virtual parent can only group
		// same-machine threads, so it must live where the threads being grouped
		// live. No selection (empty grid) = a zero row = the local machine.
		row, _ := m.Selected()
		m.prompting, m.promptRow, m.promptInput, m.promptCursor = promptNewVirtual, row, nil, 0
	case "p":
		// Pin the selected top-level thread to the TOP of the manual-order block.
		if row, ok := m.Selected(); ok {
			if row.Parent != "" {
				m.actionErr = fmt.Errorf("only top-level threads can be pinned — %q is nested", rowDisplayName(row))
				return m, nil
			}
			order := m.pinTopOrder()
			seq := m.applyPinPatch(row, &order)
			cmd := m.persistPin(row, &order, seq)
			return m, cmd
		}
	case "u":
		// Unpin: remove the manual ordering (the row rejoins the auto block).
		if row, ok := m.Selected(); ok {
			if row.PinOrder == nil {
				m.note = "not pinned"
				return m, nil
			}
			if row.AgentKind == api.DividerAgentKind {
				m.actionErr = fmt.Errorf("a divider can't be un-pinned — delete it (d)")
				return m, nil
			}
			seq := m.applyPinPatch(row, nil)
			cmd := m.persistPin(row, nil, seq)
			return m, cmd
		}
	case "m":
		// Enter MOVE MODE: reposition the selected pinned row (auto-pinning a top-level
		// row to the top first). ↑/↓ then move it within the block; Enter/Esc exit.
		if row, ok := m.Selected(); ok {
			if row.Parent != "" {
				m.actionErr = fmt.Errorf("only top-level threads can be ordered — %q is nested", rowDisplayName(row))
				return m, nil
			}
			var cmd tea.Cmd
			if row.PinOrder == nil {
				order := m.pinTopOrder()
				seq := m.applyPinPatch(row, &order)
				cmd = m.persistPin(row, &order, seq)
			}
			m.reordering, m.reorderID = true, row.ID
			return m, cmd
		}
	case "D":
		// New DIVIDER: a visual rule in the pinned block. Prompt for an optional label
		// (empty = an unlabeled line; Esc cancels). Created on the selected row's machine.
		row, _ := m.Selected()
		m.prompting, m.promptRow, m.promptInput, m.promptCursor = promptNewDivider, row, nil, 0
	case "F":
		// Fork: copy the selected thread into a new headless thread (same
		// conversation, branched). It doesn't start anything — enter it to
		// continue. (Uppercase since 44: lowercase f is the flag toggle — the
		// far more frequent action.)
		return m, m.forkSelected()
	case "x":
		cmd := m.stopSelected()
		return m, cmd
	case "d":
		// Destructive: confirm before dropping the record (y/n popup).
		if row, ok := m.Selected(); ok {
			m.confirming, m.confirmRow = confirmDelete, row
		}
	case "a":
		// Archive/unarchive INSTANTLY — no confirm (H54): archiving is cheap to
		// reverse, so the flow is act-then-undo (`U` restores the most recent).
		if row, ok := m.Selected(); ok {
			cmd := m.archiveRow(row)
			return m, cmd
		}
	case "U":
		return m.undoLastArchive()
	case "K":
		// Tickets view: a full-screen takeover of the selected thread's tickets.
		if row, ok := m.Selected(); ok {
			return m, m.openTicketView(row)
		}
	case "enter":
		return m, m.navSelected()
	}
	return m, nil
}

// handleConfirmKey drives the y/n confirmation popup for a destructive action:
// `y` (or `Y`) runs it on the captured row, ANY other key cancels (so a stray
// keystroke never deletes/archives). Routed to the owner like the direct verbs.
func (m Model) handleConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	kind, row := m.confirming, m.confirmRow
	m.confirming = confirmNone
	if s := msg.String(); s == "y" || s == "Y" {
		switch kind {
		case confirmDelete:
			cmd := m.deleteRow(row)
			return m, cmd
		}
	}
	return m, nil
}

// handleUUIDKey: inside the uuid popup, `c` copies the full uuid to the system
// clipboard (failure is a LOUD error in the error line); any key closes the popup.
func (m Model) handleUUIDKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.uuidPopup = false
	if msg.String() == "c" {
		if row, ok := m.Selected(); ok {
			if err := copyToClipboard(row.ID); err != nil {
				m.lastErr = fmt.Errorf("copy uuid: %w", err)
			} else {
				m.note = "UUID copied to clipboard"
			}
		}
	}
	return m, nil
}

// handleDetailsKey drives the thread-details takeover: esc/q/enter close it, any
// other key is ignored (it's a read-only view — no action to take).
func (m Model) handleDetailsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q", "ctrl+c", "enter", "I":
		m.detailsPopup = false
	}
	return m, nil
}

// handleTagPopupKey drives the remove-tag picker: ↑/↓ (or j/k) move, enter removes
// the highlighted tag from the thread (routed to the owner) and drops it from the
// list, esc/q closes. The popup closes when the last tag is removed.
func (m Model) handleTagPopupKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	tags := m.tagPopupRow.Tags
	switch msg.String() {
	case "esc", "ctrl+c", "q":
		m.tagPopup = false
		return m, nil
	case "up", "k":
		if m.tagPopupCursor > 0 {
			m.tagPopupCursor--
		}
		return m, nil
	case "down", "j":
		if m.tagPopupCursor < len(tags)-1 {
			m.tagPopupCursor++
		}
		return m, nil
	case "enter":
		if m.tagPopupCursor < 0 || m.tagPopupCursor >= len(tags) {
			m.tagPopup = false
			return m, nil
		}
		tag := tags[m.tagPopupCursor]
		cmd := m.removeTagRow(m.tagPopupRow, tag)
		// Drop the tag from the popup's own list optimistically so several can be
		// stripped in one sitting; close when none remain, clamp the cursor otherwise.
		remaining := removeStr(tags, tag)
		m.tagPopupRow.Tags = remaining
		if len(remaining) == 0 {
			m.tagPopup = false
		} else if m.tagPopupCursor >= len(remaining) {
			m.tagPopupCursor = len(remaining) - 1
		}
		return m, cmd
	}
	return m, nil
}

// removeTagRow removes a tag from a thread via the `thread tag --remove` verb,
// routed to the owning machine, with an optimistic patch so the TAGS column drops
// it instantly.
func (m *Model) removeTagRow(row api.ThreadRow, tag string) tea.Cmd {
	return m.routedVerb(row, &rowPatch{removeTags: []string{tag}}, "tag", "--remove", tag)
}

// handlePromptKey edits the line-prompt: Enter submits, Esc cancels; ←/→ move the
// insertion point, Home/End jump to the ends, Backspace/Delete remove around it, and
// printable runes insert AT the cursor — so the prefilled name is editable in place.
func (m Model) handlePromptKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Clamp the cursor defensively (a prompt opened before this field existed).
	if m.promptCursor > len(m.promptInput) {
		m.promptCursor = len(m.promptInput)
	}
	switch msg.String() {
	case "esc", "ctrl+c":
		m.prompting, m.promptInput, m.promptCursor = promptNone, nil, 0
		return m, nil
	case "enter":
		kind, row, input := m.prompting, m.promptRow, strings.TrimSpace(string(m.promptInput))
		m.prompting, m.promptInput, m.promptCursor = promptNone, nil, 0
		switch kind {
		case promptRename:
			// An empty submit renames to EMPTY (a nameless thread; a bare-rule divider)
			// — not a cancel. Esc cancels; the prompt is prefilled with the current name,
			// so clearing it and submitting is a deliberate "clear the name".
			cmd := m.renameRow(row, input)
			return m, cmd
		case promptTag:
			if input == "" {
				return m, nil
			}
			cmd := m.tagRow(row, input)
			return m, cmd
		case promptReparent:
			// Empty input is meaningful here: make the thread a root.
			return m, m.reparentRow(row, input)
		case promptHold:
			// Empty input clears the hold; otherwise parse a YYYY-MM-DD date.
			if input == "" {
				cmd := m.holdRow(row, 0)
				return m, cmd
			}
			d, err := time.ParseInLocation("2006-01-02", input, time.Local)
			if err != nil {
				m.actionErr = fmt.Errorf("hold: bad date %q (want YYYY-MM-DD)", input)
				return m, nil
			}
			cmd := m.holdRow(row, d.Unix())
			return m, cmd
		case promptNewVirtual:
			// Empty = cancel (a group without a name is an accident, not an ask).
			if input == "" {
				return m, nil
			}
			return m, m.newVirtualRow(row, input)
		case promptNewDivider:
			// Empty label is legitimate (a plain rule); Esc (above) is how you cancel.
			return m, m.newDividerRow(row, input)
		}
		return m, nil
	case "left", "ctrl+b":
		if m.promptCursor > 0 {
			m.promptCursor--
		}
		return m, nil
	case "right", "ctrl+f":
		if m.promptCursor < len(m.promptInput) {
			m.promptCursor++
		}
		return m, nil
	case "home", "ctrl+a":
		m.promptCursor = 0
		return m, nil
	case "end", "ctrl+e":
		m.promptCursor = len(m.promptInput)
		return m, nil
	case "backspace":
		if m.promptCursor > 0 {
			m.promptInput = append(m.promptInput[:m.promptCursor-1], m.promptInput[m.promptCursor:]...)
			m.promptCursor--
		}
		return m, nil
	case "delete":
		if m.promptCursor < len(m.promptInput) {
			m.promptInput = append(m.promptInput[:m.promptCursor], m.promptInput[m.promptCursor+1:]...)
		}
		return m, nil
	}
	var ins []rune
	switch msg.Type {
	case tea.KeyRunes:
		ins = msg.Runes
	case tea.KeySpace:
		ins = []rune{' '}
	}
	if len(ins) > 0 {
		// Insert at the cursor (copy to avoid aliasing the backing array).
		out := make([]rune, 0, len(m.promptInput)+len(ins))
		out = append(out, m.promptInput[:m.promptCursor]...)
		out = append(out, ins...)
		out = append(out, m.promptInput[m.promptCursor:]...)
		m.promptInput = out
		m.promptCursor += len(ins)
	}
	return m, nil
}

// renderPromptInput draws the line-prompt value with a block cursor at the insertion
// point (a trailing block when the cursor is at the end), so ←/→ editing is visible.
func renderPromptInput(input []rune, cursor int) string {
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(input) {
		cursor = len(input)
	}
	return string(input[:cursor]) + "█" + string(input[cursor:])
}

// renameRow renames a thread via the CLI verb (uniform local/remote: the CLI's
// --machine routing reaches the owning daemon; the TUI does not re-implement it).
// The name patch is recorded at keypress, like every routedVerb action.
func (m *Model) renameRow(row api.ThreadRow, name string) tea.Cmd {
	seq := m.recordPatch(row, &rowPatch{name: sptr(name)}, "rename")
	bin, env, machine := m.binaryPath, m.navEnv, m.machine
	return func() tea.Msg {
		args := []string{"thread", "rename", "--id", row.ID, "--name", name}
		if machine == "" || row.Machine != machine {
			args = append(args, "--machine", row.Machine)
		}
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return actionMsg{err: fmt.Errorf("rename %q: %v: %s", row.Name, err, strings.TrimSpace(string(out))), id: row.ID, seq: seq}
		}
		return actionMsg{id: row.ID, seq: seq}
	}
}

// tagRow adds a tag to a thread via the CLI verb (routing as renameRow).
func (m *Model) tagRow(row api.ThreadRow, tag string) tea.Cmd {
	seq := m.recordPatch(row, &rowPatch{addTags: []string{tag}}, "tag")
	bin, env, machine := m.binaryPath, m.navEnv, m.machine
	return func() tea.Msg {
		args := []string{"thread", "tag", "--id", row.ID, "--add", tag}
		if machine == "" || row.Machine != machine {
			args = append(args, "--machine", row.Machine)
		}
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return actionMsg{err: fmt.Errorf("tag %q: %v: %s", row.Name, err, strings.TrimSpace(string(out))), id: row.ID, seq: seq}
		}
		return actionMsg{id: row.ID, seq: seq}
	}
}

// reparentRow sets a thread's parent via the `thread reparent` verb (empty newParent
// = --root), routed to the owner. The tree reshapes, so there's NO optimistic patch
// (structural optimism is a code-smell risk) — on success the reconcile fetch refreshes
// the tree and the moved node is re-selected with its new ancestors expanded (preselect).
// A prefix is accepted: the CLI verb resolves --parent the same way it resolves --id.
func (m Model) reparentRow(row api.ThreadRow, newParent string) tea.Cmd {
	bin, env, machine := m.binaryPath, m.navEnv, m.machine
	return func() tea.Msg {
		args := []string{"thread", "reparent", "--id", row.ID}
		if newParent == "" {
			args = append(args, "--root")
		} else {
			args = append(args, "--parent", newParent)
		}
		if machine == "" || row.Machine != machine {
			args = append(args, "--machine", row.Machine)
		}
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return actionMsg{err: fmt.Errorf("reparent %q: %v: %s", row.Name, err, strings.TrimSpace(string(out)))}
		}
		return actionMsg{id: row.ID, preselect: row.ID, expand: newParent}
	}
}

// navSelected enters the selected thread. A headless thread has no pane and a dead
// thread's pane is gone, so neither can be navved to directly — entering one first
// PROMOTES it (headless -> headed) or RESUMES it (dead -> live), then jumps to the
// resulting session. That compose only works on the owning machine (promote/resume
// route to the local daemon), so cross-machine it fails LOUDLY rather than navving to
// a non-existent session (which would silently no-op). An alive headed thread skips
// straight to nav. The jump itself drives the `sesh tmux nav` primitive (outer switch +
// inner switch-client + bare-shell kick); the TUI shells out, it does not re-implement nav.
func (m Model) navSelected() tea.Cmd {
	row, ok := m.Selected()
	if !ok {
		return nil
	}
	bin, env := m.binaryPath, m.navEnv
	sidebar, resolve := m.sidebar, m.followResolver
	local := m.machine != "" && row.Machine == m.machine
	// A LOCAL thread, when we're inside its work socket's tmux, switches the current
	// client in place (no master). Otherwise: the full master nav path.
	useInClient := local && onWorkSocket(m.tmux, m.tmuxSocket)
	return func() tea.Msg {
		sessionName := row.SessionName
		// VIRTUAL: a pure grouping node — there is no agent to enter, ever
		// (Lukas's choice: a loud warning, not a fold toggle). Checked before
		// anything shells out, so the refusal is instant.
		if row.AgentKind == api.VirtualAgentKind {
			return actionMsg{err: fmt.Errorf("%q is a virtual thread (a grouping node — no agent); convert it first: sesh thread realize --id %s --agent claude|codex|pi", row.Name, row.ID)}
		}
		// DIVIDER: a visual separator — nothing to enter. Loud, before any shell-out.
		if row.AgentKind == api.DividerAgentKind {
			return actionMsg{err: fmt.Errorf("%q is a divider (a visual separator — no conversation); delete it with d", dividerLabel(row))}
		}
		// headless·busy: a turn is mid-flight — there is no pane to enter and a
		// revival would fork the conversation (the daemon would 409 anyway): loud.
		if row.Head == api.Headless && row.Busy == api.BusyBusy {
			return actionMsg{err: fmt.Errorf("%q: a headless turn is in flight — wait for it to finish (◌▶ → ◌·), then enter", row.Name)}
		}
		// headless·idle => no runtime to enter: REVIVE it first (a resumable
		// conversation, whether it last ran headed or headless), on the local
		// daemon directly or ROUTED to the owning machine (`--machine`, the same
		// mesh routing the CLI uses) — then enter.
		if row.Head == api.Headless {
			if local {
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel()
				resp, err := m.client.ThreadResume(ctx, row.ID)
				if err != nil {
					return actionMsg{err: fmt.Errorf("revive %q: %w", row.Name, err)}
				}
				sessionName = resp.Thread.SessionName
			} else {
				cmd := exec.Command(bin, "thread", "resume", "--id", row.ID, "--machine", row.Machine)
				cmd.Env = append(os.Environ(), env...)
				if out, err := cmd.CombinedOutput(); err != nil {
					return actionMsg{err: fmt.Errorf("revive %q on %s: %v: %s", row.Name, row.Machine, err, strings.TrimSpace(string(out)))}
				}
				// Revival can mint the session name — re-resolve it on the owner.
				lout, err := routedSessionName(bin, env, row.Machine, row.ID)
				if err != nil {
					return actionMsg{err: fmt.Errorf("revive %q: re-resolve session: %w", row.Name, err)}
				}
				sessionName = lout
			}
		}
		if sessionName == "" {
			return actionMsg{err: fmt.Errorf("enter %q: thread has no session", row.Name)}
		}
		target := row.Machine + ":" + sessionName
		// Outside tmux entirely (a plain shell): there's no client to switch — ATTACH
		// this terminal to the thread instead. The TUI quits and the caller execs
		// `tmux nav --attach` (which, unlike the TUI, can reach the peer registry for a
		// remote attach). Inside tmux: switch in place / via the master.
		if m.tmux == "" {
			return attachMsg{target: target, thread: row.ID}
		}
		// --thread makes nav land on the WINDOW holding this thread's pane (not the
		// session's last-active window) — resolved on the owner's work server.
		args := []string{"tmux", "nav", "--to", target, "--thread", row.ID}
		if useInClient {
			args = append(args, "--in-client")
			if m.clientName != "" {
				args = append(args, "--client", m.clientName)
			}
		}
		// A sidebar ENTER that switches master windows: tell the traveling-
		// sidebar hook to focus the ATTACH pane after it swaps the sidebar in
		// (the sibling handoff below also fires — same target, either order).
		declaredIntent := false
		if sidebar && !useInClient && resolve != nil {
			if wm := resolve(); wm != "" && wm != row.Machine {
				declareSidebarIntent("enter")
				declaredIntent = true
			}
		}
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			if declaredIntent {
				clearSidebarIntent() // no switch happened — don't leave a stale intent
			}
			return actionMsg{err: fmt.Errorf("nav %s: %v: %s", target, err, strings.TrimSpace(string(out)))}
		}
		return navDoneMsg{id: row.ID}
	}
}

// routedSessionName fetches a thread's session name from its owning machine via the
// routed CLI (`thread list --json --machine M`) — used after a routed promote/resume,
// which can mint the session name on the owner.
func routedSessionName(bin string, env []string, machine, id string) (string, error) {
	cmd := exec.Command(bin, "thread", "list", "--json", "--archived", "--machine", machine)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var th api.Thread
		if err := dec.Decode(&th); err != nil {
			return "", err
		}
		if th.ID == id {
			if th.SessionName == "" {
				return "", fmt.Errorf("thread %s has no session name on %s", id, machine)
			}
			return th.SessionName, nil
		}
	}
	return "", fmt.Errorf("thread %s not found on %s", id, machine)
}

// onWorkSocket reports whether the $TMUX value `tmux` shows we're a client on tmux
// socket `name` (its basename matches).
func onWorkSocket(tmux, name string) bool {
	if tmux == "" || name == "" {
		return false
	}
	return filepath.Base(strings.SplitN(tmux, ",", 2)[0]) == name
}

// routedVerb execs `thread <verb> --id <row>` against the row's OWNING machine,
// adding --machine when the row isn't local so the mutation ROUTES over the mesh
// (http/ssh) — exactly like rename/tag. This is why these work on remote threads:
// the local daemon doesn't own them, so a direct client call would silently miss.
// The (optional) optimistic patch is recorded and applied at KEYPRESS — the
// change renders instantly on every machine; the returned command's actionMsg
// then confirms it (re-stamping its reconcile deadline) or reverts exactly it
// on failure. Pointer receiver: callers must bind the cmd BEFORE returning m
// (`cmd := m.routedVerb(...); return m, cmd`), or the returned model may miss
// the keypress-time patch (return-operand evaluation order is unspecified).
func (m *Model) routedVerb(row api.ThreadRow, patch *rowPatch, verb string, extra ...string) tea.Cmd {
	seq := 0
	if patch != nil {
		seq = m.recordPatch(row, patch, verb)
	}
	bin, env, machine := m.binaryPath, m.navEnv, m.machine
	return func() tea.Msg {
		args := append([]string{"thread", verb, "--id", row.ID}, extra...)
		if machine == "" || row.Machine != machine {
			args = append(args, "--machine", row.Machine)
		}
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return actionMsg{err: fmt.Errorf("%s %q: %v: %s", verb, row.Name, err, strings.TrimSpace(string(out))), id: row.ID, seq: seq}
		}
		return actionMsg{id: row.ID, seq: seq}
	}
}

// rowDisplayName names a row for action messages: its name, or a short id when nameless.
func rowDisplayName(row api.ThreadRow) string {
	if row.Name != "" {
		return row.Name
	}
	if len(row.ID) >= 8 {
		return row.ID[:8]
	}
	return row.ID
}

// stopSelected ends the selected thread's runtime (agent + session) but keeps
// the record (it becomes a dead, resumable thread). Routed to the owner. The
// optimistic patch flips the state glyph to headless·idle (● → ◌) immediately —
// without it the row sat looking alive until the owner's maintainer noticed the
// pane gone AND the mesh read path caught up (seconds).
func (m *Model) stopSelected() tea.Cmd {
	row, ok := m.Selected()
	if !ok {
		return nil
	}
	head, busy := api.Headless, api.BusyIdle
	return m.routedVerb(row, &rowPatch{head: &head, busy: &busy}, "stop")
}

// forkSelected branches the selected thread's conversation into a NEW headless
// copy: `thread new --fork-from <id>` (the whole transcript, no message-id). The
// fork lands on the source's OWNING machine (its transcript lives there), so it
// routes with --machine exactly like the other verbs; --fork-from then resolves
// the source locally on that daemon. The new thread is a headless copy you can
// enter to continue from where the source left off — the source is untouched.
// It keeps the source's name marked " (fork)". On success the cursor preselects
// the new thread once the refetch lands.
func (m Model) forkSelected() tea.Cmd {
	row, ok := m.Selected()
	if !ok {
		return nil
	}
	bin, env, machine := m.binaryPath, m.navEnv, m.machine
	// Keep the source's name, marked as a fork (a nameless source forks to "(fork)").
	name := "(fork)"
	if row.Name != "" {
		name = row.Name + " (fork)"
	}
	return func() tea.Msg {
		// A VIRTUAL thread has no conversation to branch — refuse with a clear
		// message here (the daemon would refuse too, but via the opaque
		// "unknown agent" parse error, since a fork inherits the source's kind).
		if row.AgentKind == api.VirtualAgentKind {
			return actionMsg{err: fmt.Errorf("fork %q: a virtual thread has no conversation to fork — realize it first", row.Name)}
		}
		args := []string{"thread", "new", "--fork-from", row.ID, "--name", name, "--json"}
		if machine == "" || row.Machine != machine {
			args = append(args, "--machine", row.Machine)
		}
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return actionMsg{err: fmt.Errorf("fork %q: %v: %s", row.Name, err, strings.TrimSpace(string(out)))}
		}
		var forked struct {
			ID string `json:"id"`
		}
		if e := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &forked); e != nil || forked.ID == "" {
			return actionMsg{err: fmt.Errorf("fork %q: parse new thread id: %v", row.Name, e)}
		}
		// Land the cursor on the new copy once it shows up in the refetched grid.
		return actionMsg{preselect: forked.ID}
	}
}

// newVirtualRow creates a NEW virtual grouping thread named by the `v` prompt —
// a root group on the given row's machine (routed like every other action; a
// zero row = local). --no-parent is load-bearing: this subprocess inherits the
// TUI's environment, and `thread new`'s parent inference would otherwise
// silently child the group to whatever sesh thread the TUI itself runs inside.
func (m Model) newVirtualRow(row api.ThreadRow, name string) tea.Cmd {
	bin, env, machine := m.binaryPath, m.navEnv, m.machine
	return func() tea.Msg {
		args := []string{"thread", "new", "--virtual", "--name", name, "--no-parent", "--json"}
		if row.Machine != "" && (machine == "" || row.Machine != machine) {
			args = append(args, "--machine", row.Machine)
		}
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return actionMsg{err: fmt.Errorf("new virtual group %q: %v: %s", name, err, strings.TrimSpace(string(out)))}
		}
		var created struct {
			ID string `json:"id"`
		}
		if e := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &created); e != nil || created.ID == "" {
			return actionMsg{err: fmt.Errorf("new virtual group %q: parse new thread id: %v", name, e)}
		}
		// Land the cursor on the new group once it shows up in the refetched grid.
		return actionMsg{preselect: created.ID}
	}
}

// newDividerRow creates a NEW divider (a visual rule) on the selected row's machine,
// labeled by the `D` prompt (empty = unlabeled). The daemon places it at the top of
// the pinned block; reposition it with move mode / `thread pin`. Routed like `v`.
func (m Model) newDividerRow(row api.ThreadRow, name string) tea.Cmd {
	bin, env, machine := m.binaryPath, m.navEnv, m.machine
	return func() tea.Msg {
		args := []string{"thread", "new", "--divider", "--name", name, "--json"}
		if row.Machine != "" && (machine == "" || row.Machine != machine) {
			args = append(args, "--machine", row.Machine)
		}
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return actionMsg{err: fmt.Errorf("new divider: %v: %s", err, strings.TrimSpace(string(out)))}
		}
		var created struct {
			ID string `json:"id"`
		}
		if e := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &created); e != nil || created.ID == "" {
			return actionMsg{err: fmt.Errorf("new divider: parse new thread id: %v", e)}
		}
		return actionMsg{preselect: created.ID}
	}
}

// pinDoneMsg is the result of a routed pin/unpin/reorder write. The optimistic patch
// is already applied at keypress time (instant feel); success re-stamps its reconcile
// deadline, a failure surfaces loudly AND reverts exactly that step's entry (seq),
// then refetches the server's true order.
type pinDoneMsg struct {
	id  string
	seq int
	err error
}

// pinnedRowsSorted returns the currently-pinned rows (PinOrder != nil), in the exact
// order the pinned block renders them: ascending (pin_order, machine, id).
func (m Model) pinnedRowsSorted() []api.ThreadRow {
	var out []api.ThreadRow
	for _, r := range m.rows {
		if r.PinOrder != nil {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if *out[i].PinOrder != *out[j].PinOrder {
			return *out[i].PinOrder < *out[j].PinOrder
		}
		if out[i].Machine != out[j].Machine {
			return out[i].Machine < out[j].Machine
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// pinTopOrder is the order key that lands a row at the TOP of the pinned block (below
// the current minimum; 0 for an empty block).
func (m Model) pinTopOrder() float64 {
	pinned := m.pinnedRowsSorted()
	if len(pinned) == 0 {
		return 0
	}
	return *pinned[0].PinOrder - 1
}

// reorderTarget computes the order key that moves row one step (dir -1 up, +1 down)
// within the pinned block, or ok=false when it's already at that end (the block is
// bounded — there is no bottom zone to fall into). It leapfrogs the adjacent node:
// the midpoint to the node beyond it, or ±1 past the current end.
func (m Model) reorderTarget(row api.ThreadRow, dir int) (float64, bool) {
	pinned := m.pinnedRowsSorted()
	idx := -1
	for i, r := range pinned {
		if r.ID == row.ID {
			idx = i
			break
		}
	}
	if idx == -1 {
		return 0, false
	}
	if dir < 0 { // up
		if idx == 0 {
			return 0, false
		}
		if idx == 1 {
			return *pinned[0].PinOrder - 1, true
		}
		return (*pinned[idx-2].PinOrder + *pinned[idx-1].PinOrder) / 2, true
	}
	// down
	if idx == len(pinned)-1 {
		return 0, false
	}
	if idx == len(pinned)-2 {
		return *pinned[len(pinned)-1].PinOrder + 1, true
	}
	return (*pinned[idx+1].PinOrder + *pinned[idx+2].PinOrder) / 2, true
}

// applyPinPatch records and applies an optimistic pin override (order nil =
// un-pin) for row, so the block re-sorts instantly, then keeps the cursor on the
// row. recordPatch supersedes any older pin entry for the row (latest reorder
// wins — an intermediate step's obligation must not outlive it). The routed
// write (persistPin, with the returned seq) runs alongside; the reconcile poll
// GCs the entry once the server agrees (or the deadline surfaces a failure).
func (m *Model) applyPinPatch(row api.ThreadRow, order *float64) int {
	verb := "unpin"
	if order != nil {
		verb = "pin"
	}
	seq := m.recordPatch(row, &rowPatch{pinSet: true, pinOrder: order}, verb)
	m.positionCursorOn(row.ID) // the re-sort moved the row — keep the cursor on it
	return seq
}

// persistPin writes the pin change to the owning daemon (order nil = un-pin). The
// optimistic patch (entry seq) already shows the result, so this only persists +
// reports the outcome.
func (m Model) persistPin(row api.ThreadRow, order *float64, seq int) tea.Cmd {
	bin, env, machine := m.binaryPath, m.navEnv, m.machine
	return func() tea.Msg {
		var args []string
		if order == nil {
			args = []string{"thread", "unpin", "--id", row.ID}
		} else {
			args = []string{"thread", "pin", "--id", row.ID, "--order", strconv.FormatFloat(*order, 'g', -1, 64)}
		}
		if machine == "" || row.Machine != machine {
			args = append(args, "--machine", row.Machine)
		}
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return pinDoneMsg{id: row.ID, seq: seq, err: fmt.Errorf("pin %q: %v: %s", rowDisplayName(row), err, strings.TrimSpace(string(out)))}
		}
		return pinDoneMsg{id: row.ID, seq: seq}
	}
}

// handleReorderKey drives MOVE MODE: ↑/↓ (k/j) reposition the reordered row within the
// pinned block (optimistic patch + routed write per step), Enter/Esc/q commit-and-exit,
// any other key exits without acting. A no-op at a block end simply stays in the mode.
func (m Model) handleReorderKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	row, ok := m.rowByID(m.reorderID)
	if !ok { // the row vanished (deleted elsewhere) — leave the mode
		m.reordering, m.reorderID = false, ""
		return m, nil
	}
	switch msg.String() {
	case "up", "k", "down", "j":
		dir := -1
		if s := msg.String(); s == "down" || s == "j" {
			dir = 1
		}
		order, moved := m.reorderTarget(row, dir)
		if !moved {
			return m, nil // already at that end of the block
		}
		seq := m.applyPinPatch(row, &order)
		m.ensureCursorVisible()
		cmd := m.persistPin(row, &order, seq)
		return m, cmd
	case "enter", "esc", "q", "ctrl+c":
		m.reordering, m.reorderID = false, ""
		return m, nil
	default:
		// Any other key ends move mode without acting (so it can't fire a stray action).
		m.reordering, m.reorderID = false, ""
		return m, nil
	}
}

// rowByID finds a fetched row by id (after optimistic patches are applied to m.rows).
func (m Model) rowByID(id string) (api.ThreadRow, bool) {
	for _, r := range m.rows {
		if r.ID == id {
			return r, true
		}
	}
	return api.ThreadRow{}, false
}

// samePinOrder compares two optional pin-order keys (both nil, or both set & equal).
func samePinOrder(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// deleteRow drops a specific thread's record (routed to the owner). Delete removes
// the record from EVERY view, so hide it instantly (the mesh read path lags the
// write by up to one maintainer tick).
func (m *Model) deleteRow(row api.ThreadRow) tea.Cmd {
	return m.routedVerb(row, &rowPatch{hide: true}, "delete")
}

// leavesViewWith reports whether a row mutated to `next` drops out of the CURRENTLY
// displayed view — driving the optimistic hide on archive/hold so the row disappears
// at once instead of after the next mesh read.
func (m Model) leavesViewWith(next api.ThreadRow) bool {
	if i := int(m.view - viewBuiltins); i >= 0 && i < len(m.customViews) {
		return !m.customViews[i].pred.Eval(next) // the predicate no longer admits it
	}
	return !builtinViewAdmits(m.view, next)
}

// notifySelected TOGGLES the selected thread's notification gate (routed to the
// owner, with an optimistic flip so the NTF column updates instantly).
func (m *Model) notifySelected() tea.Cmd {
	row, ok := m.Selected()
	if !ok {
		return nil
	}
	want := !row.Notify
	flag := "--off"
	if want {
		flag = "--on"
	}
	return m.routedVerb(row, &rowPatch{notify: bptr(want)}, "notify", flag)
}

// flagPatch is the optimistic patch for a `thread flag` action: the target
// flagged/flagDisabled state (mirroring the daemon's one-rule semantics), plus
// a hide when the new state leaves the current view (a custom [[tui.views]]
// filter can select on flagged/flagdisabled).
func (m Model) flagPatch(row api.ThreadRow, flagged, flagDisabled bool) *rowPatch {
	patch := &rowPatch{flagged: bptr(flagged), flagDisabled: bptr(flagDisabled)}
	next := row
	next.Flagged, next.FlagDisabled = flagged, flagDisabled
	if m.leavesViewWith(next) {
		patch.hide = true
	}
	return patch
}

// flagSelected (f) toggles the selected thread's needs-attention flag:
// flagged → off; else on (which also re-enables a flag-disabled thread — the
// one-rule semantic, schema 44). The optimistic patch flips ⚑ at keypress —
// without it a remote flag sat visually dead for the whole owner-publish +
// mesh-sync + poll pipeline (the H55 complaint).
func (m *Model) flagSelected() tea.Cmd {
	row, ok := m.Selected()
	if !ok {
		return nil
	}
	if row.Flagged {
		return m.routedVerb(row, m.flagPatch(row, false, row.FlagDisabled), "flag", "--off")
	}
	return m.routedVerb(row, m.flagPatch(row, true, false), "flag", "--on")
}

// flagGateSelected (^f) toggles AUTO-flagging for the selected thread:
// disabled → enable; else disable (which also clears any current flag) —
// the parent-monitored-children knob. Optimistic like flagSelected.
func (m *Model) flagGateSelected() tea.Cmd {
	row, ok := m.Selected()
	if !ok {
		return nil
	}
	if row.FlagDisabled {
		return m.routedVerb(row, m.flagPatch(row, row.Flagged, false), "flag", "--enable")
	}
	return m.routedVerb(row, m.flagPatch(row, false, true), "flag", "--disable")
}

// archiveRow toggles a specific thread's archived state (routed to the owner). When
// the toggle removes the row from the current view it is hidden optimistically, so
// it disappears at once instead of after the next mesh read.
func (m *Model) archiveRow(row api.ThreadRow) tea.Cmd {
	newArchived := !row.Archived
	// The archived override (not just hide) lets satisfied() reconcile by FIELD in
	// a view that still admits the changed row (e.g. `all`) — a hide alone kept
	// wrongly blanking the row there until its TTL ran out.
	patch := &rowPatch{archived: bptr(newArchived)}
	next := row
	next.Archived = newArchived
	if m.leavesViewWith(next) {
		patch.hide = true
	}
	if row.Archived {
		patch.desc = fmt.Sprintf("unarchive %q", rowDisplayName(row))
		return m.routedVerb(row, patch, "archive", "--unarchive")
	}
	// The ARCHIVE direction attaches its undo entry on success (never on
	// failure — a failed archive must not become a bogus U target).
	entry := &archiveUndoEntry{id: row.ID, machine: row.Machine, name: rowDisplayName(row)}
	inner := m.routedVerb(row, patch, "archive")
	return func() tea.Msg {
		msg := inner()
		if am, ok := msg.(actionMsg); ok && am.err == nil {
			am.undoArchive = entry
			return am
		}
		return msg
	}
}

// undoLastArchive (U): un-archive the most recently archived thread (LIFO —
// repeated U walks back through this session's archives). The target comes
// from the stack, NOT the selection, so reachability is checked against ITS
// machine here rather than via the selection-based offline gate. The
// unarchive patch's real job is SUPERSEDING a still-pending archive patch for
// the same thread (recordPatch drops it), so an undo pressed before the mesh
// reflected the archive can't leave a stale archived=true obligation that
// keeps the row hidden until it expires into a bogus sync warning.
func (m Model) undoLastArchive() (tea.Model, tea.Cmd) {
	if len(m.archiveUndo) == 0 {
		m.note = "nothing to un-archive (U undoes this session's archives)"
		return m, nil
	}
	e := m.archiveUndo[len(m.archiveUndo)-1]
	if !m.machineReachable(e.machine) {
		m.actionErr = fmt.Errorf("%s is offline — can't un-archive %q until it reconnects", e.machine, e.name)
		return m, nil
	}
	m.archiveUndo = m.archiveUndo[:len(m.archiveUndo)-1]
	m.note = fmt.Sprintf("un-archived %q", e.name)
	row := api.ThreadRow{Thread: api.Thread{ID: e.id, Machine: e.machine, Name: e.name}}
	patch := &rowPatch{archived: bptr(false), desc: fmt.Sprintf("unarchive %q", e.name)}
	inner := m.routedVerb(row, patch, "archive", "--unarchive")
	return m, func() tea.Msg {
		msg := inner()
		if am, ok := msg.(actionMsg); ok && am.err == nil {
			am.preselect = e.id // land the cursor on the restored thread
			return am
		}
		return msg
	}
}

// holdToggleSelected manages the selected thread's OWN hold: if its own hold is
// active it RELEASES (clears own); otherwise it parks until the start of tomorrow. The
// decision is on the OWN hold, not the effective one — a thread held only by an inherited
// parent hold has no own hold to release, so `h` sets its own (you can't un-hold a child
// below its parent; that follows the max() rule and is reflected on the next fetch).
func (m *Model) holdToggleSelected() tea.Cmd {
	row, ok := m.Selected()
	if !ok {
		return nil
	}
	if row.OnHoldUntilUnix > time.Now().Unix() { // own hold active → release it
		return m.holdRow(row, 0)
	}
	return m.holdRow(row, startOfTomorrowUnix())
}

// holdRow sets (untilUnix > now) or clears (untilUnix == 0) the thread's OWN hold,
// routed to the owner. untilUnix is an ABSOLUTE instant computed here against the
// VIEWER's clock — i.e. the user's own "tomorrow". Setting a future hold makes the
// thread on-hold for sure, so it flips + hides optimistically. CLEARING flips
// optimistically ONLY when the effective hold IS the thread's own (no dominating
// inherited parent hold — effective ≤ own): clearing then definitely un-holds it.
// A child held higher by an ancestor stays non-optimistic — only the owning daemon
// derives the inherited max (H26), so let the next fetch reconcile that case.
func (m *Model) holdRow(row api.ThreadRow, untilUnix int64) tea.Cmd {
	if untilUnix <= time.Now().Unix() {
		return m.routedVerb(row, m.holdClearPatch(row), "hold", "--clear")
	}
	patch := &rowPatch{onHold: bptr(true)}
	next := row
	next.OnHold = true
	if m.leavesViewWith(next) {
		patch.hide = true
	}
	return m.routedVerb(row, patch, "hold", "--until-unix", fmt.Sprintf("%d", untilUnix))
}

// holdClearPatch is the optimistic patch for clearing row's own hold — or nil when
// a dominating INHERITED hold (effective > own) means clearing won't un-hold it;
// only the owning daemon derives the inherited max (H26), so that case stays
// non-optimistic and the next fetch reconciles it.
func (m Model) holdClearPatch(row api.ThreadRow) *rowPatch {
	if row.OnHoldEffectiveUnix > row.OnHoldUntilUnix {
		return nil
	}
	patch := &rowPatch{onHold: bptr(false), desc: fmt.Sprintf("release hold on %q", rowDisplayName(row))}
	next := row
	next.OnHold = false
	if m.leavesViewWith(next) {
		patch.hide = true
	}
	return patch
}

// startOfTomorrowUnix is midnight at the start of the next LOCAL day — the default
// hold deadline, so a held thread auto-returns to the active view tomorrow.
func startOfTomorrowUnix() int64 {
	t := time.Now()
	return time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location()).Unix()
}

// holdDatePrefill is the `H` prompt's prefilled value: the current hold date (so it
// can be edited in place) or empty when the thread isn't on hold.
func holdDatePrefill(row api.ThreadRow) string {
	if row.OnHoldUntilUnix == 0 {
		return ""
	}
	return time.Unix(row.OnHoldUntilUnix, 0).Format("2006-01-02")
}

// Selected returns the VISIBLE row under the cursor, if any (the filter and
// the tree's fold state decide what the cursor moves over).
func (m Model) Selected() (api.ThreadRow, bool) {
	tr, ok := m.selectedTree()
	return tr.row, ok
}

// Rows exposes the current rows (for tests).
func (m Model) Rows() []api.ThreadRow { return m.rows }

// CurrentView exposes the active view (for tests).
func (m Model) CurrentView() View { return m.view }

// Cursor exposes the cursor index (for tests).
func (m Model) Cursor() int { return m.cursor }

// VOffset / HOffset expose the scroll offsets (for tests).
func (m Model) VOffset() int { return m.vOffset }
func (m Model) HOffset() int { return m.hOffset }

// Prompting reports whether the line-prompt is open (for tests).
func (m Model) Prompting() bool { return m.prompting != promptNone }

// LastErr exposes the most recent fetch/daemon-reachability error (for tests).
func (m Model) LastErr() error { return m.lastErr }

// ActionErr exposes the most recent in-app ACTION error (reparent/delete/…),
// which persists across reconcile fetches (for tests).
func (m Model) ActionErr() error { return m.actionErr }

// Confirming reports whether a y/n destructive-action popup is open (for tests).
func (m Model) Confirming() bool { return m.confirming != confirmNone }

// MaxColWidth reports whether the per-column width cap is on (for tests).
func (m Model) MaxColWidth() bool { return m.maxColWidth }

// DetailsOpen reports whether the thread-details takeover is open (for tests).
func (m Model) DetailsOpen() bool { return m.detailsPopup }

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ---- rendering ----

var (
	styleHeader   = lipgloss.NewStyle().Bold(true)
	styleSelected = lipgloss.NewStyle().Reverse(true)
	styleDim      = lipgloss.NewStyle().Faint(true)
	styleMatch    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	styleErr      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196"))
)

// helpBindings is the full keymap, one entry per line in the `?` popup (the
// bottom line carries only legendHint — the always-on wrapped legend ate 3-4
// rows of every frame). The popup SCROLLS when the list overflows the
// terminal height, so every binding stays reachable on any size (the H1
// no-clipping lesson, vertical edition).
var helpBindings = []struct{ key, desc string }{
	{"↑/↓", "move the selection"},
	{"^j/^k", "scroll the viewport"},
	{"←/→", "collapse / expand the tree fold"},
	{"^h/^l", "pan columns"},
	{"enter", "enter the thread (revives a dead one)"},
	{"/", "filter mode (fuzzy)"},
	{"tab", "view picker (tab/↑↓ move · enter apply · esc cancel · click a view)"},
	{"f", "toggle the needs-attention flag ⚑"},
	{"ctrl+f", "toggle auto-flagging (⌀ when disabled)"},
	{"h", "hold until tomorrow / release"},
	{"H", "hold until a date (prompt)"},
	{"r", "rename"},
	{"t", "add a tag"},
	{"T", "remove a tag"},
	{"P", "set the parent thread"},
	{"v", "new virtual group"},
	{"p", "pin to the top block"},
	{"u", "unpin"},
	{"m", "move mode (reorder pinned rows)"},
	{"D", "new divider"},
	{"K", "tickets view"},
	{"I", "thread details"},
	{"i", "toggle the ID column"},
	{"w", "toggle the column-width cap"},
	{"y", "show the full UUID (c copies)"},
	{"n", "toggle notifications"},
	{"F", "fork the thread (headless copy)"},
	{"x", "stop the runtime (keep the record)"},
	{"a", "archive / unarchive (instant)"},
	{"U", "undo the last archive"},
	{"d", "delete (y/n confirm)"},
	{"o", "show / hide offline machines"},
	{"R", "force refresh"},
	{"?", "this help"},
	{"q/esc", "quit"},
	{"", ""},
	{"click", "select the row (double-click enters it)"},
	{"▸/▾", "click the fold marker to collapse/expand"},
	{"wheel", "move the selection (shift+wheel pans)"},
}

// legendHint is the one always-visible bottom line: how to see the keymap.
const legendHint = "? keys"

// reorderLegendText is the legend while MOVE MODE is active — only the reposition
// keys are live, so it REPLACES the hint (mode feedback must stay ambient).
const reorderLegendText = "MOVE MODE — ↑/↓ reposition the pinned row · enter/esc done"

// renderLegend renders the bottom legend line: the `?` hint normally, the move-mode
// keymap while reordering (wrapped to width — never clipped).
func (m Model) renderLegend() string {
	text := legendHint
	if m.reordering {
		text = reorderLegendText
	}
	if m.width > 1 {
		return styleDim.Width(m.width).Render(text)
	}
	return styleDim.Render(text)
}

// helpVisibleRows is how many binding lines fit: the height minus the fixed
// chrome (title, the two more-indicators, the footer). Height unknown (tests,
// no WindowSizeMsg yet) = everything.
func (m Model) helpVisibleRows() int {
	if m.height <= 0 {
		return len(helpBindings)
	}
	avail := m.height - 4
	if avail < 1 {
		avail = 1
	}
	if avail > len(helpBindings) {
		avail = len(helpBindings)
	}
	return avail
}

// helpMaxOffset is the largest useful scroll offset.
func (m Model) helpMaxOffset() int {
	return len(helpBindings) - m.helpVisibleRows()
}

// helpView is the `?` popup: a full-screen takeover listing every binding on
// its own line, scrollable when the list overflows the terminal height. The
// two indicator lines are ALWAYS present (blank when unneeded) so the layout
// is stable while scrolling.
func (m Model) helpView() string {
	avail := m.helpVisibleRows()
	off := m.helpOffset
	if max := m.helpMaxOffset(); off > max {
		off = max
	}
	var b strings.Builder
	b.WriteString(styleHeader.Render("sesh — keys") + "\n")
	if off > 0 {
		b.WriteString(styleDim.Render(fmt.Sprintf("  ▲ %d more", off)) + "\n")
	} else {
		b.WriteString("\n")
	}
	for _, e := range helpBindings[off : off+avail] {
		line := fmt.Sprintf("  %-8s %s", e.key, e.desc)
		if m.width > 1 {
			if r := []rune(line); len(r) > m.width {
				line = string(r[:m.width-1]) + "…"
			}
		}
		b.WriteString(styleDim.Render(line) + "\n")
	}
	if rest := len(helpBindings) - off - avail; rest > 0 {
		b.WriteString(styleDim.Render(fmt.Sprintf("  ▼ %d more", rest)) + "\n")
	} else {
		b.WriteString("\n")
	}
	b.WriteString(styleDim.Render("↑/↓ scroll · esc/q/? close") + "\n")
	return b.String()
}

// handleHelpKey: ↑/↓ (and j/k, ^j/^k) scroll the keymap; esc/q/?/enter close.
// handleViewPickerKey drives the Tab view picker: tab/↓/j advance (wrap),
// shift+tab/↑/k go back, Enter applies the highlighted view, esc/q/ctrl+c
// cancel. Applying resets the cursor and refetches, exactly as the old blind
// cycle did.
func (m Model) handleViewPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	n := m.viewCount()
	switch msg.String() {
	case "tab", "down", "j":
		m.viewPickerCursor = (m.viewPickerCursor + 1) % n
	case "shift+tab", "up", "k":
		m.viewPickerCursor = (m.viewPickerCursor - 1 + n) % n
	case "enter":
		return m.applyPickedView(m.viewPickerCursor)
	case "esc", "q", "ctrl+c":
		m.viewPicker = false
	}
	return m, nil
}

// applyPickedView switches to view i, closes the picker, and refetches.
func (m Model) applyPickedView(i int) (tea.Model, tea.Cmd) {
	m.viewPicker = false
	if i < 0 || i >= m.viewCount() {
		return m, nil
	}
	m.view = View(i)
	m.cursor = 0
	return m, m.fetch()
}

// viewPickerView renders the Tab view picker: one view per line, the selection
// marked, the active view annotated. The layout is deliberately fixed — title +
// blank, rows from line 2 — because the mouse handler maps click Y back to a
// row (viewPickerRowAtY must mirror this).
func (m Model) viewPickerView() string {
	var b strings.Builder
	b.WriteString(styleHeader.Render("view · tab/↑↓ move · enter apply · esc cancel") + "\n\n")
	for i := 0; i < m.viewCount(); i++ {
		marker := "  "
		if i == m.viewPickerCursor {
			marker = "> "
		}
		line := marker + m.viewNameAt(i)
		if View(i) == m.view {
			line += "  (current)"
		}
		if i == m.viewPickerCursor {
			b.WriteString(styleSelected.Render(line) + "\n")
		} else {
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

// viewPickerRowAtY maps a click's terminal row to a picker index (mirrors
// viewPickerView's fixed layout: rows start at line 2). ok=false outside.
func (m Model) viewPickerRowAtY(y int) (int, bool) {
	i := y - 2
	if i < 0 || i >= m.viewCount() {
		return 0, false
	}
	return i, true
}

func (m Model) handleHelpKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q", "?", "enter":
		m.helpPopup, m.helpOffset = false, 0
	case "up", "k", "ctrl+k":
		if m.helpOffset > 0 {
			m.helpOffset--
		}
	case "down", "j", "ctrl+j":
		if m.helpOffset < m.helpMaxOffset() {
			m.helpOffset++
		}
	}
	return m, nil
}

// HelpOpen reports whether the `?` keymap popup is up (tests).
func (m Model) HelpOpen() bool { return m.helpPopup }

// legendLines is the wrapped legend's height in lines (for the scroll budget —
// chromeLines). 1 when the width is unknown (unwrapped).
func (m *Model) legendLines() int {
	return strings.Count(m.renderLegend(), "\n") + 1
}

// Glyph maps a row's live state to a status glyph. These are part of the contract
// the TUI conformance asserts against REAL state.
//
// HeadGlyph renders the runtime-form axis:
//
//	● headful (a live pane — enterable)   ◌ headless (no pane)   ? unknown
//	◇ virtual (a grouping node — no agent at all; realize to convert)
func HeadGlyph(row api.ThreadRow) string {
	if row.AgentKind == api.VirtualAgentKind {
		return "◇"
	}
	if row.AgentKind == api.DividerAgentKind {
		return "─" // dividers render as a full rule (see the View loop); this is a fallback
	}
	switch row.Head {
	case api.Headful:
		return "●"
	case api.Headless:
		return "◌"
	default:
		return "?"
	}
}

// BusyGlyph renders the execution axis:
//
//	▶ busy (a turn is executing)   · idle (quiet)   ? unknown
//
// (Attention states render in the FLAG gutter cell — see FlagGlyph — not
// here; the 43-era ‼/✔ overlays were replaced by the flagged system.)
func BusyGlyph(row api.ThreadRow) string {
	switch row.Busy {
	case api.BusyBusy:
		return "▶"
	case api.BusyIdle:
		return "·"
	default:
		return "?"
	}
}

// FlagGlyph renders the flagged system's gutter cell (schema 44):
//
//	⚑ flagged (needs your attention — auto-set on unattended turn ends and
//	question stalls, or manually; NEVER auto-cleared)   ⌀ flagging disabled
//	(auto-flags suppressed — e.g. a parent-monitored child)   ' ' neither
func FlagGlyph(row api.ThreadRow) string {
	if row.Flagged {
		return "⚑"
	}
	if row.FlagDisabled {
		return "⌀"
	}
	return " "
}

// ArchivedGlyph renders the archived slot: a marker on an ARCHIVED thread, blank
// otherwise. Archived threads now surface in the default view while still headful
// (see builtinViewAdmits), so this distinguishes them at a glance without opening the
// opt-in ARCHIVED (date) column.
//
//	⊘ archived   (blank) not archived
func ArchivedGlyph(row api.ThreadRow) string {
	if row.Archived {
		return "⊘"
	}
	return " "
}

// pinMark renders the 1-cell manual-ordering marker at the very left of a row: ↕
// while the row is being MOVED (move mode), • when it's PINNED (in the block above
// the auto-sorted list), a space otherwise (so columns stay aligned across all rows).
func pinMark(row api.ThreadRow, moving bool) string {
	switch {
	case moving:
		return "↕"
	case row.PinOrder != nil:
		return "•"
	default:
		return " "
	}
}

// dividerLabel is a divider's display label, falling back to "divider" (for messages
// where a name is expected); an unlabeled divider renders as a bare rule elsewhere.
func dividerLabel(row api.ThreadRow) string {
	if row.Name != "" {
		return row.Name
	}
	return "divider"
}

// renderDividerLine draws a divider row as a full-width horizontal rule with its
// optional label ("── today ─────"); an unlabeled divider is a bare rule. A ↕ head
// shows while it's being moved. Selected = reverse video, else dim.
func (m Model) renderDividerLine(row api.ThreadRow, selected bool) string {
	width := m.width
	if width <= 0 {
		width = 40 // unknown width (tests / pre-resize): a deterministic fixed rule
	}
	avail := width - 2 // the 2-cell leading ("> " / "  ")
	if avail < 6 {
		avail = 6
	}
	head := "──"
	if m.reordering && row.ID == m.reorderID {
		head = "↕─"
	}
	text := head
	if row.Name != "" {
		text = head + " " + row.Name + " "
	}
	if n := len([]rune(text)); n < avail {
		text += strings.Repeat("─", avail-n)
	} else {
		text = string([]rune(text)[:avail])
	}
	if selected {
		return styleSelected.Render("> " + text)
	}
	return styleDim.Render("  " + text)
}

// DescendantGlyph renders the descendant-activity slot (the third HB glyph): a
// downward arrow when at least one descendant thread (child, grandchild, …) is
// running a turn, blank otherwise. It surfaces work happening BELOW a thread even
// when that descendant is folded away or filtered out of the current view.
//
//	↓ a descendant is busy   (blank) none running
func DescendantGlyph(running bool) string {
	if running {
		return "↓"
	}
	return " "
}

// descendantsRunning returns the set of thread ids that have at least one
// descendant (child, grandchild, …) currently running a turn (Busy==BusyBusy).
// Computed over ALL fetched rows so the indicator stays true even when the running
// descendant is collapsed under a folded parent or hidden by the active view.
func descendantsRunning(rows []api.ThreadRow) map[string]bool {
	parent := make(map[string]string, len(rows))
	exists := make(map[string]bool, len(rows))
	for _, r := range rows {
		parent[r.ID] = r.Parent
		exists[r.ID] = true
	}
	out := make(map[string]bool)
	for _, r := range rows {
		if r.Busy != api.BusyBusy {
			continue
		}
		// Walk up from the running thread, marking every ancestor. A node marks its
		// ancestors, never itself (its own busy axis already shows ▶). Guard against a
		// cycle on the wire (the daemon forbids them, but never trust it) with a seen set.
		seen := map[string]bool{}
		for p := r.Parent; p != "" && exists[p] && !seen[p]; p = parent[p] {
			seen[p] = true
			out[p] = true
		}
	}
	return out
}

// View renders the grid. Kept pure (Model -> string) so a snapshot test can assert
// the rendered output reflects the model's REAL rows.
func (m Model) View() string {
	if m.ticketMode != ticketNone {
		return m.ticketView() // full-screen takeover
	}
	if m.detailsPopup {
		return m.detailsView() // full-screen takeover: all fields of one thread
	}
	if m.helpPopup {
		return m.helpView() // full-screen takeover: the complete keymap
	}
	if m.viewPicker {
		return m.viewPickerView() // full-screen takeover: pick a view (Tab)
	}
	var b strings.Builder
	b.WriteString(styleHeader.Render("sesh — live threads · ["+m.viewName()+"]") + "\n")
	if m.lastErr != nil {
		b.WriteString(styleDim.Render("(daemon unreachable: "+m.lastErr.Error()+")") + "\n")
	}
	// Action errors are LOUD and persist until the next action (unlike the dim
	// daemon-reachability note) — a failed reparent/delete must not vanish silently.
	if m.actionErr != nil {
		b.WriteString(styleErr.Render("✗ "+m.actionErr.Error()) + "\n")
	}
	if m.note != "" {
		b.WriteString(styleDim.Render(m.note) + "\n")
	}
	if m.confirming != confirmNone {
		b.WriteString(styleHeader.Render(fmt.Sprintf("┃ delete %q? ┃", m.confirmRow.Name)) + "\n")
		b.WriteString(styleDim.Render("  y to confirm · any other key to cancel") + "\n")
	}
	if m.uuidPopup {
		if row, ok := m.Selected(); ok {
			b.WriteString(styleHeader.Render("┃ "+row.ID+" ┃") + "\n")
			b.WriteString(styleDim.Render("  c to copy · any other key to close") + "\n")
		}
	}
	if m.tagPopup {
		b.WriteString(styleHeader.Render("┃ remove tag · "+m.tagPopupRow.Name+" ┃") + "\n")
		for i, tag := range m.tagPopupRow.Tags {
			if i == m.tagPopupCursor {
				b.WriteString(styleSelected.Render("  > "+tag) + "\n")
			} else {
				b.WriteString("    " + tag + "\n")
			}
		}
		b.WriteString(styleDim.Render("  ↑/↓ move · enter remove · esc close") + "\n")
	}
	if m.prompting != promptNone {
		label, target := "rename", m.promptRow.Name
		switch m.prompting {
		case promptTag:
			label = "tag"
		case promptReparent:
			label = "parent uuid (empty=root)"
		case promptHold:
			label = "hold until YYYY-MM-DD (empty=clear)"
		case promptNewVirtual:
			// The row is only a machine carrier here — show WHERE the group will
			// be created, not the selected thread's name.
			label = "new virtual group (empty=cancel)"
			target = m.promptRow.Machine
			if target == "" {
				target = m.machine
			}
			if target == "" {
				target = "local"
			}
		case promptNewDivider:
			// The row is only a machine carrier here (the divider is created on the
			// selected row's machine) — show WHERE, not the selected thread's name.
			label = "new divider label (empty=unlabeled)"
			target = m.promptRow.Machine
			if target == "" {
				target = m.machine
			}
			if target == "" {
				target = "local"
			}
		}
		b.WriteString(styleHeader.Render(fmt.Sprintf("%s %q> %s", label, target, renderPromptInput(m.promptInput, m.promptCursor))) + "\n")
	}
	cols := m.activeColumns()
	vis := m.visibleMatches()
	widths := m.colWidths(cols, vis)

	// Horizontal column window: pan with h/l. Unknown width (no WindowSizeMsg) shows
	// every column. ‹/› in the header flag columns clipped off the left/right. The
	// trailing column may be partially rendered (clipped width) so a too-narrow pane
	// shows what fits of NAME instead of dropping it — see horizontalView.
	hStart, hEnd := 0, len(cols)
	vwidths := widths
	if m.width > 0 {
		hStart, hEnd, vwidths = horizontalView(cols, widths, m.hOffset, m.width-gutterWidth)
	}
	vcols := cols[hStart:hEnd]
	// descRun marks threads with a running descendant — the third HB glyph (↓).
	descRun := descendantsRunning(m.rows)
	// Gutter header, aligned under the per-row state gutter (see gutterHeader/gutterWidth).
	hdr := gutterHeader + m.renderHeader(vcols, vwidths)
	if hStart > 0 {
		hdr = "‹" + hdr[1:] // a column is clipped to the left
	}
	if hEnd < len(cols) {
		hdr += " ›"
	}
	b.WriteString(styleHeader.Render(hdr) + "\n")

	if len(vis) == 0 {
		b.WriteString(styleDim.Render("  (no threads)") + "\n")
	}
	// Vertical row window: the viewport [start:end). Unknown height shows every row.
	start, end := 0, len(vis)
	if m.height > 0 {
		start = m.vOffset
		if start > len(vis) {
			start = len(vis)
		}
		end = start + m.bodyHeight()
		if end > len(vis) {
			end = len(vis)
		}
	}
	if start > 0 {
		b.WriteString(styleDim.Render(fmt.Sprintf("  ▲ %d more", start)) + "\n")
	}
	for i := start; i < end; i++ {
		tr := vis[i]
		row := tr.row
		selected := i == m.cursor
		// A DIVIDER renders as a full-width horizontal rule (with its optional label),
		// not as a normal thread row — it has no state/columns.
		if row.AgentKind == api.DividerAgentKind {
			b.WriteString(m.renderDividerLine(row, selected) + "\n")
			continue
		}
		att := " "
		if row.Attachment == api.Attached {
			att = "*" // a client is attached
		}
		// A 1-cell manual-ordering marker: ↕ while this row is being moved, ▏ when it's
		// pinned (in the block above the auto list), a space otherwise (keeps columns aligned).
		mark := pinMark(row, m.reordering && row.ID == m.reorderID)
		desc := DescendantGlyph(descRun[row.ID])
		arch := ArchivedGlyph(row)
		flag := FlagGlyph(row)
		if selected {
			// The selected row uses reverse video; matched-rune styling AND per-column
			// colour inside it would reset the reverse — selection is the dominant cue.
			line := mark + HeadGlyph(row) + BusyGlyph(row) + desc + att + arch + flag + " " + m.renderCells(vcols, vwidths, tr, nil, false)
			b.WriteString(styleSelected.Render("> "+line) + "\n")
		} else {
			// Running-state glyphs render tinted ([[tui.glyph_color]]; default bright
			// green — ▶ this thread's turn, ↓ a descendant's); the selected branch
			// above stays untinted — reverse video is the dominant cue.
			busy := BusyGlyph(row)
			if st, ok := m.glyphColors[GlyphBusy]; ok && row.Busy == api.BusyBusy {
				busy = st.Render(busy)
			}
			if st, ok := m.glyphColors[GlyphDescendant]; ok && descRun[row.ID] {
				desc = st.Render(desc)
			}
			if st, ok := m.glyphColors[GlyphFlag]; ok && row.Flagged {
				flag = st.Render(flag)
			}
			line := mark + HeadGlyph(row) + busy + desc + att + arch + flag + " " + m.renderCells(vcols, vwidths, tr, tr.pos, true)
			b.WriteString("  " + line + "\n")
		}
	}
	if end < len(vis) {
		b.WriteString(styleDim.Render(fmt.Sprintf("  ▼ %d more", len(vis)-end)) + "\n")
	}
	// Per-machine freshness (offline browsing): show every peer's staleness, and flag any
	// that are offline. When hidden (the default), the OFFLINE line reports how many
	// last-known threads are tucked away + the `o` toggle to reveal them, so nothing
	// silently disappears; when shown, its threads are listed above (dimmed context).
	for _, mv := range m.machines {
		if mv.Self {
			continue
		}
		age := m.fetchedAt - mv.SyncedAtUnix
		switch {
		case mv.Reachable:
			b.WriteString(styleDim.Render(fmt.Sprintf("  %s · synced %ds ago", mv.Machine, age)) + "\n")
		case m.hideOffline:
			b.WriteString(styleDim.Render(fmt.Sprintf("  ! %s OFFLINE · %d threads hidden · last seen %ds ago · o to show", mv.Machine, len(mv.Threads), age)) + "\n")
		default:
			b.WriteString(styleDim.Render(fmt.Sprintf("  ! %s OFFLINE · last seen %ds ago · o to hide", mv.Machine, age)) + "\n")
		}
	}
	switch {
	case m.filtering:
		b.WriteString("\n" + m.renderFilterPrompt(len(vis), len(m.rows)) + "\n")
	case m.filter != "":
		b.WriteString(styleDim.Render(fmt.Sprintf("\n  filter: %s (%d/%d) · / to edit", m.filter, len(vis), len(m.rows))) + "\n")
		b.WriteString(m.renderLegend() + "\n")
	default:
		b.WriteString("\n" + m.renderLegend() + "\n")
	}
	return b.String()
}

// detailsView renders the full-screen thread-details takeover (`I`): every field of
// the selected thread — the record plus its live runtime axes — as an aligned
// label/value list. Read-only; esc/q/enter return to the grid.
func (m Model) detailsView() string {
	r := m.detailsRow
	type kv struct{ k, v string }
	fields := []kv{
		{"id", r.ID},
		{"name", r.Name},
		{"machine", r.Machine},
		{"agent", detailAgent(r)},
		{"model", orDash(r.Model)},
		{"state", fmt.Sprintf("%s %s  (head=%s busy=%s attach=%s)",
			HeadGlyph(r), BusyGlyph(r), orUnknown(string(r.Head)), orUnknown(string(r.Busy)), orUnknown(string(r.Attachment)))},
		{"session", orDash(r.SessionName)},
		{"cwd", orDash(r.Cwd)},
		{"cwd (rel)", orDash(m.cwdDisplay(r))},
		{"parent", detailParent(r)},
		{"tags", detailTags(r.Tags)},
		{"created", detailTime(r.CreatedAtUnix)},
		{"archived", detailArchived(r)},
		{"on hold", detailHold(r)},
		{"notify", yesNo(r.Notify)},
		{"tickets open", strconv.Itoa(r.TicketsOpen)},
		{"ticket name", detailTicket(r)},
		{"agent session", orDash(r.AgentSessionID)},
		{"started", yesNo(r.HeadlessStarted)},
	}
	// Meta is arbitrary per-thread KV — list each pair (sorted for a stable view).
	if len(r.Meta) > 0 {
		keys := make([]string, 0, len(r.Meta))
		for k := range r.Meta {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fields = append(fields, kv{"meta." + k, r.Meta[k]})
		}
	}
	labelW := 0
	for _, f := range fields {
		if n := len([]rune(f.k)); n > labelW {
			labelW = n
		}
	}
	var b strings.Builder
	title := r.Name
	if title == "" {
		title = tid8(r.ID)
	}
	b.WriteString(styleHeader.Render("thread details · "+title) + "\n\n")
	for _, f := range fields {
		b.WriteString("  " + styleDim.Render(pad(f.k, labelW)) + "  " + f.v + "\n")
	}
	b.WriteString("\n" + styleDim.Render("  esc/q to close") + "\n")
	return b.String()
}

func detailAgent(r api.ThreadRow) string {
	if r.AgentKind == api.VirtualAgentKind {
		return "virtual (grouping node — no agent)"
	}
	return orDash(r.AgentKind)
}

func detailParent(r api.ThreadRow) string {
	if r.Parent == "" {
		return "(root)"
	}
	return r.Parent
}

func detailTags(tags []string) string {
	if len(tags) == 0 {
		return "—"
	}
	return strings.Join(tags, ", ")
}

func detailTime(unix int64) string {
	if unix == 0 {
		return "—"
	}
	return time.Unix(unix, 0).Format("2006-01-02 15:04:05")
}

func detailArchived(r api.ThreadRow) string {
	if !r.Archived {
		return "no"
	}
	if r.ArchivedAtUnix == 0 {
		return "yes"
	}
	return "yes · " + detailTime(r.ArchivedAtUnix)
}

// detailHold reports the hold state: not held, or the effective deadline plus (when
// it differs from this thread's own) a note that it is inherited from an ancestor.
func detailHold(r api.ThreadRow) string {
	if !r.OnHold || r.OnHoldEffectiveUnix == 0 {
		return "no"
	}
	eff := time.Unix(r.OnHoldEffectiveUnix, 0).Format("2006-01-02")
	if r.OnHoldEffectiveUnix > r.OnHoldUntilUnix {
		return "until " + eff + " (inherited)"
	}
	return "until " + eff
}

func detailTicket(r api.ThreadRow) string {
	if r.TicketName == "" {
		return "—"
	}
	s := r.TicketName
	if r.TicketNeedsInput {
		s += " (needs input)"
	}
	return s
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func orUnknown(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// trunc shortens to n COLUMNS' worth of runes (byte slicing would split
// multi-byte runes — session/thread names contain non-ASCII).
func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
