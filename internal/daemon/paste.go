package daemon

// The typing guard on every delivery into a live pane (_dev/SCHEDULING.md
// §6.5.1). tmux.Server.SendText is `paste-buffer` then Enter, so text delivered
// while a human is mid-line in that pane is appended to their half-typed prompt
// and SUBMITTED with it — a supervisor's child threads reporting into the pane
// its user is typing in was the live case. The signal is the session's newest
// client_activity (the last INPUT from an attached client — H48; output, redraws
// and switch-client do not bump it), read live from list-clients because this
// guard is about the last few seconds, not the maintainer's ≤300 ms-stale row.
//
// Every thread-level paste (thread send, ticket send-prompt, subscription
// delivery, and later the scheduler) goes through pasteInto below; sendWhenReady
// (a first prompt into a pane created milliseconds ago) and the raw tmux
// send-text primitive (pane-level, not thread-level) deliberately do not.
//
// Three policies, chosen by the caller:
//   - defer: paste now if quiet, else QUEUE the delivery (per-thread FIFO) — the
//     daemon pastes it when the pane has been quiet for the window, or, at the
//     deadline, FAILS it loudly and auto-flags the thread rather than pasting
//     anyway (that would be the exact collision, silently). Fire-and-forget
//     senders — agents, subscriptions, keybindings — must never lose a message
//     silently, and must not block (an agent's Bash tool times out).
//   - wait: block up to a server-capped budget for a quiet pane, then paste; a
//     still-typing pane returns typing=true and queues NOTHING, so a caller that
//     wants to block loops (the CLI's --wait).
//   - skip: a typing pane is a loud refusal, nothing queued — for periodic
//     senders that have a next occurrence anyway.
//
// The blocked guard comes first and has no bypass: a terminal agent the hooks
// report as BLOCKED (claude's PreToolUse for AskUserQuestion, a permission
// prompt) has a dialog open, and paste-then-Enter would land in that dialog —
// selecting its highlighted option — instead of reaching the conversation.
// defer holds such a delivery until the block clears (unblocked or turn_ended)
// and the pane is quiet, and time spent blocked does not count against the
// typing deadline (a dialog can sit open for hours); wait and skip refuse it
// loudly. respect_typing=0 turns off only the typing guard, never this one.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/lukastk/sesh/internal/api"
)

const (
	// pasteQuietPoll is how often a held delivery re-reads client activity.
	pasteQuietPoll = 2 * time.Second
	// pasteWaitBudget caps a "wait"-mode call server-side, under the client's
	// hard 15 s HTTP timeout (the same reason /v1/threads/wait is capped).
	pasteWaitBudget = 8 * time.Second
)

// pasteGuard is the resolved typing policy for one delivery.
type pasteGuard struct {
	Window   time.Duration // quiet time required; 0 = guard off
	Deadline time.Duration // how long a deferred delivery waits before failing + flagging
}

// pasteGuardFor resolves per-request overrides (nil = the [send] config) into a guard.
func (d *Daemon) pasteGuardFor(respectMs, deadlineMs *int) pasteGuard {
	g := pasteGuard{Window: d.send.RespectTyping, Deadline: d.send.RespectTypingDeadline}
	if respectMs != nil {
		g.Window = time.Duration(*respectMs) * time.Millisecond
	}
	if deadlineMs != nil && *deadlineMs > 0 {
		g.Deadline = time.Duration(*deadlineMs) * time.Millisecond
	}
	return g
}

// errTyping is the loud refusal of a "skip"-mode paste into an in-use pane.
type errTyping struct{ ago time.Duration }

func (e errTyping) Error() string {
	return fmt.Sprintf("pane is in use: a viewer typed %s ago", e.ago.Round(time.Second))
}

// errBlocked is the refusal of a paste into a thread whose agent is blocked on
// a dialog (the reported authority state). It is never bypassed.
type errBlocked struct{ reason string }

func (e errBlocked) Error() string {
	if e.reason == "" {
		return "thread is blocked on a dialog; a paste would answer it instead of reaching the conversation"
	}
	return fmt.Sprintf("thread is blocked on a dialog (%s); a paste would answer it instead of reaching the conversation", e.reason)
}

