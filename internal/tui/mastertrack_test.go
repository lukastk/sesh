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

var _ = api.ThreadRow{}
