package conformance

// The (a)-list TUI affordances (v1-parity audit, 2026-06-10): esc-quit, Tab view
// cycling, line-prompt rename/tag, cursor wrap, the ID column toggle, and
// --cursor preselect. Same discipline as every other claim: a REAL daemon, real
// threads, assertions against independently-fetched truth.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/config"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/matrix"
	"github.com/lukastk/sesh/internal/tmux"
	"github.com/lukastk/sesh/internal/tui"
)

func init() {
	registerTUIClaim("quit-esc", claimQuitEsc)
	registerTUIClaim("view-cycle-tab", claimViewCycleTab)
	registerTUIClaim("action-rename", claimActionRename)
	registerTUIClaim("action-tag", claimActionTag)
	registerTUIClaim("cursor-wrap", claimCursorWrap)
	registerTUIClaim("id-toggle", claimIDToggle)
	registerTUIClaim("cursor-preselect", claimCursorPreselect)
	registerTUIClaim("uuid-popup-copy", claimUUIDPopupCopy)
	registerTUIClaim("columns-config", claimColumnsConfig)
	registerTUIClaim("cwd-label-column", claimCwdLabelColumn)
}

// runSpecial sends a non-rune key (esc/tab/enter/backspace/...) and, like runKey,
// executes any returned command and feeds its message back.
func runSpecial(t *testing.T, m tui.Model, kt tea.KeyType) tui.Model {
	t.Helper()
	nm, cmd := m.Update(tea.KeyMsg{Type: kt})
	m2 := nm.(tui.Model)
	if cmd == nil {
		return m2
	}
	nm2, _ := m2.Update(cmd())
	return nm2.(tui.Model)
}

// typeText sends literal runes into the model (the line prompt).
func typeText(t *testing.T, m tui.Model, text string) tui.Model {
	t.Helper()
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text)})
	return nm.(tui.Model)
}

// claimQuitEsc: Esc quits from normal mode; while the line prompt is open, Esc
// only closes the prompt (the TUI stays running).
func claimQuitEsc(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	sb.newHeadlessThread(t, "pi", "escme")

	m := tui.New(sb.Home+"/daemon.sock", false)
	m, _ = renderUntilRow(t, m, "escme")

	// Prompt open: Esc closes the prompt, does NOT quit.
	m = runKey(t, m, "r")
	if !m.Prompting() {
		t.Fatalf("r did not open the rename prompt")
	}
	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = nm.(tui.Model)
	if cmd != nil {
		if _, quit := cmd().(tea.QuitMsg); quit {
			t.Fatalf("Esc inside the prompt quit the TUI (must only close the prompt)")
		}
	}
	if m.Prompting() {
		t.Errorf("Esc did not close the prompt")
	}

	// Normal mode: Esc quits.
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatalf("Esc in normal mode returned no command (want quit)")
	}
	if _, quit := cmd().(tea.QuitMsg); !quit {
		t.Errorf("Esc in normal mode did not quit (got %T)", cmd())
	}
}

// claimViewCycleTab: Tab cycles active -> archived -> all (REAL archived state from
// the daemon decides each view's rows), and the title names the current view.
func claimViewCycleTab(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	act := sb.newHeadlessThread(t, "pi", "stayme")
	arc := sb.newHeadlessThread(t, "pi", "parkme")
	if _, stderr, err := sb.Runner.Run(t, "thread", "archive", "--id", arc.ID); err != nil {
		t.Fatalf("archive: %v\n%s", err, stderr)
	}

	m := tui.New(sb.Home+"/daemon.sock", false)
	// Wait until the maintainer has published BOTH threads' state (stayme visible,
	// parkme settled as archived and therefore absent from the active view).
	var view string
	if !waitUntil(25*time.Second, func() bool {
		m, view = render(t, m)
		return strings.Contains(view, "stayme") && !strings.Contains(view, "parkme")
	}) {
		t.Fatalf("active view never settled to stayme-only:\n%s", view)
	}
	if !strings.Contains(view, "[active]") {
		t.Errorf("default view title missing [active]: %q", firstLine(view))
	}

	m = runSpecial(t, m, tea.KeyTab) // -> archived (the Tab fetch runs against the real daemon)
	view = m.View()
	if !strings.Contains(view, "[archived]") {
		t.Errorf("archived view title missing [archived]: %q", firstLine(view))
	}
	if strings.Contains(view, "stayme") || !strings.Contains(view, "parkme") {
		t.Errorf("archived view rows wrong (want parkme only):\n%s", view)
	}

	m = runSpecial(t, m, tea.KeyTab) // -> all
	view = m.View()
	if !strings.Contains(view, "[all]") || !strings.Contains(view, "stayme") || !strings.Contains(view, "parkme") {
		t.Errorf("all view wrong (want both rows + [all]):\n%s", view)
	}

	m = runSpecial(t, m, tea.KeyTab) // -> back to active
	if m.CurrentView() != tui.ViewActive {
		t.Errorf("view did not cycle back to active, got %v", m.CurrentView())
	}
	_ = act
}

