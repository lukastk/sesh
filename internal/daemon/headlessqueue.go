package daemon

// The headless message queue (schema 75, lukastk/sesh#26).
//
// A headless thread has no pane, so a message to it is a turn: `thread send` on
// one with no live pane starts a turn at once when the thread is idle, and queues
// the message when a turn is in flight. A queued message is a row in the store's
// headless_queue table (migration 33), so it outlives a daemon restart, and the
// turn that ends drains the queue: it pops the oldest row, claims the turn slot
// and runs that message as the next turn. One turn per message, in send order.
//
// Rules the code below holds:
//   - The row is popped BEFORE its turn is started, so two drains can never both
//     deliver it. A turn that cannot start because another turn is in flight puts
//     the row back with its original seq, which keeps the order.
//   - A delivery that is refused (the thread gained a live pane, is gone, or its
//     conversation cannot resume) is put back too, and the thread is flagged with
//     the undelivered message's sender and reason. The message stays queued and
//     visible in `thread queue`; nothing is dropped silently. Its turn is retried
//     the next time the queue drains, and a later message waits behind it.
//   - A daemon restart does NOT run the survivors. A turn that was in flight when
//     the daemon died may still be running, so starting the next one would fork the
//     conversation. The daemon flags each thread that has survivors instead.
//   - `send-headless` keeps its strict refusal while a turn is in flight, and so
//     does the browser's send_headless action: only `thread send` queues.

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/api"
)

// errHeadlessTurnInFlight is startHeadlessTurn's refusal while the thread's turn
// slot is claimed. It is a 409, and the queue treats it as "wait", not "failed".
var errHeadlessTurnInFlight = errors.New("a turn is already in flight for this thread")

// headlessTurnRunner runs one headless turn. It is a variable only so the queue's
// tests can stand in for the real agent binary; production never reassigns it.
var headlessTurnRunner = agents.HeadlessTurn

// kickOutcome is what a drain did about the message whose seq the caller asked
// about (kickHeadless). seq 0 (a turn-end drain) has no such message.
type kickOutcome int

const (
	kickIdle    kickOutcome = iota // seq 0, or nothing to do
	kickStarted                    // the message's turn started now
	kickQueued                     // the message waits behind a turn in flight
	kickFailed                     // the message could not start; it is flagged and stays queued
)

