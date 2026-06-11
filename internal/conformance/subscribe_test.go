package conformance

// thread.subscribe cells (PARITY_ROADMAP C3): the turn-delivery engine over
// REAL agents — a subscribee's real completed turn lands, formatted, in the
// subscriber's REAL pane, exactly once (dedup), cycles are refused (and
// --allow-cycle permitted), unsubscribe stops delivery. Remote = the full
// cross-machine path: the subscribee lives on a PEER whose daemon delivers
// the turn into the LOCAL subscriber's pane via the routed send.

import (
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	matrix.RegisterTest("thread.subscribe", matrix.AgentAgnostic, matrix.Local, testSubscribeLocal)
	matrix.RegisterTest("thread.subscribe", matrix.AgentAgnostic, matrix.Remote, testSubscribeCrossMachine)
}

// paneContains polls the subscriber's pane for text.
func paneContains(t *testing.T, sb *Sandbox, pane, want string, within time.Duration) bool {
	t.Helper()
	return waitUntil(within, func() bool {
		cap, err := sb.rawTmux(t, "capture-pane", "-t", pane, "-p", "-S", "-200")
		return err == nil && strings.Contains(cap, want)
	})
}

func testSubscribeLocal(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)

	// Two subscribers: a REAL headed pi pane (the delivery-format assertion)
	// and a HEADLESS one (the structural dedup assertion — re-delivery would
	// start a second turn and bump its reply count). Subscribee: headless pi.
	subThread := sb.newThread(t, "pi", "watcher", "/tmp")
	subPane := sb.waitThreadReady(t, subThread.ID, "pi")
	listener := sb.newHeadlessThread(t, "pi", "listener")
	bee := sb.newHeadlessThread(t, "pi", "speaker")

	if _, stderr, err := sb.Runner.Run(t, "subscribe", bee.ID, "--from", subThread.ID); err != nil {
		t.Fatalf("subscribe: %v\n%s", err, stderr)
	}
	if _, stderr, err := sb.Runner.Run(t, "subscribe", bee.ID, "--from", listener.ID); err != nil {
		t.Fatalf("subscribe listener: %v\n%s", err, stderr)
	}
	out, _, err := sb.Runner.Run(t, "subscriptions", bee.ID)
	if err != nil || !strings.Contains(out, subThread.ID) {
		t.Errorf("subscriptions list missing the edge: %v\n%s", err, out)
	}

	// A real turn -> the delivery lands in the REAL pane: header + reply.
	sb.headlessTurn(t, bee.ID, "Reply with exactly the word MAGENTA and nothing else")
	if !paneContains(t, sb, subPane, "completed a turn", 60*time.Second) {
		cap, _ := sb.rawTmux(t, "capture-pane", "-t", subPane, "-p", "-S", "-200")
		t.Fatalf("delivery never reached the subscriber pane:\n%s", cap)
	}
	if !paneContains(t, sb, subPane, "MAGENTA", 15*time.Second) {
		t.Errorf("delivery missing the subscribee's actual reply")
	}

	// The HEADLESS subscriber received the delivery: the formatted message (the
	// subscribee's reply) lands in its transcript as a user turn. We assert on
	// the DELIVERED MARKER, not the listener generating its own reply — the
	// property under test is "delivered, exactly once", and not depending on a
	// second real agent reply keeps this (already heavy) cell from being
	// dominated by agent latency on a loaded box.
	marker := "completed a turn"
	if !waitUntil(90*time.Second, func() bool {
		out, _, rerr := sb.Runner.Run(t, "thread", "transcript", "--id", listener.ID, "--json")
		return rerr == nil && strings.Contains(out, marker) && strings.Contains(out, "MAGENTA")
	}) {
		t.Fatalf("headless subscriber never received the delivery")
	}
	// DEDUP: the delivery marker appears EXACTLY once — extra eventer ticks and
	// the deterministic completion-path trigger must not re-deliver. Give them
	// time to (wrongly) fire, then count.
	time.Sleep(6 * time.Second)
	tr2, _, err := sb.Runner.Run(t, "thread", "transcript", "--id", listener.ID, "--json")
	if err != nil {
		t.Fatal(err)
	}
	// Count the DELIVERY MARKER, not MAGENTA — the marker appears only in
	// delivered messages, whereas a verbose listener reply may repeat MAGENTA.
	if n := strings.Count(tr2, marker); n != 1 {
		t.Errorf("delivery not exactly-once: the delivery marker appears %d times in the listener transcript", n)
	}

	// Cycle refusal (watcher's turns would loop back into speaker), and
	// --allow-cycle permits it.
	if _, stderr, err := sb.Runner.Run(t, "subscribe", subThread.ID, "--from", bee.ID); err == nil {
		t.Errorf("cycle subscribe succeeded silently")
	} else if !strings.Contains(stderr, "cycle") {
		t.Errorf("cycle error wrong: %s", stderr)
	}
	if _, stderr, err := sb.Runner.Run(t, "subscribe", subThread.ID, "--from", bee.ID, "--allow-cycle"); err != nil {
		t.Errorf("--allow-cycle refused: %v\n%s", err, stderr)
	}
	if _, stderr, err := sb.Runner.Run(t, "unsubscribe", subThread.ID, "--from", bee.ID); err != nil {
		t.Fatalf("unsubscribe (cycle edge): %v\n%s", err, stderr)
	}

	// Unsubscribe stops delivery: a second turn never lands.
	if _, stderr, err := sb.Runner.Run(t, "unsubscribe", bee.ID, "--from", subThread.ID); err != nil {
		t.Fatalf("unsubscribe: %v\n%s", err, stderr)
	}
	sb.headlessTurn(t, bee.ID, "Reply with exactly the word VERDIGRIS and nothing else")
	time.Sleep(6 * time.Second) // generous: tick + would-be delivery window
	if paneContains(t, sb, subPane, "VERDIGRIS", 1*time.Second) {
		t.Errorf("delivery continued after unsubscribe")
	}

	// Unsubscribing a non-existent edge is loud.
	if _, _, err := sb.Runner.Run(t, "unsubscribe", bee.ID, "--from", subThread.ID); err == nil {
		t.Errorf("removing a non-existent edge succeeded silently")
	}
}

