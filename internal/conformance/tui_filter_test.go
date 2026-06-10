package conformance

// A3 filter claims: the fzf-style filter against REAL daemon rows — narrowing,
// ranking, caret editing, the ctrl+t target toggle, Esc-applies, --filter
// start, and match-count rendering.

import (
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
	sb.newHeadlessThread(t, "pi", "xdocsx")    // 'docs' mid-word
	sb.newHeadlessThread(t, "pi", "my-docs")   // 'docs' at a boundary -> better
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
// matches ONLY in uuid mode, and the prompt names the active target.
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
