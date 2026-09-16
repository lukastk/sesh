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
	Typing       bool // wait mode: budget spent, still typing, nothing queued
	InputAgo     time.Duration
	DeadlineUnix int64
}

// pasteNow is the "skip" policy: one guard read, then paste or refuse loudly.
func (d *Daemon) pasteNow(req pasteRequest) (pasteOutcome, error) {
	target, session, found, err := req.resolve()
	if err != nil {
		return pasteOutcome{}, err
	}
	if !found {
		return pasteOutcome{}, errors.New("thread has no live pane (dead); cannot send")
	}
	quiet, ago, err := d.paneQuiet(session, req.guard, time.Now())
	if err != nil {
		return pasteOutcome{}, err
	}
	if !quiet {
		return pasteOutcome{InputAgo: ago}, errTyping{ago: ago}
	}
	if err := d.tmux.SendText(target, req.text, true); err != nil {
		return pasteOutcome{}, err
	}
	d.pasteDelivered.Add(1)
	return pasteOutcome{Sent: true, InputAgo: ago}, nil
}

// pasteDefer is the "defer" policy: paste now if quiet, else queue it.
func (d *Daemon) pasteDefer(req pasteRequest) (pasteOutcome, error) {
	out, err := d.pasteNow(req)
	var typing errTyping
	if !errors.As(err, &typing) {
		return out, err
	}
	deadline := time.Now().Add(req.guard.Deadline)
	d.enqueuePaste(req, deadline)
	d.pasteHeld.Add(1)
	return pasteOutcome{Deferred: true, InputAgo: typing.ago, DeadlineUnix: deadline.Unix()}, nil
}

// pasteWait is the "wait" policy: block up to budget for a quiet pane; a pane
// still in use at the end returns Typing=true with nothing queued.
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
}

// pasteQueue is one thread's FIFO of held deliveries. A single drainer per
// thread keeps deliveries in the order they were asked for; it exits when the
// queue empties and a later enqueue starts a fresh one.
type pasteQueue struct {
	items    []pendingPaste
	draining bool
	inFlight bool // the drainer holds one popped item it is still polling
}

func (d *Daemon) enqueuePaste(req pasteRequest, deadline time.Time) {
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
	q.items = append(q.items, pendingPaste{req: req, queuedAt: time.Now(), deadline: deadline})
	log.Printf("send: %s → %s (%s) held — pane in use; delivering when quiet for %s, deadline %s",
		req.sender, req.thread.ID, req.thread.Name, req.guard.Window, deadline.Format("15:04:05"))
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
func (d *Daemon) deliverHeldPaste(item pendingPaste) {
	req := item.req
	for {
		target, session, found, err := req.resolve()
		if err != nil || !found {
			reason := "the pane died before the message could be delivered"
			if err != nil {
				reason = err.Error()
			}
			d.failHeldPaste(req, reason)
			return
		}
		now := time.Now()
		quiet, ago, qerr := d.paneQuiet(session, req.guard, now)
		if qerr != nil {
			d.failHeldPaste(req, qerr.Error())
			return
		}
		if quiet {
			if serr := d.tmux.SendText(target, req.text, true); serr != nil {
				d.failHeldPaste(req, serr.Error())
				return
			}
			d.pasteDelivered.Add(1)
			log.Printf("send: %s → %s (%s) delivered after a %s hold", req.sender, req.thread.ID, req.thread.Name,
				now.Sub(item.queuedAt).Round(time.Second))
			return
		}
		if !now.Before(item.deadline) {
			d.failHeldPaste(req, fmt.Sprintf("pane in use for %s (last input %s ago)", req.guard.Deadline, ago.Round(time.Second)))
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

// failHeldPaste is the loud end of a held delivery: a log line AND an auto-flag
// on the target carrying the reason, so the undelivered message is visible in
// the gutter rather than lost.
func (d *Daemon) failHeldPaste(req pasteRequest, why string) {
	d.pasteFlagged.Add(1)
	reason := fmt.Sprintf("undelivered message from %s: %s", req.sender, why)
	log.Printf("send: %s → %s (%s) FAILED: %s", req.sender, req.thread.ID, req.thread.Name, why)
	flagged, err := d.store.AutoFlag(req.thread.ID, reason)
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
