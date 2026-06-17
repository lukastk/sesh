package conformance

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
	"github.com/lukastk/sesh/internal/tui"
)

// claimDescendantRunningGlyph: the third HB glyph (↓) on a PARENT row tracks
// whether a REAL descendant thread is running a turn — and it flips BOTH ways
// (appears when a real child turn starts, clears when it finishes). The parent
// itself is quiet throughout, so the glyph is purely about the subtree. This is
// the matrix's both-directions honesty applied to the descendant indicator: the
// child's busy state is real (an actual agent turn), independently fetched.
func claimDescendantRunningGlyph(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)

	// A headless parent (no pane of its own — purely a tree node) and a real headed
	// child whose turns we can drive.
	parent := sb.newHeadlessThread(t, "pi", "bossthread")
	childOut, stderr, err := sb.Runner.Run(t, "thread", "new", "--agent", "pi",
		"--name", "kidthread", "--cwd", "/tmp", "--parent", parent.ID, "--json")
	if err != nil {
		t.Fatalf("new child --parent: %v\n%s", err, stderr)
	}
	var child api.Thread
	if err := json.Unmarshal([]byte(childOut), &child); err != nil {
		t.Fatalf("decode child thread json: %v\nraw: %s", err, childOut)
	}
	if child.Parent != parent.ID {
		t.Fatalf("child parent = %q, want %q", child.Parent, parent.ID)
	}
	pane := sb.waitThreadReady(t, child.ID, "pi")

	m := tui.New(sb.Home+"/daemon.sock", false)

	// Both threads must be present and the child idle: the parent row shows NO ↓.
	m, _ = renderUntilRow(t, m, "bossthread")
	m, _ = renderUntilRow(t, m, "kidthread")
	dn := tui.DescendantGlyph(true)
	if !waitUntil(20*time.Second, func() bool {
		mm, v := render(t, m)
		r, ok := rowByName(mm, "kidthread")
		return ok && r.Busy == api.BusyIdle && !strings.Contains(rowLine(v, "bossthread"), dn)
	}) {
		_, v := render(t, m)
		t.Fatalf("parent row showed ↓ while no descendant was running:\n%s", v)
	}

	// Start a REAL turn on the child → the parent's row must light the ↓ glyph,
	// confirmed against the independently-fetched child busy state.
	sb.sendKeys(t, pane, "Write a detailed 150-word explanation of how DNS works")
	if !waitUntil(30*time.Second, func() bool {
		mm, v := render(t, m)
		r, ok := rowByName(mm, "kidthread")
		return ok && r.Busy == api.BusyBusy && strings.Contains(rowLine(v, "bossthread"), dn)
	}) {
		_, v := render(t, m)
		t.Fatalf("parent row never showed ↓ while a real child turn ran:\n%s", v)
	}

	// The other direction: once the child's turn finishes (back to idle), the ↓
	// must CLEAR — a one-directional check is the exact codex-liveness bug class.
	if !waitUntil(60*time.Second, func() bool {
		mm, v := render(t, m)
		r, ok := rowByName(mm, "kidthread")
		return ok && r.Busy == api.BusyIdle && !strings.Contains(rowLine(v, "bossthread"), dn)
	}) {
		_, v := render(t, m)
		t.Fatalf("parent row's ↓ never cleared after the child went idle:\n%s", v)
	}
}
