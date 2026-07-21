package conformance

// thread.notify cells (PARITY_ROADMAP B2): the per-thread notification gate —
// creation default from [defaults] notifications, toggle round-trip, and the
// REAL proof: a hook observing a real turn receives SESH_NOTIFY matching the
// thread's gate, both values. Remote = routed toggle on a peer.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	matrix.RegisterTest("thread.notify", matrix.AgentAgnostic, matrix.Local, testNotifyLocal)
	matrix.RegisterTest("thread.notify", matrix.AgentAgnostic, matrix.Remote, testNotifyRemote)
}

func testNotifyLocal(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	outFile := filepath.Join(t.TempDir(), "notify.out")
	conf := `
[defaults]
notifications = true

[[hooks]]
name    = "gate-probe"
event   = "busy_changed"
to      = "idle"
command = 'echo "gate=$SESH_NOTIFY att=$SESH_ATTACHMENT $SESH_THREAD_ID" >> ` + outFile + `'
`
	if err := os.WriteFile(filepath.Join(sb.Home, "config.toml"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	sb.startDaemon(t)

	// Default ON (from config; also the built-in default).
	th := sb.newHeadlessThread(t, "pi", "gated")
	if !threadByName(t, sb, "gated").Notify {
		t.Fatalf("new thread's notify gate not defaulted on")
	}

	// Observe idle, then a real turn: the hook sees gate=1.
	if !waitUntil(15*time.Second, func() bool {
		st := sb.threadStatus(t, th.ID)
		return st.Busy == "idle"
	}) {
		t.Fatalf("never settled idle")
	}
	time.Sleep(3 * time.Second)
	sb.headlessTurn(t, th.ID, "Reply with exactly: ok")
	if !waitUntil(30*time.Second, func() bool {
		b, _ := os.ReadFile(outFile)
		return strings.Contains(string(b), "gate=1 att=detached "+th.ID)
	}) {
		b, _ := os.ReadFile(outFile)
		t.Fatalf("hook never saw gate=1:\n%s", b)
	}

	// Toggle off (round-trips the wire), turn again: the hook sees gate=0 —
	// the hook still FIRES (mechanism); honoring the gate is the hook's call.
	if _, stderr, err := sb.Runner.Run(t, "thread", "notify", "--id", th.ID, "--off"); err != nil {
		t.Fatalf("notify --off: %v\n%s", err, stderr)
	}
	if threadByName(t, sb, "gated").Notify {
		t.Fatalf("notify --off did not persist")
	}
	os.Remove(outFile)
	time.Sleep(2 * time.Second) // let the snapshot pick up the new gate
	sb.headlessTurn(t, th.ID, "Reply with exactly: ok again")
	if !waitUntil(30*time.Second, func() bool {
		b, _ := os.ReadFile(outFile)
		return strings.Contains(string(b), "gate=0 att=detached "+th.ID)
	}) {
		b, _ := os.ReadFile(outFile)
		t.Fatalf("hook never saw gate=0 after the toggle:\n%s", b)
	}

	// A config default of OFF applies at creation.
	sb2 := newSandbox(t, matrix.Local)
	if err := os.WriteFile(filepath.Join(sb2.Home, "config.toml"),
		[]byte("[defaults]\nnotifications = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sb2.startDaemon(t)
	sb2.newHeadlessThread(t, "pi", "muteborn")
	if threadByName(t, sb2, "muteborn").Notify {
		t.Errorf("[defaults] notifications=false ignored at creation")
	}
}

func testNotifyRemote(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Remote)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, "pi", "rgated")
	if _, stderr, err := sb.Runner.Run(t, "thread", "notify", "--id", th.ID, "--off"); err != nil {
		t.Fatalf("routed notify --off: %v\n%s", err, stderr)
	}
	if threadByName(t, sb, "rgated").Notify {
		t.Errorf("routed toggle did not persist on the peer")
	}
}
