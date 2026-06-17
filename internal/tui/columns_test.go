package tui

import (
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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
	line := m.renderCells(cols, widths, vis[1], nil, false)
	if !strings.Contains(line, "a-much-longer-thread-name-that-must-not-truncate") {
		t.Errorf("full-width NAME truncated: %q", line)
	}
}

// TestClippedTrailingColumnTruncates proves the partial-column fix: a full-width
// column rendered at a REDUCED width (horizontalView's partial NAME) truncates with
// an ellipsis instead of overflowing — so a too-narrow pane shows what fits of NAME
// rather than dropping it entirely (the reported bug).
func TestClippedTrailingColumnTruncates(t *testing.T) {
	m := Model{columns: []string{ColName}, rows: []api.ThreadRow{
		{Thread: api.Thread{Name: "tbi-investigation"}},
	}}
	cols := m.activeColumns()
	vis := m.visibleMatches()
	// Render the NAME cell into a clipped width of 8 (as horizontalView would).
	line := m.renderCells(cols, []int{8}, vis[0], nil, false)
	if !strings.HasPrefix(line, "tbi-inv…") {
		t.Errorf("clipped NAME should truncate to 8 cols with an ellipsis, got %q", line)
	}
	if strings.Contains(line, "investigation") {
		t.Errorf("clipped NAME must not show the full name, got %q", line)
	}
}

// TestCwdDisplayUsesOwnerRelative proves the cross-machine cwd-label fix: the CWD
// column relativizes against row.CwdRel (stamped by the OWNING machine), so a
// thread whose absolute cwd lives under a DIFFERENT home than the viewer's still
// gets ~-relativized and labeled. Without CwdRel the viewer would show the raw
// /home/owner/... path because stripping its own home can't match.
func TestCwdDisplayUsesOwnerRelative(t *testing.T) {
	// Viewer home is /Users/laptop; the thread lives under the owner's /home/server.
	m := Model{userHome: "/Users/laptop"}
	row := api.ThreadRow{
		Thread: api.Thread{Cwd: "/home/server/dev/proj"},
		CwdRel: "~/dev/proj", // owner-stamped (owner home = /home/server)
	}
	// No labeler: the owner-relative path is shown verbatim (not the raw absolute).
	if got := m.cwdDisplay(row); got != "~/dev/proj" {
		t.Errorf("cwdDisplay = %q, want owner-relative ~/dev/proj", got)
	}
	// With a labeler keyed on ~-form, the rule matches even though the viewer's home
	// would never have stripped /home/server.
	m.cwdLabeler = func(cwd string) string {
		if cwd == "~/dev/proj" {
			return "proj-label"
		}
		return "MISS:" + cwd
	}
	if got := m.cwdDisplay(row); got != "proj-label" {
		t.Errorf("cwdDisplay = %q, want proj-label (labeler ran on the owner-relative path)", got)
	}
	// Fallback: a pre-schema-8 peer (no CwdRel) under the viewer's OWN home still
	// relativizes; one under a foreign home shows the raw path (honest degradation).
	self := api.ThreadRow{Thread: api.Thread{Cwd: "/Users/laptop/notes"}}
	m.cwdLabeler = nil
	if got := m.cwdDisplay(self); got != "~/notes" {
		t.Errorf("fallback self cwdDisplay = %q, want ~/notes", got)
	}
}

// TestLegendOverflowsNotClips proves feature 1: the keymap legend WRAPS to the
// terminal width instead of being clipped at the right edge — so every binding
// (including the trailing ones) stays visible, just across more lines.
func TestLegendOverflowsNotClips(t *testing.T) {
	strip := regexp.MustCompile("\x1b\\[[0-9;]*m")
	m := Model{width: 60}
	out := m.renderLegend()
	if !strings.Contains(out, "\n") {
		t.Fatalf("legend did not wrap at width 60:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if w := len([]rune(strings.TrimRight(strip.ReplaceAllString(line, ""), " "))); w > 60 {
			t.Errorf("legend line exceeds width 60 (%d cols): %q", w, line)
		}
	}
	// The LAST binding survives — overflow keeps it, clipping would have dropped it.
	if !strings.Contains(strip.ReplaceAllString(out, ""), "q/esc quit") {
		t.Errorf("legend dropped its trailing bindings (clipped, not wrapped):\n%s", out)
	}
	// Unknown width (no WindowSizeMsg): single line (unwrapped) so size-less tests
	// keep their existing layout.
	if strings.Contains((Model{}).renderLegend(), "\n") {
		t.Errorf("legend wrapped with width unset (should be one line)")
	}
}

