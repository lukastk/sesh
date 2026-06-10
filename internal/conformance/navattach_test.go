package conformance

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	matrix.RegisterTest("tmux.nav-attach", matrix.AgentAgnostic, matrix.Local, testNavAttach)
}

// testNavAttach: `nav --attach` (the Enter-from-a-plain-shell path, outside tmux) makes
// the terminal BECOME the thread — it attaches a client to the target session. Driven in
// a pane with $TMUX unset (a stand-in for a plain shell) and asserted on the real work
// socket (a client lands on the target).
func testNavAttach(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	target := sb.createSession(t, "natt")

	dir := t.TempDir()
	sock := filepath.Join(dir, "o.sock")
	octl := func(args ...string) {
		c := exec.Command("tmux", append([]string{"-S", sock}, args...)...)
		c.Env = sandboxEnv(nil)
		c.Run() //nolint:errcheck
	}
	octl("-f", "/dev/null", "new-session", "-d", "-s", "o", "-x", "120", "-y", "40")
	t.Cleanup(func() { octl("kill-server") })

	env := fmt.Sprintf("SESH_HOME=%s SESH_MACHINE=%s SESH_TMUX_SOCKET=%s SESH_MASTER_SOCKET=%s",
		sb.Home, sb.Machine, sb.TmuxSocket, sb.MasterSocket)
	octl("send-keys", "-t", "o:0", "-l",
		fmt.Sprintf("env -u TMUX %s %s tmux nav --to %s:%s --attach", env, seshBin(t), sb.Machine, target))
	octl("send-keys", "-t", "o:0", "Enter")

	if !waitUntil(10*time.Second, func() bool { return navClientSession(t, sb) == target }) {
		t.Errorf("nav --attach did not attach a client to %q (got %q)", target, navClientSession(t, sb))
	}
}
