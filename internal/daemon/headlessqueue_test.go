package daemon

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/api"
)

// stubHeadlessTurns replaces the agent binary with a turn that reports its prompt
// on started and then runs until the test sends on hold (one turn per send). The
// returned channel is closed at cleanup, which releases any turn still running.
func stubHeadlessTurns(t *testing.T) (started chan string, hold chan struct{}) {
	t.Helper()
	started = make(chan string, 32)
	hold = make(chan struct{})
	prev := headlessTurnRunner
	headlessTurnRunner = func(spec agents.TurnSpec) (string, string, error) {
		started <- spec.Prompt
		<-hold
		return "reply to " + spec.Prompt, spec.SessionID, nil
	}
	t.Cleanup(func() {
		headlessTurnRunner = prev
		close(hold)
	})
	return started, hold
}

func awaitTurnStart(t *testing.T, started chan string, want string) {
	t.Helper()
	select {
	case got := <-started:
		if got != want {
			t.Fatalf("turn started with %q, want %q", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no turn started for %q", want)
	}
}

// awaitIdle waits until the thread has no turn in flight and no queued message.
func awaitIdle(t *testing.T, d *Daemon, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		n, err := d.store.HeadlessQueueCount(id)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 && !d.turnInFlight(id) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("thread %s never went idle with an empty queue", id)
}

func postSend(t *testing.T, d *Daemon, req api.ThreadSendRequest) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	d.handleThreadSend(rec, httptest.NewRequest(http.MethodPost, "/v1/threads/send", strings.NewReader(string(b))))
	return rec
}

func sendResponse(t *testing.T, rec *httptest.ResponseRecorder) api.ThreadSendResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("send = %d %s, want 200", rec.Code, rec.Body.String())
	}
	var resp api.ThreadSendResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func queueOf(t *testing.T, d *Daemon, id string) api.ThreadQueueResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	d.handleThreadQueue(rec, httptest.NewRequest(http.MethodGet, "/v1/threads/queue?id="+id, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("queue = %d %s", rec.Code, rec.Body.String())
	}
	var out api.ThreadQueueResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// A message sent while a headless turn runs waits behind it, and the queue drains
// one turn per message in send order: two queued messages run after the first,
// first-sent first.
func TestHeadlessQueueDeliversInOrderAfterTheRunningTurn(t *testing.T) {
	d := newBrowserTestDaemon(t, "self", "")
	th := insertArchiveGateThread(t, d, "queue-order")
	started, hold := stubHeadlessTurns(t)

	first := sendResponse(t, postSend(t, d, api.ThreadSendRequest{ID: th.ID, Text: "A"}))
	if first.Sent != th.ID {
		t.Fatalf("idle send = %+v, want started (sent=%s)", first, th.ID)
	}
	awaitTurnStart(t, started, "A")

	b := sendResponse(t, postSend(t, d, api.ThreadSendRequest{ID: th.ID, Text: "B"}))
	c := sendResponse(t, postSend(t, d, api.ThreadSendRequest{ID: th.ID, Text: "C"}))
	if !b.Deferred || b.HeldReason != api.HeldReasonTurn || b.QueueSeq == 0 || c.QueueSeq <= b.QueueSeq {
		t.Fatalf("queued sends = %+v / %+v, want deferred turn-held seqs in order", b, c)
	}
	q := queueOf(t, d, th.ID)
	if !q.Working || len(q.Messages) != 2 || q.Messages[0].Text != "B" || q.Messages[1].Text != "C" {
		t.Fatalf("queue while A runs = %+v, want working with [B C]", q)
	}
	if n := postSend(t, d, api.ThreadSendRequest{ID: th.ID, Text: "x"}); n.Code != http.StatusOK {
		t.Fatalf("third queued send = %d", n.Code)
	}

	// Let each turn finish; the next queued message starts as soon as one does.
	for _, want := range []string{"B", "C"} {
		hold <- struct{}{} // finish the running turn
		awaitTurnStart(t, started, want)
	}
	hold <- struct{}{} // finish C (the trailing "x" starts next)
	awaitTurnStart(t, started, "x")
	hold <- struct{}{}
	awaitIdle(t, d, th.ID)
}

// skip keeps the refusal: a busy headless thread answers 409 and queues nothing.
func TestHeadlessSendSkipRefusesWhileBusy(t *testing.T) {
	d := newBrowserTestDaemon(t, "self", "")
	th := insertArchiveGateThread(t, d, "skip-busy")
	started, hold := stubHeadlessTurns(t)

	sendResponse(t, postSend(t, d, api.ThreadSendRequest{ID: th.ID, Text: "A"}))
	awaitTurnStart(t, started, "A")

	rec := postSend(t, d, api.ThreadSendRequest{ID: th.ID, Text: "B", OnTyping: api.OnTypingSkip})
	if rec.Code != http.StatusConflict {
		t.Fatalf("skip while busy = %d %s, want 409", rec.Code, rec.Body.String())
	}
	if n, _ := d.store.HeadlessQueueCount(th.ID); n != 0 {
		t.Fatalf("skip left %d message(s) queued", n)
	}
	hold <- struct{}{}
	awaitIdle(t, d, th.ID)
}

// A queued message survives a daemon restart as a queued message, is never run by
// the restart, and flags its thread so it is seen; drop removes it loudly-on-purpose.
func TestHeadlessQueueSurvivingRestartIsFlaggedNotRun(t *testing.T) {
	d := newBrowserTestDaemon(t, "self", "")
	th := insertArchiveGateThread(t, d, "survivor")
	started, _ := stubHeadlessTurns(t)
	if _, err := d.store.EnqueueHeadless(th.ID, "thread send", "kept over restart", time.Now()); err != nil {
		t.Fatal(err)
	}

	if err := d.reportSurvivingHeadlessQueues(); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-started:
		t.Fatalf("restart report ran a turn for %q", p)
	default:
	}
	got, err := d.store.GetThread(th.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Flagged || !strings.Contains(got.FlagReason, "survived a daemon restart") {
		t.Fatalf("thread after restart = flagged %v reason %q, want the restart flag", got.Flagged, got.FlagReason)
	}
	q := queueOf(t, d, th.ID)
	if len(q.Messages) != 1 || q.Messages[0].Text != "kept over restart" {
		t.Fatalf("queue after restart = %+v, want the kept message", q)
	}

	// Deleting the thread would drop the message, so it refuses.
	b, _ := json.Marshal(api.DeleteThreadRequest{ID: th.ID})
	rec := httptest.NewRecorder()
	d.handleThreadDelete(rec, httptest.NewRequest(http.MethodPost, "/v1/threads/delete", strings.NewReader(string(b))))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "queued message") {
		t.Fatalf("delete with a queued message = %d %s, want a 409 naming it", rec.Code, rec.Body.String())
	}

	// Dropping it is the explicit way out.
	dropBody, _ := json.Marshal(api.ThreadQueueDropRequest{ID: th.ID, Seq: q.Messages[0].Seq})
	drop := httptest.NewRecorder()
	d.handleThreadQueueDrop(drop, httptest.NewRequest(http.MethodPost, "/v1/threads/queue/drop", strings.NewReader(string(dropBody))))
	if drop.Code != http.StatusOK {
		t.Fatalf("drop = %d %s", drop.Code, drop.Body.String())
	}
	if n := len(queueOf(t, d, th.ID).Messages); n != 0 {
		t.Fatalf("queue after drop has %d message(s)", n)
	}
}

