package conformance

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/matrix"
	"github.com/lukastk/sesh/internal/tui"
)

func init() {
	registerTUIClaim("command-palette", claimCommandPalette)
	registerTUIClaim("keymap-config", claimKeymapConfig)
	registerTUIClaim("action-set-parent", claimActionSetParent)
}

// typePaletteQuery types a query into whichever fuzzy popup is open (palette or
// parent picker), one rune at a time as a terminal delivers it.
func typePaletteQuery(t *testing.T, m tui.Model, q string) tui.Model {
	t.Helper()
	for _, r := range q {
		nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = nm.(tui.Model)
		if cmd != nil {
			nm2, _ := m.Update(cmd())
			m = nm2.(tui.Model)
		}
	}
	return m
}

// claimCommandPalette: `p` opens the palette, a FUZZY query finds a command that
// carries NO key at all, and Enter runs it for real — the daemon's own record
// changes. That is the whole premise of the feature (2026-08: the grid's keys were
// cut down to a fixed set and everything else moved here), so it is asserted
// against the real store rather than against model state.
func claimCommandPalette(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, "pi", "palette-me")

	bin := seshBin(t)
	env := []string{"SESH_HOME=" + sb.Home, "SESH_MACHINE=" + sb.Machine}
	m := tui.New(sb.Home+"/daemon.sock", false).WithExec(bin, env).WithLocal(sb.Machine, sb.TmuxSocket)
	m, _ = renderUntilRow(t, m, "palette-me")

	// `p` opens the palette (it used to pin) and lists every command.
	m = runKey(t, m, "p")
	if !m.PaletteOpen() {
		t.Fatalf("`p` did not open the command palette")
	}
	if len(m.PaletteCommands()) != len(tui.Commands()) {
		t.Fatalf("palette listed %d of %d commands", len(m.PaletteCommands()), len(tui.Commands()))
	}
	view := m.View()
	if !strings.Contains(view, "sesh — commands") {
		t.Fatalf("the palette did not take over the screen:\n%s", view)
	}
	// It shows each command's CURRENT key, so it doubles as the discoverable keymap.
	if !strings.Contains(view, "toggle the needs-attention flag") {
		t.Errorf("palette does not list the flag command:\n%s", view)
	}

	// FUZZY search: "tagad" is a subsequence of "add a tag", a command with no key.
	m = typePaletteQuery(t, m, "tagad")
	if got := m.PaletteCommands(); len(got) == 0 || got[0] != "tag-add" {
		t.Fatalf("fuzzy query %q should rank tag-add first, got %v", m.PaletteQuery(), got)
	}
	m = runSpecial(t, m, tea.KeyEnter)
	if m.PaletteOpen() {
		t.Fatalf("enter did not close the palette")
	}
	if !m.Prompting() {
		t.Fatalf("running tag-add from the palette did not open its prompt")
	}
	m = typeText(t, m, "from-palette")
	m = runSpecial(t, m, tea.KeyEnter)
	if m.ActionErr() != nil {
		t.Fatalf("tag-add from the palette errored: %v", m.ActionErr())
	}
	// The REAL record carries the tag — the palette really drove the routed verb.
	if !waitUntil(10*time.Second, func() bool {
		for _, th2 := range sb.listThreads(t) {
			if th2.ID == th.ID {
				for _, tg := range th2.Tags {
					if tg == "from-palette" {
						return true
					}
				}
			}
		}
		return false
	}) {
		t.Fatalf("the daemon never recorded the tag added via the command palette")
	}

	// Esc cancels without running anything: flag the thread from the palette, then
	// prove a cancelled palette leaves the flag exactly as it was.
	m = runCommand(t, m, "flag")
	if !waitUntil(10*time.Second, func() bool { return sb.threadSnapshot(t, th.ID).Flagged }) {
		t.Fatalf("flag from the palette never reached the daemon")
	}
	m = runKey(t, m, "p")
	m = typePaletteQuery(t, m, "flag")
	m = runSpecial(t, m, tea.KeyEsc)
	if m.PaletteOpen() {
		t.Errorf("esc did not close the palette")
	}
	m, _ = render(t, m)
	if !sb.threadSnapshot(t, th.ID).Flagged {
		t.Errorf("esc in the palette ran the highlighted command (the flag was cleared)")
	}
}

