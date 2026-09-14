package tui

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/api"
)

// trackModel is a sidebar with three rows and tracking armed, cursor on "a".
func trackModel() Model {
	m := Model{sidebar: true, rows: rowsWith("a", "b", "c"), width: 40, height: 20}
	m.masterTracking = true
	m.masterTrackCountdown = masterTrackBackstopTicks
	return m
}

func cursorID(t *testing.T, m Model) string {
	t.Helper()
	row, ok := m.Selected()
	if !ok {
		return ""
	}
	return row.ID
}

// TestMasterTrackDue pins the cadence truth table: a rung bell resolves at once (and
// restarts the backstop, since it just produced a fresh reading), an expired countdown
// resolves anyway, and an ordinary tick spends nothing but a file read.
func TestMasterTrackDue(t *testing.T) {
	cases := []struct {
		name              string
		bell, last        string
		countdown         int
		wantDue           bool
		wantNextCountdown int
	}{
		{"bell rang", "9", "8", 7, true, masterTrackBackstopTicks},
		{"bell rang from absent", "9", "", 7, true, masterTrackBackstopTicks},
		{"quiet tick", "9", "9", 7, false, 6},
		{"backstop expires", "9", "9", 1, true, masterTrackBackstopTicks},
		{"no bell ever, backstop still runs", "", "", 1, true, masterTrackBackstopTicks},
		{"no bell ever, quiet", "", "", 5, false, 4},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			due, next := masterTrackDue(c.bell, c.last, c.countdown)
			if due != c.wantDue || next != c.wantNextCountdown {
				t.Fatalf("masterTrackDue(%q,%q,%d) = (%v,%d), want (%v,%d)",
					c.bell, c.last, c.countdown, due, next, c.wantDue, c.wantNextCountdown)
			}
		})
	}
}

// TestMasterTrackMovesCursorOnCockpitChange: the cockpit moved to another thread, so
// the sidebar's cursor moves with it — the reported bug.
func TestMasterTrackMovesCursorOnCockpitChange(t *testing.T) {
	m := trackModel()
	if got := cursorID(t, m); got != "a" {
		t.Fatalf("baseline cursor = %q, want a", got)
	}
	nm, cmd := m.Update(masterCursorMsg{id: "c", ok: true})
	got := nm.(Model)
	if id := cursorID(t, got); id != "c" {
		t.Fatalf("cursor = %q after the cockpit moved to c, want c", id)
	}
	if got.lastMasterThread != "c" {
		t.Errorf("lastMasterThread = %q, want c", got.lastMasterThread)
	}
	// The cockpit is ALREADY on c, so the move must not arm a follow back to it.
	if got.lastFollowedID != "c" {
		t.Errorf("lastFollowedID = %q, want c so the tracker does not nav back", got.lastFollowedID)
	}
	if cmd != nil {
		t.Errorf("tracking a cockpit move must issue no command, got one")
	}
}

// TestMasterTrackDoesNotFightTheUser is the load-bearing one. Arrowing the sidebar onto
// a row the follow policy deliberately skips (a headless thread — a preview must never
// revive) leaves the cockpit where it was. A tracker that moved on "the cockpit
// disagrees with the cursor" would yank the cursor straight back and make the sidebar
// unbrowsable; one that moves only on a CHANGE leaves it alone.
func TestMasterTrackDoesNotFightTheUser(t *testing.T) {
	m := trackModel()
	m.lastMasterThread = "a" // the cockpit is showing a...
	m.cursor = 2             // ...and the user has arrowed down to c
	nm, _ := m.Update(masterCursorMsg{id: "a", ok: true})
	if id := cursorID(t, nm.(Model)); id != "c" {
		t.Fatalf("cursor = %q — an unchanged cockpit yanked the user's selection back", id)
	}
}