// threadBlocked reports the hooks' blocked state for a thread on this daemon.
func (d *Daemon) threadBlocked(id string) (bool, string) {
	st, ok := d.reportedState(id)
	if !ok || !st.blocked {
		return false, ""
	}
	return true, st.blockedReason
}

// Held-delivery reasons, as the send responses report them.
const (
	heldTyping  = "typing"
	heldBlocked = "blocked"
)

// paneInputAgo reports how long ago the session last saw input from an attached
// client. attached=false means nobody is viewing it (quiet by definition).
func (d *Daemon) paneInputAgo(session string, now time.Time) (ago time.Duration, attached bool, err error) {
	sessions, err := d.tmux.AttachedSessions()
	if err != nil {
		return 0, false, err
	}
	act, ok := sessions[session]
	if !ok {
		return 0, false, nil
	}
	return now.Sub(time.Unix(act, 0)), true, nil
}

// paneQuiet is the guard predicate: quiet unless attached with input inside the window.
func (d *Daemon) paneQuiet(session string, g pasteGuard, now time.Time) (quiet bool, ago time.Duration, err error) {
	if g.Window <= 0 {
		return true, 0, nil
	}
	ago, attached, err := d.paneInputAgo(session, now)
	if err != nil {
		return false, 0, err
	}
	if !attached || ago >= g.Window {
		return true, ago, nil
	}
	return false, ago, nil
}

// pasteRequest is one delivery into a thread's pane. resolve re-resolves the
// target at delivery time (a deferred delivery may outlive a stop+revive of the
// pane it was first aimed at) and reports whether a live target still exists.
type pasteRequest struct {
	thread  api.Thread
	text    string
	guard   pasteGuard
	sender  string // who is delivering, for logs and the flag reason
	resolve func() (target, session string, found bool, err error)
}

// pasteOutcome is what a paste attempt decided.
type pasteOutcome struct {
	Sent         bool
	Deferred     bool
	Typing       bool   // wait mode: budget spent, still typing, nothing queued
	HeldReason   string // with Deferred: heldTyping or heldBlocked
	InputAgo     time.Duration
	DeadlineUnix int64
}

// pasteNow is the "skip" policy: one guard read, then paste or refuse loudly.
func (d *Daemon) pasteNow(req pasteRequest) (pasteOutcome, error) {
	// Resolution and the guard read both happen before any write, so their
	// failures are typed refusals: nothing was delivered.
	target, session, found, err := req.resolve()
	if err != nil {
		return pasteOutcome{}, deliveryRefused{err}
	}
	if !found {
		return pasteOutcome{}, deliveryRefused{errors.New("thread has no live pane (dead); cannot send")}
	}
	// Pi uses no editor input: the terminal typing guard is inapplicable. Same
	// for mypi, which speaks the same RPC steering protocol.
	if piFamily(req.thread.AgentKind) {
		if err := d.deliverHeaded(req.thread, target, req.text); err != nil {
			return pasteOutcome{}, err
		}
		d.pasteDelivered.Add(1)
		return pasteOutcome{Sent: true}, nil
	}
	if blocked, why := d.threadBlocked(req.thread.ID); blocked {
		return pasteOutcome{}, errBlocked{reason: why}
	}
	quiet, ago, err := d.paneQuiet(session, req.guard, time.Now())
	if err != nil {
		return pasteOutcome{}, deliveryRefused{err}
	}
	if !quiet {
		return pasteOutcome{InputAgo: ago}, errTyping{ago: ago}
	}
	if err := d.deliverHeaded(req.thread, target, req.text); err != nil {
		return pasteOutcome{}, err
	}
	d.pasteDelivered.Add(1)
	return pasteOutcome{Sent: true, InputAgo: ago}, nil
}

// pasteDefer is the "defer" policy: paste now if quiet and unblocked, else queue it.
func (d *Daemon) pasteDefer(req pasteRequest) (pasteOutcome, error) {
	out, err := d.pasteNow(req)
	var typing errTyping
	var blocked errBlocked
	reason := heldTyping
	switch {
	case errors.As(err, &typing):
	case errors.As(err, &blocked):
		reason = heldBlocked
	default:
		return out, err
	}
	deadline := time.Now().Add(req.guard.Deadline)
	// The text is recorded before the hold, so a deadline or a restart cannot discard
	// it: a failed hold keeps its row (state failed) until it is resent or dropped.
	seq, err := d.store.InsertHeldPaste(req.thread.ID, req.sender, req.text, time.Now())
	if err != nil {
		return pasteOutcome{}, deliveryRefused{fmt.Errorf("hold the message (not sent): %w", err)}
	}
	d.enqueuePaste(req, deadline, reason, seq)
	d.pasteHeld.Add(1)
	return pasteOutcome{Deferred: true, HeldReason: reason, InputAgo: typing.ago, DeadlineUnix: deadline.Unix()}, nil
}

