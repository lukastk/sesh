package conformance

import (
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/matrix"
	"github.com/lukastk/sesh/internal/tmux"
)

func init() {
	// Master-tmux infrastructure. Remote: the proof requires a real ssh hop (the peer
	// window ssh-attaches into the peer's work server). ssh-localhost stands in.
	matrix.RegisterTest("master.up", matrix.AgentAgnostic, matrix.Remote, testMasterUp)
	matrix.RegisterTest("master.reconnect", matrix.AgentAgnostic, matrix.Remote, testMasterReconnect)
	matrix.RegisterTest("master.holding", matrix.AgentAgnostic, matrix.Local, testMasterHolding)
	matrix.RegisterTest("master.remote-work-context", matrix.AgentAgnostic, matrix.Remote, testMasterRemoteWorkContext)
}

// testMasterHolding: a master window for a machine with NO live threads falls back to
// a holding "scratch" shell session in the work server (so the window attaches and
// stays a work-server client, instead of looping on "no sessions").
func testMasterHolding(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	self := newSandbox(t, matrix.Local)
	self.startDaemon(t)
	// No threads => the work server has no sessions.
	if _, stderr, err := self.Runner.Run(t, "master", "up", "--machines", self.Machine); err != nil {
		t.Fatalf("master up: %v\n%s", err, stderr)
	}
	t.Cleanup(func() { self.Runner.Run(t, "master", "down") }) //nolint:errcheck
	// The window attaches anyway (to the holding session) — a real client appears...
	if !waitUntil(15*time.Second, func() bool { return tmuxClientCount(self.TmuxSocket) >= 1 }) {
		t.Errorf("master window never attached to an EMPTY work server (no holding fallback)")
	}
	// ...and the holding 'scratch' session was created.
	if !strSliceHas(masterSessionNamesOn(self.TmuxSocket), "scratch") {
		t.Errorf("no holding 'scratch' session on the empty work server")
	}
}

func masterSessionNamesOn(socket string) []string {
	out, err := exec.Command("tmux", "-L", socket, "list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		return nil
	}
	var names []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			names = append(names, l)
		}
	}
	return names
}

// testMasterRemoteWorkContext covers the macOS Keychain failure shape: an always-on
// remote cockpit can reach a freshly logged-in Mac before its work server exists. The
// ssh attach must ask the peer's supervised daemon to create that server; starting tmux
// directly in ssh gives every later local pane ssh's audit session and makes Claude's
// login Keychain unreadable. A daemon-only sentinel in tmux's captured global environment
// is the external proof of which process context created the real server.
func testMasterRemoteWorkContext(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	ensureSSHLocalhost(t)
	self := newSandbox(t, matrix.Local)
	self.startDaemon(t)
	const sentinelKey = "SESH_TEST_DAEMON_CONTEXT"
	const sentinelValue = "peer-daemon"
	peer := newSandbox(t, matrix.Local, withSandboxEnv(sentinelKey, sentinelValue))
	peer.startDaemon(t)
	registerMasterPeer(t, self, peer, "localhost", "")

	// The peer daemon is live but has never touched its work server.
	if got := tmuxSessionCount(peer.TmuxSocket); got != 0 {
		t.Fatalf("peer work server unexpectedly has %d sessions before master up", got)
	}
	if _, stderr, err := self.Runner.Run(t, "master", "up", "--machines", peer.Machine); err != nil {
		t.Fatalf("master up: %v\n%s", err, stderr)
	}
	t.Cleanup(func() { self.Runner.Run(t, "master", "down") }) //nolint:errcheck

	if !waitUntil(20*time.Second, func() bool { return tmuxClientCount(peer.TmuxSocket) >= 1 }) {
		t.Fatalf("peer window never attached into the empty peer work server")
	}
	if !strSliceHas(masterSessionNamesOn(peer.TmuxSocket), "scratch") {
		t.Fatalf("peer work server has no holding scratch session")
	}
	out, err := exec.Command("tmux", "-L", peer.TmuxSocket, "show-environment", "-g", sentinelKey).CombinedOutput()
	if err != nil {
		t.Fatalf("work server did not inherit the peer daemon's context sentinel: %v: %s", err, out)
	}
	if got, want := strings.TrimSpace(string(out)), sentinelKey+"="+sentinelValue; got != want {
		t.Fatalf("work server context = %q, want %q (ssh created the server instead of the peer daemon)", got, want)
	}
}

