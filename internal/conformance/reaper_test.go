package conformance

import (
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/matrix"
)

// TestUITermViewerReaper is an honest end-to-end test of the live-terminal viewer
// reaper (internal/daemon/terminal.go). It seeds the work server with leaked
// `uiterm-*` sessions BEFORE the daemon starts — exactly the orphan state a daemon
// crash/restart/-9 leaves behind (the defer cleanup can't run when the process dies) —
// then asserts the daemon's startup sweep kills every viewer while leaving real
// sessions untouched, and that `sesh tmux info` hides viewer sessions from consumers
// (session pickers, kill-empty-sessions). Outside the matrix (a hygiene loop, not a
// (loc)×(agent) cell).
func TestUITermViewerReaper(t *testing.T) {
	sb := newSandbox(t, matrix.Local)

	// Leak two viewer sessions + one real session before the daemon comes up.
	for _, name := range []string{"uiterm-deadbeef-111", "uiterm-deadbeef-222", "keepme"} {
		if out, err := sb.rawTmux(t, "new-session", "-d", "-s", name); err != nil {
			t.Fatalf("seed session %s: %v: %s", name, err, out)
		}
	}

	sb.startDaemon(t)

	// Startup sweep: every orphaned uiterm-* must be killed.
	if !waitUntil(10*time.Second, func() bool {
		out, err := sb.rawTmux(t, "list-sessions", "-F", "#{session_name}")
		return err == nil && !strings.Contains(out, "uiterm-")
	}) {
		out, _ := sb.rawTmux(t, "list-sessions", "-F", "#{session_name}")
		t.Fatalf("reaper did not sweep orphaned uiterm-* sessions; sessions=%q", out)
	}
	// A non-viewer session must survive the sweep.
	if out, err := sb.rawTmux(t, "list-sessions", "-F", "#{session_name}"); err != nil || !strings.Contains(out, "keepme") {
		t.Fatalf("reaper killed a non-viewer session (or list failed: %v); sessions=%q", err, out)
	}

	// `sesh tmux info` must HIDE viewer sessions even when one exists live (the picker /
	// kill-empty-sessions filter). Create one after startup so the 60s reaper tick hasn't
	// run yet; raw tmux still sees it, but `tmux info` must not list it.
	if out, err := sb.rawTmux(t, "new-session", "-d", "-s", "uiterm-cafef00d-999"); err != nil {
		t.Fatalf("seed live viewer: %v: %s", err, out)
	}
	t.Cleanup(func() { sb.rawTmux(t, "kill-session", "-t", "=uiterm-cafef00d-999") }) //nolint:errcheck
	raw, err := sb.rawTmux(t, "list-sessions", "-F", "#{session_name}")
	if err != nil || !strings.Contains(raw, "uiterm-cafef00d-999") {
		t.Fatalf("raw tmux should still see the live viewer (err=%v); sessions=%q", err, raw)
	}
	info, _, err := sb.Runner.Run(t, "tmux", "info")
	if err != nil {
		t.Fatalf("sesh tmux info: %v", err)
	}
	if strings.Contains(info, "uiterm-") {
		t.Errorf("sesh tmux info must not list uiterm-* viewer sessions; got:\n%s", info)
	}
	if !strings.Contains(info, "keepme") {
		t.Errorf("sesh tmux info should still list real sessions; got:\n%s", info)
	}
}
