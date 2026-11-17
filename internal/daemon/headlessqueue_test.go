package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
