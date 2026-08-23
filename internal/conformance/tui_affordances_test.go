package conformance

// The (a)-list TUI affordances (v1-parity audit, 2026-06-10): esc-quit, Tab view
// cycling, line-prompt rename/tag, cursor wrap, the ID column toggle, and
// --cursor preselect. Same discipline as every other claim: a REAL daemon, real
// threads, assertions against independently-fetched truth.

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/config"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/lukastk/sesh/internal/api"
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
	registerTUIClaim("column-colors", claimColumnColors)
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
	registerTUIClaim("action-hold", claimActionHold)
	registerTUIClaim("view-hold", claimViewHold)
	registerTUIClaim("view-active-archived-live", claimViewActiveArchivedLive)
	registerTUIClaim("view-archived-order", claimViewArchivedOrder)
	registerTUIClaim("column-max-width", claimColumnMaxWidth)
	registerTUIClaim("thread-details", claimThreadDetails)
}

// claimColumnMaxWidth: the per-column width cap against a REAL thread. By default a
// full-width NAME longer than the built-in cap is truncated with an ellipsis; the `w`
// key toggles the cap off so the full name is shown; and a [[tui.column_width]] config
// override raises the cap so a wide name fits again (config -> render).
func claimColumnMaxWidth(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	// 50 chars — longer than the default 40-char NAME cap. "TAILEND" is the last 7 chars,
	// so it is present iff the whole name renders (uncapped / a big-enough cap).
	longName := "capdemo-" + strings.Repeat("z", 35) + "-TAILEND"
	sb.newHeadlessThread(t, "pi", longName)

	cols := []string{tui.ColName}
	// Default: cap ON. The rendered NAME is truncated — no TAILEND, an ellipsis present.
	m := tui.New(sb.Home+"/daemon.sock", false).WithColumns(cols)
	m, view := renderUntilRowView(t, m, longName)
	if !m.MaxColWidth() {
		t.Fatalf("the width cap should default ON")
	}
	if strings.Contains(view, "TAILEND") {
		t.Errorf("capped NAME should not show the tail of a >40-char name:\n%s", view)
	}
	if !strings.Contains(view, "…") {
		t.Errorf("capped NAME should show a truncation ellipsis:\n%s", view)
	}

	// `w` toggles the cap OFF: the full name (incl. TAILEND) is now visible.
	m = runKey(t, m, "w")
	if m.MaxColWidth() {
		t.Fatalf("`w` did not toggle the cap off")
	}
	if v := m.View(); !strings.Contains(v, "TAILEND") {
		t.Errorf("with the cap off the full NAME should be visible:\n%s", v)
	}

	// A [[tui.column_width]] override raises the cap above the name length, so the full
	// name shows even with the cap ON (proves config -> render).
	if err := os.WriteFile(filepath.Join(sb.Home, "config.toml"),
		[]byte("[[tui.column_width]]\nname = \"name\"\nmax = 60\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tcfg, err := config.LoadTUI(sb.Home)
	if err != nil {
		t.Fatal(err)
	}
	var specs []tui.ColumnWidthSpec
	for _, cw := range tcfg.ColumnWidths {
		specs = append(specs, tui.ColumnWidthSpec{Name: cw.Name, Max: cw.Max})
	}
	widths, err := tui.ResolveColumnWidths(specs)
	if err != nil {
		t.Fatal(err)
	}
	m2 := tui.New(sb.Home+"/daemon.sock", false).WithColumns(cols).
		WithMaxColumnWidths(tcfg.MaxColumnWidthsDefault()).WithColumnWidths(widths)
	m2, view2 := renderUntilRowView(t, m2, longName)
	if !m2.MaxColWidth() {
		t.Fatalf("the cap should still be ON (only the width was overridden)")
	}
	if !strings.Contains(view2, "TAILEND") {
		t.Errorf("a column_width override of 60 should show the full 50-char NAME:\n%s", view2)
	}
}

// claimThreadDetails: `I` opens a read-only takeover showing the selected thread's
// REAL fields (its full uuid, machine, agent, cwd, and live runtime axis), and esc
// closes it back to the grid.
func claimThreadDetails(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := sb.newHeadlessThreadAt(t, "pi", "detailme", "/tmp")

	m := tui.New(sb.Home+"/daemon.sock", false).WithLocal(sb.Machine, sb.TmuxSocket)
	m, _ = renderUntilRow(t, m, "detailme")

	m = runKey(t, m, "I")
	if !m.DetailsOpen() {
		t.Fatalf("`I` did not open the thread-details view")
	}
	view := m.View()
	for _, want := range []string{th.ID, "detailme", sb.Machine, "pi", "/tmp", "headless"} {
		if !strings.Contains(view, want) {
			t.Errorf("details view missing %q:\n%s", want, view)
		}
	}
	// The FULL uuid is shown (not just tid8) — this is real thread data, not a fixture.
	if strings.Count(view, th.ID) == 0 {
		t.Errorf("details view should show the full real uuid %s", th.ID)
	}

	// esc closes it — back to the grid (the row is visible again).
	m = runSpecial(t, m, tea.KeyEsc)
	if m.DetailsOpen() {
		t.Fatalf("esc should close the details view")
	}
	if !strings.Contains(m.View(), "detailme") {
		t.Errorf("closing details should return to the grid:\n%s", m.View())
	}
}

// nextView advances the grid one view: Tab opens the view PICKER on the
// CURRENT view, a second Tab advances one, Enter applies (the apply's fetch
// runs and feeds back via runSpecial).
func nextView(t *testing.T, m tui.Model) tui.Model {
	t.Helper()
	m = runSpecial(t, m, tea.KeyTab)
	m = runSpecial(t, m, tea.KeyTab)
	return runSpecial(t, m, tea.KeyEnter)
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

// claimQuitEsc: while the line prompt is open, Esc closes the PROMPT and does not
// quit. In normal mode Esc no longer quits either — since the command palette
// landed, esc/q DISMISS the message lines and `quit` lives in the palette, with
// ctrl+c as the always-available kill. This claim pins all three against a real
// daemon, because "the TUI won't close" and "the TUI closed when I hit esc" are
// both things you only notice in the real thing.
func claimQuitEsc(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	sb.newHeadlessThread(t, "pi", "escme")

	// WithExec/WithLocal are REQUIRED for any claim whose model performs a routed
	// action: without them the TUI shells out to os.Executable() — which under
	// `go test` is the TEST BINARY — and re-runs the whole suite as a subprocess
	// instead of running `sesh`. (This claim only opened popups before, so it got
	// away with the bare constructor; the reparent below does not.)
	bin := seshBin(t)
	env := []string{"SESH_HOME=" + sb.Home, "SESH_MACHINE=" + sb.Machine}
	m := tui.New(sb.Home+"/daemon.sock", false).WithExec(bin, env).WithLocal(sb.Machine, sb.TmuxSocket)
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

	// Normal mode: Esc DISMISSES rather than quitting. Give it something to
	// dismiss first — a real refused action on a real record — so the assertion
	// cannot pass vacuously against an already-empty message line.
	m = runCommand(t, m, "set-parent-uuid")
	m = typeText(t, m, "not-a-real-uuid")
	m = runSpecial(t, m, tea.KeyEnter)
	if !waitUntil(10*time.Second, func() bool {
		m, _ = render(t, m)
		return m.ActionErr() != nil
	}) {
		t.Fatalf("reparent to a bogus uuid should have set a loud action error")
	}
	nm, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = nm.(tui.Model)
	if cmd != nil {
		if _, quit := cmd().(tea.QuitMsg); quit {
			t.Fatalf("Esc in normal mode must NOT quit any more — quit moved to the palette")
		}
	}
	if m.ActionErr() != nil {
		t.Errorf("Esc in normal mode should dismiss the error line, still: %v", m.ActionErr())
	}

	// ctrl+c is the always-available kill and cannot be configured away.
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatalf("ctrl+c returned no command (want quit)")
	}
	if _, quit := cmd().(tea.QuitMsg); !quit {
		t.Errorf("ctrl+c did not quit (got %T)", cmd())
	}

	// And `quit` really is reachable from the palette.
	m2 := runCommandNoApply(t, m, "quit")
	if m2 == nil {
		t.Errorf("quit is not reachable from the command palette")
	}
}

// runCommandNoApply drives the palette to a command and returns the tea.Cmd Enter
// produced WITHOUT feeding it back into the model — for a command (like quit)
// whose message must not be applied mid-claim.
func runCommandNoApply(t *testing.T, m tui.Model, id string) tea.Cmd {
	t.Helper()
	m = runKey(t, m, "p")
	idx := -1
	for i, c := range m.PaletteCommands() {
		if c == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatalf("command %q is not in the palette", id)
	}
	for range idx {
		m = runSpecial(t, m, tea.KeyDown)
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return cmd
}

// claimViewCycleTab: Tab cycles active -> on hold -> archived -> all (REAL archived
// state from the daemon decides each view's rows), and the title names the current view.
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
	// The built-in cycle is active -> on hold -> archived -> all -> active. Settle on
	// BOTH threads being PUBLISHED (the [all] condition) before asserting — waiting on
	// absence alone is trivially true pre-publish and races the maintainer (this claim
	// flaked exactly that way once). Three Tabs from active reaches [all].
	var view string
	m = nextView(t, m) // -> [on hold]
	m = nextView(t, m) // -> [archived]
	m = nextView(t, m) // -> [all]
	if !waitUntil(25*time.Second, func() bool {
		m, view = render(t, m)
		return strings.Contains(view, "stayme") && strings.Contains(view, "parkme")
	}) {
		t.Fatalf("[all] never showed both threads:\n%s", view)
	}
	if !strings.Contains(view, "[all]") {
		t.Errorf("title missing [all]: %q", firstLine(view))
	}

	m = nextView(t, m) // wraps -> [active]
	view = m.View()
	if !strings.Contains(view, "[active]") {
		t.Errorf("title missing [active]: %q", firstLine(view))
	}
	if !strings.Contains(view, "stayme") || strings.Contains(view, "parkme") {
		t.Errorf("active view rows wrong (want stayme only):\n%s", view)
	}

	m = nextView(t, m) // -> [on hold] (empty — neither thread is held)
	view = m.View()
	if !strings.Contains(view, "[on hold]") {
		t.Errorf("on-hold view title missing [on hold]: %q", firstLine(view))
	}
	if strings.Contains(view, "stayme") || strings.Contains(view, "parkme") {
		t.Errorf("on-hold view should be empty (neither thread is held):\n%s", view)
	}

	m = nextView(t, m) // -> archived
	view = m.View()
	if !strings.Contains(view, "[archived]") {
		t.Errorf("archived view title missing [archived]: %q", firstLine(view))
	}
	if strings.Contains(view, "stayme") || !strings.Contains(view, "parkme") {
		t.Errorf("archived view rows wrong (want parkme only):\n%s", view)
	}

	m = nextView(t, m) // -> all
	m = nextView(t, m) // -> back to active
	if m.CurrentView() != tui.ViewActive {
		t.Errorf("view did not cycle back to active, got %v", m.CurrentView())
	}

	// The PICKER itself: Tab opens a popup listing every view with the current
	// one annotated; Esc cancels without switching; a mouse CLICK on a view line
	// applies it directly (rows render from line 2 — the click contract).
	m = runSpecial(t, m, tea.KeyTab)
	pv := m.View()
	for _, want := range []string{"active", "on hold", "archived", "all", "(current)"} {
		if !strings.Contains(pv, want) {
			t.Errorf("view picker missing %q:\n%s", want, pv)
		}
	}
	m = runSpecial(t, m, tea.KeyEsc)
	if m.CurrentView() != tui.ViewActive || strings.Contains(m.View(), "(current)") {
		t.Errorf("esc should cancel the picker and keep [active], got %v", m.CurrentView())
	}
	m = runSpecial(t, m, tea.KeyTab) // reopen
	// Click the "archived" line: index 2 → terminal row 2+2.
	nm, cmd := m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 3, Y: 4})
	m = nm.(tui.Model)
	if cmd != nil {
		nm2, _ := m.Update(cmd()) // the apply's fetch
		m = nm2.(tui.Model)
	}
	if m.CurrentView() != tui.ViewArchived {
		t.Errorf("clicking the archived line should apply it, got %v", m.CurrentView())
	}
	if !strings.Contains(m.View(), "[archived]") {
		t.Errorf("clicked view not applied: %q", firstLine(m.View()))
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

// claimActionHold: `h` toggles hold — it parks the thread on the daemon (record
// gains a future on_hold_until), the row leaves the default active view at once
// (optimistic hide), and `h` again on the parked thread releases it (record zeroed).
// `H` opens the explicit-date prompt and submitting a date parks it to that date.
func claimActionHold(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, "pi", "holdme")

	bin := seshBin(t)
	env := []string{"SESH_HOME=" + sb.Home, "SESH_MACHINE=" + sb.Machine}
	m := tui.New(sb.Home+"/daemon.sock", false).WithExec(bin, env).WithLocal(sb.Machine, sb.TmuxSocket)
	m, _ = renderUntilRow(t, m, "holdme")

	// h: park until the start of tomorrow. The row leaves the active view at once.
	m = runKey(t, m, "h")
	if strings.Contains(m.View(), "holdme") {
		t.Errorf("held thread did not leave the active view optimistically:\n%s", m.View())
	}
	held := holdUntilOf(t, sb, th.ID)
	if held <= time.Now().Unix() {
		t.Fatalf("h did not park the thread on the daemon (on_hold_until=%d)", held)
	}

	// The `on hold` view (one Tab from active) shows it.
	m = nextView(t, m) // active -> on hold
	if !waitUntil(15*time.Second, func() bool {
		var v string
		m, v = render(t, m)
		return strings.Contains(v, "[on hold]") && strings.Contains(v, "holdme")
	}) {
		_, v := render(t, m)
		t.Fatalf("on-hold view never showed the parked thread:\n%s", v)
	}

	// h again (in the on-hold view) releases it — record zeroed, leaves this view.
	m = runKey(t, m, "h")
	if held := holdUntilOf(t, sb, th.ID); held != 0 {
		t.Errorf("h did not release the hold (on_hold_until=%d)", held)
	}

	// H opens the explicit-date prompt; submitting a date parks it to that date.
	m = nextView(t, m) // on hold -> archived
	m = nextView(t, m) // archived -> all (holdme is visible here)
	m, _ = renderUntilRow(t, m, "holdme")
	m = runCommand(t, m, "hold-until")
	if !m.Prompting() {
		t.Fatalf("H did not open the hold-date prompt")
	}
	for i := 0; i < 12; i++ { // clear any prefilled date so we type a clean value
		m = runSpecial(t, m, tea.KeyBackspace)
	}
	m = typeText(t, m, "2099-01-01")
	m = runSpecial(t, m, tea.KeyEnter)
	want := time.Date(2099, 1, 1, 0, 0, 0, 0, time.Local).Unix()
	if held := holdUntilOf(t, sb, th.ID); held != want {
		t.Errorf("H date prompt did not park to 2099-01-01 (got on_hold_until=%d, want %d)", held, want)
	}
}

// claimViewHold: the DEFAULT view excludes on-hold threads while keeping un-held
// ones, and the `on hold` view is the complement — the core of the feature.
func claimViewHold(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	sb.newHeadlessThread(t, "pi", "working")
	parked := sb.newHeadlessThread(t, "pi", "parked")
	future := time.Now().Add(48 * time.Hour).Unix()
	if _, stderr, err := sb.Runner.Run(t, "thread", "hold", "--id", parked.ID, "--until-unix", strconv.FormatInt(future, 10)); err != nil {
		t.Fatalf("hold: %v\n%s", err, stderr)
	}

	m := tui.New(sb.Home+"/daemon.sock", false)
	// Default view (active): shows `working`, hides `parked`. Settle on PRESENCE of
	// the kept thread first (absence alone is vacuously true pre-publish).
	var view string
	if !waitUntil(25*time.Second, func() bool {
		m, view = render(t, m)
		return strings.Contains(view, "[active]") && strings.Contains(view, "working")
	}) {
		t.Fatalf("active view never showed the un-held thread:\n%s", view)
	}
	if strings.Contains(view, "parked") {
		t.Errorf("default active view should HIDE on-hold threads:\n%s", view)
	}

	// HOLD BEATS FLAG (2026-07-26): FLAGGING the held thread must NOT surface it
	// in active — parking a thread actually parks it.
	if _, stderr, err := sb.Runner.Run(t, "thread", "flag", "--on", "--id", parked.ID); err != nil {
		t.Fatalf("flag --on: %v\n%s", err, stderr)
	}

	// The `on hold` view (one Tab) is the complement: parked shown, working
	// hidden. Settle until parked's ⚑ renders here — that proves the MODEL's
	// data carries the flag, so the active-view recheck below cannot pass
	// vacuously on a stale pre-flag fetch.
	m = nextView(t, m)
	if !waitUntil(10*time.Second, func() bool {
		m, view = render(t, m)
		return strings.Contains(view, "[on hold]") && strings.Contains(view, "parked") && strings.Contains(view, "⚑")
	}) {
		t.Fatalf("on-hold view never showed the parked thread with its flag:\n%s", view)
	}
	if strings.Contains(view, "working") {
		t.Errorf("on-hold view should hide un-held threads:\n%s", view)
	}

	// Back around to active (on hold → archived → all → active): the flagged
	// on-hold thread must STILL be hidden there (hold beats flag).
	m = nextView(t, m)
	m = nextView(t, m)
	m = nextView(t, m)
	if !waitUntil(10*time.Second, func() bool {
		m, view = render(t, m)
		return strings.Contains(view, "[active]") && strings.Contains(view, "working")
	}) {
		t.Fatalf("active view lost its baseline after flagging:\n%s", view)
	}
	if strings.Contains(view, "parked") {
		t.Errorf("active view must keep HIDING a flagged on-hold thread (hold beats flag):\n%s", view)
	}
}

// claimViewArchivedOrder: the archived view orders by most-recently-archived first
// (archived_at DESC). Three threads are archived in sequence (oldest→newest, with
// >1s gaps so the daemon stamps distinct second-granular archived_at values); the
// archived view must render the newest-archived at the top — the order a user
// reaching for "what did I just park" expects.
func claimViewArchivedOrder(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	a := sb.newHeadlessThread(t, "pi", "arch-alpha")
	b := sb.newHeadlessThread(t, "pi", "arch-bravo")
	c := sb.newHeadlessThread(t, "pi", "arch-charlie")

	for i, th := range []api.Thread{a, b, c} {
		if i > 0 {
			time.Sleep(1100 * time.Millisecond) // distinct second-granular archive stamps
		}
		if _, stderr, err := sb.Runner.Run(t, "thread", "archive", "--id", th.ID); err != nil {
			t.Fatalf("archive %s: %v\n%s", th.Name, err, stderr)
		}
	}

	m := tui.New(sb.Home+"/daemon.sock", false)
	// Reach the archived view: Tab twice (active -> on hold -> archived).
	m = nextView(t, m)
	m = nextView(t, m)
	var view string
	if !waitUntil(25*time.Second, func() bool {
		m, view = render(t, m)
		return strings.Contains(view, "[archived]") &&
			strings.Contains(view, "arch-alpha") &&
			strings.Contains(view, "arch-bravo") &&
			strings.Contains(view, "arch-charlie")
	}) {
		t.Fatalf("archived view never showed all three threads:\n%s", view)
	}

	// Most recently archived first: charlie (last archived) above bravo above alpha.
	ia := lineIndexOf(view, "arch-alpha")
	ib := lineIndexOf(view, "arch-bravo")
	ic := lineIndexOf(view, "arch-charlie")
	if !(ic < ib && ib < ia) {
		t.Errorf("archived view not ordered most-recently-archived first:\n  charlie@%d bravo@%d alpha@%d\n%s", ic, ib, ia, view)
	}
}

// claimViewActiveArchivedLive: the DEFAULT (active) view keeps an ARCHIVED thread
// visible WHILE IT IS STILL HEADFUL (a live pane), and hides an archived thread once
// it is headless — i.e. (not archived OR headful) AND not on hold. The shown archived
// row carries the ⊘ gutter glyph so it is recognisable. This is the whole feature, and
// it is proven against REAL runtime state: a real headed agent (headful) vs a real
// headless record, both archived on the daemon.
func claimViewActiveArchivedLive(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)

	// A real headed agent → headful (a live pane); archive it while it keeps running.
	live := sb.newThread(t, "pi", "livearch", "/tmp")
	sb.waitThreadReady(t, live.ID, "pi")
	// A headless record → not headful; archive it too (it must drop out of the view).
	parked := sb.newHeadlessThread(t, "pi", "parkedarch")
	for _, th := range []api.Thread{live, parked} {
		if _, stderr, err := sb.Runner.Run(t, "thread", "archive", "--id", th.ID); err != nil {
			t.Fatalf("archive %s: %v\n%s", th.Name, err, stderr)
		}
	}

	m := tui.New(sb.Home+"/daemon.sock", false)
	// Settle on the archived-but-headful thread appearing in the DEFAULT view WITH the ⊘
	// glyph. Requiring the glyph (not just the row) is the honest wait: `livearch` is
	// headful so it would render in the active view even before the archive propagates to
	// the maintainer's snapshot — the ⊘ only shows once the row really reads archived, so
	// this proves "archived AND headful AND kept in the default view" together.
	var view string
	if !waitUntil(25*time.Second, func() bool {
		m, view = render(t, m)
		line := lineWith(view, "livearch")
		return strings.Contains(view, "[active]") && strings.Contains(line, "⊘")
	}) {
		t.Fatalf("default view never showed the archived-but-headful thread with the ⊘ glyph:\n%s", view)
	}
	// The archived-but-HEADLESS thread must NOT appear in the default view (by the time
	// livearch reads archived, parkedarch was archived in the same tick and is hidden).
	if strings.Contains(view, "parkedarch") {
		t.Errorf("default view must HIDE an archived HEADLESS thread:\n%s", view)
	}
}