// headlessSend is the queueing path of `thread send` for a thread with no live
// pane. It is reached only for a thread whose agent keeps a conversation.
func (d *Daemon) headlessSend(w http.ResponseWriter, thread api.Thread, req api.ThreadSendRequest) {
	text, err := d.expandPrompt(req.Text)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	seq, err := d.store.EnqueueHeadless(thread.ID, "thread send", text, time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	switch d.kickHeadless(thread.ID, seq) {
	case kickStarted:
		writeJSON(w, http.StatusOK, api.ThreadSendResponse{Schema: api.SchemaVersion, ID: thread.ID, Sent: thread.ID, QueueSeq: seq})
	case kickQueued:
		if req.OnTyping == api.OnTypingSkip {
			// skip means refuse loudly while busy, never queue: take our message back out.
			if _, derr := d.store.DeleteHeadless(thread.ID, seq); derr != nil {
				writeError(w, http.StatusInternalServerError, derr.Error())
				return
			}
			writeError(w, http.StatusConflict, "thread send: a turn is in flight for this thread — not sent (on_typing=skip)")
			return
		}
		writeJSON(w, http.StatusOK, api.ThreadSendResponse{Schema: api.SchemaVersion, ID: thread.ID,
			Deferred: true, HeldReason: api.HeldReasonTurn, QueueSeq: seq})
	case kickFailed:
		writeError(w, http.StatusConflict, fmt.Sprintf(
			"thread send: message %d is queued but could not start its turn; the thread is flagged with the reason. Fix it, then send again or drop it with `sesh thread queue --id %s --drop %d`",
			seq, thread.ID, seq))
	}
}

// kickHeadless delivers a thread's queued messages as far as it can: it starts the
// oldest queued message's turn when no turn is in flight, and returns. The started
// turn's end calls it again, so a queue drains one turn at a time. seq names the
// message the caller is waiting on (0 for none), and the result says where it ended up.
func (d *Daemon) kickHeadless(threadID string, seq int64) kickOutcome {
	outcome := kickIdle
	if seq != 0 {
		outcome = kickQueued
	}
	for {
		msg, ok, err := d.store.HeadlessQueueHead(threadID)
		if err != nil {
			d.flagUndeliveredHeadless(threadID, "the headless queue could not be read: "+err.Error())
			return outcome
		}
		if !ok {
			return outcome
		}
		if _, err := d.store.DeleteHeadless(threadID, msg.Seq); err != nil {
			d.flagUndeliveredHeadless(threadID, fmt.Sprintf("queued message %d could not be taken off the queue: %v", msg.Seq, err))
			return outcome
		}
		// Put the message back (same seq, same place in line) and say why it stayed.
		restore := func(reason string) {
			if rerr := d.store.RestoreHeadless(msg); rerr != nil {
				d.flagUndeliveredHeadless(threadID, fmt.Sprintf("queued message %d from %s was not put back after %s: %v", msg.Seq, msg.Sender, reason, rerr))
			}
		}
		thread, gerr := d.store.GetThread(threadID)
		if gerr != nil {
			restore("the thread lookup failed: " + gerr.Error())
			d.flagUndeliveredHeadless(threadID, fmt.Sprintf("queued message %d from %s is held: the thread lookup failed: %v", msg.Seq, msg.Sender, gerr))
			if msg.Seq == seq {
				return kickFailed
			}
			return outcome
		}
		status, serr := d.startHeadlessTurn(thread, msg.Text, "", "")
		switch {
		case serr == nil:
			log.Printf("headless: queued message %d from %s started as a turn of %s (%s)", msg.Seq, msg.Sender, threadID, thread.Name)
			if msg.Seq == seq {
				return kickStarted
			}
			return kickQueued
		case errors.Is(serr, errHeadlessTurnInFlight):
			restore("a turn was in flight")
			// The turn in flight ends after it has checked the queue, so the row we
			// just put back would be missed. Re-check the slot ourselves and go again
			// if it is already free; otherwise that turn's end drains the queue.
			if !d.turnInFlight(threadID) {
				continue
			}
			return outcome
		default:
			restore("its turn could not start")
			d.flagUndeliveredHeadless(threadID, fmt.Sprintf("queued message %d from %s was not delivered (%d): %v — it stays queued (see `sesh thread queue --id %s`)",
				msg.Seq, msg.Sender, status, serr, threadID))
			if msg.Seq == seq {
				return kickFailed
			}
			return outcome
		}
	}
}

// flagUndeliveredHeadless is the loud end of a queued delivery that did not happen:
// a log line and an auto-flag carrying the reason, like a held pane paste.
func (d *Daemon) flagUndeliveredHeadless(threadID, reason string) {
	log.Printf("headless: %s: %s", threadID, reason)
	if _, err := d.store.AutoFlag(threadID, "undelivered message: "+reason); err != nil {
		log.Printf("headless: flagging %s: %v", threadID, err)
	}
}

// reportSurvivingHeadlessQueues runs at daemon start. Queues that survived the
// restart stay queued (see the package comment) and each thread is flagged, so
// the messages are visible in the gutter instead of waiting unnoticed.
func (d *Daemon) reportSurvivingHeadlessQueues() error {
	counts, err := d.store.ThreadsWithHeadlessQueue()
	if err != nil {
		return err
	}
	for id, n := range counts {
		reason := fmt.Sprintf("%d queued message(s) survived a daemon restart and were not delivered; see `sesh thread queue --id %s`", n, id)
		log.Printf("headless: %s: %s", id, reason)
		if _, err := d.store.AutoFlag(id, reason); err != nil {
			return fmt.Errorf("flag %s for surviving queued messages: %w", id, err)
		}
	}
	return nil
}

// handleThreadQueue lists a thread's queued messages (GET /v1/threads/queue?id=).
func (d *Daemon) handleThreadQueue(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "thread queue: id is required")
		return
	}
	if _, err := d.store.GetThread(id); err != nil {
		d.threadOpErr(w, err)
		return
	}
	msgs, err := d.store.HeadlessQueue(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := api.ThreadQueueResponse{Schema: api.SchemaVersion, ID: id, Working: d.turnInFlight(id), Messages: []api.QueuedMessage{}}
	for _, m := range msgs {
		out.Messages = append(out.Messages, api.QueuedMessage{Seq: m.Seq, Sender: m.Sender, Text: m.Text, QueuedAtUnix: m.QueuedAtUnix})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleThreadQueueDrop removes one queued message, which is never delivered
// (POST /v1/threads/queue/drop). The operator asked for it, so no flag is raised.
func (d *Daemon) handleThreadQueueDrop(w http.ResponseWriter, r *http.Request) {
	var req api.ThreadQueueDropRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == "" || req.Seq <= 0 {
		writeError(w, http.StatusBadRequest, "thread queue drop: id and a positive seq are required")
		return
	}
	found, err := d.store.DeleteHeadless(req.ID, req.Seq)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, fmt.Sprintf("thread queue drop: no queued message %d for thread %s", req.Seq, req.ID))
		return
	}
	log.Printf("headless: queued message %d of %s dropped by request", req.Seq, req.ID)
	writeJSON(w, http.StatusOK, map[string]any{"schema": api.SchemaVersion, "id": req.ID, "dropped": req.Seq})
}
