package conformance

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	matrix.RegisterTest("tmux.nav-in-client", matrix.AgentAgnostic, matrix.Local, testNavInClient)
}

// testNavInClient asserts `tmux nav --in-client`: inside a tmux client on the work
// socket, navigating to a LOCAL session switches THAT client in place (no master),
// and it errors loudly for a remote target or when not on the work socket.
func testNavInClient(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)

	// Two real sesh tmux sessions on the work socket.
	s1 := sb.createSession(t, "ica")
	s2 := sb.createSession(t, "icb")

	// Attach exactly ONE client to s1 (a detached holder whose pane attaches).
	hold := "navhold"
	if out, err := sb.rawTmux(t, "new-session", "-d", "-s", hold,
		"env -u TMUX tmux -L "+sb.TmuxSocket+" attach -t "+s1); err != nil {
		t.Fatalf("attach holder: %v\n%s", err, out)
	}
	if !waitUntil(8*time.Second, func() bool { return navClientSession(t, sb) == s1 }) {
		t.Fatalf("holder never attached to %s (client on %q)", s1, navClientSession(t, sb))
	}

	spOut, err := sb.rawTmux(t, "display-message", "-t", s1, "-p", "#{socket_path}")
	if err != nil {
		t.Fatalf("socket_path: %v\n%s", err, spOut)
	}
	tmuxVal := strings.TrimSpace(spOut) + ",1,1" // basename == work socket => passes the in-client guard

	// nav --in-client to s2: the SAME client switches in place.
	if _, stderr, err := runNav(t, sb, tmuxVal, "--in-client", "--to", sb.Machine+":"+s2); err != nil {
		t.Fatalf("nav --in-client: %v\n%s", err, stderr)
	}
	if !waitUntil(8*time.Second, func() bool { return navClientSession(t, sb) == s2 }) {
		t.Errorf("in-client nav did not switch the client to %s (still %q)", s2, navClientSession(t, sb))
	}

	// Loud errors (never a silent no-op):
	if _, _, err := runNav(t, sb, tmuxVal, "--in-client", "--to", "othermachine:"+s2); err == nil {
		t.Errorf("nav --in-client to a REMOTE target should error")
	}
	if _, _, err := runNav(t, sb, "", "--in-client", "--to", sb.Machine+":"+s2); err == nil {
		t.Errorf("nav --in-client when NOT on the work socket should error")
	}
}

// createSession creates a real sesh tmux session on the work server, returning its name.
func (sb *Sandbox) createSession(t *testing.T, name string) string {
	t.Helper()
	stdout, stderr, err := sb.Runner.Run(t, "tmux", "create-session", "--name", name)
	if err != nil {
		t.Fatalf("create-session %s: %v\n%s", name, err, stderr)
	}
	return strings.TrimSpace(stdout)
}

// navClientSession returns the session the (single) work-socket client is viewing.
func navClientSession(t *testing.T, sb *Sandbox) string {
	t.Helper()
	out, err := sb.rawTmux(t, "list-clients", "-F", "#{client_session}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.SplitN(strings.TrimSpace(out), "\n", 2)[0])
}

// runNav runs `sesh tmux nav <args>` with the sandbox config and an explicit $TMUX
// (empty => $TMUX unset), so the in-client guard sees exactly what we set.
func runNav(t *testing.T, sb *Sandbox, tmuxVal string, args ...string) (string, string, error) {
	t.Helper()
	bin := seshBin(t)
	env := sandboxEnv(map[string]string{
		"SESH_HOME": sb.Home, "SESH_MACHINE": sb.Machine,
		"SESH_TMUX_SOCKET": sb.TmuxSocket, "SESH_MASTER_SOCKET": sb.MasterSocket,
	})
	filtered := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, "TMUX=") {
			filtered = append(filtered, kv)
		}
	}
	if tmuxVal != "" {
		filtered = append(filtered, "TMUX="+tmuxVal)
	}
	cmd := exec.Command(bin, append([]string{"tmux", "nav"}, args...)...)
	cmd.Env = filtered
	return runCmd(cmd)
}