// lineWith returns the first rendered line containing sub ("" if absent).
func lineWith(view, sub string) string {
	for _, l := range strings.Split(view, "\n") {
		if strings.Contains(l, sub) {
			return l
		}
	}
	return ""
}

// lineIndexOf returns the index of the first rendered line containing sub (-1 if
// absent) — used to assert relative render order between rows.
func lineIndexOf(view, sub string) int {
	for i, line := range strings.Split(view, "\n") {
		if strings.Contains(line, sub) {
			return i
		}
	}
	return -1
}

// holdUntilOf reads a thread's persisted on_hold_until from the daemon record.
func holdUntilOf(t *testing.T, sb *Sandbox, id string) int64 {
	t.Helper()
	for _, th := range sb.listThreads(t) {
		if th.ID == id {
			return th.OnHoldUntilUnix
		}
	}
	t.Fatalf("thread %s not found in listing", id)
	return 0
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

	m = runCommand(t, m, "tag-add")
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
	m = runCommand(t, m, "tag-remove")
	if !strings.Contains(m.View(), "remove tag") {
		t.Fatalf("T did not open the remove-tag popup: %q", m.View())
	}
	m = runSpecial(t, m, tea.KeyDown)
	m = runSpecial(t, m, tea.KeyEnter)
	if m.ActionErr() != nil {
		t.Fatalf("untag action errored: %v", m.ActionErr())
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
	m = runCommand(t, m, "set-parent-uuid")
	if !m.Prompting() {
		t.Fatalf("set-parent-uuid did not open the reparent prompt")
	}
	m = typeText(t, m, alpha.ID)
	m = runSpecial(t, m, tea.KeyEnter)
	if m.ActionErr() != nil {
		t.Fatalf("reparent errored: %v", m.ActionErr())
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
	m = runCommand(t, m, "set-parent-uuid")
	m = typeText(t, m, beta.ID)
	m = runSpecial(t, m, tea.KeyEnter)
	if m.ActionErr() == nil {
		t.Errorf("reparenting alpha under its descendant beta was NOT refused")
	}
	// The warning PERSISTS across reconcile fetches (the bug: action errors used to be
	// cleared by the very next poll, so a failed reparent flashed and vanished).
	m, _ = render(t, m)
	if m.ActionErr() == nil {
		t.Errorf("the cycle-rejection warning vanished after a reconcile fetch (should persist)")
	}
	if p := threadParentOf(t, sb, alpha.ID); p != "" {
		t.Errorf("alpha.parent changed despite the cycle rejection: %q", p)
	}

	// Self-parent: reparenting beta under ITSELF is refused LOUDLY, record untouched.
	beforeParent := threadParentOf(t, sb, beta.ID)
	m = selectRowByName(t, m, "beta")
	m = runCommand(t, m, "set-parent-uuid")
	m = typeText(t, m, beta.ID)
	m = runSpecial(t, m, tea.KeyEnter)
	if m.ActionErr() == nil {
		t.Errorf("reparenting beta under itself was NOT refused")
	}
	if p := threadParentOf(t, sb, beta.ID); p != beforeParent {
		t.Errorf("beta.parent changed despite the self-parent rejection: %q", p)
	}

	// A non-existent parent uuid is refused LOUDLY (no silent no-op).
	m = selectRowByName(t, m, "beta")
	m = runCommand(t, m, "set-parent-uuid")
	m = typeText(t, m, "ffffffff-ffff-ffff-ffff-ffffffffffff")
	m = runSpecial(t, m, tea.KeyEnter)
	if m.ActionErr() == nil {
		t.Errorf("reparenting under a non-existent uuid was NOT refused (silent no-op)")
	}

	// Detach: an empty submit makes beta a root again (asserted via daemon truth — a
	// stale UI error from the prior rejections may linger in actionErr, which is fine).
	m = selectRowByName(t, m, "beta")
	m = runCommand(t, m, "set-parent-uuid")
	m = runSpecial(t, m, tea.KeyEnter) // empty input = root
	if !waitUntil(10*time.Second, func() bool { return threadParentOf(t, sb, beta.ID) == "" }) {
		t.Errorf("empty submit did not detach beta to a root; parent=%q", threadParentOf(t, sb, beta.ID))
	}
}

var ansiRE = regexp.MustCompile("\x1b\\[[0-9;]*m")

func stripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

// claimColumnColors: the per-column colour system (NAME/CWD defaults +
// [[tui.column_color]]) actually tints cells — and, crucially, the colour is purely
// cosmetic: stripping the ANSI from a coloured row yields the SAME text/layout as an
// uncoloured render, so colour never shifts a column. A non-selected row is used (a
// selected row is reverse-video, which suppresses the per-column tint by design).
func claimColumnColors(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	// Force a colour profile: under `go test` stdout isn't a TTY, so lipgloss would
	// otherwise strip all colour and the assertion would be vacuous.
	prof := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prof)

	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	sb.newHeadlessThread(t, "pi", "colorme")
	sb.newHeadlessThread(t, "pi", "cursor-here")

	colors, err := tui.ResolveColumnColors(nil) // defaults: NAME blue, CWD green
	if err != nil {
		t.Fatal(err)
	}
	cols := []string{tui.ColMachine, tui.ColName, tui.ColCwd}
	mkModel := func() tui.Model {
		return tui.New(sb.Home+"/daemon.sock", false).WithLocal(sb.Machine, sb.TmuxSocket).WithColumns(cols)
	}
	mc := mkModel().WithColumnColors(colors) // coloured
	mp := mkModel()                          // control: no colours
	if !waitUntil(20*time.Second, func() bool {
		mc, _ = render(t, mc)
		mp, _ = render(t, mp)
		return len(mc.Rows()) == 2 && len(mp.Rows()) == 2
	}) {
		t.Fatalf("2 rows never appeared")
	}
	// Park the cursor on the OTHER row so "colorme" renders non-selected (coloured).
	mc = selectRowByName(t, mc, "cursor-here")
	mp = selectRowByName(t, mp, "cursor-here")

	colored := rowLine(mc.View(), "colorme")
	plain := rowLine(mp.View(), "colorme")

	// Colour really emitted on the coloured render, and absent on the control.
	if !strings.Contains(colored, "\x1b[") {
		t.Errorf("no ANSI colour in the coloured row: %q", colored)
	}
	if strings.Contains(plain, "\x1b[") {
		t.Errorf("control row unexpectedly carries ANSI: %q", plain)
	}
	// Cosmetic only: stripping the colour yields the identical layout — colour must
	// not change column widths or content.
	if stripANSI(colored) != plain {
		t.Errorf("colour shifted the layout:\n colored(stripped)=%q\n plain          =%q", stripANSI(colored), plain)
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
	// Longer than any fixed column (so it proves NAME is full-width) but within the
	// default width cap (40) so it renders untruncated here — the cap itself is proven
	// by the column-max-width claim.
	longName := "a-long-thread-name-under-the-cap"
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

	// Unconfigured: the same row falls back to the raw path (outside ~ here). The temp
	// boxDir is longer than the default CWD width cap, so disable the cap (`w`) for this
	// assertion — it's about labeling/fallback, not width (covered by column-max-width).
	m2 := tui.New(sb.Home+"/daemon.sock", false).WithColumns([]string{tui.ColName, tui.ColCwd}).WithMaxColumnWidths(false)
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
	if m.ActionErr() != nil {
		t.Fatalf("remote notify errored: %v", m.ActionErr())
	}
	if !waitUntil(15*time.Second, func() bool { return !threadByName(t, peer, "remote-notif").Notify }) {
		t.Errorf("notify did not route to the owner (peer still shows notify on) — the remote-mutation bug")
	}
	// Optimistic: the bell cleared immediately on the routed success.
	if strings.Contains(rowLine(m.View(), "remote-notif"), "▪") {
		t.Errorf("remote notify not reflected optimistically: %q", rowLine(m.View(), "remote-notif"))
	}

	// 'a' (archive) on the remote row also routes — the peer's record becomes
	// archived. Archive is INSTANT since H54 (no confirmation; U undoes), and
	// since the keypress-optimism change the row leaves the grid at the press.
	m = runKey(t, m, "a")
	if m.Confirming() {
		t.Fatalf("a opened a confirmation — archive must be instant")
	}
	if m.ActionErr() != nil {
		t.Fatalf("remote archive errored: %v", m.ActionErr())
	}
	if _, ok := rowByName(m, "remote-notif"); ok {
		t.Errorf("archived remote row still rendered immediately (keypress optimism missing)")
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

	// Wait until the daemon has PUBLISHED all three rows, so the preselecting model's
	// FIRST fetch already has the complete parent←child←grand chain. Otherwise mc-grand
	// can momentarily orphan-promote to a root (before mc-child publishes), consuming the
	// one-shot preselect prematurely and leaving mc-parent collapsed (a load-only flake).
	probe := tui.New(sb.Home+"/daemon.sock", false)
	if !waitUntil(25*time.Second, func() bool { probe, _ = render(t, probe); return len(probe.Rows()) == 3 }) {
		t.Fatalf("the parent/child/grand tree never fully published (got %d rows)", len(probe.Rows()))
	}

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
