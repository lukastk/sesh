package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/api"
)

// TestSidebarNavDoesNotQuit (issue #8): in SIDEBAR mode a successful nav keeps
// the TUI running (it is a persistent pane beside the thread, not a popup over
// it) and notes what was entered; the normal mode still quits so the popup gets
// out of the way.
func TestSidebarNavDoesNotQuit(t *testing.T) {
	t.Setenv("TMUX", "") // focusSiblingPane must no-op outside tmux (unit context)

	sb := Model{sidebar: true}
	nm, cmd := sb.Update(navDoneMsg{name: "worker"})
	got := nm.(Model)
	if cmd != nil {
		if _, quit := cmd().(tea.QuitMsg); quit {
			t.Fatalf("sidebar nav QUIT the TUI — a persistent pane must stay open")
		}
	}
	if !strings.Contains(got.note, `entered "worker"`) {
		t.Errorf("sidebar nav note %q missing the entered-thread feedback", got.note)
	}

	// Normal (popup) mode: unchanged — a successful nav quits.
	popup := Model{}
	_, cmd = popup.Update(navDoneMsg{})
	if cmd == nil {
		t.Fatalf("popup nav produced no command (expected quit)")
	}
	if _, quit := cmd().(tea.QuitMsg); !quit {
		t.Fatalf("popup nav did not quit the TUI")
	}
}

// TestSidebarSingleClickEnters (Lukas): in SIDEBAR mode ONE click on a row
// enters it (non-nil nav cmd, cursor moved) — the sidebar is a jump list, not a
// select-then-double-click grid; the offline gate still refuses instantly; the
// normal grid keeps its single-click-selects behavior (pinned by
// TestClickSelectsRow).
func TestSidebarSingleClickEnters(t *testing.T) {
	m := flatModel("a", "b", "c")
	m.sidebar = true
	nm, cmd := m.handleLeftClick(click(20, firstRowY+1))
	got := nm.(Model)
	if got.Cursor() != 1 {
		t.Fatalf("sidebar click should select row 1, cursor=%d", got.Cursor())
	}
	if cmd == nil {
		t.Fatalf("sidebar single click should ENTER (non-nil nav cmd)")
	}

	// Offline owner: refused instantly and loudly, no nav cmd.
	off := flatModel("a", "b")
	off.sidebar = true
	off.rows[1].Machine = "peer"
	off.machines = []api.MachineView{{Machine: "peer", Reachable: false}}
	nm2, cmd2 := off.handleLeftClick(click(20, firstRowY+1))
	g2 := nm2.(Model)
	if cmd2 != nil || g2.ActionErr() == nil {
		t.Fatalf("sidebar click on an offline row: cmd=%v err=%v — want instant loud refusal", cmd2, g2.ActionErr())
	}
}

// TestSidebarFollow (Lukas): moving the selection FOLLOWS — the sibling pane
// navs to the selected thread while focus stays in the sidebar; Enter is what
// commits focus. This pins the policy truth table: debounced (a stale seq is
// dropped), dedup'd (the already-shown thread re-navs nothing), live headful
// same-machine reachable rows only (a preview must never revive a dead thread
// or switch master windows), and disabled entirely without a known sibling
// machine.
func TestSidebarFollow(t *testing.T) {
	// followNav declares the window-switch intent via the AMBIENT $TMUX; scrub it
	// so running the cmds below can never write options on the developer's own
	// live tmux server.
	t.Setenv("TMUX", "")
	row := func(id string, head api.Head, machine string) api.ThreadRow {
		return api.ThreadRow{Thread: api.Thread{ID: id, Name: id, Machine: machine, SessionName: "s_" + id}, Head: head}
	}
	base := func() Model {
		m := Model{sidebar: true, followResolver: func() string { return "mymain" }, machine: "mymain",
			machines: []api.MachineView{{Machine: "mymain", Self: true, Reachable: true}, {Machine: "peer", Reachable: false}},
			rows: []api.ThreadRow{
				row("live", api.Headful, "mymain"),
				row("dead", api.Headless, "mymain"),
				row("far", api.Headful, "peer"),
			}}
		return m
	}

	// Arrow-down arms a debounce tick; the settled tick on a live same-machine
	// row yields a follow cmd.
	m := base()
	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = nm.(Model)
	if cmd == nil {
		t.Fatalf("moving the selection in sidebar mode should arm the follow debounce")
	}
	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want 1", m.cursor)
	}
	m.cursor = 0 // put it on the live row for the eligibility checks below
	if _, ok := m.followEligible(m.followSeq); !ok {
		t.Fatalf("live headful same-machine row must be follow-eligible")
	}
	// A STALE seq (the user kept moving) is dropped.
	if _, ok := m.followEligible(m.followSeq - 1); ok {
		t.Fatalf("a stale debounce tick must not follow")
	}
	// Dedup: the thread the sibling already shows re-navs nothing.
	m.lastFollowedID = "live"
	if _, ok := m.followEligible(m.followSeq); ok {
		t.Fatalf("the already-shown thread must not re-follow")
	}
	m.lastFollowedID = ""
	// A headless row must NEVER follow (a preview must not revive).
	m.cursor = 1
	if _, ok := m.followEligible(m.followSeq); ok {
		t.Fatalf("a headless row must not follow (would revive)")
	}
	// An UNREACHABLE owner must not follow (eligibility).
	m.cursor = 2
	if _, ok := m.followEligible(m.followSeq); ok {
		t.Fatalf("an unreachable row must not follow")
	}
	// A REACHABLE row on ANOTHER machine DOES follow (the traveling sidebar
	// comes along on the window switch — Lukas; originally excluded). The cmd
	// really attempts the nav: with a bogus binary that surfaces as a loud
	// actionMsg error — proof it exec'd rather than silently skipping.
	crossRow := row("far2", api.Headful, "elsewhere")
	if msg := m.followNav(crossRow)(); msg == nil {
		t.Fatalf("followNav to another machine must attempt the nav (traveling sidebar), got nil")
	} else if _, isAct := msg.(actionMsg); !isAct {
		t.Fatalf("expected the attempted nav's result, got %T", msg)
	}
	// A resolver that reads an UNKNOWN window (renamed / no cockpit context)
	// still skips — there is no master to drive.
	m5 := base()
	m5.followResolver = func() string { return "" }
	if msg := m5.followNav(row("live", api.Headful, "mymain"))(); msg != nil {
		t.Fatalf("an unresolvable sibling machine must skip the follow, got %T", msg)
	}
	// No known sibling machine = follow disabled entirely (armFollow returns nil).
	m2 := base()
	m2.followResolver = nil
	if cmd := m2.armFollow(); cmd != nil {
		t.Fatalf("follow must be disabled without a known sibling machine")
	}
	// Not in sidebar mode: arrows never arm a follow.
	m3 := base()
	m3.sidebar = false
	if cmd := m3.armFollow(); cmd != nil {
		t.Fatalf("the normal grid must never follow")
	}

	// The follow's navDoneMsg records the shown thread and does NOT hand focus
	// (no cmd at all — focus stays in the sidebar); an ENTER navDoneMsg still
	// hands focus (non-nil cmd; TMUX scrubbed so it no-ops here).
	t.Setenv("TMUX", "")
	m4 := base()
	nm4, cmd4 := m4.Update(navDoneMsg{name: "live", id: "live", follow: true})
	g4 := nm4.(Model)
	if cmd4 != nil {
		t.Fatalf("a follow nav must not run a focus handoff")
	}
	if g4.lastFollowedID != "live" {
		t.Fatalf("follow did not record lastFollowedID")
	}
}

