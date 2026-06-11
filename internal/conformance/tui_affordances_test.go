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
	registerTUIClaim("action-untag", claimActionUntag)
	registerTUIClaim("action-reparent", claimActionReparent)
	registerTUIClaim("cursor-wrap", claimCursorWrap)
	registerTUIClaim("id-toggle", claimIDToggle)
	registerTUIClaim("cursor-preselect", claimCursorPreselect)
	registerTUIClaim("uuid-popup-copy", claimUUIDPopupCopy)
	registerTUIClaim("columns-config", claimColumnsConfig)
	registerTUIClaim("cwd-label-column", claimCwdLabelColumn)
	registerTUIClaim("columns-reorder", claimColumnsReorder)
	registerTUIClaim("notify-toggle", claimNotifyToggle)
	registerTUIClaim("action-mutate-remote", claimActionMutateRemote)
	registerTUIClaim("master-cursor", claimMasterCursor)
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
	// Settle on BOTH threads being PUBLISHED (the [all] condition) before
	// cycling — waiting on absence alone is trivially true pre-publish and
	// races the maintainer (this claim flaked exactly that way once).
	var view string
	m = runSpecial(t, m, tea.KeyTab)
	m = runSpecial(t, m, tea.KeyTab) // -> [all]
	if !waitUntil(25*time.Second, func() bool {
		m, view = render(t, m)
		return strings.Contains(view, "stayme") && strings.Contains(view, "parkme")
	}) {
		t.Fatalf("[all] never showed both threads:\n%s", view)
	}
	if !strings.Contains(view, "[all]") {
		t.Errorf("title missing [all]: %q", firstLine(view))
	}

	m = runSpecial(t, m, tea.KeyTab) // wraps -> [active]
	view = m.View()
	if !strings.Contains(view, "[active]") {
		t.Errorf("title missing [active]: %q", firstLine(view))
	}
	if !strings.Contains(view, "stayme") || strings.Contains(view, "parkme") {
		t.Errorf("active view rows wrong (want stayme only):\n%s", view)
	}

	m = runSpecial(t, m, tea.KeyTab) // -> archived
	view = m.View()
	if !strings.Contains(view, "[archived]") {
		t.Errorf("archived view title missing [archived]: %q", firstLine(view))
	}
	if strings.Contains(view, "stayme") || !strings.Contains(view, "parkme") {
		t.Errorf("archived view rows wrong (want parkme only):\n%s", view)
	}
	m = runSpecial(t, m, tea.KeyTab) // -> all again for the cycle-back check

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

	// OPTIMISTIC: the new name shows IMMEDIATELY — runSpecial drains only the action
	// command, so the reconcile fetch (which reads the lagging snapshot) has NOT run
	// yet. The row can only show "newname77" here via the optimistic overlay.
	if !strings.Contains(m.View(), "newname77") {
		t.Errorf("rename not reflected optimistically (still waiting on the snapshot): %q", rowLine(m.View(), "newname77"))
	}

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

	// OPTIMISTIC: the tag shows immediately (the reconcile fetch hasn't run).
	if !strings.Contains(m.View(), "urgent9") {
		t.Errorf("tag not reflected optimistically: %q", rowLine(m.View(), "tagme"))
	}

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

