package tui

import (
	"regexp"
	"strings"
	"testing"
	"time"

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

// TestHelpPopupAndLegendHint: the always-on legend is now the one-line "? keys"
// hint (the full keymap ate 3-4 rows of every frame); `?` opens a full-screen
// popup whose keymap WRAPS to the terminal width instead of clipping (the H1
// lesson carried over — every binding stays visible on any width), and
// esc/q/? close it. Move mode still replaces the hint with its own legend.
func TestHelpPopupAndLegendHint(t *testing.T) {
	strip := regexp.MustCompile("\x1b\\[[0-9;]*m")
	m := Model{width: 60}
	if got := strip.ReplaceAllString(m.renderLegend(), ""); !strings.Contains(got, "? keys") {
		t.Fatalf("bottom legend should be the ? hint, got %q", got)
	}
	m.reordering = true
	if got := strip.ReplaceAllString(m.renderLegend(), ""); !strings.Contains(got, "MOVE MODE") {
		t.Fatalf("move mode must keep its ambient legend, got %q", got)
	}
	m.reordering = false

	// ? opens the popup; the wrapped keymap keeps every binding on-width.
	mm, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	m = mm.(Model)
	if !m.HelpOpen() {
		t.Fatalf("? did not open the help popup")
	}
	out := m.helpView()
	for _, line := range strings.Split(out, "\n") {
		if w := len([]rune(strings.TrimRight(strip.ReplaceAllString(line, ""), " "))); w > 60 {
			t.Errorf("help line exceeds width 60 (%d cols): %q", w, line)
		}
	}
	var flatParts []string
	for _, line := range strings.Split(strip.ReplaceAllString(out, ""), "\n") {
		flatParts = append(flatParts, strings.TrimSpace(line))
	}
	flat := strings.Join(flatParts, " ")
	for _, binding := range []string{"f flag", "F fork", "q/esc quit"} {
		if !strings.Contains(flat, binding) {
			t.Errorf("help popup missing %q (clipped, not wrapped?):\n%s", binding, out)
		}
	}
	// The popup takes over View() and esc closes it.
	if v := m.View(); !strings.Contains(v, "sesh — keys") {
		t.Errorf("View() did not take over for the help popup")
	}
	mm, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m = mm.(Model); m.HelpOpen() {
		t.Errorf("esc did not close the help popup")
	}
}

func TestFixedColumnsTruncate(t *testing.T) {
	// The cap must be on for a fixed column to reserve its fixed width and truncate;
	// with the cap off (the `w` toggle) it grows to its content instead.
	m := Model{columns: []string{ColMachine}, maxColWidth: true, rows: []api.ThreadRow{
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

// TestColumnMaxWidthCap: with the width cap ON (the runtime default), a full-width
// column is clamped to its max and truncates a longer cell; with the cap OFF it grows
// to the full content so nothing is hidden.
func TestColumnMaxWidthCap(t *testing.T) {
	long := strings.Repeat("x", 60)
	mk := func(cap bool) Model {
		return Model{columns: []string{ColName}, maxColWidth: cap, rows: []api.ThreadRow{
			{Thread: api.Thread{Name: long}},
		}}
	}
	// Cap ON: NAME width clamps to the built-in default and the cell truncates.
	m := mk(true)
	cols := m.activeColumns()
	vis := m.visibleMatches()
	widths := m.colWidths(cols, vis)
	if widths[0] != defaultNameMaxW {
		t.Errorf("capped NAME width = %d, want %d", widths[0], defaultNameMaxW)
	}
	line := m.renderCells(cols, widths, vis[0], nil, false)
	if !strings.Contains(line, "…") {
		t.Errorf("capped NAME should truncate with an ellipsis: %q", line)
	}
	if strings.Contains(line, long) {
		t.Errorf("capped NAME must not show the full 60-char name: %q", line)
	}
	// Cap OFF: NAME grows to fit; the full name is visible.
	m2 := mk(false)
	w2 := m2.colWidths(cols, m2.visibleMatches())
	if w2[0] < 60 {
		t.Errorf("uncapped NAME width = %d, want >= 60", w2[0])
	}
	line2 := m2.renderCells(cols, w2, m2.visibleMatches()[0], nil, false)
	if !strings.Contains(line2, long) {
		t.Errorf("uncapped NAME should show the full name: %q", line2)
	}
}

// TestColumnWidthOverride: a per-column [[tui.column_width]] override replaces the
// built-in cap for that column.
func TestColumnWidthOverride(t *testing.T) {
	long := strings.Repeat("y", 60)
	m := Model{columns: []string{ColName}, maxColWidth: true, colMax: map[string]int{ColName: 20},
		rows: []api.ThreadRow{{Thread: api.Thread{Name: long}}}}
	cols := m.activeColumns()
	widths := m.colWidths(cols, m.visibleMatches())
	if widths[0] != 20 {
		t.Errorf("override NAME width = %d, want 20", widths[0])
	}
}

// TestColumnWidthToggleKey: `w` flips the width cap per session.
func TestColumnWidthToggleKey(t *testing.T) {
	m := Model{maxColWidth: true}
	mm, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("w")})
	if mm.(Model).maxColWidth {
		t.Errorf("`w` should toggle the width cap off")
	}
	mm2, _ := mm.(Model).handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("w")})
	if !mm2.(Model).maxColWidth {
		t.Errorf("`w` should toggle the width cap back on")
	}
}

