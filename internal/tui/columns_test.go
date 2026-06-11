package tui

import (
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/api"
)

func TestResolveColumnsLoudOnUnknown(t *testing.T) {
	if _, err := ResolveColumns([]string{"name", "bogus"}); err == nil {
		t.Fatalf("unknown column must be a loud error")
	} else if !strings.Contains(err.Error(), "bogus") || !strings.Contains(err.Error(), "machine") {
		t.Errorf("error should name the offender and the valid set: %v", err)
	}
	if _, err := ResolveColumns([]string{" ", ""}); err == nil {
		t.Fatalf("empty column set must be loud")
	}
}

func TestResolveColumnsDefaults(t *testing.T) {
	got, err := ResolveColumns(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range got {
		if n == ColHead || n == ColBusy || n == ColID {
			t.Errorf("default columns must not include %s (glyphs/i-toggle carry it)", n)
		}
	}
}

func TestFullWidthColumnsSizeToLongestCell(t *testing.T) {
	m := Model{columns: []string{ColName}, rows: []api.ThreadRow{
		{Thread: api.Thread{Name: "short"}},
		{Thread: api.Thread{Name: "a-much-longer-thread-name-that-must-not-truncate"}},
	}}
	cols := m.activeColumns()
	vis := m.visibleMatches()
	widths := m.colWidths(cols, vis)
	line := m.renderCells(cols, widths, vis[1], nil)
	if !strings.Contains(line, "a-much-longer-thread-name-that-must-not-truncate") {
		t.Errorf("full-width NAME truncated: %q", line)
	}
}

func TestFixedColumnsTruncate(t *testing.T) {
	m := Model{columns: []string{ColMachine}, rows: []api.ThreadRow{
		{Thread: api.Thread{Machine: "an-extremely-long-machine-name"}},
	}}
	cols := m.activeColumns()
	vis := m.visibleMatches()
	widths := m.colWidths(cols, vis)
	line := m.renderCells(cols, widths, vis[0], nil)
	if strings.Contains(line, "an-extremely-long-machine-name") {
		t.Errorf("fixed MACHINE did not truncate: %q", line)
	}
	if !strings.Contains(line, "…") {
		t.Errorf("truncation marker missing: %q", line)
	}
}

func TestColumnsRenderInConfiguredOrder(t *testing.T) {
	// --columns / [tui] columns order is honored (not a fixed built-in order).
	m := Model{columns: []string{ColTags, ColMachine, ColName}, rows: []api.ThreadRow{
		{Thread: api.Thread{Name: "n", Machine: "mac", Tags: []string{"t"}}},
	}}
	cols := m.activeColumns()
	got := []string{}
	for _, c := range cols {
		got = append(got, c.name)
	}
	want := []string{ColTags, ColMachine, ColName}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("column order = %v, want %v", got, want)
	}
	// `i` prepends ID without disturbing the rest.
	m.showID = true
	cols = m.activeColumns()
	if cols[0].name != ColID {
		t.Errorf("i did not prepend ID: %v", cols)
	}
}