// testSubscribeCrossMachine: the subscribee lives on a PEER; its daemon owns
// delivery and reaches the LOCAL subscriber's pane via the routed send.
func testSubscribeCrossMachine(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	ensureSSHLocalhost(t)
	local := newSandbox(t, matrix.Local)
	local.startDaemon(t)
	peer := newSandbox(t, matrix.Local)
	peer.startDaemon(t)
	bin := seshBin(t)
	// BOTH daemons need each other: the local one to route the subscribe, the
	// peer one to find the subscriber's owner + route the delivery back.
	if _, stderr, err := local.Runner.Run(t, "peer", "add", "--machine", peer.Machine, "--ssh", "localhost",
		"--home", peer.Home, "--binary", bin, "--tmux-socket", peer.TmuxSocket); err != nil {
		t.Fatalf("peer add local->peer: %v\n%s", err, stderr)
	}
	if _, stderr, err := peer.Runner.Run(t, "peer", "add", "--machine", local.Machine, "--ssh", "localhost",
		"--home", local.Home, "--binary", bin, "--tmux-socket", local.TmuxSocket); err != nil {
		t.Fatalf("peer add peer->local: %v\n%s", err, stderr)
	}

	subThread := local.newThread(t, "pi", "farwatcher", "/tmp")
	subPane := local.waitThreadReady(t, subThread.ID, "pi")
	bee := peer.newHeadlessThread(t, "pi", "farspeaker")

	// The edge lives on the SUBSCRIBEE's owner (the peer).
	if _, stderr, err := peer.Runner.Run(t, "subscribe", bee.ID, "--from", subThread.ID); err != nil {
		t.Fatalf("subscribe on peer: %v\n%s", err, stderr)
	}

	peer.headlessTurn(t, bee.ID, "Reply with exactly the word COBALT and nothing else")
	if !paneContains(t, local, subPane, "COBALT", 90*time.Second) {
		cap, _ := local.rawTmux(t, "capture-pane", "-t", subPane, "-p", "-S", "-200")
		t.Fatalf("cross-machine delivery never reached the local pane:\n%s", cap)
	}
}
