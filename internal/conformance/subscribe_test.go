package conformance

// thread.subscribe cells (PARITY_ROADMAP C3): the turn-delivery engine over
// REAL agents — a subscribee's real completed turn lands, formatted, in the
// subscriber's REAL pane, exactly once (dedup), cycles are refused (and
// --allow-cycle permitted), unsubscribe stops delivery. Remote = the full
// cross-machine path: the subscribee lives on a PEER whose daemon delivers
// the turn into the LOCAL subscriber's pane via the routed send.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
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
	// Pane visibility is not delivery success: the exact production regression
	// left the whole report sitting in the composer. Prove the headed agent
	// accepted Enter by finding the formatted report as a real user turn.
	if !waitUntil(90*time.Second, func() bool {
		out, _, rerr := sb.Runner.Run(t, "thread", "transcript", "--id", subThread.ID, "--json")
		return rerr == nil && strings.Contains(out, "completed a turn") && strings.Contains(out, "MAGENTA")
	}) {
		t.Fatalf("headed subscriber showed the delivery but never submitted it")
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
	// Count the delivery marker, not MAGENTA: the marker appears in every
	// delivered user message, while a duplicate is impossible to hide even if
	// the listener has not produced an assistant reply yet.
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

// TestCodexSubscriptionDeliverySubmits is a focused regression probe for the
// production failure where a completed-turn delivery was visibly pasted into a
// headed Codex composer but its Enter was not accepted. Unlike the matrix's
// paneContains assertion, this proves the formatted delivery became a real user
// turn in Codex's transcript. The second delivery deliberately lands while the
// subscriber is already working, matching a supervising parent receiving a
// child's report mid-turn; the first idle delivery rules out a one-shot/startup
// explanation and makes the pair a repeated-delivery check.
func TestCodexSubscriptionDeliverySubmits(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)

	parent := sb.newThread(t, "codex", "codex-parent", "/tmp")
	parentPane := sb.waitThreadReady(t, parent.ID, "codex")
	child := sb.newHeadlessThread(t, "pi", "reporting-child")
	if _, stderr, err := sb.Runner.Run(t, "subscribe", child.ID, "--from", parent.ID); err != nil {
		t.Fatalf("subscribe: %v\n%s", err, stderr)
	}

	const first = "SUBSCRIPTION_IDLE_SENTINEL_7B91"
	sb.headlessTurn(t, child.ID, "Reply with exactly "+first+" and nothing else")
	if !paneContains(t, sb, parentPane, first, 30*time.Second) {
		cap, _ := sb.rawTmux(t, "capture-pane", "-t", parentPane, "-p", "-S", "-200")
		t.Fatalf("idle delivery never reached parent pane:\n%s", cap)
	}
	if !waitUntil(30*time.Second, func() bool {
		return sb.threadStatus(t, parent.ID).Busy == api.BusyBusy
	}) {
		cap, _ := sb.rawTmux(t, "capture-pane", "-t", parentPane, "-p", "-S", "-200")
		t.Fatalf("idle delivery remained pasted without starting a Codex turn:\n%s", cap)
	}
	if !waitUntil(120*time.Second, func() bool {
		out, _, err := sb.Runner.Run(t, "thread", "transcript", "--id", parent.ID, "--json")
		return err == nil && strings.Contains(out, first)
	}) {
		cap, _ := sb.rawTmux(t, "capture-pane", "-t", parentPane, "-p", "-S", "-200")
		t.Fatalf("idle delivery was pasted but never submitted into Codex transcript:\n%s", cap)
	}
	if !waitUntil(120*time.Second, func() bool {
		return sb.threadStatus(t, parent.ID).Busy == api.BusyIdle
	}) {
		t.Fatalf("parent never settled after idle delivery")
	}

	// Keep the parent occupied long enough for the child's quick reply to finish
	// and exercise headed-busy delivery rather than only the easy idle path.
	if _, stderr, err := sb.Runner.Run(t, "thread", "send", "--id", parent.ID, "--text",
		"Run the shell command sleep 20.\n\nWhen it completes, reply with exactly PARENT_BASE_TURN_DONE."); err != nil {
		t.Fatalf("start parent turn: %v\n%s", err, stderr)
	}
	if !waitUntil(30*time.Second, func() bool {
		return sb.threadStatus(t, parent.ID).Busy == api.BusyBusy
	}) {
		t.Fatalf("parent never became busy")
	}

	const second = "SUBSCRIPTION_BUSY_SENTINEL_3D42"
	sb.headlessTurn(t, child.ID, "Reply with exactly "+second+" and nothing else")
	if !paneContains(t, sb, parentPane, second, 30*time.Second) {
		cap, _ := sb.rawTmux(t, "capture-pane", "-t", parentPane, "-p", "-S", "-200")
		t.Fatalf("busy delivery never reached parent pane:\n%s", cap)
	}
	if !waitUntil(120*time.Second, func() bool {
		out, _, err := sb.Runner.Run(t, "thread", "transcript", "--id", parent.ID, "--json")
		return err == nil && strings.Contains(out, second)
	}) {
		cap, _ := sb.rawTmux(t, "capture-pane", "-t", parentPane, "-p", "-S", "-200")
		t.Fatalf("busy delivery was pasted but never submitted into Codex transcript:\n%s", cap)
	}
	if !waitUntil(120*time.Second, func() bool {
		return sb.threadStatus(t, parent.ID).Busy == api.BusyIdle
	}) {
		t.Fatalf("parent never settled after busy delivery")
	}
}

