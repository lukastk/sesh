package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/api"
)

func fptr(f float64) *float64 { return &f }

func pinTestRow(id, name, machine string, order *float64) api.ThreadRow {
	return api.ThreadRow{
		Thread: api.Thread{ID: id, Name: name, Machine: machine, AgentKind: "pi", PinOrder: order},
		Head:   api.Headless, Busy: api.BusyIdle,
	}
}

func selfMachines() []api.MachineView {
	return []api.MachineView{{Machine: "mymain", Self: true, Reachable: true}}
}

// Pinned top-level threads render as a block ABOVE the auto-sorted roots, ordered by
// their fractional key; unpinned roots keep their (fetch) order; a pinned parent still
// carries its subtree.
func TestPinnedRootsRenderFirst(t *testing.T) {
	m := Model{
		machine:       "mymain",
		defaultExpand: true,
		rows: []api.ThreadRow{
			pinTestRow("A", "a-thread", "mymain", fptr(5)),
			pinTestRow("B", "b-thread", "mymain", fptr(2)),
			pinTestRow("C", "c-thread", "mymain", nil),
			pinTestRow("D", "d-thread", "mymain", nil),
			{Thread: api.Thread{ID: "E", Name: "child", Machine: "mymain", AgentKind: "pi", Parent: "A"}, Head: api.Headless, Busy: api.BusyIdle},
		},
		machines: selfMachines(),
	}
	var got []string
	for _, tr := range m.visibleMatches() {
		got = append(got, tr.row.ID)
	}
	want := []string{"B", "A", "E", "C", "D"} // B(2),A(5) pinned first; E under A; then unpinned C,D
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestPinTopOrder(t *testing.T) {
	empty := Model{}
	if got := empty.pinTopOrder(); got != 0 {
		t.Fatalf("empty block top = %v, want 0", got)
	}
	m := Model{rows: []api.ThreadRow{
		pinTestRow("p1", "", "mymain", fptr(10)),
		pinTestRow("p2", "", "mymain", fptr(20)),
	}}
	if got := m.pinTopOrder(); got != 9 {
		t.Fatalf("top = %v, want 9 (below the min)", got)
	}
}

// reorderTarget leapfrogs the adjacent pinned node one step, and is bounded (no move
// past the ends of the block).
func TestReorderTarget(t *testing.T) {
	rows := []api.ThreadRow{
		pinTestRow("p1", "", "mymain", fptr(10)),
		pinTestRow("p2", "", "mymain", fptr(20)),
		pinTestRow("p3", "", "mymain", fptr(30)),
	}
	m := Model{rows: rows}
	by := map[string]api.ThreadRow{}
	for _, r := range rows {
		by[r.ID] = r
	}
	cases := []struct {
		id      string
		dir     int
		wantOK  bool
		wantVal float64
	}{
		{"p1", -1, false, 0}, // already top
		{"p3", 1, false, 0},  // already bottom
		{"p2", -1, true, 9},  // up past p1 → below the top
		{"p2", 1, true, 31},  // down past p3 → below the bottom
		{"p1", 1, true, 25},  // down past p2 → between p2,p3
		{"p3", -1, true, 15}, // up past p2 → between p1,p2
	}
	for _, c := range cases {
		got, ok := m.reorderTarget(by[c.id], c.dir)
		if ok != c.wantOK || (ok && got != c.wantVal) {
			t.Errorf("reorderTarget(%s,%d) = (%v,%v), want (%v,%v)", c.id, c.dir, got, ok, c.wantVal, c.wantOK)
		}
	}
}

// `p` pins a top-level thread (optimistic patch applied instantly); a nested thread is
// refused loudly and never pinned.
func TestPinKey(t *testing.T) {
	m := Model{
		machine:  "mymain",
		rows:     []api.ThreadRow{pinTestRow("t1", "top", "mymain", nil)},
		machines: selfMachines(),
	}
	nm, cmd := m.Update(keyMsg("p"))
	got := nm.(Model)
	if cmd == nil {
		t.Fatal("p should return a persist command")
	}
	p := got.pending["t1"]
	if p == nil || !p.pinSet || p.pinOrder == nil {
		t.Fatalf("p should apply an optimistic pin patch, got %#v", p)
	}

	child := Model{
		machine:  "mymain",
		rows:     []api.ThreadRow{{Thread: api.Thread{ID: "c1", Name: "kid", Machine: "mymain", AgentKind: "pi", Parent: "x"}}},
		machines: selfMachines(),
	}
	nm2, cmd2 := child.Update(keyMsg("p"))
	g2 := nm2.(Model)
	if cmd2 != nil || g2.ActionErr() == nil || g2.pending["c1"] != nil {
		t.Fatalf("pinning a child must refuse loudly and not pin (err=%v cmd=%v)", g2.ActionErr(), cmd2)
	}
}

// `u` unpins a pinned thread (patch with a nil order); on an unpinned thread it's a
// no-op note.
func TestUnpinKey(t *testing.T) {
	m := Model{
		machine:  "mymain",
		rows:     []api.ThreadRow{pinTestRow("t1", "top", "mymain", fptr(3))},
		machines: selfMachines(),
	}
	nm, cmd := m.Update(keyMsg("u"))
	got := nm.(Model)
	if cmd == nil {
		t.Fatal("u on a pinned thread should return a persist command")
	}
	p := got.pending["t1"]
	if p == nil || !p.pinSet || p.pinOrder != nil {
		t.Fatalf("u should apply an unpin patch (pinSet, nil order), got %#v", p)
	}

	notpinned := Model{
		machine:  "mymain",
		rows:     []api.ThreadRow{pinTestRow("t2", "top", "mymain", nil)},
		machines: selfMachines(),
	}
	nm2, cmd2 := notpinned.Update(keyMsg("u"))
	if cmd2 != nil || nm2.(Model).pending["t2"] != nil {
		t.Fatalf("u on an unpinned thread must be a no-op")
	}
}

// `m` enters move mode on a top-level row; ↑/↓ reposition it; Enter exits. A nested
// row is refused.
func TestMoveMode(t *testing.T) {
	m := Model{
		machine: "mymain",
		rows: []api.ThreadRow{
			pinTestRow("p1", "one", "mymain", fptr(10)),
			pinTestRow("p2", "two", "mymain", fptr(20)),
		},
		machines: selfMachines(),
	}
	// Cursor on p2 (the 2nd visible row).
	m.cursor = 1
	nm, _ := m.Update(keyMsg("m"))
	got := nm.(Model)
	if !got.reordering || got.reorderID != "p2" {
		t.Fatalf("m should enter move mode on p2 (reordering=%v id=%q)", got.reordering, got.reorderID)
	}
	// Up: p2 leapfrogs p1 to the top (order 9).
	nm2, cmd := got.Update(tea.KeyMsg{Type: tea.KeyUp})
	g2 := nm2.(Model)
	if cmd == nil {
		t.Fatal("moving in move mode should persist")
	}
	if p := g2.pending["p2"]; p == nil || p.pinOrder == nil || *p.pinOrder != 9 {
		t.Fatalf("up should reposition p2 to order 9, got %#v", p)
	}
	if !g2.reordering {
		t.Fatal("a move keeps move mode active")
	}
	// Enter exits move mode.
	nm3, _ := g2.Update(keyMsg("enter"))
	if nm3.(Model).reordering {
		t.Fatal("enter should leave move mode")
	}

	// `m` on a nested row is refused.
	child := Model{
		machine:  "mymain",
		rows:     []api.ThreadRow{{Thread: api.Thread{ID: "c1", Name: "kid", Machine: "mymain", AgentKind: "pi", Parent: "x"}}},
		machines: selfMachines(),
	}
	nm4, _ := child.Update(keyMsg("m"))
	if nm4.(Model).reordering || nm4.(Model).ActionErr() == nil {
		t.Fatal("m on a nested row must refuse and not enter move mode")
	}
}

// `D` opens the divider-label prompt; a divider renders as a full rule; Enter on a
// divider refuses (nothing to enter).
func TestDivider(t *testing.T) {
	m := Model{
		machine:  "mymain",
		rows:     []api.ThreadRow{pinTestRow("t1", "top", "mymain", nil)},
		machines: selfMachines(),
	}
	nm, _ := m.Update(keyMsg("D"))
	dm2 := nm.(Model)
	if dm2.prompting != promptNewDivider {
		t.Fatalf("D should open the divider prompt, got %v", dm2.prompting)
	}
	// The prompt HEADER must name the DIVIDER action (regression: it fell through to
	// the default "rename" label). Assert the specific header text is rendered.
	if view := dm2.View(); !strings.Contains(view, "new divider label") {
		t.Errorf("D prompt header should read 'new divider label ...', not the default rename; view:\n%s", view)
	}

	div := api.ThreadRow{Thread: api.Thread{ID: "d1", Name: "today", Machine: "mymain", AgentKind: api.DividerAgentKind, PinOrder: fptr(1)}}
	dm := Model{width: 30}
	line := dm.renderDividerLine(div, false)
	if !strings.Contains(line, "today") || !strings.Contains(line, "─") {
		t.Fatalf("labeled divider should show the label + rule: %q", line)
	}
	bare := dm.renderDividerLine(api.ThreadRow{Thread: api.Thread{ID: "d2", AgentKind: api.DividerAgentKind}}, false)
	if strings.Contains(bare, "divider") || !strings.HasPrefix(strings.TrimSpace(bare), "─") {
		t.Fatalf("unlabeled divider should be a bare rule: %q", bare)
	}

	// Enter on a divider refuses loudly.
	em := Model{machine: "mymain", rows: []api.ThreadRow{div}, machines: selfMachines()}
	msg, ok := em.navSelected()().(actionMsg)
	if !ok || msg.err == nil || !strings.Contains(msg.err.Error(), "divider") {
		t.Fatalf("entering a divider must refuse, got %#v", msg)
	}
}

func TestPinMark(t *testing.T) {
	if got := pinMark(pinTestRow("a", "", "m", fptr(1)), false); got != "•" {
		t.Errorf("pinned mark = %q, want •", got)
	}
	if got := pinMark(pinTestRow("a", "", "m", nil), false); got != " " {
		t.Errorf("unpinned mark = %q, want space", got)
	}
	if got := pinMark(pinTestRow("a", "", "m", fptr(1)), true); got != "↕" {
		t.Errorf("moving mark = %q, want ↕", got)
	}
}

func TestSamePinOrder(t *testing.T) {
	if !samePinOrder(nil, nil) || samePinOrder(fptr(1), nil) || samePinOrder(nil, fptr(1)) {
		t.Fatal("nil comparisons wrong")
	}
	if !samePinOrder(fptr(2.5), fptr(2.5)) || samePinOrder(fptr(1), fptr(2)) {
		t.Fatal("value comparisons wrong")
	}
}
