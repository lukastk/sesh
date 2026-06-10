package conformance

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/matrix"
	"github.com/lukastk/sesh/internal/tmux"
)

func init() {
	matrix.RegisterTest("tmux.nav-master-multi", matrix.AgentAgnostic, matrix.Remote, testNavMasterMulti)
}

// testNavMasterMulti: a machine's work server commonly has SEVERAL clients — this
// master's window supervisor, other masters' supervisors, and the user's direct
// attaches. The master-path nav must move what THIS master's window shows and nothing
// else. The supervisor's attach records its client identity ("tty pid") in a marker
// file (tmux.MasterClientMarker); nav's inner switch targets exactly that client.
//
// Real setup: `master up` with an ssh-localhost peer (real hop, real supervisor that
// writes the real marker), plus a REAL direct-attach client on the peer's work server.
// Assert both directions: the marker client lands on the target session AND the direct
// client did not move. (The old `list-clients | head -1` resolution moved whichever
// client sorted first — the user's direct attach as often as the master's window.)
func testNavMasterMulti(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	self, peer := setupMasterPair(t)
	// A second thread on the peer = the nav target (the direct client parks on the first).
	tgt := peer.newThread(t, "pi", "navtgt", "/tmp")
	peer.waitThreadReady(t, tgt.ID, "pi")

	if _, stderr, err := self.Runner.Run(t, "master", "up", "--machines", self.Machine+","+peer.Machine); err != nil {
		t.Fatalf("master up: %v\n%s", err, stderr)
	}
	t.Cleanup(func() { self.Runner.Run(t, "master", "down") }) //nolint:errcheck

	// The peer window's supervisor attached (over real ssh) and recorded its marker.
	marker := tmux.MasterClientMarker(peer.Home, self.Machine)
	var markerClient string
	if !waitUntil(20*time.Second, func() bool {
		b, err := os.ReadFile(marker)
		if err != nil {
			return false
		}
		f := strings.Fields(string(b))
		if len(f) != 2 {
			return false
		}
		markerClient = f[0]
		// live = the recorded (name, pid) pair is a current client of the work server
		out, _ := exec.Command("tmux", "-L", peer.TmuxSocket, "list-clients", "-F", "#{client_name} #{client_pid}").Output()
		return strings.Contains(string(out), f[0]+" "+f[1])
	}) {
		t.Fatalf("the peer window's attach never recorded a live marker at %s", marker)
	}

	// A REAL direct-attach client on the peer's work server, parked on the FIRST
	// thread's session (sesh_peerw) — the client nav must NOT move.
	dir := t.TempDir()
	outerSock := filepath.Join(dir, "outer.sock")
	octl := func(args ...string) {
		c := exec.Command("tmux", append([]string{"-S", outerSock}, args...)...)
		c.Env = sandboxEnv(nil)
		c.Run() //nolint:errcheck
	}
	octl("-f", "/dev/null", "new-session", "-d", "-s", "o", "-x", "120", "-y", "40")
	octl("send-keys", "-t", "o:0", "-l", "env -u TMUX tmux -L "+peer.TmuxSocket+" attach -t sesh_peerw")
	octl("send-keys", "-t", "o:0", "Enter")
	t.Cleanup(func() { octl("kill-server") })

	var directClient string
	if !waitUntil(10*time.Second, func() bool {
		for name := range clientSessionsByNameOn(t, peer.TmuxSocket) {
			if name != markerClient {
				directClient = name
				return true
			}
		}
		return false
	}) {
		t.Fatalf("direct client never attached; clients = %v", clientSessionsByNameOn(t, peer.TmuxSocket))
	}

	// nav (master path) from self to the peer's target thread.
	if _, stderr, err := self.Runner.Run(t, "tmux", "nav", "--to", peer.Machine+":"+tgt.SessionName); err != nil {
		t.Fatalf("nav: %v\n%s", err, stderr)
	}

	// The MASTER WINDOW's client (the marker one) is on the target; the direct
	// attach did not move.
	if !waitUntil(10*time.Second, func() bool {
		cs := clientSessionsByNameOn(t, peer.TmuxSocket)
		return cs[markerClient] == tgt.SessionName && cs[directClient] == "sesh_peerw"
	}) {
		t.Errorf("master-path nav(multi): want marker client %s -> %q and direct client %s staying on sesh_peerw, got %v",
			markerClient, tgt.SessionName, directClient, clientSessionsByNameOn(t, peer.TmuxSocket))
	}
}

// clientSessionsByNameOn is clientSessionsByName for an arbitrary work socket.
func clientSessionsByNameOn(t *testing.T, socket string) map[string]string {
	out, _ := exec.Command("tmux", "-L", socket, "list-clients", "-F", "#{client_name}\t#{client_session}").Output()
	m := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if f := strings.SplitN(strings.TrimSpace(l), "\t", 2); len(f) == 2 {
			m[f[0]] = f[1]
		}
	}
	return m
}