// pasteWait is the "wait" policy: block up to budget for a quiet pane; a pane
// still in use at the end returns Typing=true with nothing queued. A blocked
// thread is refused at once (errBlocked from pasteNow): a dialog outlasts any
// wait budget, and looping on it would only hide the refusal.
func (d *Daemon) pasteWait(ctx context.Context, req pasteRequest, budget time.Duration) (pasteOutcome, error) {
	if budget <= 0 || budget > pasteWaitBudget {
		budget = pasteWaitBudget
	}
	until := time.Now().Add(budget)
	for {
		out, err := d.pasteNow(req)
		var typing errTyping
		if !errors.As(err, &typing) {
			return out, err
		}
		remaining := time.Until(until)
		if remaining <= 0 {
			return pasteOutcome{Typing: true, InputAgo: typing.ago}, nil
		}
		wait := min(remaining, pasteQuietPoll)
		select {
		case <-ctx.Done():
			return pasteOutcome{Typing: true, InputAgo: typing.ago}, nil
		case <-time.After(wait):
		}
	}
}

// --- the deferred-delivery queue ---

// pendingPaste is a queued delivery.
type pendingPaste struct {
	req      pasteRequest
	queuedAt time.Time
	deadline time.Time
	seq      int64 // the held_pastes row that holds this text (migration 35)
}

// pasteQueue is one thread's FIFO of held deliveries. A single drainer per
// thread keeps deliveries in the order they were asked for; it exits when the
// queue empties and a later enqueue starts a fresh one.
type pasteQueue struct {
	items    []pendingPaste
	draining bool
	inFlight bool // the drainer holds one popped item it is still polling
}

func (d *Daemon) enqueuePaste(req pasteRequest, deadline time.Time, reason string, seq int64) {
	d.pasteMu.Lock()
	defer d.pasteMu.Unlock()
	if d.pasteQueues == nil {
		d.pasteQueues = map[string]*pasteQueue{}
	}
	q := d.pasteQueues[req.thread.ID]
	if q == nil {
		q = &pasteQueue{}
		d.pasteQueues[req.thread.ID] = q
	}
	q.items = append(q.items, pendingPaste{req: req, queuedAt: time.Now(), deadline: deadline, seq: seq})
	if reason == heldBlocked {
		log.Printf("send: %s → %s (%s) held — the thread is blocked on a dialog; delivering once it unblocks",
			req.sender, req.thread.ID, req.thread.Name)
	} else {
		log.Printf("send: %s → %s (%s) held — pane in use; delivering when quiet for %s, deadline %s",
			req.sender, req.thread.ID, req.thread.Name, req.guard.Window, deadline.Format("15:04:05"))
	}
	if !q.draining {
		q.draining = true
		go d.drainPastes(req.thread.ID)
	}
}

// pendingPastes is the number of held deliveries for a thread (tests/doctor).
func (d *Daemon) pendingPastes(threadID string) int {
	d.pasteMu.Lock()
	defer d.pasteMu.Unlock()
	if q := d.pasteQueues[threadID]; q != nil {
		n := len(q.items)
		if q.inFlight {
			n++
		}
		return n
	}
	return 0
}

// drainPastes delivers a thread's held pastes in order: each waits for the quiet
// window (or its deadline), re-resolving the pane each poll.
func (d *Daemon) drainPastes(threadID string) {
	for {
		d.pasteMu.Lock()
		q := d.pasteQueues[threadID]
		if q == nil || len(q.items) == 0 {
			if q != nil {
				q.draining = false
				delete(d.pasteQueues, threadID)
			}
			d.pasteMu.Unlock()
			return
		}
		item := q.items[0]
		q.items = q.items[1:]
		q.inFlight = true
		d.pasteMu.Unlock()
		d.deliverHeldPaste(item)
		d.pasteMu.Lock()
		q.inFlight = false
		d.pasteMu.Unlock()
	}
}