// TestCodexLongSingleLineSendSubmits reproduces the shape used by direct child
// completion reports in production: roughly 1.1-1.3 KiB with no newline. That
// took SendText's old literal-key path rather than bracketed paste. Seeing the
// tail marker in the composer is not success; Codex must begin a turn and record
// the whole message in its transcript. Three sequential reports pin recurrence.
func TestCodexLongSingleLineSendSubmits(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)

	parent := sb.newThread(t, "codex", "direct-report-parent", "/tmp")
	parentPane := sb.waitThreadReady(t, parent.ID, "codex")
	for attempt := 1; attempt <= 3; attempt++ {
		begin := fmt.Sprintf("DIRECT_REPORT_%d_BEGIN_91C4", attempt)
		end := fmt.Sprintf("DIRECT_REPORT_%d_END_62AF", attempt)
		text := begin + " " + strings.Repeat("production report detail; ", 48) + end
		if strings.Contains(text, "\n") || len(text) < 1100 || len(text) > 1300 {
			t.Fatalf("bad reproduction payload shape: %d bytes", len(text))
		}

		if _, stderr, err := sb.Runner.Run(t, "thread", "send", "--id", parent.ID, "--text", text); err != nil {
			t.Fatalf("direct report %d send: %v\n%s", attempt, err, stderr)
		}
		if !paneContains(t, sb, parentPane, end, 30*time.Second) {
			cap, _ := sb.rawTmux(t, "capture-pane", "-t", parentPane, "-p", "-S", "-200")
			t.Fatalf("direct report %d tail never reached Codex composer:\n%s", attempt, cap)
		}
		if !waitUntil(30*time.Second, func() bool {
			return sb.threadStatus(t, parent.ID).Busy == api.BusyBusy
		}) {
			cap, _ := sb.rawTmux(t, "capture-pane", "-t", parentPane, "-p", "-S", "-200")
			t.Fatalf("full direct report %d was pasted but Enter did not submit it:\n%s", attempt, cap)
		}
		if !waitUntil(120*time.Second, func() bool {
			out, _, err := sb.Runner.Run(t, "thread", "transcript", "--id", parent.ID, "--json")
			return err == nil && strings.Contains(out, begin) && strings.Contains(out, end)
		}) {
			cap, _ := sb.rawTmux(t, "capture-pane", "-t", parentPane, "-p", "-S", "-200")
			t.Fatalf("direct report %d never became a complete Codex user turn:\n%s", attempt, cap)
		}
		if !waitUntil(120*time.Second, func() bool {
			return sb.threadStatus(t, parent.ID).Busy == api.BusyIdle
		}) {
			t.Fatalf("Codex never settled after direct report %d", attempt)
		}
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

	// deliverTo resolves a non-local subscriber from the subscribee owner's
	// replicated mesh cache. Do not race first sync: prove the peer can already
	// see the real local thread before allowing a completion to consume dedup.
	if !waitUntil(30*time.Second, func() bool {
		out, _, rerr := peer.Runner.Run(t, "mesh", "--json")
		return rerr == nil && strings.Contains(out, subThread.ID)
	}) {
		t.Fatalf("peer mesh cache never learned the remote subscriber")
	}

	// The edge lives on the SUBSCRIBEE's owner (the peer).
	if _, stderr, err := peer.Runner.Run(t, "subscribe", bee.ID, "--from", subThread.ID); err != nil {
		t.Fatalf("subscribe on peer: %v\n%s", err, stderr)
	}

	peer.headlessTurn(t, bee.ID, "Reply with exactly the word COBALT and nothing else")
	if !paneContains(t, local, subPane, "COBALT", 90*time.Second) {
		cap, _ := local.rawTmux(t, "capture-pane", "-t", subPane, "-p", "-S", "-200")
		t.Fatalf("cross-machine delivery never reached the local pane:\n%s", cap)
	}
	if !waitUntil(90*time.Second, func() bool {
		out, _, rerr := local.Runner.Run(t, "thread", "transcript", "--id", subThread.ID, "--json")
		return rerr == nil && strings.Contains(out, "completed a turn") && strings.Contains(out, "COBALT")
	}) {
		t.Fatalf("cross-machine delivery reached the pane but never became a submitted turn")
	}
}