// setupMasterPair starts a self daemon + a peer daemon (ssh-localhost), registers the
// peer (with its work socket, for the master attach), and puts a real headed thread on
// each so each work server has a session to attach into. Returns (self, peer).
func setupMasterPair(t *testing.T, selfOpts ...sandboxOpt) (self, peer *Sandbox) {
	t.Helper()
	return setupMasterPairVia(t, "localhost", "", selfOpts...)
}

// setupMasterPairVia is setupMasterPair with an explicit ssh destination and port, so a
// test can put a controllable relay between the master window and the peer's sshd.
func setupMasterPairVia(t *testing.T, sshDest, port string, selfOpts ...sandboxOpt) (self, peer *Sandbox) {
	t.Helper()
	ensureSSHLocalhost(t)
	self = newSandbox(t, matrix.Local, selfOpts...)
	self.startDaemon(t)
	peer = newSandbox(t, matrix.Local)
	peer.startDaemon(t)
	registerMasterPeer(t, self, peer, sshDest, port)
	// Headed threads => a real session exists on each work server (so attach succeeds).
	self.newThread(t, "pi", "selfw", "/tmp")
	peer.newThread(t, "pi", "peerw", "/tmp")
	if !waitUntil(agentStartTimeout, func() bool { return tmuxSessionCount(self.TmuxSocket) >= 1 }) {
		t.Fatalf("self work server never got a session")
	}
	if !waitUntil(agentStartTimeout, func() bool { return tmuxSessionCount(peer.TmuxSocket) >= 1 }) {
		t.Fatalf("peer work server never got a session")
	}
	return self, peer
}

func registerMasterPeer(t *testing.T, self, peer *Sandbox, sshDest, port string) {
	t.Helper()
	bin := seshBin(t)
	add := []string{"peer", "add", "--machine", peer.Machine, "--ssh", sshDest, "--home", peer.Home, "--binary", bin, "--tmux-socket", peer.TmuxSocket}
	if port != "" {
		add = append(add, "--port", port)
	}
	if _, stderr, err := self.Runner.Run(t, add...); err != nil {
		t.Fatalf("peer add: %v\n%s", err, stderr)
	}
}

// testMasterUp asserts `sesh master up` builds the cockpit: one window per machine,
// named after the machine, each GENUINELY attached into THAT machine's work server
// (a real tmux client appears — for the peer, created over a real ssh hop). Asserted
// via raw tmux, never internal state.
func testMasterUp(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	self, peer := setupMasterPair(t)
	if _, stderr, err := self.Runner.Run(t, "master", "up", "--machines", self.Machine+","+peer.Machine); err != nil {
		t.Fatalf("master up: %v\n%s", err, stderr)
	}
	t.Cleanup(func() { self.Runner.Run(t, "master", "down") }) //nolint:errcheck

	// One window per machine, named after the machine.
	wins := masterWindowNames(self.MasterSocket)
	if !strSliceHas(wins, self.Machine) || !strSliceHas(wins, peer.Machine) {
		t.Fatalf("master windows = %v, want both %q and %q", wins, self.Machine, peer.Machine)
	}

	// Each window's supervisor genuinely attaches into that machine's WORK server.
	if !waitUntil(20*time.Second, func() bool { return tmuxClientCount(self.TmuxSocket) >= 1 }) {
		t.Errorf("self window never attached into self's work server (%s)", self.TmuxSocket)
	}
	if !waitUntil(20*time.Second, func() bool { return tmuxClientCount(peer.TmuxSocket) >= 1 }) {
		t.Errorf("peer window never attached into the peer's work server over ssh (%s)", peer.TmuxSocket)
	}
}

// masterWedgeHealTimeout bounds the WEDGED-drop heal: ssh's keepalive gives up after
// peers.SSHKeepaliveArgs' interval × count (~45s), then the supervisor's backoff (≤5s)
// and a fresh attach. Generous, because the failure it guards against is unbounded.
const masterWedgeHealTimeout = 120 * time.Second

