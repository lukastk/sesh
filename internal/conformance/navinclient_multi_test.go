package conformance

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	matrix.RegisterTest("tmux.nav-in-client-multi", matrix.AgentAgnostic, matrix.Local, testNavInClientMulti)
}

// testNavInClientMulti: when MULTIPLE clients are attached to the same work-socket
// session (the real case: a master window AND a direct attach), `nav --in-client` must
// switch the client whose keystroke triggered it — the user who pressed Enter — not just
// any attached client. Two REAL clients view one session; the nav is sent FROM client B,
// and we assert that B (captured by name) is the one that moved, and the other did not.
// (The old `list-clients | head -1` resolution moved the wrong client here.)
func testNavInClientMulti(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	target := sb.createSession(t, "mctgt")
	if _, err := sb.rawTmux(t, "new-session", "-d", "-s", "mcscr", "-c", "/tmp"); err != nil {
		t.Fatalf("scratch session: %v", err)
	}

	dir := t.TempDir()
	octl := func(sock string, args ...string) {
		c := exec.Command("tmux", append([]string{"-S", sock}, args...)...)
		c.Env = sandboxEnv(nil) // no inherited SESH_* into the outer client
		c.Run()                 //nolint:errcheck
	}
	startClient := func(sock string) {
		octl(sock, "-f", "/dev/null", "new-session", "-d", "-s", "o", "-x", "120", "-y", "40")
		octl(sock, "send-keys", "-t", "o:0", "-l", "env -u TMUX tmux -L "+sb.TmuxSocket+" attach -t mcscr")
		octl(sock, "send-keys", "-t", "o:0", "Enter")
	}
	aSock, bSock := filepath.Join(dir, "a.sock"), filepath.Join(dir, "b.sock")
	startClient(aSock)
	startClient(bSock)
	t.Cleanup(func() { octl(aSock, "kill-server"); octl(bSock, "kill-server") })

	if !waitUntil(10*time.Second, func() bool { return workClientTotal(t, sb) == 2 }) {
		t.Fatalf("expected 2 work-socket clients, got %v", workClientSessions(t, sb))
	}

	// Capture B's client name (display-message resolves to the client that SENT it).
	bFile := filepath.Join(dir, "bclient")
	octl(bSock, "send-keys", "-t", "o:0", "-l",
		"tmux -L "+sb.TmuxSocket+" display-message -p '#{client_name}' > "+bFile)
	octl(bSock, "send-keys", "-t", "o:0", "Enter")
	var bClient string
	if !waitUntil(8*time.Second, func() bool {
		b, _ := os.ReadFile(bFile)
		bClient = strings.TrimSpace(string(b))
		return bClient != ""
	}) {
		t.Fatalf("could not capture client B's name")
	}

	// nav --in-client to target, SENT FROM client B.
	env := fmt.Sprintf("SESH_HOME=%s SESH_MACHINE=%s SESH_TMUX_SOCKET=%s SESH_MASTER_SOCKET=%s",
		sb.Home, sb.Machine, sb.TmuxSocket, sb.MasterSocket)
	octl(bSock, "send-keys", "-t", "o:0", "-l",
		fmt.Sprintf("%s %s tmux nav --to %s:%s --in-client", env, seshBin(t), sb.Machine, target))
	octl(bSock, "send-keys", "-t", "o:0", "Enter")

	// B (the Enter-presser) is now on target; the OTHER client stayed on mcscr.
	if !waitUntil(10*time.Second, func() bool {
		cs := clientSessionsByName(t, sb)
		other := ""
		for name, sess := range cs {
			if name != bClient {
				other = sess
			}
		}
		return cs[bClient] == target && other == "mcscr"
	}) {
		t.Errorf("nav --in-client(multi): want B(%s)->%q + other->mcscr, got %v", bClient, target, clientSessionsByName(t, sb))
	}
}

func workClientTotal(t *testing.T, sb *Sandbox) int {
	n := 0
	for _, c := range workClientSessions(t, sb) {
		n += c
	}
	return n
}

func workClientSessions(t *testing.T, sb *Sandbox) map[string]int {
	out, _ := sb.rawTmux(t, "list-clients", "-F", "#{client_session}")
	m := map[string]int{}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			m[l]++
		}
	}
	return m
}

func clientSessionsByName(t *testing.T, sb *Sandbox) map[string]string {
	out, _ := sb.rawTmux(t, "list-clients", "-F", "#{client_name}\t#{client_session}")
	m := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if f := strings.SplitN(strings.TrimSpace(l), "\t", 2); len(f) == 2 {
			m[f[0]] = f[1]
		}
	}
	return m
}
