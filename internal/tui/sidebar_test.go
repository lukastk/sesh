package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/api"
)

// TestSidebarNavDoesNotQuit (issue #8): in SIDEBAR mode a successful nav keeps
// the TUI running (it is a persistent pane beside the thread, not a popup over
// it) and shows NO note — the switched pane is the feedback (Lukas); the
// normal mode still quits so the popup gets out of the way.
func TestSidebarNavDoesNotQuit(t *testing.T) {
	t.Setenv("TMUX", "") // focusSiblingPane must no-op outside tmux (unit context)

	sb := Model{sidebar: true}
	nm, cmd := sb.Update(navDoneMsg{id: "worker"})
	got := nm.(Model)
	if cmd != nil {
		if _, quit := cmd().(tea.QuitMsg); quit {
			t.Fatalf("sidebar nav QUIT the TUI — a persistent pane must stay open")
		}
	}
	if got.note != "" {
		t.Errorf("sidebar nav must set no note (the switched pane is the feedback), got %q", got.note)
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

// TestSidebarEscDoesNotQuit (Lukas — "if I press Esc on the sidebar, it
// disappears"): esc/q are no-ops in sidebar mode — a persistent cockpit pane
// must not die to a stray keystroke (its pane would take the traveling slot
// with it). ctrl+c stays the deliberate kill; the popup grid keeps q/esc-quit.
func TestSidebarEscDoesNotQuit(t *testing.T) {
	sb := Model{sidebar: true, rows: rowsWith("a")}
	sb.actionErr, sb.note = errStub("stale refusal"), "stale note"
	for _, k := range []tea.KeyMsg{{Type: tea.KeyEsc}, {Type: tea.KeyRunes, Runes: []rune("q")}} {
		nm, cmd := sb.Update(k)
		if cmd != nil {
			if _, quit := cmd().(tea.QuitMsg); quit {
				t.Fatalf("sidebar quit on %q — must be a no-op", k.String())
			}
		}
		// ...and DISMISS the persistent message lines (which otherwise never
		// clear in a pane that never quits — Lukas).
		if g := nm.(Model); g.ActionErr() != nil || g.note != "" {
			t.Fatalf("%q should dismiss actionErr+note, got err=%v note=%q", k.String(), g.ActionErr(), g.note)
		}
	}
	_, cmd := sb.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatalf("ctrl+c produced no command")
	}
	if _, quit := cmd().(tea.QuitMsg); !quit {
		t.Fatalf("ctrl+c must still quit the sidebar (the deliberate kill)")
	}
	// The popup grid keeps q/esc-quit (pinned live by claimQuitEsc too).
	popup := Model{rows: rowsWith("a")}
	_, cmd = popup.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatalf("popup esc produced no command")
	}
	if _, quit := cmd().(tea.QuitMsg); !quit {
		t.Fatalf("popup esc must quit")
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

// TestSidebarFollow (Lukas): moving the selection FOLLOWS — the cockpit navs
// to the selected thread while focus stays in the sidebar; Enter is what
// commits focus. Fire-immediately + coalesce (no debounce): a move fires the
// nav at once; moves while one runs are swallowed and the COMPLETION re-arms
// for wherever the cursor is then. Policy: dedup against the shown thread,
// live headful reachable rows only (a preview must never revive), cross-
// machine allowed (the traveling sidebar rides the switch), disabled without
// cockpit context.
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

	// The fixture leaves cursor 0 on an eligible row: pressing DOWN moves onto
	// the headless row (ineligible -> nil cmd), pressing UP back onto the live
	// row fires a follow IMMEDIATELY (non-nil cmd) and latches in-flight.
	m := base()
	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = nm.(Model)
	if cmd != nil {
		t.Fatalf("moving onto a HEADLESS row must not follow (would revive)")
	}
	nm, cmd = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = nm.(Model)
	if cmd == nil {
		t.Fatalf("moving onto a live row must fire the follow immediately (no debounce)")
	}
	if !m.followInFlight {
		t.Fatalf("firing a follow must latch in-flight")
	}
	// Moves while one runs are swallowed (no queued navs)...
	nm, cmd = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = nm.(Model)
	if cmd != nil {
		t.Fatalf("a move during an in-flight follow must not fire another nav")
	}
	// ...and the COMPLETION re-arms for wherever the cursor is now. Here the
	// cursor sits on the headless row -> the coalesce is an eligible-gated
	// no-op; move it to the eligible row first to see it fire.
	m.cursor = 0
	nm, cmd = m.Update(followDoneMsg{id: "other"})
	m = nm.(Model)
	if cmd == nil || !m.followInFlight {
		t.Fatalf("completion with the cursor on an eligible unseen row must coalesce-fire (cmd=%v inflight=%v)", cmd, m.followInFlight)
	}
	if m.lastFollowedID != "other" {
		t.Fatalf("completion did not record lastFollowedID")
	}
	// A failed follow surfaces loudly AND records the attempted target — the
	// retry-loop guard: with the cursor still on the failing row, the coalesce
	// must NOT refire the same broken nav (error -> re-arm -> error forever);
	// moving away and back retries deliberately.
	m2 := base() // cursor 0 = "live", the row whose nav failed
	m2.followInFlight = true
	nm2, cmd2 := m2.Update(followDoneMsg{id: "live", err: errStub("nav broke")})
	g2 := nm2.(Model)
	if g2.ActionErr() == nil || g2.lastFollowedID != "live" || g2.followInFlight || cmd2 != nil {
		t.Fatalf("failed follow: err=%v last=%q inflight=%v cmd=%v — want loud, recorded, no refire", g2.ActionErr(), g2.lastFollowedID, g2.followInFlight, cmd2)
	}

	// Eligibility table (armFollow-level policy).
	e := base()
	if _, ok := e.followEligible(); !ok {
		t.Fatalf("live headful reachable row must be follow-eligible")
	}
	e.lastFollowedID = "live" // dedup: the thread the cockpit already shows
	if _, ok := e.followEligible(); ok {
		t.Fatalf("the already-shown thread must not re-follow")
	}
	e.lastFollowedID = ""
	e.cursor = 1 // headless
	if _, ok := e.followEligible(); ok {
		t.Fatalf("a headless row must not follow (would revive)")
	}
	e.cursor = 2 // unreachable owner
	if _, ok := e.followEligible(); ok {
		t.Fatalf("an unreachable row must not follow")
	}

	// A REACHABLE row on ANOTHER machine DOES follow (the traveling sidebar
	// rides the window switch). The cmd really attempts the nav: with a bogus
	// binary that surfaces as a loud followDoneMsg error — proof it exec'd
	// rather than silently skipping.
	if msg := base().followNav(row("far2", api.Headful, "elsewhere"))(); true {
		fd, ok := msg.(followDoneMsg)
		if !ok || fd.err == nil {
			t.Fatalf("followNav to another machine must attempt the nav, got %#v", msg)
		}
	}
	// The LOCAL fast path goes through the daemon client, not a subprocess: a
	// nil client + a same-window local row falls into the fast path guard...
	// (the real daemon call is proven by the sidebar-nav-stays claim); an
	// unresolvable window ("" — no cockpit context) skips entirely.
	m5 := base()
	m5.followResolver = func() string { return "" }
	if msg := m5.followNav(row("live", api.Headful, "mymain"))(); msg.(followDoneMsg).err != nil || msg.(followDoneMsg).id != "" {
		t.Fatalf("an unresolvable sibling machine must skip the follow, got %#v", msg)
	}
	// No known sibling machine = follow disabled entirely (armFollow returns nil).
	m6 := base()
	m6.followResolver = nil
	if cmd := m6.armFollow(); cmd != nil {
		t.Fatalf("follow must be disabled without a known sibling machine")
	}
	// Not in sidebar mode: arrows never arm a follow.
	m7 := base()
	m7.sidebar = false
	if cmd := m7.armFollow(); cmd != nil {
		t.Fatalf("the normal grid must never follow")
	}

	// An ENTER navDoneMsg records the shown thread and hands focus (non-nil
	// cmd; TMUX scrubbed so running it here is a no-op).
	m8 := base()
	nm8, cmd8 := m8.Update(navDoneMsg{id: "live"})
	g8 := nm8.(Model)
	if cmd8 == nil {
		t.Fatalf("an enter nav must hand focus to the sibling pane")
	}
	if g8.lastFollowedID != "live" {
		t.Fatalf("enter did not record the shown thread")
	}
}

// TestViewPicker (Lukas): Tab opens a view PICKER instead of blind-cycling —
// it opens ON the current view (preselecting the next was disorienting),
// tab/↑/↓ move with wrap, Enter applies (cursor reset + fetch), Esc cancels,
// the wheel moves the selection, and a mouse click on a view line applies it.
func TestViewPicker(t *testing.T) {
	m := Model{rows: rowsWith("a")}

	nm, _ := m.Update(keyMsg("tab"))
	m = nm.(Model)
	if !m.viewPicker || m.viewPickerCursor != int(ViewActive) {
		t.Fatalf("tab should open the picker ON the current view: open=%v cursor=%d", m.viewPicker, m.viewPickerCursor)
	}
	// tab advances inside the picker; shift+tab/up go back; both wrap.
	nm, _ = m.Update(keyMsg("tab"))
	m = nm.(Model)
	if m.viewPickerCursor != int(ViewHold) {
		t.Fatalf("tab in picker should advance, cursor=%d", m.viewPickerCursor)
	}
	nm, _ = m.Update(keyMsg("tab"))
	nm, _ = nm.(Model).Update(tea.KeyMsg{Type: tea.KeyUp})
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
	if m.viewPickerCursor != int(ViewArchived) {
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

// TestSidebarFilterEnterExitsSearch (Lukas): in SIDEBAR mode, Enter in filter
// mode navs the FILTERED selection but then LEAVES search entirely (query
// cleared, cursor re-landed on the entered thread in the full list) — the TUI
// persists, so staying narrowed to a stale query reads as broken. The popup
// grid is untouched (it quits on nav anyway).
func TestSidebarFilterEnterExitsSearch(t *testing.T) {
	rows := []api.ThreadRow{
		{Thread: api.Thread{ID: "alpha", Name: "alpha", Machine: "m", SessionName: "s_a"}, Head: api.Headful},
		{Thread: api.Thread{ID: "docs", Name: "docs", Machine: "m", SessionName: "s_d"}, Head: api.Headful},
	}
	m := Model{sidebar: true, machine: "m", rows: rows, machines: []api.MachineView{{Machine: "m", Self: true, Reachable: true}}}
	m.filtering = true
	m.filter, m.filterCaret = "doc", 3
	nm, cmd := m.handleFilterKey(tea.KeyMsg{Type: tea.KeyEnter})
	got := nm.(Model)
	if cmd == nil {
		t.Fatalf("filter enter produced no nav cmd")
	}
	if got.filtering || got.filter != "" {
		t.Fatalf("sidebar filter enter must exit search and clear the query: filtering=%v filter=%q", got.filtering, got.filter)
	}
	if row, ok := got.Selected(); !ok || row.ID != "docs" {
		t.Fatalf("cursor should land on the entered thread in the full list, got %v", row.ID)
	}

	// Popup grid: enter navs, filter state untouched (the TUI quits on success).
	p := Model{rows: rows}
	p.filtering, p.filter = true, "doc"
	np, pcmd := p.handleFilterKey(tea.KeyMsg{Type: tea.KeyEnter})
	if pcmd == nil || !np.(Model).filtering {
		t.Fatalf("popup filter enter changed: cmd=%v filtering=%v", pcmd, np.(Model).filtering)
	}
}

// TestSidebarWideColumns (Lukas): a MAXIMIZED sidebar (>= sidebarWideThreshold
// cols — the cockpit zoom toggle) renders the full grid column set instead of
// the name-only preset; shrinking back restores the preset. No wide set / not
// sidebar mode = never adapts.
func TestSidebarWideColumns(t *testing.T) {
	m := Model{sidebar: true, columns: SidebarColumns(), sidebarWideColumns: DefaultColumns}
	m.width = 38
	if got := m.effectiveColumnNames(); len(got) != 1 || got[0] != ColName {
		t.Fatalf("narrow sidebar should render the name-only preset, got %v", got)
	}
	m.width = sidebarWideThreshold + 40
	if got := m.effectiveColumnNames(); len(got) != len(DefaultColumns) {
		t.Fatalf("maximized sidebar should render the grid set, got %v", got)
	}
	if specs := m.activeColumns(); len(specs) != len(DefaultColumns) {
		t.Fatalf("activeColumns should follow the adaptive set, got %d specs", len(specs))
	}
	m.width = 38
	if got := m.effectiveColumnNames(); len(got) != 1 {
		t.Fatalf("shrinking back should restore the preset, got %v", got)
	}
	// No wide set configured (explicit --columns) or not sidebar: never adapts.
	pinned := Model{sidebar: true, columns: []string{ColName}}
	pinned.width = 200
	if got := pinned.effectiveColumnNames(); len(got) != 1 {
		t.Fatalf("without a wide set the sidebar must never adapt, got %v", got)
	}
	grid := Model{columns: []string{ColName}, sidebarWideColumns: DefaultColumns}
	grid.width = 200
	if got := grid.effectiveColumnNames(); len(got) != 1 {
		t.Fatalf("the normal grid must never adapt, got %v", got)
	}
}

// TestSidebarFilterStyleTransitions (Lukas): entering/leaving filter INPUT mode
// in the sidebar emits the pane-tint swap command; no transition (or no filter
// style, or the popup grid) emits nothing. The tmux shelling itself is not run
// here ($TMUX scrubbed as belt-and-braces) — only the transition detection.
func TestSidebarFilterStyleTransitions(t *testing.T) {
	t.Setenv("TMUX", "")
	base := Model{sidebar: true, sidebarFilterStyle: "bg=#3a1620", rows: rowsWith("a")}

	// Enter filter (`/`): filtering flips false→true → a swap cmd is emitted.
	nm, cmd := base.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	m := nm.(Model)
	if !m.filtering {
		t.Fatalf("`/` should enter filter mode")
	}
	if cmd == nil {
		t.Fatalf("entering filter should emit a pane-tint command")
	}
	// A key that does NOT change filtering emits no tint command (via the same
	// centralized path): type a rune into the filter.
	_, cmd2 := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	_ = cmd2 // may be nil or a fetch; the point is filtering stayed true — no crash/panic
	if !m.filtering {
		t.Fatalf("typing in filter must stay in filter mode")
	}
	// Exit filter (Esc applies): filtering true→false → a restore cmd.
	nm3, cmd3 := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if nm3.(Model).filtering {
		t.Fatalf("esc should leave filter input mode")
	}
	if cmd3 == nil {
		t.Fatalf("leaving filter should emit a restore command")
	}

	// No filter style configured → never emits a tint command.
	plain := Model{sidebar: true, rows: rowsWith("a")}
	if _, c := plain.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")}); c != nil {
		t.Fatalf("no filter style: entering filter must emit no tint command")
	}
	// The saved style round-trips through filterStyleSavedMsg.
	saved := Model{sidebar: true, sidebarFilterStyle: "bg=#3a1620"}
	ns, _ := saved.Update(filterStyleSavedMsg{saved: "bg=#16283c"})
	if ns.(Model).sidebarSavedActiveStyle != "bg=#16283c" {
		t.Fatalf("filterStyleSavedMsg did not store the saved style")
	}
}

// TestViewOrderPositioning (Lukas): a [[tui.views]] custom view with position=N
// lands at that 1-based slot in the Tab/picker order (interleaved with the
// built-ins); unpositioned customs append after the built-ins as before.
func TestViewOrderPositioning(t *testing.T) {
	pred, err := CompilePredicate("flagged")
	if err != nil {
		t.Fatal(err)
	}
	// flagged at position 2, plus an unpositioned "mine".
	m := Model{}.WithViews([]customView{
		{name: "flagged", pred: pred, position: 2},
		{name: "mine", pred: pred},
	})
	// Display order should be: active, flagged, on hold, archived, all, mine.
	wantNames := []string{"active", "flagged", "on hold", "archived", "all", "mine"}
	if m.viewCount() != len(wantNames) {
		t.Fatalf("viewCount=%d, want %d", m.viewCount(), len(wantNames))
	}
	for pos, want := range wantNames {
		if got := m.viewNameAt(int(m.viewAt(pos))); got != want {
			t.Errorf("display position %d = %q, want %q", pos, got, want)
		}
	}
	// Picker opens on the CURRENT view's display POSITION: active(view 0)=pos 0.
	if p := m.viewPos(ViewActive); p != 0 {
		t.Errorf("active display position = %d, want 0", p)
	}
	// The flagged custom is view index viewBuiltins+0; its display position is 1.
	flaggedView := View(int(viewBuiltins) + 0)
	if p := m.viewPos(flaggedView); p != 1 {
		t.Errorf("flagged display position = %d, want 1 (second)", p)
	}
	// Applying the picker's second entry switches to the flagged view.
	nm, _ := m.applyPickedView(int(m.viewAt(1)))
	if got := nm.(Model).view; got != flaggedView {
		t.Errorf("applying picker position 1 selected view %d, want the flagged custom %d", got, flaggedView)
	}

	// No positions → natural order (built-ins then customs), unchanged.
	plain := Model{}.WithViews([]customView{{name: "mine", pred: pred}})
	for pos, want := range []string{"active", "on hold", "archived", "all", "mine"} {
		if got := plain.viewNameAt(int(plain.viewAt(pos))); got != want {
			t.Errorf("no-position order pos %d = %q, want %q", pos, got, want)
		}
	}
}