// claimActionUntag: `T` opens a picker over the selected thread's tags; navigating
// to one and pressing enter REMOVES exactly that tag on the daemon (the other tags
// survive), and the TAGS column drops it optimistically. The mirror of action-tag.
func claimActionUntag(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, "pi", "untagme")
	// Seed two tags (append order: keepme1, dropme2).
	if _, stderr, err := sb.Runner.Run(t, "thread", "tag", "--id", th.ID, "--add", "keepme1", "--add", "dropme2"); err != nil {
		t.Fatalf("seed tags: %v\n%s", err, stderr)
	}

	bin := seshBin(t)
	env := []string{"SESH_HOME=" + sb.Home, "SESH_MACHINE=" + sb.Machine}
	m := tui.New(sb.Home+"/daemon.sock", false).WithExec(bin, env).WithLocal(sb.Machine, sb.TmuxSocket)
	m, _ = renderUntilRow(t, m, "untagme")
	// Wait for both tags to land in the rendered row (the maintainer must publish them).
	if !waitUntil(15*time.Second, func() bool {
		m, _ = render(t, m)
		line := rowLine(m.View(), "untagme")
		return strings.Contains(line, "keepme1") && strings.Contains(line, "dropme2")
	}) {
		t.Fatalf("tags never appeared in the row: %q", rowLine(m.View(), "untagme"))
	}

	// Open the picker, move to the SECOND tag (dropme2), remove it.
	m = runKey(t, m, "T")
	if !strings.Contains(m.View(), "remove tag") {
		t.Fatalf("T did not open the remove-tag popup: %q", m.View())
	}
	m = runSpecial(t, m, tea.KeyDown)
	m = runSpecial(t, m, tea.KeyEnter)
	if m.LastErr() != nil {
		t.Fatalf("untag action errored: %v", m.LastErr())
	}
	m = runKey(t, m, "q") // close the popup so rowLine reads the grid row, not the popup header

	// OPTIMISTIC: the removed tag is gone from the row, the kept one remains.
	line := rowLine(m.View(), "untagme")
	if strings.Contains(line, "dropme2") {
		t.Errorf("removed tag still shown optimistically: %q", line)
	}
	if !strings.Contains(line, "keepme1") {
		t.Errorf("kept tag vanished optimistically: %q", line)
	}

	// DAEMON TRUTH: the record dropped exactly dropme2.
	var tags []string
	for _, th2 := range sb.listThreads(t) {
		if th2.ID == th.ID {
			tags = th2.Tags
		}
	}
	if containsStrTest(tags, "dropme2") {
		t.Errorf("daemon record still has dropme2: %v", tags)
	}
	if !containsStrTest(tags, "keepme1") {
		t.Errorf("daemon record lost keepme1 (over-removed): %v", tags)
	}
}