// testMasterReconnect asserts the per-window supervisor self-heals from BOTH shapes of
// drop, for the local window and the ssh-localhost peer window:
//
//   - CLEAN: the attach exits (detach-client). The drop is observed (client count really
//     hits 0) before the heal, so a no-op can't pass.
//   - WEDGED: the connection is blackholed and the attach does NOT exit. This is the
//     shape that wedged the macbook cockpit after every sleep, and the clean case cannot
//     stand in for it: the supervisor re-establishes only when the attach process exits,
//     so with no ssh keepalive it waits forever while the window paints a stale frame and
//     `tmux nav` keeps "succeeding" against a dead client. Asserted on the MARKER file,
//     because that is what nav actually resolves.
func testMasterReconnect(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	// Reach the peer THROUGH a relay we can blackhole. The clean phase below behaves
	// identically through it, so both shapes exercise one setup.
	relay := newBlackholeRelay(t, "127.0.0.1:22")
	self, peer := setupMasterPairVia(t, "127.0.0.1", relay.Port)
	if _, stderr, err := self.Runner.Run(t, "master", "up", "--machines", self.Machine+","+peer.Machine); err != nil {
		t.Fatalf("master up: %v\n%s", err, stderr)
	}
	t.Cleanup(func() { self.Runner.Run(t, "master", "down") }) //nolint:errcheck

	for _, m := range []struct {
		name   string
		socket string
	}{{"self", self.TmuxSocket}, {"peer(ssh)", peer.TmuxSocket}} {
		// initial attach
		if !waitUntil(20*time.Second, func() bool { return tmuxClientCount(m.socket) >= 1 }) {
			t.Fatalf("%s window never attached initially", m.name)
		}
		// DROP it — detach the supervisor's client.
		exec.Command("tmux", "-L", m.socket, "detach-client").Run() //nolint:errcheck
		// The drop really happened (count hits 0) — so the heal below isn't a no-op.
		if !waitUntil(10*time.Second, func() bool { return tmuxClientCount(m.socket) == 0 }) {
			t.Errorf("%s: detach did not drop the client", m.name)
		}
		// HEAL — the supervisor re-establishes the attach.
		if !waitUntil(20*time.Second, func() bool { return tmuxClientCount(m.socket) >= 1 }) {
			t.Errorf("%s window did not reconnect after the drop", m.name)
		}
	}

	// --- WEDGED drop (ssh path only: a local attach has no network to lose) ---
	marker := tmux.MasterClientMarker(peer.Home, self.Machine)
	before := strings.TrimSpace(readFileString(marker))
	if before == "" {
		t.Fatalf("peer window never recorded its client in %s", marker)
	}
	if !clientListed(peer.TmuxSocket, before) {
		t.Fatalf("marker %q does not name a live client on the peer work server before the freeze", before)
	}

	relay.Freeze()

	// The zombie client stays listed on the peer's work server — its sshd still holds the
	// pty, and only the far machine's ClientAliveInterval reaps that — so a client-COUNT
	// assertion would pass vacuously here. The honest observable is the marker naming a
	// DIFFERENT, live client: that is precisely what nav resolves, i.e. the difference
	// between the user's next thread selection landing and silently switching a dead one.
	healed := waitUntil(masterWedgeHealTimeout, func() bool {
		now := strings.TrimSpace(readFileString(marker))
		return now != "" && now != before && clientListed(peer.TmuxSocket, now)
	})
	if !healed {
		t.Errorf("peer window did not re-establish after a BLACKHOLED connection: marker %s still %q after %s "+
			"(the attach never exited, so the supervisor never re-ran — this is the post-sleep cockpit wedge)",
			marker, strings.TrimSpace(readFileString(marker)), masterWedgeHealTimeout)
	}
}

// readFileString reads a file, returning "" when it is missing (a marker the supervisor
// has not written yet is a legitimate transient state, not an error).
func readFileString(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

// clientListed reports whether "<client_name> <client_pid>" is a current client of the
// work server on socket — the SAME liveness contract nav's inner switch applies (see
// tmux.InnerSwitchScript), so this asserts what nav will actually conclude.
func clientListed(socket, nameAndPID string) bool {
	out, err := exec.Command("tmux", "-L", socket, "list-clients", "-F", "#{client_name} #{client_pid}").Output()
	if err != nil {
		return false
	}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(l) == nameAndPID {
			return true
		}
	}
	return false
}