func TestResolveColumnWidths(t *testing.T) {
	got, err := ResolveColumnWidths([]ColumnWidthSpec{{Name: "name", Max: 50}, {Name: "CWD", Max: 24}})
	if err != nil {
		t.Fatal(err)
	}
	if got["name"] != 50 || got["cwd"] != 24 {
		t.Errorf("resolved widths = %v, want name:50 cwd:24", got)
	}
	if _, err := ResolveColumnWidths([]ColumnWidthSpec{{Name: "bogus", Max: 10}}); err == nil {
		t.Errorf("unknown column must be a loud error")
	}
	if _, err := ResolveColumnWidths([]ColumnWidthSpec{{Name: "name", Max: 0}}); err == nil {
		t.Errorf("non-positive max must be a loud error")
	}
	if m, _ := ResolveColumnWidths(nil); m != nil {
		t.Errorf("empty specs should resolve to a nil map, got %v", m)
	}
}

// TestDetailsPopup: `I` opens the read-only details takeover on the selected row and
// shows its real fields; esc closes it.
func TestDetailsPopup(t *testing.T) {
	row := api.ThreadRow{
		Thread: api.Thread{ID: "abcdef12-3456-7890", Name: "deets", Machine: "mymain",
			AgentKind: "pi", Cwd: "/home/x/proj", Tags: []string{"a", "b"}},
		Head: api.Headless, Busy: api.BusyIdle,
	}
	m := Model{rows: []api.ThreadRow{row}, machine: "mymain",
		machines: []api.MachineView{{Machine: "mymain", Self: true, Reachable: true}}}
	mm, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("I")})
	got := mm.(Model)
	if !got.DetailsOpen() {
		t.Fatalf("`I` did not open the details popup")
	}
	view := got.View()
	for _, want := range []string{"abcdef12-3456-7890", "deets", "mymain", "pi", "/home/x/proj", "a, b", "headless"} {
		if !strings.Contains(view, want) {
			t.Errorf("details view missing %q:\n%s", want, view)
		}
	}
	// esc closes it (back to the grid).
	mm2, _ := got.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if mm2.(Model).DetailsOpen() {
		t.Errorf("esc should close the details popup")
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
	m.pending["t1"] = &rowPatch{name: sptr("new"), deadline: futureDeadline()}
	m.pending["t1"].merge(&rowPatch{addTags: []string{"b"}, notify: bptr(false), deadline: futureDeadline()})
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

	// Deadline expiry: a patch the server NEVER confirms is dropped once its
	// wall-clock deadline passes (and surfaces loudly via actionErr).
	m.rows = []api.ThreadRow{row}
	m.pending = map[string]*rowPatch{"t1": {name: sptr("never"), deadline: time.Now().Add(-time.Second)}}
	m.applyPending(true)
	if len(m.pending) != 0 {
		t.Errorf("unconfirmed patch never expired (would mask a silent failure)")
	}
	if m.actionErr == nil {
		t.Errorf("deadline expiry must surface loudly via actionErr")
	}
}