// deliverHeldPaste polls one held delivery to its end: pasted, or failed + flagged.
// While the thread is blocked the typing deadline does not run: it restarts
// from each blocked poll, so the full typing window applies after the block.
func (d *Daemon) deliverHeldPaste(item pendingPaste) {
	req := item.req
	for {
		target, session, found, err := req.resolve()
		if err != nil || !found {
			reason := "the pane died before the message could be delivered"
			if err != nil {
				reason = err.Error()
			}
			d.failHeldPaste(item, reason)
			return
		}
		now := time.Now()
		if blocked, _ := d.threadBlocked(req.thread.ID); blocked {
			item.deadline = now.Add(req.guard.Deadline)
			select {
			case <-d.pasteStop:
				log.Printf("send: %s → %s (%s) DROPPED at shutdown — the held delivery does not survive a restart", req.sender, req.thread.ID, req.thread.Name)
				return
			case <-time.After(pasteQuietPoll):
			}
			continue
		}
		quiet, ago, qerr := d.paneQuiet(session, req.guard, now)
		if qerr != nil {
			d.failHeldPaste(item, qerr.Error())
			return
		}
		if quiet {
			if serr := d.deliverHeaded(req.thread, target, req.text); serr != nil {
				d.failHeldPaste(item, serr.Error())
				return
			}
			d.pasteDelivered.Add(1)
			if derr := d.store.DeleteHeldPaste(item.seq); derr != nil {
				log.Printf("send: %s → %s: delivered, but its held record was not cleared: %v", req.sender, req.thread.ID, derr)
			}
			log.Printf("send: %s → %s (%s) delivered after a %s hold", req.sender, req.thread.ID, req.thread.Name,
				now.Sub(item.queuedAt).Round(time.Second))
			return
		}
		if !now.Before(item.deadline) {
			d.failHeldPaste(item, fmt.Sprintf("pane in use for %s (last input %s ago)", req.guard.Deadline, ago.Round(time.Second)))
			return
		}
		select {
		case <-d.pasteStop:
			log.Printf("send: %s → %s (%s) DROPPED at shutdown — the held delivery does not survive a restart", req.sender, req.thread.ID, req.thread.Name)
			return
		case <-time.After(min(pasteQuietPoll, time.Until(item.deadline))):
		}
	}
}

// failHeldPaste is the loud end of a held delivery: the text stays in its
// held_pastes row (state failed, with the reason) so it can be resent, a log line,
// AND an auto-flag on the target carrying the reason, so the undelivered message is
// visible in the gutter rather than lost.
func (d *Daemon) failHeldPaste(item pendingPaste, why string) {
	req := item.req
	d.pasteFlagged.Add(1)
	reason := fmt.Sprintf("undelivered message from %s: %s", req.sender, why)
	log.Printf("send: %s → %s (%s) FAILED: %s", req.sender, req.thread.ID, req.thread.Name, why)
	if err := d.store.FailHeldPaste(item.seq, reason); err != nil {
		log.Printf("send: keeping the text of %s's undelivered message: %v", req.thread.ID, err)
	}
	flagged, err := d.store.AutoFlag(req.thread.ID, reason+fmt.Sprintf(" (text kept: sesh thread queue --id %s --resend %d)", req.thread.ID, item.seq))
	switch {
	case err != nil:
		log.Printf("send: flagging %s after the failed delivery: %v", req.thread.ID, err)
	case !flagged:
		log.Printf("send: %s not flagged for the failed delivery (already flagged, or auto-flag disabled) — reason: %s", req.thread.ID, reason)
	}
}

// stopPastes wakes every held delivery at shutdown so they log their loss.
func (d *Daemon) stopPastes() {
	d.pasteStopOnce.Do(func() {
		if d.pasteStop != nil { // a bare test daemon never made one
			close(d.pasteStop)
		}
	})
}

// pasteQueueStats is a test/doctor observable.
func (d *Daemon) pasteQueueStats() (held, delivered, flagged int64) {
	return d.pasteHeld.Load(), d.pasteDelivered.Load(), d.pasteFlagged.Load()
}
