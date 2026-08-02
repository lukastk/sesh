package conformance

// A3 filter claims: the fzf-style filter against REAL daemon rows — narrowing,
// ranking, caret editing, the ctrl+t target toggle, Esc-applies, --filter
// start, and match-count rendering.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/matrix"
	"github.com/lukastk/sesh/internal/tui"
)

func init() {
	registerTUIClaim("filter-narrow", claimFilterNarrow)
	registerTUIClaim("filter-rank", claimFilterRank)
	registerTUIClaim("filter-caret", claimFilterCaret)
	registerTUIClaim("filter-target-uuid", claimFilterTargetUUID)
	registerTUIClaim("filter-esc-applies", claimFilterEscApplies)
	registerTUIClaim("filter-start-flag", claimFilterStartFlag)
	registerTUIClaim("custom-views", claimCustomViews)
}

// threeRowModel: a sandbox with three distinctly-named real threads, model
// settled on all three rows.
func threeRowModel(t *testing.T) (*Sandbox, tui.Model) {
	t.Helper()
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	sb.newHeadlessThread(t, "pi", "alpha-api")
	sb.newHeadlessThread(t, "pi", "beta-web")
	sb.newHeadlessThread(t, "pi", "gamma-docs")
	m := tui.New(sb.Home+"/daemon.sock", false)
	if !waitUntilT(t, func() bool { m, _ = render(t, m); return len(m.Rows()) == 3 }) {
		t.Fatalf("3 rows never appeared, got %d", len(m.Rows()))
	}
	return sb, m
}

// claimFilterNarrow: `/` + typing narrows the grid to matching real rows and
// the prompt shows the live matched/total count.
func claimFilterNarrow(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	_, m := threeRowModel(t)
	m = runKey(t, m, "/")
	if !m.Filtering() {
		t.Fatalf("/ did not enter filter mode")
	}
	m = typeText(t, m, "beta")
	view := m.View()
	if strings.Contains(view, "alpha-api") || strings.Contains(view, "gamma-docs") {
		t.Errorf("non-matching rows still visible:\n%s", view)
	}
	if !strings.Contains(view, "beta-web") {
		t.Errorf("matching row missing:\n%s", view)
	}
	if !strings.Contains(view, "1/3") {
		t.Errorf("count line missing 1/3:\n%s", view)
	}
	sel, ok := m.Selected()
	if !ok || sel.Name != "beta-web" {
		t.Errorf("Selected() = %v, want beta-web (cursor follows the match)", sel.Name)
	}
	// Backspace widens again.
	for range "beta" {
		m = runSpecial(t, m, tea.KeyBackspace)
	}
	if !strings.Contains(m.View(), "3/3") {
		t.Errorf("emptied query did not restore all rows: %s", m.View())
	}
}

// claimFilterRank: a query that matches one name at a word boundary and
// another mid-word ranks the boundary match FIRST (the fzf scoring contract).
func claimFilterRank(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	sb.newHeadlessThread(t, "pi", "xdocsx")  // 'docs' mid-word
	sb.newHeadlessThread(t, "pi", "my-docs") // 'docs' at a boundary -> better
	m := tui.New(sb.Home+"/daemon.sock", false)
	if !waitUntilT(t, func() bool { m, _ = render(t, m); return len(m.Rows()) == 2 }) {
		t.Fatalf("rows never appeared")
	}
	m = runKey(t, m, "/")
	m = typeText(t, m, "docs")
	sel, ok := m.Selected()
	if !ok || sel.Name != "my-docs" {
		t.Errorf("best match = %q, want my-docs (boundary bonus must outrank mid-word)", sel.Name)
	}
}

// claimFilterCaret: ←/home position the caret; typed runes insert AT the
// caret, not the end.
func claimFilterCaret(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	_, m := threeRowModel(t)
	m = runKey(t, m, "/")
	m = typeText(t, m, "bta")
	m = runSpecial(t, m, tea.KeyLeft)
	m = runSpecial(t, m, tea.KeyLeft) // caret after 'b'
	m = typeText(t, m, "e")           // -> "beta"
	if m.FilterQuery() != "beta" {
		t.Fatalf("caret insert produced %q, want beta", m.FilterQuery())
	}
	if !strings.Contains(m.View(), "beta-web") || strings.Contains(m.View(), "1/3") == false {
		t.Errorf("caret-edited query did not match beta-web: %s", m.View())
	}
}

