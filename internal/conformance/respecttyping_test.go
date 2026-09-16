package conformance

// thread.send-respect-typing cells (_dev/SCHEDULING.md §6.5.1): a paste into a
// live pane is appended to whatever a viewer has half-typed and SUBMITTED with
// it, so the owning daemon holds a `thread send` while the pane's session sees
// viewer input, delivers it once the pane has been quiet for the window, and —
// still in use at the deadline — fails loudly and auto-flags the thread.
//
// The honest proof needs a REAL viewer: a nested tmux client attached to the
// agent's session, typing THROUGH that client (which is what bumps tmux's
// client_activity — the daemon's own send-keys into the pane does not, and a
// stubbed activity would prove nothing). A real pi runs in the pane so the
// delivery's landing is observable as a real turn starting. The remote cell
// drives every verb over the real ssh hop; the viewer sits on the peer's socket.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	matrix.RegisterTest("thread.send-respect-typing", matrix.AgentAgnostic, matrix.Local,
		func(t *testing.T) { testSendRespectTyping(t, matrix.Local) })
	matrix.RegisterTest("thread.send-respect-typing", matrix.AgentAgnostic, matrix.Remote,
		func(t *testing.T) { testSendRespectTyping(t, matrix.Remote) })
}

// threadRecord reads a thread's stored record from the OWNER's daemon
// (`thread list --json` is JSONL of flat api.Thread rows).
func (sb *Sandbox) threadRecord(t *testing.T, id string) api.Thread {
	t.Helper()
	stdout, stderr, err := sb.daemonRunner.Run(t, "thread", "list", "--json", "--archived")
	if err != nil {
		t.Fatalf("thread list: %v\n%s", err, stderr)
	}
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		var th api.Thread
		if json.Unmarshal([]byte(line), &th) == nil && th.ID == id {
			return th
		}
	}
	t.Fatalf("thread %s not in the owner's list", id)
	return api.Thread{}
}