func TestMasterCursorAsyncAndNestedJump(t *testing.T) {
	// Non-blocking + concurrent: a master-cursor model's Init returns a batch that kicks
	// the fetch AND the master-cursor resolve together (the resolve overlaps startup so the
	// cursor lands by first render, but still does not BLOCK it). A plain model's Init stays
	// a lone fetch (what the conformance harness drives via Init()()).
	m := New("/tmp/none.sock", true).WithLocal("self", "sock").WithMasterCursor("peer")
	if m.Init() == nil {
		t.Fatal("Init must kick the fetch + resolve (startup must not block on the resolve)")
	}
	if New("/tmp/none.sock", true).WithLocal("self", "sock").Init() == nil {
		t.Fatal("a non-master-cursor Init must still kick the fetch")
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

// TestPreselectEscalatesViewForArchived covers the ticket: prefix+a (`tui --cursor`)
// must preselect the current thread even when it is hidden by the default `active`
// view (e.g. an archived thread). The model escalates to ViewAll and lands it.
func TestPreselectEscalatesViewForArchived(t *testing.T) {
	// Start on the default active view, preselecting an ARCHIVED thread that the
	// active-view fetch filters out (so its row is absent from the message). The mesh
	// did contain it, so preselectSeen is true.
	m := Model{
		view:        ViewActive,
		preselectID: "arch",
		expanded:    map[string]bool{},
	}
	activeRows := []api.ThreadRow{{Thread: api.Thread{ID: "a", Name: "active"}}}
	updated, cmd := m.Update(meshMsg{rows: activeRows, preselectSeen: true, fetchedAt: 1})
	mm := updated.(Model)
	if mm.view != ViewAll {
		t.Fatalf("preselect of a hidden thread must escalate to ViewAll, got view=%d", mm.view)
	}
	if mm.preselectID != "arch" {
		t.Fatalf("preselect must stay pending until the refetch lands it, got %q", mm.preselectID)
	}
	if cmd == nil {
		t.Fatal("escalation must trigger a refetch cmd")
	}
	// The ViewAll refetch now includes the archived row → the cursor lands and the
	// preselect is released.
	allRows := append(activeRows, api.ThreadRow{Thread: api.Thread{ID: "arch", Name: "archived", Archived: true}})
	updated2, _ := mm.Update(meshMsg{rows: allRows, preselectSeen: true, fetchedAt: 2})
	mm2 := updated2.(Model)
	if mm2.preselectID != "" {
		t.Fatalf("preselect should have landed and cleared, still %q", mm2.preselectID)
	}
	if sel, ok := mm2.Selected(); !ok || sel.ID != "arch" {
		t.Fatalf("cursor not on the archived thread after escalation: %+v ok=%v", sel, ok)
	}
}

// TestPreselectDoesNotEscalateOnPublishLag: a preselect target that is NOT yet in the
// mesh (preselectSeen=false — publish lag, not a view filter) must keep waiting on the
// current view, not flip to ViewAll.
func TestPreselectDoesNotEscalateOnPublishLag(t *testing.T) {
	m := Model{view: ViewActive, preselectID: "later", expanded: map[string]bool{}}
	updated, _ := m.Update(meshMsg{rows: []api.ThreadRow{{Thread: api.Thread{ID: "a"}}}, preselectSeen: false, fetchedAt: 1})
	mm := updated.(Model)
	if mm.view != ViewActive {
		t.Fatalf("an unpublished preselect must not escalate the view, got view=%d", mm.view)
	}
	if mm.preselectID != "later" {
		t.Fatalf("preselect must remain pending, got %q", mm.preselectID)
	}
}

// TestMasterCursorPreselectRefetchesImmediately covers the master prefix+a path (the
// async master-cursor resolve, `sesh tui --filter` + SESH_TUI_MASTER_MACHINE): when the
// resolve lands on a thread that the current view hides (archived), the preselectMsg
// handler must trigger an IMMEDIATE refetch — not wait up to refreshInterval (3s) for the
// next poll — so the escalation-to-ViewAll happens without a visible delay. The unit test
// asserts the handoff: a hidden target stays pending AND a refetch cmd is returned.
func TestMasterCursorPreselectRefetchesImmediately(t *testing.T) {
	// Active view holding only an active thread — the archived master-cursor target is not
	// in the rows, so positionCursorOn fails and the preselect must persist + refetch.
	m := Model{
		view:      ViewActive,
		rows:      []api.ThreadRow{{Thread: api.Thread{ID: "a", Name: "active"}}},
		expanded:  map[string]bool{},
		filtering: true, // the master binding runs `--filter`
	}
	updated, cmd := m.Update(preselectMsg{id: "arch"})
	mm := updated.(Model)
	if mm.preselectID != "arch" {
		t.Fatalf("a hidden master-cursor target must persist as a pending preselect, got %q", mm.preselectID)
	}
	if cmd == nil {
		t.Fatal("a hidden master-cursor target must trigger an immediate refetch, not wait for the 3s poll")
	}
	// And a target already visible lands immediately with no lingering preselect / refetch.
	m2 := Model{view: ViewActive, rows: []api.ThreadRow{{Thread: api.Thread{ID: "a", Name: "active"}}}, expanded: map[string]bool{}}
	updated2, cmd2 := m2.Update(preselectMsg{id: "a"})
	mm2 := updated2.(Model)
	if mm2.preselectID != "" {
		t.Fatalf("a visible target should land immediately, preselect still %q", mm2.preselectID)
	}
	if cmd2 != nil {
		t.Fatal("a visible target needs no refetch")
	}
	if sel, ok := mm2.Selected(); !ok || sel.ID != "a" {
		t.Fatalf("cursor not on the visible target: %+v ok=%v", sel, ok)
	}
}
