package conformance

import (
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	matrix.RegisterTest("tmux.nav-master-http", matrix.AgentAgnostic, matrix.Remote, testNavMasterHTTP)
}

// testNavMasterHTTP is the HTTP twin of the master-path nav: it proves the INNER
// switch-client is carried by the peer's TRANSPORT, not a hardcoded ssh hop. The
// peer is registered as an http peer (api_addr set) with a DELIBERATELY BROKEN ssh
// dest (http-only.invalid) — so if nav's inner switch landed, only HTTP could have
// carried it (a silent ssh attempt would fail to connect). This is the parity proof
// matching the other .http cells.
//
// The outer half (master select-window) is local to `self`; `self`'s own master
// window attaches into its LOCAL work server (no ssh). The peer's master window
// merely needs to EXIST for the outer select — its supervisor can't attach over the
// broken ssh, which is fine. The inner switch then goes self→peer over the API.
func testNavMasterHTTP(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	// self: ordinary daemon. peer: daemon with its TCP API exposed.
	self := newSandbox(t, matrix.Local)
	self.startDaemon(t)
	addr, token := freePort(t), "navhttp-token"
	peer := newSandbox(t, matrix.Local, withAPI(addr, token))
	peer.startDaemon(t)
	bin := seshBin(t)

	// Register the peer as HTTP transport with a BROKEN ssh dest: only HTTP can
	// carry the inner switch.
	if _, stderr, err := self.Runner.Run(t, "peer", "add", "--machine", peer.Machine,
		"--ssh", "http-only.invalid", "--home", peer.Home, "--binary", bin,
		"--tmux-socket", peer.TmuxSocket, "--api-addr", addr, "--api-token", token); err != nil {
		t.Fatalf("peer add (http): %v\n%s", err, stderr)
	}

	// Two real sessions on the peer's work server: a parking session and the nav target.
	self.newThread(t, "pi", "selfw", "/tmp")
	park := peer.newThread(t, "pi", "parkw", "/tmp")
	tgt := peer.newThread(t, "pi", "navtgt", "/tmp")
	if !waitUntil(agentStartTimeout, func() bool { return tmuxSessionCount(peer.TmuxSocket) >= 2 }) {
		t.Fatalf("peer work server never got both sessions")
	}

	// The master window for the peer must exist for the outer select (its supervisor
	// can't attach over the broken ssh — that's fine, the window still exists).
	if _, stderr, err := self.Runner.Run(t, "master", "up", "--machines", self.Machine+","+peer.Machine); err != nil {
		t.Fatalf("master up: %v\n%s", err, stderr)
	}
	t.Cleanup(func() { self.Runner.Run(t, "master", "down") }) //nolint:errcheck

	// A single REAL client on the peer's work server, parked on parkw. With exactly
	// one client and no master marker (the supervisor never attached), the inner
	// switch moves THIS client — the deterministic single-client branch.
	dir := t.TempDir()
	outerSock := filepath.Join(dir, "outer.sock")
	octl := func(args ...string) {
		c := exec.Command("tmux", append([]string{"-S", outerSock}, args...)...)
		c.Env = sandboxEnv(nil)
		c.Run() //nolint:errcheck
	}
	octl("-f", "/dev/null", "new-session", "-d", "-s", "o", "-x", "120", "-y", "40")
	octl("send-keys", "-t", "o:0", "-l", "env -u TMUX tmux -L "+peer.TmuxSocket+" attach -t "+park.SessionName)
	octl("send-keys", "-t", "o:0", "Enter")
	t.Cleanup(func() { octl("kill-server") })
	if !waitUntil(10*time.Second, func() bool { return innerClientSession(t, peer.TmuxSocket) == park.SessionName }) {
		t.Fatalf("direct client never parked on %q; got %q", park.SessionName, innerClientSession(t, peer.TmuxSocket))
	}

	// nav self -> peer:navtgt. The inner switch is carried over HTTP (broken ssh).
	if _, stderr, err := self.Runner.Run(t, "tmux", "nav", "--to", peer.Machine+":"+tgt.SessionName); err != nil {
		t.Fatalf("nav (http inner switch): %v\n%s", err, stderr)
	}

	// The client moved to the target — which, with ssh broken, PROVES HTTP carried it.
	if !waitUntil(10*time.Second, func() bool { return innerClientSession(t, peer.TmuxSocket) == tgt.SessionName }) {
		t.Errorf("http nav: peer client on %q, want %q (a silent ssh attempt would have failed)",
			innerClientSession(t, peer.TmuxSocket), tgt.SessionName)
	}
}