func containsStrTest(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// claimActionReparent: `P` opens a prompt to paste a parent uuid; submitting really
// sets the thread's parent on the daemon (the moved node renders nested under it), a
// cycle is refused LOUDLY with the record untouched, and an empty submit detaches the
// thread back to a root. The TUI mirror of the `thread reparent` verb.
func claimActionReparent(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	alpha := sb.newHeadlessThread(t, "pi", "alpha")
	beta := sb.newHeadlessThread(t, "pi", "beta")

	bin := seshBin(t)
	env := []string{"SESH_HOME=" + sb.Home, "SESH_MACHINE=" + sb.Machine}
	m := tui.New(sb.Home+"/daemon.sock", false).WithExec(bin, env).WithLocal(sb.Machine, sb.TmuxSocket)
	if !waitUntil(20*time.Second, func() bool { m, _ = render(t, m); return len(m.Rows()) == 2 }) {
		t.Fatalf("2 rows never appeared")
	}

	// Put the cursor on beta (the node we move), paste alpha's full uuid, submit.
	m = selectRowByName(t, m, "beta")
	m = runKey(t, m, "P")
	if !m.Prompting() {
		t.Fatalf("P did not open the reparent prompt")
	}
	m = typeText(t, m, alpha.ID)
	m = runSpecial(t, m, tea.KeyEnter)
	if m.LastErr() != nil {
		t.Fatalf("reparent errored: %v", m.LastErr())
	}
	if !waitUntil(10*time.Second, func() bool { return threadParentOf(t, sb, beta.ID) == alpha.ID }) {
		t.Fatalf("daemon did not set beta.parent = alpha (%s); got %q", alpha.ID, threadParentOf(t, sb, beta.ID))
	}
	// The moved node renders nested under alpha (its ancestors auto-expand via preselect).
	if !waitUntil(10*time.Second, func() bool {
		m, _ = render(t, m)
		l := rowLine(m.View(), "beta")
		return strings.Contains(l, "└") || strings.Contains(l, "├")
	}) {
		t.Errorf("beta is not rendered nested under alpha:\n%s", m.View())
	}

	// Cycle guard: making alpha a child of its own descendant beta is refused LOUDLY,
	// and alpha's record stays a root.
	m = selectRowByName(t, m, "alpha")
	m = runKey(t, m, "P")
	m = typeText(t, m, beta.ID)
	m = runSpecial(t, m, tea.KeyEnter)
	if m.LastErr() == nil {
		t.Errorf("reparenting alpha under its descendant beta was NOT refused")
	}
	if p := threadParentOf(t, sb, alpha.ID); p != "" {
		t.Errorf("alpha.parent changed despite the cycle rejection: %q", p)
	}

	// Detach: an empty submit makes beta a root again (asserted via daemon truth — a
	// stale UI error from the cycle test may linger in lastErr, which is fine).
	m = selectRowByName(t, m, "beta")
	m = runKey(t, m, "P")
	m = runSpecial(t, m, tea.KeyEnter) // empty input = root
	if !waitUntil(10*time.Second, func() bool { return threadParentOf(t, sb, beta.ID) == "" }) {
		t.Errorf("empty submit did not detach beta to a root; parent=%q", threadParentOf(t, sb, beta.ID))
	}
}

// selectRowByName moves the cursor (wrapping) until the selected row has the given name.
func selectRowByName(t *testing.T, m tui.Model, name string) tui.Model {
	t.Helper()
	for i := 0; i < len(m.Rows())+2; i++ {
		if row, ok := m.Selected(); ok && row.Name == name {
			return m
		}
		m = runSpecial(t, m, tea.KeyDown)
	}
	if row, ok := m.Selected(); !ok || row.Name != name {
		t.Fatalf("could not select row %q", name)
	}
	return m
}

// threadParentOf reads a thread's parent id from the daemon's truth.
func threadParentOf(t *testing.T, sb *Sandbox, id string) string {
	t.Helper()
	for _, th := range sb.listThreads(t) {
		if th.ID == id {
			return th.Parent
		}
	}
	return ""
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

// claimNotifyToggle: `n` really flips the selected thread's notify gate on the
// daemon, and the NTF column renders the muted state.
func claimNotifyToggle(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, "pi", "muteme")

	m := tui.New(sb.Home+"/daemon.sock", false).
		WithExec(seshBin(t), []string{"SESH_HOME=" + sb.Home, "SESH_MACHINE=" + sb.Machine}).
		WithLocal(sb.Machine, sb.TmuxSocket)
	m, _ = renderUntilRow(t, m, "muteme")
	// Default notify=on → the NTF bell ◉ is shown.
	if !strings.Contains(rowLine(m.View(), "muteme"), "▪") {
		t.Errorf("NTF column missing the bell for a notify-on thread: %q", rowLine(m.View(), "muteme"))
	}
	m = runKey(t, m, "n")
	if threadByName(t, sb, "muteme").Notify {
		t.Fatalf("n did not flip the gate off on the daemon")
	}
	// OPTIMISTIC: the bell disappears IMMEDIATELY — runKey drained only the action,
	// so the reconcile fetch hasn't run; this is the optimistic overlay, not a
	// snapshot refresh.
	if strings.Contains(rowLine(m.View(), "muteme"), "▪") {
		t.Errorf("NTF bell not cleared optimistically after muting: %q", rowLine(m.View(), "muteme"))
	}
	// And it STAYS cleared across reconciling fetches (sticky until the daemon agrees).
	if !waitUntil(15*time.Second, func() bool {
		m, _ = render(t, m)
		return !strings.Contains(rowLine(m.View(), "muteme"), "▪")
	}) {
		t.Errorf("NTF bell still shown after muting: %q", rowLine(m.View(), "muteme"))
	}
	m = runKey(t, m, "n")
	if !threadByName(t, sb, "muteme").Notify {
		t.Errorf("second n did not flip the gate back on")
	}
	// Re-enabled → the bell returns.
	if !waitUntil(15*time.Second, func() bool {
		m, _ = render(t, m)
		return strings.Contains(rowLine(m.View(), "muteme"), "▪")
	}) {
		t.Errorf("NTF bell did not return after re-enabling: %q", rowLine(m.View(), "muteme"))
	}
	_ = th
}

// claimActionMutateRemote: notify / archive on a thread owned by ANOTHER machine
// ROUTE to the owner. The local daemon doesn't own a remote thread, so a direct
// client call silently missed it (the bug: 'n' did nothing on a remote row). These
// verbs now exec the routed CLI (--machine), like rename/tag. Real ssh-localhost peer.
func claimActionMutateRemote(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	ensureSSHLocalhost(t)
	self := newSandbox(t, matrix.Local)
	self.startDaemon(t)
	peer := newSandbox(t, matrix.Local)
	peer.startDaemon(t)
	bin := seshBin(t)
	if _, stderr, err := self.Runner.Run(t, "peer", "add", "--machine", peer.Machine,
		"--ssh", "localhost", "--home", peer.Home, "--binary", bin, "--tmux-socket", peer.TmuxSocket); err != nil {
		t.Fatalf("peer add: %v\n%s", err, stderr)
	}
	// A thread on the PEER (default notify on). self has none, so it's the only row.
	th := peer.newHeadlessThread(t, "pi", "remote-notif")

	navEnv := []string{"SESH_HOME=" + self.Home, "SESH_MACHINE=" + self.Machine}
	// --all-machines so the peer's thread is visible; WithLocal so a local machine is
	// known (remote rows then get --machine; a local row would not).
	m := tui.New(self.Home+"/daemon.sock", true).WithExec(bin, navEnv).WithLocal(self.Machine, self.TmuxSocket)
	m, _ = renderUntilRow(t, m, "remote-notif")
	if sel, _ := m.Selected(); sel.Name != "remote-notif" {
		t.Fatalf("cursor not on the remote row: %q", sel.Name)
	}

	// 'n' on the REMOTE row must flip the gate ON THE PEER (routed). The pre-fix
	// direct-client call hit self's daemon, which doesn't own the thread → no-op.
	m = runKey(t, m, "n")
	if m.LastErr() != nil {
		t.Fatalf("remote notify errored: %v", m.LastErr())
	}
	if !waitUntil(15*time.Second, func() bool { return !threadByName(t, peer, "remote-notif").Notify }) {
		t.Errorf("notify did not route to the owner (peer still shows notify on) — the remote-mutation bug")
	}
	// Optimistic: the bell cleared immediately on the routed success.
	if strings.Contains(rowLine(m.View(), "remote-notif"), "▪") {
		t.Errorf("remote notify not reflected optimistically: %q", rowLine(m.View(), "remote-notif"))
	}

	// 'a' (archive) on the remote row also routes — the peer's record becomes archived.
	m = runKey(t, m, "a")
	if m.LastErr() != nil {
		t.Fatalf("remote archive errored: %v", m.LastErr())
	}
	if !waitUntil(15*time.Second, func() bool {
		for _, x := range peer.listThreadsArchived(t) {
			if x.ID == th.ID && x.Archived {
				return true
			}
		}
		return false
	}) {
		t.Errorf("archive did not route to the owner (peer record not archived)")
	}
}

// claimMasterCursor: preselecting a NESTED CHILD (the same positionCursorOn path the
// master prefix+s async resolve uses) auto-expands the child's ancestors so the
// otherwise-collapsed child becomes the visible cursor target. Real daemon, real
// parent→child→grandchild tree (children collapsed by default).
func claimMasterCursor(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	parent := sb.newHeadlessThread(t, "pi", "mc-parent")
	if _, stderr, err := sb.Runner.Run(t, "thread", "new", "--agent", "pi", "--name", "mc-child",
		"--cwd", "/tmp", "--headless", "--parent", parent.ID); err != nil {
		t.Fatalf("new child: %v\n%s", err, stderr)
	}
	child := threadByName(t, sb, "mc-child")
	if _, stderr, err := sb.Runner.Run(t, "thread", "new", "--agent", "pi", "--name", "mc-grand",
		"--cwd", "/tmp", "--headless", "--parent", child.ID); err != nil {
		t.Fatalf("new grandchild: %v\n%s", err, stderr)
	}
	grand := threadByName(t, sb, "mc-grand")

	// Children collapsed by default → the grandchild is hidden until its ancestors
	// are expanded. Preselect it (what the async master-cursor resolve feeds).
	m := tui.New(sb.Home+"/daemon.sock", false).WithPreselect(grand.ID)
	// renderUntilRow only finds mc-grand once preselect has expanded mc-parent + mc-child.
	m, _ = renderUntilRow(t, m, "mc-grand")
	if sel, ok := m.Selected(); !ok || sel.ID != grand.ID {
		t.Errorf("cursor not on the nested grandchild after preselect: %+v (ok=%v)", sel, ok)
	}
	// The parent renders as an EXPANDED node (▾), proving the ancestors were opened.
	if pl := rowLine(m.View(), "mc-parent"); !strings.Contains(pl, "▾") {
		t.Errorf("ancestor mc-parent not expanded (no ▾): %q", pl)
	}
}

// claimColumnsReorder: [[tui.column]] moves (absolute position + relative
// anchor) reposition columns over the default set, end-to-end through the
// config loader, rendered against a real thread.
func claimColumnsReorder(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	sb.newHeadlessThread(t, "pi", "ordered")

	// Move NOTIFY to absolute position 1, and CREATED to just after NAME.
	conf := "[[tui.column]]\nname = \"notify\"\nposition = 1\n[[tui.column]]\nname = \"created\"\nafter = \"name\"\n"
	if err := os.WriteFile(filepath.Join(sb.Home, "config.toml"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	tcfg, err := config.LoadTUI(sb.Home)
	if err != nil {
		t.Fatal(err)
	}
	cols, err := tui.ResolveColumns(nil) // default set
	if err != nil {
		t.Fatal(err)
	}
	var moves []tui.ColumnMove
	for _, mv := range tcfg.ColumnMoves {
		moves = append(moves, tui.ColumnMove{Name: mv.Name, After: mv.After, Before: mv.Before, Position: mv.Position})
	}
	cols, err = tui.ApplyColumnMoves(cols, moves)
	if err != nil {
		t.Fatal(err)
	}
	// NOTIFY first, CREATED immediately after NAME.
	if cols[0] != tui.ColNotify {
		t.Errorf("notify not first after position=1: %v", cols)
	}
	ni, ci := -1, -1
	for i, c := range cols {
		if c == tui.ColName {
			ni = i
		}
		if c == tui.ColCreated {
			ci = i
		}
	}
	if ci != ni+1 {
		t.Errorf("created not right after name: %v", cols)
	}

	// The header row renders in that order (NTF column header before MACHINE).
	m := tui.New(sb.Home+"/daemon.sock", false).WithColumns(cols)
	m, view := renderUntilRowView(t, m, "ordered")
	header := firstHeaderLine(view)
	if strings.Index(header, "NTF") > strings.Index(header, "MACHINE") {
		t.Errorf("NTF header not before MACHINE: %q", header)
	}
}

// firstHeaderLine returns the column-header line (the one with MACHINE).
func firstHeaderLine(view string) string {
	for _, l := range strings.Split(view, "\n") {
		if strings.Contains(l, "MACHINE") {
			return l
		}
	}
	return ""
}