// TestViewPicker (Lukas): Tab opens a view PICKER instead of blind-cycling —
// it preselects the NEXT view (tab+enter ≡ the old single tab), tab/↑/↓ move
// with wrap, Enter applies (cursor reset + fetch), Esc cancels, the wheel moves
// the selection, and a mouse click on a view line applies it directly.
func TestViewPicker(t *testing.T) {
	m := Model{rows: rowsWith("a")}

	nm, _ := m.Update(keyMsg("tab"))
	m = nm.(Model)
	if !m.viewPicker || m.viewPickerCursor != int(ViewHold) {
		t.Fatalf("tab should open the picker preselecting the NEXT view: open=%v cursor=%d", m.viewPicker, m.viewPickerCursor)
	}
	// tab advances inside the picker; shift+tab/up go back; both wrap.
	nm, _ = m.Update(keyMsg("tab"))
	m = nm.(Model)
	if m.viewPickerCursor != int(ViewArchived) {
		t.Fatalf("tab in picker should advance, cursor=%d", m.viewPickerCursor)
	}
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = nm.(Model)
	if m.viewPickerCursor != int(ViewHold) {
		t.Fatalf("up in picker should go back, cursor=%d", m.viewPickerCursor)
	}
	// Enter applies: view switches, picker closes, a fetch cmd is returned.
	nm, cmd := m.Update(keyMsg("enter"))
	m = nm.(Model)
	if m.viewPicker || m.view != ViewHold || cmd == nil {
		t.Fatalf("enter should apply the picked view + fetch: open=%v view=%v cmd=%v", m.viewPicker, m.view, cmd)
	}

	// Esc cancels without switching.
	nm, _ = m.Update(keyMsg("tab"))
	m = nm.(Model)
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = nm.(Model)
	if m.viewPicker || m.view != ViewHold {
		t.Fatalf("esc should cancel the picker keeping the view: open=%v view=%v", m.viewPicker, m.view)
	}

	// Mouse: wheel moves the selection; a click on a view line applies it
	// (rows render from line 2, so view index i is at Y=i+2).
	nm, _ = m.Update(keyMsg("tab"))
	m = nm.(Model)
	nm, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	m = nm.(Model)
	if m.viewPickerCursor != int(ViewAll) {
		t.Fatalf("wheel down should advance the picker cursor, got %d", m.viewPickerCursor)
	}
	nm, cmd = m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 1, Y: 2 + int(ViewAll)})
	m = nm.(Model)
	if m.viewPicker || m.view != ViewAll || cmd == nil {
		t.Fatalf("click on a view line should apply it: open=%v view=%v cmd=%v", m.viewPicker, m.view, cmd)
	}
	// The render lists every view and annotates the current one.
	nm, _ = m.Update(keyMsg("tab"))
	m = nm.(Model)
	pv := m.View()
	for _, want := range []string{"active", "on hold", "archived", "all", "(current)"} {
		if !strings.Contains(pv, want) {
			t.Errorf("picker render missing %q:\n%s", want, pv)
		}
	}
}

// TestSidebarColumnsPreset pins the --sidebar column preset: NAME only, and it
// resolves cleanly through the normal column machinery.
func TestSidebarColumnsPreset(t *testing.T) {
	cols, err := ResolveColumns(SidebarColumns())
	if err != nil {
		t.Fatalf("sidebar preset does not resolve: %v", err)
	}
	if len(cols) != 1 || cols[0] != ColName {
		t.Fatalf("sidebar preset = %v, want [%s]", cols, ColName)
	}
}