// claimFilterTargetUUID: ctrl+t switches the search target — a tid8 query
// matches ONLY in uuid mode, the prompt names the active target, and a FULL uuid
// finds its thread wherever it sits in the tree (including nested two deep).
//
// The nested half is the regression for Lukas's 2026-08-02 report: searching a
// real thread's full uuid rendered "(no threads)". The uuid matcher was correct;
// the filter's children-exclusion default dropped the row before it could rank.
// This claim previously only ever built FLAT threads, which is exactly why it
// stayed green through the bug — so it now builds a real tree on a real daemon.
func claimFilterTargetUUID(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb, m := threeRowModel(t)
	var betaID string
	for _, th := range sb.listThreads(t) {
		if th.Name == "beta-web" {
			betaID = th.ID
		}
	}
	q := betaID[:6]
	m = runKey(t, m, "/")
	m = typeText(t, m, q)
	if v := m.View(); strings.Contains(v, "beta-web") && strings.Contains(v, "1/3") {
		t.Logf("note: tid prefix %q coincidentally fuzzy-matches names", q)
	}
	if !strings.Contains(m.View(), "name+cwd ^t→uuid") {
		t.Errorf("prompt missing the cols-target label + hint: %s", m.View())
	}
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	m = nm.(tui.Model)
	view := m.View()
	if !strings.Contains(view, "uuid ^t→name+cwd") {
		t.Errorf("prompt missing the uuid-target label after ctrl+t: %s", view)
	}
	if !strings.Contains(view, "1/3") {
		t.Errorf("uuid query %q did not narrow to exactly the real thread: %s", q, view)
	}
	sel, ok := m.Selected()
	if !ok || sel.ID != betaID {
		t.Errorf("uuid-target selection = %v, want %s", sel.ID, betaID)
	}

	// NESTED: build a real 2-deep tree on the daemon (beta under alpha, gamma
	// under beta) and search gamma's FULL uuid. A uuid is exact and unambiguous,
	// so it must resolve regardless of depth.
	var alphaID, gammaID string
	for _, th := range sb.listThreads(t) {
		switch th.Name {
		case "alpha-api":
			alphaID = th.ID
		case "gamma-docs":
			gammaID = th.ID
		}
	}
	if alphaID == "" || gammaID == "" {
		t.Fatalf("could not resolve alpha/gamma ids (alpha=%q gamma=%q)", alphaID, gammaID)
	}
	if _, stderr, err := sb.Runner.Run(t, "thread", "reparent", "--id", betaID, "--parent", alphaID); err != nil {
		t.Fatalf("reparent beta under alpha: %v\n%s", err, stderr)
	}
	if _, stderr, err := sb.Runner.Run(t, "thread", "reparent", "--id", gammaID, "--parent", betaID); err != nil {
		t.Fatalf("reparent gamma under beta: %v\n%s", err, stderr)
	}
	// Settle on the real nesting before asserting (the daemon publishes async);
	// asserting the search first could pass on a stale still-flat row set.
	if !waitUntil(10*time.Second, func() bool {
		return threadParentOf(t, sb, gammaID) == betaID && threadParentOf(t, sb, betaID) == alphaID
	}) {
		t.Fatalf("daemon never nested gamma under beta under alpha")
	}

	// Settle until the MODEL's own row set carries the nesting, not merely until
	// the daemon does. Waiting on the row COUNT alone would let the assertions
	// below pass vacuously against a still-flat cached snapshot — in which case
	// the uuid would match because the row was top-level, proving nothing.
	m2 := tui.New(sb.Home+"/daemon.sock", false)
	if !waitUntil(20*time.Second, func() bool {
		m2, _ = render(t, m2)
		if len(m2.Rows()) != 3 {
			return false
		}
		for _, r := range m2.Rows() {
			if r.ID == gammaID {
				return r.Parent == betaID
			}
		}
		return false
	}) {
		t.Fatalf("the TUI row set never showed gamma nested under beta")
	}
	m2 = runKey(t, m2, "/")
	nm2, _ := m2.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	m2 = nm2.(tui.Model)
	m2 = typeText(t, m2, gammaID)
	if !strings.Contains(m2.View(), "1/3") {
		t.Errorf("full uuid of a 2-deep NESTED thread did not match it (the reported bug):\n%s", m2.View())
	}
	if sel, ok := m2.Selected(); !ok || sel.ID != gammaID {
		t.Errorf("nested uuid search selected %v, want gamma %s\n%s", sel.ID, gammaID, m2.View())
	}
	// ^y opts INTO excluding children: the nested target then drops out, proving
	// the toggle still works and that the match above was not a vacuous pass.
	nm2, _ = m2.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	m2 = nm2.(tui.Model)
	if !strings.Contains(m2.View(), "0/3") {
		t.Errorf("^y did not exclude the nested thread from the uuid search:\n%s", m2.View())
	}
}