// A message whose delivery cannot start stays queued and visible: the kick reports
// it failed, the queue still holds it, and nothing ran.
func TestHeadlessQueueFailedDeliveryStaysQueued(t *testing.T) {
	d := newBrowserTestDaemon(t, "self", "")
	stubHeadlessTurns(t)
	// A thread that does not exist: its delivery cannot start.
	seq, err := d.store.EnqueueHeadless("no-such-thread", "thread send", "orphan", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := d.kickHeadless("no-such-thread", seq); got != kickFailed {
		t.Fatalf("kick = %v, want kickFailed", got)
	}
	msgs, err := d.store.HeadlessQueue("no-such-thread")
	if err != nil || len(msgs) != 1 || msgs[0].Seq != seq {
		t.Fatalf("queue after failed delivery = %+v err=%v, want the message kept", msgs, err)
	}
}

// A turn started the way `sesh delegate` starts one (send-headless, the path its
// worker uses) holds the thread's slot: a `thread send` to that thread queues behind
// it instead of starting a second --resume turn on the same conversation.
func TestSendQueuesBehindADelegateStyleTurn(t *testing.T) {
	d := newBrowserTestDaemon(t, "self", "")
	th := insertArchiveGateThread(t, d, "delegate-worker")
	started, hold := stubHeadlessTurns(t)

	b, _ := json.Marshal(api.ThreadSendRequest{ID: th.ID, Text: "the task"})
	rec := httptest.NewRecorder()
	d.handleThreadSendHeadless(rec, httptest.NewRequest(http.MethodPost, "/v1/threads/send-headless", strings.NewReader(string(b))))
	if rec.Code != http.StatusOK {
		t.Fatalf("send-headless = %d %s", rec.Code, rec.Body.String())
	}
	awaitTurnStart(t, started, "the task")

	resp := sendResponse(t, postSend(t, d, api.ThreadSendRequest{ID: th.ID, Text: "extra context from the parent"}))
	if resp.Sent != "" || !resp.Deferred || resp.HeldReason != api.HeldReasonTurn {
		t.Fatalf("send during a delegate turn = %+v, want queued behind it", resp)
	}
	select {
	case p := <-started:
		t.Fatalf("a second turn started for %q while the first was in flight", p)
	default:
	}
	hold <- struct{}{}
	awaitTurnStart(t, started, "extra context from the parent")
	hold <- struct{}{}
	awaitIdle(t, d, th.ID)
}

// A turn process recorded by a previous daemon and still alive keeps the thread's
// slot after a restart: sends queue (send-headless refuses), nothing starts, and when
// the process exits the slot is released and the queue delivers in order.
func TestSurvivingLiveTurnHoldsTheSlotAfterRestart(t *testing.T) {
	d := newBrowserTestDaemon(t, "self", "")
	th := insertArchiveGateThread(t, d, "restart-survivor")
	started, hold := stubHeadlessTurns(t)

	proc := exec.Command("sleep", "30")
	if err := proc.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proc.Process.Kill(); _ = proc.Wait() })
	if err := d.store.SetHeadlessTurn(th.ID, proc.Process.Pid, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := d.reclaimSurvivingHeadlessTurns(); err != nil {
		t.Fatal(err)
	}

	resp := sendResponse(t, postSend(t, d, api.ThreadSendRequest{ID: th.ID, Text: "after the restart"}))
	if resp.Sent != "" || !resp.Deferred {
		t.Fatalf("send to a thread with a surviving turn = %+v, want queued", resp)
	}
	b, _ := json.Marshal(api.ThreadSendRequest{ID: th.ID, Text: "strict"})
	rec := httptest.NewRecorder()
	d.handleThreadSendHeadless(rec, httptest.NewRequest(http.MethodPost, "/v1/threads/send-headless", strings.NewReader(string(b))))
	if rec.Code != http.StatusConflict {
		t.Fatalf("send-headless over a surviving turn = %d, want 409", rec.Code)
	}
	select {
	case p := <-started:
		t.Fatalf("a turn started for %q while the surviving process ran", p)
	case <-time.After(1500 * time.Millisecond):
	}

	// The process exits (reaped here, as init would do in production): the slot
	// frees, the queued message runs.
	_ = proc.Process.Kill()
	_ = proc.Wait()
	awaitTurnStart(t, started, "after the restart")
	hold <- struct{}{}
	awaitIdle(t, d, th.ID)
}

