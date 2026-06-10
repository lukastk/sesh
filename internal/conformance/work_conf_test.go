package conformance

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	matrix.RegisterTest("tmux.work-conf", matrix.AgentAgnostic, matrix.Local, testWorkConf)
}

// testWorkConf: with SESH_TMUX_CONF set, the WORK tmux server the daemon spawns
// sessions on is started with `tmux -f <conf>`, so sesh's tmux can carry its own UI
// (e.g. the per-thread status bar) separate from the user's default ~/.tmux.conf.
// Proven honestly: a sentinel user-option that ONLY this conf sets is live on the work
// socket — read with raw tmux, not sesh's own state.
func testWorkConf(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	// A work conf whose sole job is to set a sentinel the default config never would.
	conf := filepath.Join(t.TempDir(), "work.conf")
	const sentinel = "loaded-by-sesh-work-conf"
	if err := os.WriteFile(conf, []byte("set -g @sesh-work-conf \""+sentinel+"\"\n"), 0o644); err != nil {
		t.Fatalf("write work conf: %v", err)
	}

	sb := newSandbox(t, matrix.Local, withTmuxConf(conf))
	sb.startDaemon(t)
	// Creating a thread starts the work server — which must source the conf.
	sb.newThread(t, "pi", "wc", "/tmp")

	got := ""
	ok := waitUntil(10*time.Second, func() bool {
		out, _ := exec.Command("tmux", "-L", sb.TmuxSocket, "show", "-gv", "@sesh-work-conf").Output()
		got = strings.TrimSpace(string(out))
		return got == sentinel
	})
	if !ok {
		t.Errorf("work server did not source SESH_TMUX_CONF: @sesh-work-conf=%q, want %q", got, sentinel)
	}
}