// TestMasterTrackIgnoresNoInformation: a failed resolve, or no readable cockpit
// context, must change nothing — and must NOT be recorded as "the cockpit shows
// nothing", or the next successful resolve would look like a change and jump the cursor.
func TestMasterTrackIgnoresNoInformation(t *testing.T) {
	m := trackModel()
	m.lastMasterThread = "b"
	m.cursor = 2
	nm, _ := m.Update(masterCursorMsg{}) // ok:false
	got := nm.(Model)
	if id := cursorID(t, got); id != "c" {
		t.Fatalf("cursor = %q, want c — a no-information resolve moved the cursor", id)
	}
	if got.lastMasterThread != "b" {
		t.Fatalf("lastMasterThread = %q, want b — no-information overwrote the baseline", got.lastMasterThread)
	}
}

// TestMasterTrackEmptyThreadIsObservedNotFollowed: the master window moved onto a
// plain-shell pane. That IS a change (record it, so navigating back to the previous
// thread later reads as a change again), but there is no row to move to.
func TestMasterTrackEmptyThreadIsObservedNotFollowed(t *testing.T) {
	m := trackModel()
	m.lastMasterThread = "b"
	m.cursor = 1
	nm, _ := m.Update(masterCursorMsg{id: "", ok: true})
	got := nm.(Model)
	if id := cursorID(t, got); id != "b" {
		t.Fatalf("cursor = %q, want b — a shell pane must not move the cursor", id)
	}
	if got.lastMasterThread != "" {
		t.Fatalf("lastMasterThread = %q, want empty — the observation was not recorded", got.lastMasterThread)
	}
	// ...and navigating back to b now reads as a change again.
	nm2, _ := got.Update(masterCursorMsg{id: "b", ok: true})
	if id := cursorID(t, nm2.(Model)); id != "b" {
		t.Fatalf("cursor = %q after returning to b", id)
	}
}

// TestMasterTrackLeavesCursorWhenRowNotInView (Lukas, 2026-08-28): the cockpit moved to
// a thread this view does not contain — on hold, or archived while the sidebar sits on
// `active`, or filtered out. Leave the cursor alone: no jump, no view switch, and no
// pending preselect that would land minutes later.
func TestMasterTrackLeavesCursorWhenRowNotInView(t *testing.T) {
	m := trackModel()
	m.cursor = 1
	viewBefore := m.view
	nm, _ := m.Update(masterCursorMsg{id: "not-in-this-view", ok: true})
	got := nm.(Model)
	if id := cursorID(t, got); id != "b" {
		t.Fatalf("cursor = %q, want b — an invisible thread moved the cursor", id)
	}
	if got.view != viewBefore {
		t.Fatalf("view changed to %v — an ambient tracker must not retitle the list", got.view)
	}
	if got.preselectID != "" {
		t.Fatalf("preselectID = %q — a pending preselect would land whenever the row happened to appear", got.preselectID)
	}
	// The observation is still recorded, so returning to a visible thread tracks again.
	if got.lastMasterThread != "not-in-this-view" {
		t.Errorf("lastMasterThread = %q", got.lastMasterThread)
	}
}

