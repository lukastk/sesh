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
