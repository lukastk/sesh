package conformance

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/matrix"
	"github.com/lukastk/sesh/internal/tui"
)

func init() { registerTUIClaim("goto-uuid", claimGotoUUID) }

// claimGotoUUID: the `goto-uuid` command jumps the selection to a thread named ONLY
// by its uuid — including one the CURRENT view hides, where it switches to the first
// view that shows it and lands the cursor there. Driven against a REAL daemon with
// REAL archived state, because "which view is this thread in" is exactly what a
// stubbed row set cannot prove; and driven through the COMMAND PALETTE, the real
// user path for a command with no key.
//
// EVERY destination view is seeded with a DECOY row that sorts ABOVE the target
// (alphabetically in `active`; archived last, so it heads the archived view's
// most-recently-archived-first order). Without it the target would be the only row
// in its view, so a cursor left at position 0 would "land" on it by accident and the
// claim would pass without the jump doing anything — which is exactly what a
// neutered preselect did on the first version of this test.
func claimGotoUUID(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	sb.newHeadlessThread(t, "pi", "goto-aaa-live")  // decoy: heads the active view
	live := sb.newHeadlessThread(t, "pi", "goto-live")
	parked := sb.newHeadlessThread(t, "pi", "goto-parked")
	decoyAttic := sb.newHeadlessThread(t, "pi", "goto-aaa-attic") // decoy: heads the archived view

	// WithExec/WithLocal so nothing in the model can shell out to the TEST binary.
	bin := seshBin(t)
	env := []string{"SESH_HOME=" + sb.Home, "SESH_MACHINE=" + sb.Machine}
	m := tui.New(sb.Home+"/daemon.sock", false).WithExec(bin, env).WithLocal(sb.Machine, sb.TmuxSocket)
	m = renderUntilCount(t, m, 4)

	// Park two for REAL. `parked` is archived FIRST so the decoy heads the archived
	// view under either tie-break (archived_at DESC, then name).
	for _, id := range []string{parked.ID, decoyAttic.ID} {
		if _, stderr, err := sb.Runner.Run(t, "thread", "archive", "--id", id); err != nil {
			t.Fatalf("archive: %v\n%s", err, stderr)
		}
	}
	// ...and wait until the default view genuinely stops showing them (settling on
	// their earlier PRESENCE above, so the jump below can't pass vacuously).
	m = renderUntilGone(t, m, "goto-parked")
	m = renderUntilGone(t, m, "goto-aaa-attic")

	// GOTO the ARCHIVED thread by its SHORT id: the grid must switch to the first
	// view that shows it (archived) and put the cursor on it.
	m = notAlreadyOn(t, m, parked.ID)
	m = gotoInTUI(t, m, parked.ID[:8])
	m, view := settleCursorOn(t, m, parked.ID)
	if m.ActionErr() != nil {
		t.Fatalf("goto to an archived thread errored: %v", m.ActionErr())
	}
	if m.CurrentView() != tui.ViewArchived {
		t.Errorf("goto landed in view %d, want the archived view; render:\n%s", m.CurrentView(), view)
	}
	if !strings.Contains(view, "[archived]") || !strings.Contains(view, "goto-parked") {
		t.Errorf("archived view does not render the thread jumped to:\n%s", view)
	}

	// And back the other way: from `archived`, a goto to the still-active thread
	// switches to `active` — the view is picked per thread, not once.
	m = notAlreadyOn(t, m, live.ID)
	m = gotoInTUI(t, m, live.ID)
	m, view = settleCursorOn(t, m, live.ID)
	if m.ActionErr() != nil {
		t.Fatalf("goto back to the active thread errored: %v", m.ActionErr())
	}
	if m.CurrentView() != tui.ViewActive {
		t.Errorf("goto landed in view %d, want the active view; render:\n%s", m.CurrentView(), view)
	}

	// A uuid nothing matches is refused LOUDLY and moves nothing.
	m = gotoInTUI(t, m, "deadbeef")
	if m.ActionErr() == nil {
		t.Errorf("goto to an unknown uuid must set a loud error")
	}
	if got, _ := m.Selected(); got.ID != live.ID {
		t.Errorf("a refused goto moved the cursor to %q", got.Name)
	}
	if m.CurrentView() != tui.ViewActive {
		t.Errorf("a refused goto changed the view to %d", m.CurrentView())
	}
}

// notAlreadyOn fails the claim if the cursor already rests on the thread the next
// jump targets — the jump would then prove nothing.
func notAlreadyOn(t *testing.T, m tui.Model, id string) tui.Model {
	t.Helper()
	if got, ok := m.Selected(); ok && got.ID == id {
		t.Fatalf("baseline: cursor is ALREADY on %s (%s) — the jump would prove nothing", id[:8], got.Name)
	}
	return m
}

// gotoInTUI runs `goto-uuid` THROUGH THE PALETTE and submits a uuid into its prompt
// — the real keystroke path (p → the command → the typed uuid → enter).
func gotoInTUI(t *testing.T, m tui.Model, uuid string) tui.Model {
	t.Helper()
	m = runCommand(t, m, "goto-uuid")
	if !m.Prompting() {
		t.Fatalf("goto-uuid did not open its prompt")
	}
	if v := m.View(); !strings.Contains(v, "go to uuid") {
		t.Fatalf("the goto prompt is not rendered:\n%s", v)
	}
	m = typeText(t, m, uuid)
	return runSpecial(t, m, tea.KeyEnter)
}

// settleCursorOn polls the REAL fetch until the cursor rests on id (a view switch
// lands the cursor via the refetch, not synchronously).
func settleCursorOn(t *testing.T, m tui.Model, id string) (tui.Model, string) {
	t.Helper()
	var view string
	if !waitUntil(15*time.Second, func() bool {
		m, view = render(t, m)
		r, ok := m.Selected()
		return ok && r.ID == id
	}) {
		t.Fatalf("cursor never landed on %s (err=%v); render:\n%s", id[:8], m.ActionErr(), view)
	}
	return m, view
}