// TestMasterTrackTickSpendsResolveOnlyWhenDue drives the real tick handler against a
// real bell file: a quiet tick reschedules and nothing else, a rung bell produces the
// resolve as well.
func TestMasterTrackTickSpendsResolveOnlyWhenDue(t *testing.T) {
	bell := filepath.Join(t.TempDir(), "nav-bell")
	m := trackModel()
	m.masterTrackBell = bell
	m.masterTrackSeen = ""

	// Quiet: no bell file at all, backstop not expired.
	nm, cmd := m.Update(masterTrackTickMsg{})
	got := nm.(Model)
	if got.masterTrackCountdown != masterTrackBackstopTicks-1 {
		t.Fatalf("countdown = %d, want %d", got.masterTrackCountdown, masterTrackBackstopTicks-1)
	}
	if cmd == nil {
		t.Fatal("quiet tick did not reschedule — tracking would stop forever")
	}
	if _, isBatch := cmd().(tea.BatchMsg); isBatch {
		t.Fatal("quiet tick issued a batch — it must not spend a resolve")
	}

	// Bell rung: resolve AND reschedule (a batch), and the countdown restarts.
	if err := os.WriteFile(bell, []byte("12345\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nm, cmd = got.Update(masterTrackTickMsg{})
	got = nm.(Model)
	if got.masterTrackSeen != "12345" {
		t.Fatalf("masterTrackSeen = %q, want 12345", got.masterTrackSeen)
	}
	if got.masterTrackCountdown != masterTrackBackstopTicks {
		t.Fatalf("countdown = %d, want the backstop restarted at %d", got.masterTrackCountdown, masterTrackBackstopTicks)
	}
	if cmd == nil {
		t.Fatal("rung bell produced no command")
	}
	if _, isBatch := cmd().(tea.BatchMsg); !isBatch {
		t.Fatal("rung bell did not spend a resolve (expected a resolve+reschedule batch)")
	}
}

// TestMasterTrackTickWaitsOutAFollow: while the sidebar's own follow is running, a tick
// spends no resolve (it would be discarded) and leaves the bell UNCONSUMED — so a bell
// that rang meanwhile, possibly for a genuine cockpit-side move, is still resolved on the
// first tick after the follow lands.
func TestMasterTrackTickWaitsOutAFollow(t *testing.T) {
	bell := filepath.Join(t.TempDir(), "nav-bell")
	if err := os.WriteFile(bell, []byte("777\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := trackModel()
	m.masterTrackBell = bell
	m.followInFlight = true

	nm, cmd := m.Update(masterTrackTickMsg{})
	got := nm.(Model)
	if cmd == nil {
		t.Fatal("tick during a follow did not reschedule — tracking would stop forever")
	}
	if _, isBatch := cmd().(tea.BatchMsg); isBatch {
		t.Fatal("tick during a follow spent a resolve")
	}
	if got.masterTrackSeen != "" {
		t.Fatalf("masterTrackSeen = %q — the bell was consumed while its resolve was skipped", got.masterTrackSeen)
	}

	got.followInFlight = false
	nm, cmd = got.Update(masterTrackTickMsg{})
	if _, isBatch := cmd().(tea.BatchMsg); !isBatch || nm.(Model).masterTrackSeen != "777" {
		t.Fatal("the first tick after the follow did not resolve the bell that rang during it")
	}
}

// TestReadNavBell: absent, unreadable and empty paths are "" — not an error, and not
// information the tracker may act on.
func TestReadNavBell(t *testing.T) {
	dir := t.TempDir()
	if got := readNavBell(""); got != "" {
		t.Errorf("unset path = %q, want empty", got)
	}
	if got := readNavBell(filepath.Join(dir, "absent")); got != "" {
		t.Errorf("absent bell = %q, want empty", got)
	}
	p := filepath.Join(dir, "nav-bell")
	if err := os.WriteFile(p, []byte("  1770000000000000000  \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readNavBell(p); got != "1770000000000000000" {
		t.Errorf("bell = %q, want the trimmed value", got)
	}
}

// TestMasterTrackingOffByDefault: a plain (non-sidebar) TUI must neither tick nor track
// — Init has to stay a LONE fetch, which the conformance harness drives as Init()().
func TestMasterTrackingOffByDefault(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "none.sock"), true).WithLocal("self", "sock")
	if m.masterTracking {
		t.Fatal("tracking is on for a plain Model")
	}
	if _, isBatch := m.Init()().(tea.BatchMsg); isBatch {
		t.Fatal("plain Init produced a batch — the harness drives it as a lone fetch")
	}
	// And a tracked sidebar DOES start the ticker.
	sb := m.WithSidebar().WithMasterTracking("/tmp/none")
	if _, isBatch := sb.Init()().(tea.BatchMsg); !isBatch {
		t.Fatal("tracked sidebar Init did not batch in the track ticker")
	}
}

// TestMasterTrackPreselectSeedsBaseline: the startup master-cursor resolve is itself an
// observation, so tracking's first resolve must not re-land a cursor already in place.
func TestMasterTrackPreselectSeedsBaseline(t *testing.T) {
	m := trackModel()
	nm, _ := m.Update(preselectMsg{id: "b"})
	got := nm.(Model)
	if got.lastMasterThread != "b" {
		t.Fatalf("lastMasterThread = %q after the startup preselect, want b", got.lastMasterThread)
	}
	// The user then arrows away; the first tracking resolve reports the same thread and
	// must leave them alone.
	got.cursor = 2
	nm2, _ := got.Update(masterCursorMsg{id: "b", ok: true})
	if id := cursorID(t, nm2.(Model)); id != "c" {
		t.Fatalf("cursor = %q — the startup preselect was not recorded as the baseline", id)
	}
}

// followTrackModel is trackModel with selection-follow armed: every row headful with a
// live session on the local machine, so each arrow press is follow-eligible. The cockpit
// starts on "a", where the cursor is. TMUX is scrubbed by the callers, and the follow
// cmds returned by Update are never RUN — the test delivers the completions itself, in
// the order that races in real life.
func followTrackModel() Model {
	m := trackModel()
	for i := range m.rows {
		m.rows[i].Machine = "self"
		m.rows[i].SessionName = "s_" + m.rows[i].ID
		m.rows[i].Head = api.Headful
	}
	m.machine = "self"
	m.followResolver = func() string { return "self" }
	m.lastMasterThread = "a"
	m.lastFollowedID = "a"
	return m
}

func pressKey(t *testing.T, m Model, k tea.KeyType) (Model, tea.Cmd) {
	t.Helper()
	nm, cmd := m.Update(tea.KeyMsg{Type: k})
	return nm.(Model), cmd
}

// TestMasterTrackDoesNotRevertAFollowInProgress is the pocket4 report (Lukas,
// 2026-09-14): arrowing down the sidebar "jumps back and forth", re-selecting threads
// the cursor had already passed. The sidebar's OWN follows are what rang the bell, and
// the resolve they trigger reports where the cockpit was a moment ago — not where the
// cursor is now.
//
// The interleaving: ↓ to b fires a follow; ↓ to c is swallowed (one follow at a time);
// b's nav lands and rings the bell; the coalesce fires the follow to c; the bell's
// resolve reports "b". b differs from the last observation (a), so a change-driven
// tracker moved the cursor back to b — then forward to c when c's resolve arrived.
func TestMasterTrackDoesNotRevertAFollowInProgress(t *testing.T) {
	t.Setenv("TMUX", "")
	m := followTrackModel()

	m, cmd := pressKey(t, m, tea.KeyDown) // -> b, follow b in flight
	if cmd == nil || !m.followInFlight {
		t.Fatalf("baseline: ↓ onto a live row must fire a follow (cmd=%v inflight=%v)", cmd, m.followInFlight)
	}
	m, _ = pressKey(t, m, tea.KeyDown) // -> c, swallowed while b runs
	if id := cursorID(t, m); id != "c" {
		t.Fatalf("baseline: cursor = %q after two ↓, want c", id)
	}

	// A resolve (the backstop, or a bell from elsewhere) lands while b's nav is still
	// running — the cockpit has already switched to b, but the follow has not reported.
	nm, _ := m.Update(masterCursorMsg{id: "b", ok: true, epoch: m.masterTrackEpoch})
	m = nm.(Model)
	if id := cursorID(t, m); id != "c" {
		t.Fatalf("cursor = %q — a resolve that landed mid-follow reverted the selection", id)
	}

	nm, cmd = m.Update(followDoneMsg{id: "b"}) // b landed; coalesce fires the follow to c
	m = nm.(Model)
	if cmd == nil || !m.followInFlight {
		t.Fatalf("baseline: b's completion must coalesce-fire the follow to c")
	}

	// The resolve the b-nav's bell triggered comes back while c's nav is still running.
	nm, _ = m.Update(masterCursorMsg{id: "b", ok: true})
	m = nm.(Model)
	if id := cursorID(t, m); id != "c" {
		t.Fatalf("cursor = %q — the tracker reverted the user's selection to a thread the sidebar itself had just navved through", id)
	}

	// c lands. The cursor is where the user left it, and a resolve reporting c is not a move.
	nm, _ = m.Update(followDoneMsg{id: "c"})
	m = nm.(Model)
	nm, _ = m.Update(masterCursorMsg{id: "c", ok: true})
	m = nm.(Model)
	if id := cursorID(t, m); id != "c" {
		t.Fatalf("cursor = %q after c landed, want c", id)
	}
}

// TestMasterTrackDiscardsAResolveOvertakenByAFollow: a resolve issued BEFORE a follow
// changed the cockpit may land AFTER it, carrying the pre-follow thread. It is stale by
// construction and must not move the cursor, even once the follow has finished.
func TestMasterTrackDiscardsAResolveOvertakenByAFollow(t *testing.T) {
	t.Setenv("TMUX", "")
	m := followTrackModel() // cockpit and cursor on a

	// A backstop resolve goes out while the cockpit still shows a...
	stale := masterCursorMsg{id: "a", ok: true, epoch: m.masterTrackEpoch}
	// ...the user arrows to b and its (fast, local) follow completes...
	m, _ = pressKey(t, m, tea.KeyDown)
	nm, _ := m.Update(followDoneMsg{id: "b"})
	m = nm.(Model)
	// ...and only then does the resolve's reply arrive.
	nm, _ = m.Update(stale)
	m = nm.(Model)
	if id := cursorID(t, m); id != "b" {
		t.Fatalf("cursor = %q — a resolve issued before the follow moved the cursor after it", id)
	}
	// A resolve issued AFTER the follow is fresh and still tracks a real cockpit move.
	nm, _ = m.Update(masterCursorMsg{id: "c", ok: true, epoch: m.masterTrackEpoch})
	if id := cursorID(t, nm.(Model)); id != "c" {
		t.Fatalf("cursor = %q — a fresh resolve of an external move to c was ignored", id)
	}
}

// TestMasterTrackOwnNavIsAnObservation: when a follow or an Enter lands, the sidebar
// KNOWS what the cockpit shows. Recording it is what stops the later bell resolve from
// reading as a change, and what keeps the headless-row rule honest: arrowing onto a row
// follow skips, then receiving the resolve of the thread the sidebar last navved to,
// must leave the cursor alone.
func TestMasterTrackOwnNavIsAnObservation(t *testing.T) {
	t.Setenv("TMUX", "")
	m := followTrackModel()
	m.rows[2].Head = api.Headless // c is not follow-eligible

	m, _ = pressKey(t, m, tea.KeyDown) // -> b, follow fires
	nm, _ := m.Update(followDoneMsg{id: "b"})
	m = nm.(Model)
	if m.lastMasterThread != "b" {
		t.Fatalf("lastMasterThread = %q after the follow to b landed, want b", m.lastMasterThread)
	}
	m, cmd := pressKey(t, m, tea.KeyDown) // -> c, headless: no follow, cockpit stays on b
	if cmd != nil {
		t.Fatalf("baseline: a headless row must not follow")
	}
	nm, _ = m.Update(masterCursorMsg{id: "b", ok: true, epoch: m.masterTrackEpoch})
	if id := cursorID(t, nm.(Model)); id != "c" {
		t.Fatalf("cursor = %q — the cockpit did not move, yet the cursor was yanked back", id)
	}

	// Enter records its own landing too.
	nm, _ = m.Update(navDoneMsg{id: "c"})
	if got := nm.(Model).lastMasterThread; got != "c" {
		t.Fatalf("lastMasterThread = %q after an Enter nav to c, want c", got)
	}
}