func TestApplyColumnMoves(t *testing.T) {
	base := []string{ColMachine, ColAgent, ColName, ColCwd, ColTags, ColNotify}

	// Absolute: notify → position 1 (first), rest keep order.
	got, err := ApplyColumnMoves(base, []ColumnMove{{Name: ColNotify, Position: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != ColNotify || got[1] != ColMachine {
		t.Errorf("absolute move: %v", got)
	}

	// Relative: created inserted just after name (not in base → added).
	got, err = ApplyColumnMoves(base, []ColumnMove{{Name: ColCreated, After: ColName}})
	if err != nil {
		t.Fatal(err)
	}
	if i := indexOfStr(got, ColCreated); i < 1 || got[i-1] != ColName {
		t.Errorf("after move: %v", got)
	}

	// before.
	got, _ = ApplyColumnMoves(base, []ColumnMove{{Name: ColTags, Before: ColMachine}})
	if got[0] != ColTags {
		t.Errorf("before move: %v", got)
	}

	// Loud: unknown column, no anchor, both anchors, unknown anchor.
	for _, mv := range []ColumnMove{
		{Name: "bogus", Position: 1},
		{Name: ColTags},
		{Name: ColTags, After: ColName, Position: 2},
		{Name: ColTags, After: "nope"},
		{Name: ColTags, Position: 0, Before: ""},
	} {
		if _, err := ApplyColumnMoves(base, []ColumnMove{mv}); err == nil {
			t.Errorf("move %+v should be loud", mv)
		}
	}
}

func indexOfStr(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func TestOptimisticPending(t *testing.T) {
	row := api.ThreadRow{Thread: api.Thread{ID: "t1", Name: "old", Notify: true, Tags: []string{"a"}}}
	m := Model{rows: []api.ThreadRow{row}, pending: map[string]*rowPatch{}}

	// Record a rename + a tag + a notify-off, all confirmed.
	m.pending["t1"] = &rowPatch{name: sptr("new"), ttl: optimisticTTL}
	m.pending["t1"].merge(&rowPatch{addTags: []string{"b"}, notify: bptr(false), ttl: optimisticTTL})
	m.applyPending(false) // instant overlay, no GC
	got := m.rows[0]
	if got.Name != "new" || got.Notify != false || !containsStr(got.Tags, "b") {
		t.Fatalf("optimistic overlay not applied: %+v", got)
	}

	// A reconcile fetch that's STILL STALE must NOT clobber the optimistic value.
	m.rows = []api.ThreadRow{row} // server still shows old name/notify/tags
	m.applyPending(true)
	if m.rows[0].Name != "new" || m.rows[0].Notify != false {
		t.Errorf("stale reconcile clobbered the optimistic patch: %+v", m.rows[0])
	}
	if len(m.pending) != 1 {
		t.Errorf("patch dropped while still unconfirmed")
	}

	// Server catches up → patch is dropped (no longer needed).
	m.rows = []api.ThreadRow{{Thread: api.Thread{ID: "t1", Name: "new", Notify: false, Tags: []string{"a", "b"}}}}
	m.applyPending(true)
	if len(m.pending) != 0 {
		t.Errorf("satisfied patch not GC'd: %+v", m.pending)
	}

	// TTL expiry: a patch the server NEVER confirms is dropped after optimisticTTL cycles.
	m.rows = []api.ThreadRow{row}
	m.pending = map[string]*rowPatch{"t1": {name: sptr("never"), ttl: optimisticTTL}}
	for i := 0; i < optimisticTTL; i++ {
		m.rows = []api.ThreadRow{row}
		m.applyPending(true)
	}
	if len(m.pending) != 0 {
		t.Errorf("unconfirmed patch never expired (would mask a silent failure)")
	}
}

func TestMasterCursorAsyncAndNestedJump(t *testing.T) {
	// Async/non-blocking: a master-cursor model's Init is STILL just the fetch — the
	// resolve fires later (from the first meshMsg), so prefix+s startup is not gated.
	m := New("/tmp/none.sock", true).WithLocal("self", "sock").WithMasterCursor("peer")
	if m.Init() == nil {
		t.Fatal("Init must still kick the fetch (startup must not block on the resolve)")
	}

	// Nested jump: a collapsed child becomes the cursor target, ancestors expanded.
	mm := Model{
		rows: []api.ThreadRow{
			{Thread: api.Thread{ID: "p", Name: "parent"}},
			{Thread: api.Thread{ID: "c", Name: "child", Parent: "p"}},
			{Thread: api.Thread{ID: "g", Name: "grand", Parent: "c"}},
		},
		expanded: map[string]bool{}, // all collapsed by default
	}
	// Before: the grandchild is hidden (its ancestors are folded).
	visible := func(id string) bool {
		for _, tr := range mm.visibleMatches() {
			if tr.row.ID == id {
				return true
			}
		}
		return false
	}
	if visible("g") {
		t.Fatal("grandchild should be hidden while ancestors are collapsed")
	}
	// The preselectMsg path (what the async resolve produces) jumps to it.
	updated, _ := mm.Update(preselectMsg{id: "g"})
	mm = updated.(Model)
	if !mm.expanded["p"] || !mm.expanded["c"] {
		t.Errorf("ancestors not expanded for the nested jump: %v", mm.expanded)
	}
	if sel, ok := mm.Selected(); !ok || sel.ID != "g" {
		t.Errorf("cursor not on the nested child: %+v", sel)
	}
	// An empty resolve (no master client / plain shell) is a clean no-op.
	updated, _ = Model{rows: mm.rows, expanded: map[string]bool{}, cursor: 0}.Update(preselectMsg{id: ""})
	if updated.(Model).cursor != 0 {
		t.Errorf("empty preselect should not move the cursor")
	}
}
