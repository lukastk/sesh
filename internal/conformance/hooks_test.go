package conformance

// daemon.hooks cells (PARITY_ROADMAP B1): [[hooks]] really fire on REAL
// observed edges. Local: a real headless turn's busy edges fire a file-touch
// hook with the event env; agent filters scope; disable mutes (and enable
// restores); test runs synchronously. Remote: v1's OBSERVER-BOUND design — the
// LOCAL daemon's hook fires for a PEER's thread edge, observed purely through
// the mesh cache (no hook anywhere on the peer).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	matrix.RegisterTest("daemon.hooks", matrix.AgentAgnostic, matrix.Local, testHooksLocal)
	matrix.RegisterTest("daemon.hooks", matrix.AgentAgnostic, matrix.Remote, testHooksObserverBound)
}

// hookConfig writes a [[hooks]] config that appends the event env to outFile.
func hookConfig(t *testing.T, home, outFile string) {
	t.Helper()
	conf := `
[[hooks]]
name    = "on-busy-edge"
event   = "busy_changed"
command = 'echo "$SESH_EVENT $SESH_EVENT_FROM>$SESH_EVENT_TO $SESH_THREAD_ID $SESH_THREAD_NAME" >> ` + outFile + `'

[[hooks]]
name    = "codex-only"
event   = "busy_changed"
agent   = "codex"
command = 'echo "codex $SESH_THREAD_ID" >> ` + outFile + `'
`
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
}

func testHooksLocal(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	outFile := filepath.Join(t.TempDir(), "hook.out")
	hookConfig(t, sb.Home, outFile)
	sb.startDaemon(t)

	th := sb.newHeadlessThread(t, "pi", "hookme")
	// Let the eventer OBSERVE the thread idle first — an idle->busy edge can
	// only exist if the previous state was seen (a turn started before the
	// first observation is just thread_created).
	if !waitUntil(15*time.Second, func() bool {
		st := sb.threadStatus(t, th.ID)
		return st.Head == "headless" && st.Busy == "idle"
	}) {
		t.Fatalf("thread never settled idle")
	}
	time.Sleep(3 * time.Second) // ≥ one eventer tick past the maintainer publish
	// A REAL turn: busy_changed idle->busy then busy->idle.
	sb.headlessTurn(t, th.ID, "Reply with exactly: ok")

	if !waitUntil(30*time.Second, func() bool {
		b, _ := os.ReadFile(outFile)
		return strings.Contains(string(b), "busy>idle "+th.ID+" hookme")
	}) {
		b, _ := os.ReadFile(outFile)
		t.Fatalf("hook never fired on the busy->idle edge; file:\n%s", b)
	}
	b, _ := os.ReadFile(outFile)
	if !strings.Contains(string(b), "idle>busy "+th.ID) {
		t.Errorf("the idle->busy edge was not observed:\n%s", b)
	}
	// The agent filter scoped: the codex-only hook must NOT have fired for pi.
	if strings.Contains(string(b), "codex "+th.ID) {
		t.Errorf("agent-filtered hook fired for the wrong agent:\n%s", b)
	}

	// disable mutes (persisted server-side), enable restores.
	if _, stderr, err := sb.Runner.Run(t, "hooks", "disable", "--name", "on-busy-edge"); err != nil {
		t.Fatalf("hooks disable: %v\n%s", err, stderr)
	}
	os.Remove(outFile)
	sb.headlessTurn(t, th.ID, "Reply with exactly: ok again")
	time.Sleep(3 * time.Second) // give a wrongly-unmuted hook time to fire
	if b, _ := os.ReadFile(outFile); strings.Contains(string(b), th.ID) {
		t.Errorf("disabled hook still fired:\n%s", b)
	}
	if _, stderr, err := sb.Runner.Run(t, "hooks", "enable", "--name", "on-busy-edge"); err != nil {
		t.Fatalf("hooks enable: %v\n%s", err, stderr)
	}

	// list shows both hooks + state; test runs synchronously.
	out, _, err := sb.Runner.Run(t, "hooks", "list")
	if err != nil || !strings.Contains(out, "on-busy-edge") || !strings.Contains(out, "codex-only") {
		t.Errorf("hooks list wrong: %v\n%s", err, out)
	}
	if _, stderr, err := sb.Runner.Run(t, "hooks", "test", "--name", "on-busy-edge", "--thread", th.ID); err != nil {
		t.Fatalf("hooks test: %v\n%s", err, stderr)
	}
	if b, _ := os.ReadFile(outFile); !strings.Contains(string(b), th.ID) {
		t.Errorf("hooks test did not run the real command:\n%s", string(b))
	}

	// A BROKEN hook (unknown event) refuses the daemon.
	bad := newSandbox(t, matrix.Local)
	if err := os.WriteFile(filepath.Join(bad.Home, "config.toml"),
		[]byte("[[hooks]]\nname='x'\nevent='nope'\ncommand='true'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, stderr, err := bad.daemonRunner.Run(t, "daemon", "start"); err == nil {
		t.Errorf("daemon started despite an unknown hook event")
	} else if !strings.Contains(stderr, "nope") {
		t.Errorf("broken-hook error does not name the event: %s", stderr)
	}
}

// testHooksObserverBound: the killer property — a hook on the LOCAL daemon
// fires for a PEER thread's busy edge, which the local daemon only knows
// about through its mesh cache. No hook exists on the peer.
func testHooksObserverBound(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	ensureSSHLocalhost(t)
	local := newSandbox(t, matrix.Local)
	outFile := filepath.Join(t.TempDir(), "hook.out")
	hookConfig(t, local.Home, outFile)
	local.startDaemon(t)

	peer := newSandbox(t, matrix.Local)
	peer.startDaemon(t)
	bin := seshBin(t)
	if _, stderr, err := local.Runner.Run(t, "peer", "add", "--machine", peer.Machine, "--ssh", "localhost",
		"--home", peer.Home, "--binary", bin, "--tmux-socket", peer.TmuxSocket); err != nil {
		t.Fatalf("peer add: %v\n%s", err, stderr)
	}

	th := peer.newHeadlessThread(t, "pi", "farhook")
	peer.headlessTurn(t, th.ID, "Reply with exactly: ok")

	if !waitUntil(45*time.Second, func() bool {
		b, _ := os.ReadFile(outFile)
		return strings.Contains(string(b), "busy>idle "+th.ID+" farhook")
	}) {
		b, _ := os.ReadFile(outFile)
		t.Fatalf("the LOCAL hook never fired for the PEER thread's edge (observer-bound design); file:\n%s", b)
	}
}