// claimKeymapConfig: a [[tui.key]] rebinding really moves a command's key against
// a live daemon — the NEW key performs the routed action, and the key it was moved
// off does nothing. Both halves matter: a rebind that only added a key (leaving the
// old one live) would look right in the help and be wrong in the hand.
func claimKeymapConfig(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, "pi", "rebind-me")

	// Exactly what cmd/sesh does with the config's [[tui.key]] entries.
	km, err := tui.ResolveKeymap([]tui.KeySpec{{Command: "flag", Key: "g"}})
	if err != nil {
		t.Fatalf("ResolveKeymap: %v", err)
	}
	bin := seshBin(t)
	env := []string{"SESH_HOME=" + sb.Home, "SESH_MACHINE=" + sb.Machine}
	m := tui.New(sb.Home+"/daemon.sock", false).WithExec(bin, env).
		WithLocal(sb.Machine, sb.TmuxSocket).WithKeymap(km)
	m, _ = renderUntilRow(t, m, "rebind-me")

	// The DEFAULT key must now be inert. Assert it against the real record after a
	// settle window, so this can't pass just because the write hadn't landed yet.
	m = runKey(t, m, "f")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if sb.threadSnapshot(t, th.ID).Flagged {
			t.Fatalf("`f` still flagged the thread after [[tui.key]] moved flag to `g`")
		}
		m, _ = render(t, m)
	}

	// The REBOUND key performs the real routed action.
	m = runKey(t, m, "g")
	if !waitUntil(10*time.Second, func() bool { return sb.threadSnapshot(t, th.ID).Flagged }) {
		t.Fatalf("the rebound key `g` never flagged the thread on the daemon")
	}
	// And the rendered keymap agrees with what the keys actually do.
	m = runKey(t, m, "?")
	help := m.View()
	if !strings.Contains(help, "g ") {
		t.Errorf("the `?` keymap does not show flag on its rebound key g:\n%s", help)
	}
}

// claimActionSetParent: the INTERACTIVE reparent picker — run it on the child,
// pick the new parent from the list, Enter — moves the thread for real. It also
// pins the two guards that keep the picker from offering a choice the daemon would
// refuse: a descendant (a cycle) never appears, and the detach-to-root entry
// really clears the parent.
func claimActionSetParent(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	alpha := sb.newHeadlessThread(t, "pi", "alpha-parent")
	sb.newHeadlessThread(t, "pi", "beta-mover")
	gamma := sb.newHeadlessThread(t, "pi", "gamma-leaf")

	bin := seshBin(t)
	env := []string{"SESH_HOME=" + sb.Home, "SESH_MACHINE=" + sb.Machine}
	m := tui.New(sb.Home+"/daemon.sock", false).WithExec(bin, env).WithLocal(sb.Machine, sb.TmuxSocket)
	if !waitUntil(20*time.Second, func() bool { m, _ = render(t, m); return len(m.Rows()) == 3 }) {
		t.Fatalf("3 rows never appeared")
	}

	// Put gamma under alpha by PICKING alpha — no uuid typed anywhere.
	m = selectRowByName(t, m, "gamma-leaf")
	m = runCommand(t, m, "set-parent")
	if !m.ParentPickOpen() {
		t.Fatalf("set-parent did not open the parent picker")
	}
	if v := m.View(); !strings.Contains(v, `set parent of "gamma-leaf"`) {
		t.Fatalf("the picker does not name the child it acts on:\n%s", v)
	}
	m = typePaletteQuery(t, m, "alpha")
	if got := m.ParentPickCandidates(); len(got) == 0 || got[0] != alpha.ID {
		t.Fatalf("query \"alpha\" should rank alpha-parent first, got %v", got)
	}
	m = runSpecial(t, m, tea.KeyEnter)
	if m.ParentPickOpen() {
		t.Fatalf("enter did not close the picker")
	}
	if m.ActionErr() != nil {
		t.Fatalf("set-parent errored: %v", m.ActionErr())
	}
	if !waitUntil(10*time.Second, func() bool { return threadParentOf(t, sb, gamma.ID) == alpha.ID }) {
		t.Fatalf("daemon did not set gamma.parent = alpha (%s); got %q", alpha.ID, threadParentOf(t, sb, gamma.ID))
	}

	// CYCLE GUARD, against real data: with gamma now a child of alpha, opening the
	// picker on alpha must not offer gamma (or alpha itself). Settle until the model
	// actually SEES the new parent first — asserting the absence against a pre-move
	// snapshot would pass vacuously.
	if !waitUntil(10*time.Second, func() bool {
		m, _ = render(t, m)
		for _, r := range m.Rows() {
			if r.ID == gamma.ID {
				return r.Parent == alpha.ID
			}
		}
		return false
	}) {
		t.Fatalf("the TUI never saw gamma nested under alpha")
	}
	m = selectRowByName(t, m, "alpha-parent")
	m = runCommand(t, m, "set-parent")
	for _, id := range m.ParentPickCandidates() {
		if id == gamma.ID {
			t.Errorf("the picker offered alpha's own descendant gamma — that would be a cycle")
		}
		if id == alpha.ID {
			t.Errorf("the picker offered the thread itself as its own parent")
		}
	}
	m = runSpecial(t, m, tea.KeyEsc)

	// DETACH: the "(root)" entry clears the parent on the real record. It is the
	// first candidate for a thread that has a parent.
	m = selectRowByName(t, m, "gamma-leaf")
	m = runCommand(t, m, "set-parent")
	cands := m.ParentPickCandidates()
	if len(cands) == 0 || cands[0] != "" {
		t.Fatalf("a parented thread should offer detach-to-root first, got %v", cands)
	}
	m = runSpecial(t, m, tea.KeyEnter)
	if !waitUntil(10*time.Second, func() bool { return threadParentOf(t, sb, gamma.ID) == "" }) {
		t.Fatalf("picking (root) did not clear gamma's parent; got %q", threadParentOf(t, sb, gamma.ID))
	}
}