func TestFixedColumnsTruncate(t *testing.T) {
	m := Model{columns: []string{ColMachine}, rows: []api.ThreadRow{
		{Thread: api.Thread{Machine: "an-extremely-long-machine-name"}},
	}}
	cols := m.activeColumns()
	vis := m.visibleMatches()
	widths := m.colWidths(cols, vis)
	line := m.renderCells(cols, widths, vis[0], nil, false)
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

// TestFilterExcludesChildThreadsByDefault: a filter query searches only top-level
// threads by default; child threads (Parent != "") are excluded until ^y toggles
// filterChildren on.
func TestFilterExcludesChildThreadsByDefault(t *testing.T) {
	mm := Model{
		rows: []api.ThreadRow{
			{Thread: api.Thread{ID: "p", Name: "alpha-parent"}},
			{Thread: api.Thread{ID: "c", Name: "alpha-child", Parent: "p"}},
			{Thread: api.Thread{ID: "r", Name: "alpha-root"}},
		},
		filter:   "alpha",
		expanded: map[string]bool{},
	}
	ids := func() map[string]bool {
		out := map[string]bool{}
		for _, tr := range mm.visibleMatches() {
			out[tr.row.ID] = true
		}
		return out
	}
	// Default: child "c" excluded; top-level "p" and "r" kept.
	got := ids()
	if got["c"] {
		t.Errorf("default filter must EXCLUDE child thread c; visible=%v", got)
	}
	if !got["p"] || !got["r"] {
		t.Errorf("default filter must keep top-level threads p,r; visible=%v", got)
	}
	// Toggle on: the child is now included.
	mm.filterChildren = true
	got = ids()
	if !got["c"] || !got["p"] || !got["r"] {
		t.Errorf("filterChildren=on must include the child; visible=%v", got)
	}
}

// TestFilterChildToggleKeyIsCtrlY proves the ^k/^y rebinding: in filter mode ^k
// moves the selection UP (the symmetric partner of ^j = down) and ^y toggles the
// include-children search. The old binding had ^k toggle children, shadowing the
// move-up key.
func TestFilterChildToggleKeyIsCtrlY(t *testing.T) {
	mm := Model{
		filtering: true,
		rows: []api.ThreadRow{
			{Thread: api.Thread{ID: "p", Name: "alpha-one"}},
			{Thread: api.Thread{ID: "r", Name: "alpha-two"}},
		},
		filter:   "alpha",
		expanded: map[string]bool{},
		cursor:   1, // on the 2nd visible match
	}
	step := func(kt tea.KeyType) { m2, _ := mm.handleFilterKey(tea.KeyMsg{Type: kt}); mm = m2.(Model) }

	// ^k moves the selection up (1 -> 0) and does NOT toggle children.
	step(tea.KeyCtrlK)
	if mm.cursor != 0 {
		t.Errorf("^k should move the selection up to 0, got cursor=%d", mm.cursor)
	}
	if mm.filterChildren {
		t.Errorf("^k must NOT toggle filterChildren")
	}

	// ^y toggles children and does NOT move the selection.
	step(tea.KeyCtrlY)
	if !mm.filterChildren {
		t.Errorf("^y should toggle filterChildren on")
	}
	if mm.cursor != 0 {
		t.Errorf("^y must not move the selection, got cursor=%d", mm.cursor)
	}
	step(tea.KeyCtrlY)
	if mm.filterChildren {
		t.Errorf("^y should toggle filterChildren back off")
	}
}