// claimFilterEscApplies: Esc leaves filter mode but KEEPS the query active
// (rows stay narrowed; the applied-filter line shows); / re-edits; a second
// Esc (normal mode) quits.
func claimFilterEscApplies(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	_, m := threeRowModel(t)
	m = runKey(t, m, "/")
	m = typeText(t, m, "beta")
	m = runSpecial(t, m, tea.KeyEsc)
	if m.Filtering() {
		t.Fatalf("Esc did not leave filter mode")
	}
	view := m.View()
	if strings.Contains(view, "alpha-api") {
		t.Errorf("applied filter no longer narrows after Esc:\n%s", view)
	}
	if !strings.Contains(view, "filter: beta (1/3)") {
		t.Errorf("applied-filter line missing:\n%s", view)
	}
	m = runKey(t, m, "/")
	if !m.Filtering() || m.FilterQuery() != "beta" {
		t.Errorf("/ did not re-edit the applied query (filtering=%v q=%q)", m.Filtering(), m.FilterQuery())
	}
	m = runSpecial(t, m, tea.KeyEsc) // apply again
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatalf("normal-mode Esc returned no command")
	}
	if _, quit := cmd().(tea.QuitMsg); !quit {
		t.Errorf("normal-mode Esc did not quit")
	}
}

// claimFilterStartFlag: WithFilterStart (the --filter flag / popup binding)
// opens the TUI already in filter mode.
func claimFilterStartFlag(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	sb.newHeadlessThread(t, "pi", "startme")
	m := tui.New(sb.Home+"/daemon.sock", false).WithFilterStart()
	m, _ = renderUntilRow(t, m, "startme")
	if !m.Filtering() {
		t.Fatalf("--filter did not start in filter mode")
	}
	m = typeText(t, m, "start")
	if !strings.Contains(m.View(), "1/1") {
		t.Errorf("typing immediately did not filter: %s", m.View())
	}
}

// waitUntilT adapts waitUntil with a generous settle window.
func waitUntilT(t *testing.T, cond func() bool) bool {
	t.Helper()
	return waitUntil(25*time.Second, cond)
}

// claimCustomViews: a [[tui.views]] view compiled from config shows EXACTLY
// the rows its predicate admits, against REAL ticket state — Tab reaches it,
// the title names it, and closing the real ticket flips the row out of the
// view (both directions).
func claimCustomViews(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	tk := sb.newHeadlessThread(t, "pi", "withticket")
	sb.newHeadlessThread(t, "pi", "noticket")

	// A real ticket bound to withticket (create, then activate onto the thread).
	out, stderr, err := sb.Runner.Run(t, "ticket", "create", "--name", "fix it", "--json")
	if err != nil {
		t.Fatalf("ticket create: %v\n%s", err, stderr)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &created); err != nil {
		t.Fatalf("decode ticket: %v\n%s", err, out)
	}
	if _, stderr, err := sb.Runner.Run(t, "ticket", "set-status", "--id", created.ID, "--status", "active", "--thread", tk.ID); err != nil {
		t.Fatalf("ticket activate: %v\n%s", err, stderr)
	}

	views, err := tui.CompileViews([]tui.ViewSpec{{Name: "ticketed", Filter: "ticketed and not archived"}})
	if err != nil {
		t.Fatal(err)
	}
	m := tui.New(sb.Home+"/daemon.sock", false).WithViews(views)

	// Tab to the custom view by its TITLE, not a hardcoded count — the number
	// of built-ins has grown before (`on hold`, H25) and a count silently
	// lands on the wrong view (this claim was red on clean main for exactly
	// that reason). Then wait for the maintainer's snapshot to carry the
	// ticket join.
	for range 8 {
		if strings.Contains(m.View(), "[ticketed]") {
			break
		}
		m = nextView(t, m)
		m, _ = render(t, m)
	}
	if !strings.Contains(m.View(), "[ticketed]") {
		t.Fatalf("tabbing never reached the custom view: %q", firstLine(m.View()))
	}
	if !waitUntilT(t, func() bool {
		m, _ = render(t, m)
		v := m.View()
		return strings.Contains(v, "withticket") && !strings.Contains(v, "noticket")
	}) {
		t.Fatalf("custom view never settled to exactly the ticketed thread:\n%s", m.View())
	}

	// Close the ticket -> the row leaves the view (the predicate tracks REAL
	// ticket state, not a snapshot of it).
	if _, stderr, err := sb.Runner.Run(t, "ticket", "set-status", "--id", created.ID, "--status", "done"); err != nil {
		t.Fatalf("ticket done: %v\n%s", err, stderr)
	}
	if !waitUntilT(t, func() bool {
		m, _ = render(t, m)
		return !strings.Contains(m.View(), "withticket")
	}) {
		t.Fatalf("closed ticket did not flip the row out of the view:\n%s", m.View())
	}
}