// A turn that fails still leaves its conversation started: the next turn resumes it
// (--resume) and does not create it again (--session-id), which claude refuses for a
// session that already exists. This was the stuck-session bug: the first turn of a
// delegate worker failed after writing its transcript, so the thread never recorded
// the conversation as started, and every later turn was refused.
func TestFailedFirstTurnLeavesTheConversationStarted(t *testing.T) {
	d := newBrowserTestDaemon(t, "self", "")
	th := insertArchiveGateThread(t, d, "failed-first-turn")
	th.HeadlessStarted = false
	var calls []bool
	done := make(chan struct{}, 4)
	prev := headlessTurnRunner
	headlessTurnRunner = func(spec agents.TurnSpec) (string, string, error) {
		calls = append(calls, spec.Started)
		if spec.OnStart != nil {
			spec.OnStart(0)
		}
		done <- struct{}{}
		if len(calls) == 1 {
			return "", "", errors.New("exit status 1: already in use")
		}
		return "ok", spec.SessionID, nil
	}
	t.Cleanup(func() { headlessTurnRunner = prev })

	if status, err := d.startHeadlessTurn(th, "first", "", ""); err != nil || status != http.StatusOK {
		t.Fatalf("first turn = %d %v", status, err)
	}
	<-done
	awaitIdle(t, d, th.ID)
	// The send paths load the thread fresh for each turn; do the same here.
	th, err := d.store.GetThread(th.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !th.HeadlessStarted {
		t.Fatal("the failed first turn left the thread not started")
	}
	if status, err := d.startHeadlessTurn(th, "second", "", ""); err != nil || status != http.StatusOK {
		t.Fatalf("second turn = %d %v", status, err)
	}
	<-done
	awaitIdle(t, d, th.ID)
	if len(calls) != 2 || calls[0] || !calls[1] {
		t.Fatalf("turn Started flags = %v, want [false true]: the second turn must resume the failed first one", calls)
	}
}

// A held pane paste that cannot be delivered keeps its text as an undelivered
// message and flags the thread with a resend hint (schema 77). Before, the text was
// dropped at the deadline and only the flag remained.
func TestHeldPasteFailureKeepsTheText(t *testing.T) {
	d := newBrowserTestDaemon(t, "self", "")
	th := insertArchiveGateThread(t, d, "held-paste")
	seq, err := d.store.InsertHeldPaste(th.ID, "thread send", "the important note", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	gone := func() (string, string, bool, error) { return "", "", false, nil }
	d.deliverHeldPaste(pendingPaste{req: pasteRequest{thread: th, text: "the important note", sender: "thread send", resolve: gone}, seq: seq})

	und, err := d.store.UndeliveredPastes(th.ID)
	if err != nil || len(und) != 1 || und[0].Text != "the important note" {
		t.Fatalf("undelivered = %+v err=%v, want the text kept", und, err)
	}
	got, _ := d.store.GetThread(th.ID)
	if !got.Flagged || !strings.Contains(got.FlagReason, "--resend") {
		t.Fatalf("flag = %v %q, want a flag naming the resend", got.Flagged, got.FlagReason)
	}
}

// Resend refuses while the thread has no live pane and keeps the message where it
// was, so nothing is lost by asking.
func TestResendWithoutAPaneKeepsTheMessage(t *testing.T) {
	d := newBrowserTestDaemon(t, "self", "")
	th := insertArchiveGateThread(t, d, "resend-no-pane")
	seq, _ := d.store.InsertHeldPaste(th.ID, "thread send", "keep me", time.Now())
	if err := d.store.FailHeldPaste(seq, "pane in use"); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(api.ThreadQueueResendRequest{ID: th.ID, Seq: seq})
	rec := httptest.NewRecorder()
	d.handleThreadQueueResend(rec, httptest.NewRequest(http.MethodPost, "/v1/threads/queue/resend", strings.NewReader(string(b))))
	if rec.Code != http.StatusConflict {
		t.Fatalf("resend with no pane = %d %s, want 409", rec.Code, rec.Body.String())
	}
	und, _ := d.store.UndeliveredPastes(th.ID)
	if len(und) != 1 || und[0].Text != "keep me" {
		t.Fatalf("after refused resend undelivered = %+v, want the message kept", und)
	}
}

// A held paste that was still waiting when the daemon restarted is failed with its
// text, and its thread is flagged.
func TestHeldPasteLostAtRestartIsFlagged(t *testing.T) {
	d := newBrowserTestDaemon(t, "self", "")
	th := insertArchiveGateThread(t, d, "restart-held")
	if _, err := d.store.InsertHeldPaste(th.ID, "thread send", "waiting text", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := d.reportSurvivingHeadlessQueues(); err != nil {
		t.Fatal(err)
	}
	und, _ := d.store.UndeliveredPastes(th.ID)
	if len(und) != 1 || und[0].Text != "waiting text" || !strings.Contains(und[0].Reason, "restarted") {
		t.Fatalf("undelivered after restart = %+v", und)
	}
	got, _ := d.store.GetThread(th.ID)
	if !got.Flagged {
		t.Fatal("thread not flagged for a held paste lost at restart")
	}
}