// blackholeRelay is a loopback TCP relay whose established connections can be FROZEN:
// after Freeze() it stops forwarding bytes in both directions on every pair open at that
// moment and — the whole point — never closes them. No FIN, no RST, just silence. That is
// what a laptop's ssh connections become when the network dies under them during sleep,
// and it is the one drop shape `tmux detach-client` can never produce.
//
// Connections accepted AFTER Freeze relay normally: the network came back on wake, only
// the pre-sleep socket is dead. Without that, the supervisor could never reconnect and the
// test would prove nothing about the fix.
type blackholeRelay struct {
	Port string

	mu   sync.Mutex
	dead []chan struct{}
	// held keeps every frozen conn referenced: a garbage-collected net.Conn is finalized
	// CLOSED, which would send exactly the FIN this relay exists to withhold.
	held []net.Conn
}

func newBlackholeRelay(t *testing.T, upstream string) *blackholeRelay {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("blackhole relay: listen: %v", err)
	}
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("blackhole relay: port: %v", err)
	}
	r := &blackholeRelay{Port: port}
	go func() {
		for {
			down, err := ln.Accept()
			if err != nil {
				return
			}
			up, err := net.Dial("tcp", upstream)
			if err != nil {
				down.Close() //nolint:errcheck
				continue
			}
			r.pair(down, up)
		}
	}()
	t.Cleanup(func() {
		ln.Close() //nolint:errcheck
		r.mu.Lock()
		held := append([]net.Conn(nil), r.held...)
		r.mu.Unlock()
		for _, c := range held {
			c.Close() //nolint:errcheck
		}
		// ssh recorded a host key for [127.0.0.1]:<ephemeral port>; don't leave it in the
		// developer's known_hosts (a fresh port every run would accumulate forever).
		exec.Command("ssh-keygen", "-R", "[127.0.0.1]:"+port).Run() //nolint:errcheck
	})
	return r
}

// Freeze blackholes every connection currently open through the relay.
func (r *blackholeRelay) Freeze() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range r.dead {
		close(d)
	}
	r.dead = nil
}

func (r *blackholeRelay) pair(down, up net.Conn) {
	dead := make(chan struct{})
	r.mu.Lock()
	r.dead = append(r.dead, dead)
	r.held = append(r.held, down, up)
	r.mu.Unlock()
	go r.pump(down, up, dead)
	go r.pump(up, down, dead)
}

func (r *blackholeRelay) pump(src, dst net.Conn, dead <-chan struct{}) {
	buf := make([]byte, 32*1024)
	for {
		select {
		case <-dead:
			return // frozen: stop forwarding and deliberately close NOTHING
		default:
		}
		src.SetReadDeadline(time.Now().Add(200 * time.Millisecond)) //nolint:errcheck
		n, rerr := src.Read(buf)
		if n > 0 {
			select {
			case <-dead:
				return
			default:
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				closeBoth(src, dst)
				return
			}
		}
		if rerr != nil {
			if ne, ok := rerr.(net.Error); ok && ne.Timeout() {
				continue
			}
			// A REAL end-of-stream (the clean detach-client phase): propagate the close so
			// the other end tears down too. Only a freeze withholds it.
			closeBoth(src, dst)
			return
		}
	}
}

func closeBoth(a, b net.Conn) {
	a.Close() //nolint:errcheck
	b.Close() //nolint:errcheck
}

// --- raw tmux helpers (assert the observable effect, not sesh's own state) ---

func masterWindowNames(socket string) []string {
	out, err := exec.Command("tmux", "-L", socket, "list-windows", "-t", masterSessionName, "-F", "#{window_name}").Output()
	if err != nil {
		return nil
	}
	var names []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			names = append(names, l)
		}
	}
	return names
}

func tmuxClientCount(socket string) int  { return tmuxLineCount(socket, "list-clients") }
func tmuxSessionCount(socket string) int { return tmuxLineCount(socket, "list-sessions") }

func tmuxLineCount(socket, sub string) int {
	out, err := exec.Command("tmux", "-L", socket, sub).Output()
	if err != nil {
		return 0
	}
	n := 0
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n
}

func strSliceHas(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

const masterSessionName = "master"