func testSendRespectTyping(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)
	th := sb.newThread(t, "pi", "typing-guard", "/tmp")
	pane := sb.waitThreadReady(t, th.ID, "pi")
	session := th.SessionName
	sb.attachViewer(t, session)
	viewer := "viewer_" + session
	if !waitUntil(10*time.Second, func() bool {
		out, err := sb.rawTmux(t, "list-clients", "-t", "="+session, "-F", "#{client_name}")
		return err == nil && strings.TrimSpace(out) != ""
	}) {
		t.Fatalf("viewer never attached to %s", session)
	}
	capture := func() string {
		out, _ := sb.rawTmux(t, "capture-pane", "-t", pane, "-p")
		return out
	}
	// typeThrough sends keystrokes into the viewer session; the nested client
	// forwards them into the agent pane, exactly like a human at the keyboard.
	typeThrough := func(text string) {
		t.Helper()
		if out, err := sb.rawTmux(t, "send-keys", "-t", viewer+":", "-l", text); err != nil {
			t.Fatalf("type through viewer %q: %v\n%s", viewer, err, out)
		}
	}

	// 1. A viewer mid-line: the send is HELD (deferred), nothing lands while the
	//    viewer keeps typing, and the agent stays idle.
	typeThrough("draft ")
	time.Sleep(300 * time.Millisecond)
	stdout, stderr, err := sb.Runner.Run(t, "thread", "send", "--id", th.ID,
		"--text", "GUARD-MSG-ONE: write three sentences about tmux",
		"--respect-typing", "3s", "--typing-deadline", "60s")
	if err != nil {
		t.Fatalf("deferred send: %v\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "deferred "+th.ID) {
		t.Fatalf("send with a typing viewer was not deferred: stdout=%q stderr=%q", stdout, stderr)
	}
	// Keep typing for 2s: the delivery must NOT land (the guard is the whole point).
	for i := 0; i < 4; i++ {
		typeThrough("k")
		time.Sleep(500 * time.Millisecond)
		if strings.Contains(capture(), "GUARD-MSG-ONE") {
			t.Fatalf("the held message landed while the viewer was still typing — the collision the guard exists to prevent:\n%s", capture())
		}
	}
	// (No busy assertion here: the viewer's own keystroke echoes latch the
	// content-diff heuristic exactly like agent output would — H47 — so the
	// pane's TEXT is the only honest signal of whether the message landed.)
	// 2. Stop typing: within the 3s window (+ the daemon's 2s poll) it lands, and
	//    the agent starts a real turn on it.
	if !waitUntil(15*time.Second, func() bool { return strings.Contains(capture(), "GUARD-MSG-ONE") }) {
		t.Fatalf("held message never delivered after the pane went quiet:\n%s", capture())
	}
	if !waitUntil(30*time.Second, func() bool { return sb.threadStatus(t, th.ID).Busy == api.BusyBusy }) {
		t.Fatalf("agent never started working after the deferred delivery landed")
	}
	// Let the turn finish before the next scenario so busy reads are unambiguous.
	waitUntil(120*time.Second, func() bool { return sb.threadStatus(t, th.ID).Busy == api.BusyIdle })

	// 3. skip policy: a typing viewer is a loud refusal, exit non-zero, nothing queued.
	typeThrough("more ")
	time.Sleep(300 * time.Millisecond)
	if _, stderr, err := sb.Runner.Run(t, "thread", "send", "--id", th.ID, "--text", "GUARD-MSG-SKIP",
		"--respect-typing", "30s", "--on-typing", "skip"); err == nil {
		t.Fatalf("skip policy with a typing viewer must fail loudly; it succeeded")
	} else if !strings.Contains(stderr, "pane is in use") {
		t.Fatalf("skip refusal must name the cause; got: %s", stderr)
	}
	time.Sleep(3 * time.Second)
	if strings.Contains(capture(), "GUARD-MSG-SKIP") {
		t.Fatalf("a skipped send reached the pane:\n%s", capture())
	}

	// 4. --respect-typing 0 overrides the guard: the paste lands at once.
	typeThrough("still typing ")
	if stdout, stderr, err := sb.Runner.Run(t, "thread", "send", "--id", th.ID, "--text", "GUARD-MSG-FORCE",
		"--respect-typing", "0"); err != nil || !strings.Contains(stdout, "sent "+th.ID) {
		t.Fatalf("--respect-typing 0 must paste immediately: %v stdout=%q stderr=%q", err, stdout, stderr)
	}
	if !waitUntil(5*time.Second, func() bool { return strings.Contains(capture(), "GUARD-MSG-FORCE") }) {
		t.Fatalf("forced paste never reached the pane:\n%s", capture())
	}
	waitUntil(120*time.Second, func() bool { return sb.threadStatus(t, th.ID).Busy == api.BusyIdle })

	// 5. The deadline: a viewer who never stops makes the held delivery FAIL —
	//    nothing pasted — and the thread is flagged with the reason.
	if rec := sb.threadRecord(t, th.ID); rec.Flagged {
		// A turn end may have auto-flagged the thread above; clear so the
		// undelivered-message flag is unmistakably this scenario's.
		if _, stderr, err := sb.Runner.Run(t, "thread", "flag", "--id", th.ID, "--off"); err != nil {
			t.Fatalf("flag --off: %v\n%s", err, stderr)
		}
	}
	typeThrough("busy ")
	time.Sleep(300 * time.Millisecond)
	if stdout, stderr, err := sb.Runner.Run(t, "thread", "send", "--id", th.ID, "--text", "GUARD-MSG-LOST",
		"--respect-typing", "4s", "--typing-deadline", "5s"); err != nil || !strings.Contains(stdout, "deferred") {
		t.Fatalf("deadline scenario: want deferred, got %v stdout=%q stderr=%q", err, stdout, stderr)
	}
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(700 * time.Millisecond):
				sb.rawTmux(t, "send-keys", "-t", viewer+":", "-l", "z") //nolint:errcheck
			}
		}
	}()
	flagged := waitUntil(20*time.Second, func() bool { return sb.threadRecord(t, th.ID).Flagged })
	close(stop)
	if !flagged {
		t.Fatalf("thread was never flagged after the typing deadline lapsed")
	}
	if reason := sb.threadRecord(t, th.ID).FlagReason; !strings.Contains(reason, "undelivered message from thread send") {
		t.Fatalf("flag reason = %q, want the undelivered-message reason", reason)
	}
	if strings.Contains(capture(), "GUARD-MSG-LOST") {
		t.Fatalf("the deadline-failed message was pasted anyway:\n%s", capture())
	}
}