// claimActionRename: the r line-prompt really renames the thread on the daemon
// (independently fetched), prefilled with the current name.
func claimActionRename(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, "pi", "oldname")

	bin := seshBin(t)
	env := []string{"SESH_HOME=" + sb.Home, "SESH_MACHINE=" + sb.Machine}
	m := tui.New(sb.Home+"/daemon.sock", false).WithExec(bin, env).WithLocal(sb.Machine, sb.TmuxSocket)
	m, _ = renderUntilRow(t, m, "oldname")

	m = runKey(t, m, "r")
	if !m.Prompting() {
		t.Fatalf("r did not open the rename prompt")
	}
	for range "oldname" { // the prompt prefills the current name — clear it
		m = runSpecial(t, m, tea.KeyBackspace)
	}
	m = typeText(t, m, "newname77")
	m = runSpecial(t, m, tea.KeyEnter) // submits: execs `thread rename` against the real daemon

	renamed := false
	for _, th2 := range sb.listThreads(t) {
		if th2.ID == th.ID && th2.Name == "newname77" {
			renamed = true
		}
	}
	if !renamed {
		t.Fatalf("daemon record was not renamed to newname77 (TUI rename did not act)")
	}
}

// claimActionTag: the t line-prompt really adds a tag (daemon truth).
func claimActionTag(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, "pi", "tagme")

	bin := seshBin(t)
	env := []string{"SESH_HOME=" + sb.Home, "SESH_MACHINE=" + sb.Machine}
	m := tui.New(sb.Home+"/daemon.sock", false).WithExec(bin, env).WithLocal(sb.Machine, sb.TmuxSocket)
	m, _ = renderUntilRow(t, m, "tagme")

	m = runKey(t, m, "t")
	if !m.Prompting() {
		t.Fatalf("t did not open the tag prompt")
	}
	m = typeText(t, m, "urgent9")
	m = runSpecial(t, m, tea.KeyEnter)

	tagged := false
	for _, th2 := range sb.listThreads(t) {
		if th2.ID == th.ID {
			for _, tag := range th2.Tags {
				if tag == "urgent9" {
					tagged = true
				}
			}
		}
	}
	if !tagged {
		t.Fatalf("daemon record did not gain tag urgent9 (TUI tag did not act)")
	}
}

// claimCursorWrap: up from the top wraps to the bottom row, down from the bottom
// wraps to the top — over REAL rows.
func claimCursorWrap(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	sb.newHeadlessThread(t, "pi", "aaa-first")
	sb.newHeadlessThread(t, "pi", "zzz-last")

	m := tui.New(sb.Home+"/daemon.sock", false)
	// Wait for BOTH rows (waiting on one races the maintainer's publish of the
	// other — an early lesson: this exact race flaked as a one-off twice).
	if !waitUntil(25*time.Second, func() bool {
		m, _ = render(t, m)
		return len(m.Rows()) == 2
	}) {
		t.Fatalf("want 2 rows, got %d", len(m.Rows()))
	}
	if m.Cursor() != 0 {
		t.Fatalf("cursor starts at %d, want 0", m.Cursor())
	}
	m = runSpecial(t, m, tea.KeyUp)
	if m.Cursor() != 1 {
		t.Errorf("up from top wrapped to %d, want 1 (bottom)", m.Cursor())
	}
	m = runSpecial(t, m, tea.KeyDown)
	if m.Cursor() != 0 {
		t.Errorf("down from bottom wrapped to %d, want 0 (top)", m.Cursor())
	}
}

