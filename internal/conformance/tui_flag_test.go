package conformance

import (
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
	"github.com/lukastk/sesh/internal/tui"
)

func init() {
	registerTUIClaim("action-flag", claimActionFlag)
	registerTUIClaim("view-flag-pierce", claimViewFlagPierce)
}

// claimActionFlag: f toggles the selected thread's flag ON THE DAEMON (record
// truth, not just the render) and the ⚑ glyph appears; F again clears it; ^f
// disables auto-flagging (⌁); F on the disabled thread RE-ENABLES + flags —
// the one-rule semantic, proven against the real store.
func claimActionFlag(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, "pi", "flagme")

	bin := seshBin(t)
	env := []string{"SESH_HOME=" + sb.Home, "SESH_MACHINE=" + sb.Machine}
	m := tui.New(sb.Home+"/daemon.sock", false).WithExec(bin, env).WithLocal(sb.Machine, sb.TmuxSocket)
	m, _ = renderUntilRow(t, m, "flagme")

	flagState := func() (bool, bool) {
		t.Helper()
		s := sb.threadSnapshot(t, th.ID)
		return s.Flagged, s.FlagDisabled
	}

	// f: flag on the daemon + ⚑ renders after the reconcile fetch.
	m = runKey(t, m, "f")
	if !waitUntil(10*time.Second, func() bool { f, _ := flagState(); return f }) {
		t.Fatalf("f never flagged the thread on the daemon")
	}
	if !waitUntil(10*time.Second, func() bool {
		var v string
		m, v = render(t, m)
		return strings.Contains(v, "⚑")
	}) {
		_, v := render(t, m)
		t.Fatalf("⚑ never rendered:\n%s", v)
	}

	// f again: clears (flags never auto-clear — this IS the clear).
	m = runKey(t, m, "f")
	if !waitUntil(10*time.Second, func() bool { f, _ := flagState(); return !f }) {
		t.Fatalf("f never cleared the flag")
	}

	// ^f: disable auto-flagging → ⌁ renders; then f: re-enables AND flags.
	m = runKey(t, m, "ctrl+f")
	if !waitUntil(10*time.Second, func() bool { _, d := flagState(); return d }) {
		t.Fatalf("^f never disabled flagging on the daemon")
	}
	if !waitUntil(10*time.Second, func() bool {
		var v string
		m, v = render(t, m)
		return strings.Contains(v, "⌁")
	}) {
		_, v := render(t, m)
		t.Fatalf("⌁ never rendered:\n%s", v)
	}
	m = runKey(t, m, "f")
	if !waitUntil(10*time.Second, func() bool {
		f, d := flagState()
		return f && !d
	}) {
		f, d := flagState()
		t.Fatalf("f on a disabled thread must re-enable AND flag (flagged=%v disabled=%v)", f, d)
	}
	_ = m
}

// claimViewFlagPierce: a FLAGGED child stays visible under a COLLAPSED parent
// in the live render (fold-piercing — a flag never hides inside a fold),
// while an unflagged sibling stays hidden; unflagging re-hides it.
func claimViewFlagPierce(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	parent := sb.newHeadlessThread(t, "pi", "trunk")
	hot := sb.newHeadlessThread(t, "pi", "hotchild")
	cold := sb.newHeadlessThread(t, "pi", "coldchild")
	for _, child := range []api.Thread{hot, cold} {
		if _, stderr, err := sb.Runner.Run(t, "thread", "reparent", "--id", child.ID, "--parent", parent.ID); err != nil {
			t.Fatalf("reparent %s: %v\n%s", child.Name, err, stderr)
		}
	}
	if _, stderr, err := sb.Runner.Run(t, "thread", "flag", "--on", "--id", hot.ID); err != nil {
		t.Fatalf("flag --on: %v\n%s", err, stderr)
	}

	m := tui.New(sb.Home+"/daemon.sock", false).WithLocal(sb.Machine, sb.TmuxSocket)
	// Collapsed by default: the flagged child pierces, the unflagged one hides.
	if !waitUntil(15*time.Second, func() bool {
		var v string
		m, v = render(t, m)
		return strings.Contains(v, "trunk") && strings.Contains(v, "hotchild") && !strings.Contains(v, "coldchild")
	}) {
		_, v := render(t, m)
		t.Fatalf("flagged child never pierced the collapsed fold:\n%s", v)
	}
	// The parent still shows the collapsed ▸ marker (more is hidden).
	if _, v := render(t, m); !strings.Contains(v, "▸") {
		t.Fatalf("pierced collapsed parent lost its ▸ marker:\n%s", v)
	}

	// Unflag → the pierced child re-hides under the fold.
	if _, stderr, err := sb.Runner.Run(t, "thread", "flag", "--off", "--id", hot.ID); err != nil {
		t.Fatalf("flag --off: %v\n%s", err, stderr)
	}
	if !waitUntil(15*time.Second, func() bool {
		var v string
		m, v = render(t, m)
		return strings.Contains(v, "trunk") && !strings.Contains(v, "hotchild")
	}) {
		_, v := render(t, m)
		t.Fatalf("unflagged child never re-hid under the fold:\n%s", v)
	}
}
