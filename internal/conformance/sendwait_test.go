package conformance

import (
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

// thread.send-wait (schema 43, issue #7): `thread send --wait` blocks until
// the delivered turn SETTLES (idle/blocked), released by the real turn
// boundary, and `thread wait --until` waits fail LOUDLY on timeout instead of
// hanging or lying. (The 5s stall guard — herdr's agent_prompt_stalled idea —
// cannot be honestly driven in a cell: a wedged pane requires freezing the
// real agent, and the tmux server SIGCONTs any stopped pane child immediately
// [verified live: SIGSTOP flips back within one ps sample; SIGTTIN is caught
// by node agents]. Its CLI composition is pinned by TestSendWaitStallGuard in
// cmd/sesh against a scripted daemon — a unit test outside the matrix, where
// mocking is legitimate.)
func init() {
	for _, loc := range matrix.AllLocalities {
		loc := loc
		for _, a := range matrix.AllAgents {
			a := a
			matrix.RegisterTest("thread.send-wait", a, loc,
				func(t *testing.T) { testSendWait(t, string(a), loc) })
		}
	}
}

func testSendWait(t *testing.T, agent string, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	th := sb.newThread(t, agent, "sw", "/tmp")
	sb.waitThreadReady(t, th.ID, agent)

	// (1) Happy path: --wait returns ONLY once the real turn settled. The
	// sentinel is a concatenation the agent must COMPUTE, so finding it in the
	// pane after return proves the reply existed before the wait released
	// (the echoed prompt can't contain it).
	start := time.Now()
	stdout, stderr, err := sb.Runner.Run(t, "thread", "send", "--id", th.ID,
		"--text", "Reply with exactly the concatenation of the words GREEN and LIGHT as one single uppercase word, nothing else.",
		"--wait", "--timeout", "120s")
	if err != nil {
		t.Fatalf("send --wait: %v\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "settled: idle") {
		t.Fatalf("send --wait output %q, want settled: idle", stdout)
	}
	if elapsed := time.Since(start); elapsed < 1*time.Second {
		t.Fatalf("send --wait returned in %s — faster than any real turn (released early?)", elapsed)
	}
	if st := sb.threadStatus(t, th.ID); st.Busy != api.BusyIdle {
		t.Fatalf("post-wait status = %s, want idle", st.Busy)
	}
	pane, _, ok := sb.markedPane(t, th.ID)
	if !ok {
		t.Fatalf("no marked pane for %s", th.ID)
	}
	if out, err := sb.rawTmux(t, "capture-pane", "-p", "-t", pane); err != nil || !strings.Contains(out, "GREENLIGHT") {
		t.Fatalf("reply sentinel not in pane after --wait returned (err=%v):\n%s", err, out)
	}

	// (2) Fail-loud: a wait whose condition never arrives (the settled thread
	// will not go busy on its own) must ERROR on timeout, naming the state —
	// never hang past the deadline and never exit 0.
	start = time.Now()
	_, stderr, err = sb.Runner.Run(t, "thread", "wait", "--id", th.ID,
		"--until", "busy", "--timeout", "3s")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("wait --until busy on an idle thread exited 0 — a timed-out wait must be loud")
	}
	if !strings.Contains(stderr, "did not reach busy") || !strings.Contains(stderr, "idle") {
		t.Fatalf("timeout error must name the target and last state:\n%s", stderr)
	}
	if elapsed > 15*time.Second {
		t.Fatalf("3s wait took %s", elapsed)
	}
}