// claimIDToggle: `i` shows the thread's REAL id (tid8) in its rendered row —
// without it the TUI has no id surface at all.
func claimIDToggle(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, "pi", "idme")

	m := tui.New(sb.Home+"/daemon.sock", false)
	m, _ = renderUntilRow(t, m, "idme")
	if view := m.View(); strings.Contains(view, th.ID[:8]) {
		t.Fatalf("id rendered before the toggle")
	}
	m = runKey(t, m, "i")
	view := m.View()
	if !strings.Contains(rowLine(view, "idme"), th.ID[:8]) {
		t.Errorf("row does not show the real tid8 after i: %q", rowLine(view, "idme"))
	}
	m = runKey(t, m, "i")
	if strings.Contains(m.View(), th.ID[:8]) {
		t.Errorf("id still rendered after toggling off")
	}
}

// claimCursorPreselect: the --cursor path — the REAL pane's @sesh-thread-id is
// resolved (the binding carrier's resolution, tmux.ThreadIDOfPane) and the first
// fetch puts the cursor on that thread's row.
func claimCursorPreselect(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	sb.newHeadlessThread(t, "pi", "other")
	th := sb.newThread(t, "pi", "target", "/tmp")
	pane := sb.waitThreadReady(t, th.ID, "pi")

	id, err := tmux.ThreadIDOfPane(sb.TmuxSocket, pane)
	if err != nil {
		t.Fatalf("resolve pane %s: %v", pane, err)
	}
	if id != th.ID {
		t.Fatalf("pane %s resolved to %q, want %q", pane, id, th.ID)
	}

	m := tui.New(sb.Home+"/daemon.sock", false).WithPreselect(id)
	m, _ = renderUntilRow(t, m, "target")
	sel, ok := m.Selected()
	if !ok || sel.ID != th.ID {
		t.Errorf("preselect cursor on %v (ok=%v), want thread %s", sel.ID, ok, th.ID)
	}
}

func firstLine(s string) string { return strings.SplitN(s, "\n", 2)[0] }

// claimUUIDPopupCopy: `y` opens a popup showing the selected thread's FULL real
// uuid; `c` inside it pipes that uuid through the real clipboard exec path. The
// system clipboard itself is unreachable on a headless box, so the observable
// boundary is a PATH-stubbed wl-copy capturing stdin — the TUI's entire copy path
// (selection -> popup -> exec -> stdin) runs for real.
func claimUUIDPopupCopy(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, "pi", "yankme")

	stub := t.TempDir()
	captured := filepath.Join(stub, "captured")
	script := "#!/bin/sh\ncat > " + captured + "\n"
	if err := os.WriteFile(filepath.Join(stub, "wl-copy"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stub+string(os.PathListSeparator)+os.Getenv("PATH"))

	m := tui.New(sb.Home+"/daemon.sock", false)
	m, _ = renderUntilRow(t, m, "yankme")

	m = runKey(t, m, "y")
	view := m.View()
	if !strings.Contains(view, th.ID) {
		t.Fatalf("uuid popup does not show the FULL real uuid %s:\n%s", th.ID, view)
	}
	m = runKey(t, m, "c")
	got, err := os.ReadFile(captured)
	if err != nil {
		t.Fatalf("clipboard tool was never invoked: %v", err)
	}
	if strings.TrimSpace(string(got)) != th.ID {
		t.Errorf("clipboard received %q, want the full uuid %q", strings.TrimSpace(string(got)), th.ID)
	}
	if strings.Contains(m.View(), th.ID+" ┃") {
		t.Errorf("popup still open after c")
	}

	// Any other key closes without copying.
	m = runKey(t, m, "y")
	m = runSpecial(t, m, tea.KeyEsc)
	if strings.Contains(m.View(), "c to copy") {
		t.Errorf("popup did not close on a non-c key")
	}
}

// claimColumnsConfig: the column system against a REAL daemon — the default
// set hides HEAD/BUSY text (glyphs carry state), a [tui] columns config default
// is honored end-to-end (ResolveColumns over the loaded config), an override
// set renders exactly those columns, and full-width NAME shows a long real
// name untruncated.
func claimColumnsConfig(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	longName := "a-very-long-thread-name-for-column-sizing"
	th := sb.newHeadlessThread(t, "pi", longName)

	// Default set: HEAD/BUSY text columns absent, NAME full-width (untruncated).
	m := tui.New(sb.Home+"/daemon.sock", false)
	m, view := renderUntilRowView(t, m, longName)
	if strings.Contains(view, "HEAD") || strings.Contains(view, "BUSY") {
		t.Errorf("default view shows HEAD/BUSY text columns:\n%s", view)
	}
	if !strings.Contains(view, longName) {
		t.Errorf("full-width NAME truncated the real name:\n%s", view)
	}

	// A [tui] columns config default flows through LoadTUI -> ResolveColumns.
	if err := os.WriteFile(filepath.Join(sb.Home, "config.toml"),
		[]byte("[tui]\ncolumns = [\"name\", \"head\", \"busy\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tcfg, err := config.LoadTUI(sb.Home)
	if err != nil {
		t.Fatal(err)
	}
	cols, err := tui.ResolveColumns(tcfg.Columns)
	if err != nil {
		t.Fatal(err)
	}
	m2 := tui.New(sb.Home+"/daemon.sock", false).WithColumns(cols)
	m2, view2 := renderUntilRowView(t, m2, longName)
	if !strings.Contains(view2, "HEAD") || !strings.Contains(view2, "BUSY") {
		t.Errorf("configured HEAD/BUSY columns not rendered:\n%s", view2)
	}
	if strings.Contains(view2, "MACHINE") {
		t.Errorf("non-configured MACHINE column rendered:\n%s", view2)
	}
	if !strings.Contains(rowLine(view2, longName), "headful") && !strings.Contains(rowLine(view2, longName), "headless") {
		t.Errorf("HEAD column cell missing the real axis value: %q", rowLine(view2, longName))
	}

	// Unknown names stay loud.
	if _, err := tui.ResolveColumns([]string{"nope"}); err == nil {
		t.Errorf("unknown column resolved silently")
	}
	_ = th
}

// renderUntilRowView is renderUntilRow but also returns the final rendered view.
func renderUntilRowView(t *testing.T, m tui.Model, name string) (tui.Model, string) {
	t.Helper()
	m, _ = renderUntilRow(t, m, name)
	return m, m.View()
}

// claimCwdLabelColumn: the CWD column renders a REAL thread's REAL cwd through
// the [[cwd_label]] rules (the same compiled transform `sesh cwd-label` uses),
// and the unconfigured fallback is the ~-relative path.
func claimCwdLabelColumn(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)

	// A real box-shaped cwd.
	boxes := t.TempDir()
	boxDir := filepath.Join(boxes, "20260610_zz9abc__labbox")
	if err := os.MkdirAll(boxDir, 0o755); err != nil {
		t.Fatal(err)
	}
	th := sb.newHeadlessThreadAt(t, "pi", "labelme", boxDir)

	rules := "[[cwd_label]]\nmatch = '^" + regexpQuoteDir(boxes) + "/[0-9]{8}_(?P<boxid>[a-z0-9]+)__(?P<boxname>[^/]+)$'\nlabel = '{boxname} <{boxid}>'\n"
	if err := os.WriteFile(filepath.Join(sb.Home, "config.toml"), []byte(rules), 0o644); err != nil {
		t.Fatal(err)
	}
	labels, err := config.LoadCwdLabels(sb.Home)
	if err != nil {
		t.Fatal(err)
	}
	m := tui.New(sb.Home+"/daemon.sock", false).
		WithColumns([]string{tui.ColName, tui.ColCwd}).
		WithCwdLabeler(func(cwd string) string {
			out, lerr := labels.LabelFor(cwd, "")
			if lerr != nil {
				t.Fatalf("label %s: %v", cwd, lerr)
			}
			return out
		})
	m, _ = renderUntilRow(t, m, "labelme")
	line := rowLine(m.View(), "labelme")
	if !strings.Contains(line, "labbox <zz9abc>") {
		t.Errorf("CWD column did not render the rule label: %q", line)
	}
	if strings.Contains(line, boxDir) {
		t.Errorf("raw cwd leaked into the labeled column: %q", line)
	}

	// Unconfigured: the same row falls back to the raw path (outside ~ here).
	m2 := tui.New(sb.Home+"/daemon.sock", false).WithColumns([]string{tui.ColName, tui.ColCwd})
	m2, _ = renderUntilRow(t, m2, "labelme")
	if !strings.Contains(rowLine(m2.View(), "labelme"), boxDir) {
		t.Errorf("fallback CWD column missing the real path: %q", rowLine(m2.View(), "labelme"))
	}
	_ = th
}
