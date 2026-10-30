package daemon

// The `send` and `send_headless` browser actions (capability browser.send-v1).
//
// They reuse the EXISTING per-kind delivery paths and add nothing of their own:
//
//   - pi/mypi, live pane → literal RPC steering through deliverHeaded (the
//     typing guard is inapplicable: pi takes no editor input). A delivery error
//     after the socket is open is reported `uncertain`, never retried, and never
//     followed by a terminal paste — PI_DELIVERY's rule, verbatim.
//   - claude/codex, live pane → the guarded terminal paste. The typing guard is
//     NOT bypassable from the browser: the only choice is defer (queue it, the
//     default) or skip (refuse loudly). There is no "force" value.
//   - send_headless → startHeadlessTurn on an idle thread; a live pane or an
//     in-flight turn is a loud 409.
//
// `sent` means the delivery left Sesh. It is NOT a claim that the agent started
// or finished a turn — the caller still watches live-v1 / the display projection
// / headless-reply for that.
//
// Every call carries a caller-minted request_id. A replay inside the owner's
// bounded recovery window returns the RECORDED outcome instead of delivering a
// second time, which is what makes a browser retry after a dropped connection
// safe. The window is bounded and in-memory: a replay after eviction is a NEW
// delivery, and the response says so (`replayed:false`).

import (
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/store"
)

const (
	// browserSendMaxText bounds one delivered message. The browser request body
	// is already capped at 1 MiB; this is the explicit per-message limit.
	browserSendMaxText = 256 << 10
	// browserSendRecoveryCapacity / TTL bound the idempotency registry. Both are
	// deliberately generous: an evicted request_id means a retry delivers again.
	browserSendRecoveryCapacity = 1024
	browserSendRecoveryTTL      = 30 * time.Minute
)

// browserSendRecord is one recorded delivery attempt. done is closed when the
// attempt finishes, so a concurrent replay of the same request_id waits for the
// first attempt's outcome instead of racing a second delivery.
type browserSendRecord struct {
	threadID string
	action   string
	text     string
	at       time.Time
	done     chan struct{}
	resp     api.BrowserSendResponse
	status   int
}

type browserSendRegistry struct {
	mu      sync.Mutex
	records map[string]*browserSendRecord
}

func newBrowserSendRegistry() *browserSendRegistry {
	return &browserSendRegistry{records: map[string]*browserSendRecord{}}
}

// reserve claims requestID for a new attempt, or returns the existing record to
// wait on / read. A request_id reused for a DIFFERENT thread, action or text is
// a loud conflict: silently answering with the first attempt's outcome would
// report a delivery that never happened for this message.
func (r *browserSendRegistry) reserve(requestID, threadID, action, text string, now time.Time) (*browserSendRecord, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, rec := range r.records {
		if now.Sub(rec.at) > browserSendRecoveryTTL && isClosed(rec.done) {
			delete(r.records, key)
		}
	}
	if rec, ok := r.records[requestID]; ok {
		if rec.threadID != threadID || rec.action != action || rec.text != text {
			return nil, false, fmt.Errorf("request_id %s was already used for a different %s delivery on thread %s", requestID, rec.action, rec.threadID)
		}
		return rec, false, nil
	}
	if len(r.records) >= browserSendRecoveryCapacity {
		return nil, false, fmt.Errorf("browser send recovery registry is full; retry once in-flight deliveries settle")
	}
	rec := &browserSendRecord{threadID: threadID, action: action, text: text, at: now, done: make(chan struct{})}
	r.records[requestID] = rec
	return rec, true, nil
}

// complete records an attempt's outcome and releases every waiter.
func (r *browserSendRegistry) complete(rec *browserSendRecord, status int, resp api.BrowserSendResponse) {
	r.mu.Lock()
	rec.status, rec.resp = status, resp
	r.mu.Unlock()
	close(rec.done)
}

// result reads a settled record's recorded outcome.
func (r *browserSendRegistry) result(rec *browserSendRecord) (int, api.BrowserSendResponse) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return rec.status, rec.resp
}

// abandon drops a reservation whose attempt never ran (a validation refusal), so
// the request_id is not burned by a message that was never delivered.
func (r *browserSendRegistry) abandon(requestID string, rec *browserSendRecord) {
	r.mu.Lock()
	if current, ok := r.records[requestID]; ok && current == rec {
		delete(r.records, requestID)
	}
	r.mu.Unlock()
	close(rec.done)
}

func isClosed(done chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

// browserSendRoute validates a send action's own fields. Lifecycle fields belong
// to the other verbs and are refused here, exactly as they are there.
func browserSendRoute(req api.BrowserThreadActionRequest) (text, onTyping string, err error) {
	if req.Name != nil || req.Parent != nil || req.Archived != nil || req.Force != nil || len(req.Add)+len(req.Remove) != 0 {
		return "", "", fmt.Errorf("browser action %s accepts only text, request_id, on_typing and model", req.Action)
	}
	if req.Text == nil || *req.Text == "" {
		return "", "", fmt.Errorf("browser action %s requires non-empty text", req.Action)
	}
	if len(*req.Text) > browserSendMaxText {
		return "", "", fmt.Errorf("browser action %s text exceeds %d bytes", req.Action, browserSendMaxText)
	}
	parsed, perr := uuid.Parse(req.RequestID)
	if perr != nil || parsed.String() != req.RequestID {
		return "", "", fmt.Errorf("browser action %s requires request_id to be a canonical UUID", req.Action)
	}
	policy := req.OnTyping
	switch policy {
	case "":
		policy = api.OnTypingDefer
	case api.OnTypingDefer, api.OnTypingSkip:
	default:
		// `wait` is deliberately not offered: it would hold a browser request —
		// and, for a remote owner, a hub→peer request — open for the guard's
		// whole budget. Defer queues; skip refuses loudly.
		return "", "", fmt.Errorf("browser action %s on_typing must be %q or %q", req.Action, api.OnTypingDefer, api.OnTypingSkip)
	}
	if req.Action == "send_headless" {
		if req.OnTyping != "" {
			return "", "", fmt.Errorf("browser action send_headless has no pane to guard; on_typing is not accepted")
		}
		policy = ""
	}
	return *req.Text, policy, nil
}

// serveBrowserSend is the OWNER-side send: the hub forwards the unchanged action
// body to the owner's own /v1/browser/threads/action, so this only ever runs on
// the daemon that owns the thread and holds its recovery registry.
func (d *Daemon) serveBrowserSend(w http.ResponseWriter, r *http.Request, req api.BrowserThreadActionRequest, owner string) {
	text, onTyping, err := browserSendRoute(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rec, fresh, err := d.browserSends.reserve(req.RequestID, req.ThreadID, req.Action, text, time.Now())
	if err != nil {
		writeError(w, http.StatusConflict, "browser send: "+err.Error())
		return
	}
	if !fresh {
		// A replay: wait for the first attempt to settle (its delivery is already
		// bounded) and answer with its recorded outcome. Never deliver twice.
		select {
		case <-rec.done:
		case <-r.Context().Done():
			writeError(w, http.StatusRequestTimeout, "browser send: the original attempt for this request_id is still in flight")
			return
		}
		status, resp := d.browserSends.result(rec)
		if status == 0 {
			writeError(w, http.StatusConflict, "browser send: this request_id was refused before delivery; mint a new one")
			return
		}
		resp.Replayed = true
		writeJSON(w, status, resp)
		return
	}

	thread, err := d.store.GetThread(req.ThreadID)
	if err != nil {
		d.browserSends.abandon(req.RequestID, rec)
		if errors.Is(err, store.ErrThreadNotFound) {
			writeError(w, http.StatusNotFound, "thread not found: "+req.ThreadID)
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !api.HasRuntime(thread.AgentKind) || !api.HasConversation(thread.AgentKind) {
		d.browserSends.abandon(req.RequestID, rec)
		writeError(w, http.StatusConflict, nodeRefusal(thread, "browser "+req.Action))
		return
	}

	status, resp := d.browserSendAttempt(r, thread, req, text, onTyping, owner)
	d.browserSends.complete(rec, status, resp)
	writeJSON(w, status, resp)
}

// browserSendAttempt performs one delivery and maps it onto the explicit outcome
// vocabulary. Every branch names an outcome; there is no implicit success.
func (d *Daemon) browserSendAttempt(r *http.Request, thread api.Thread, req api.BrowserThreadActionRequest, text, onTyping, owner string) (int, api.BrowserSendResponse) {
	resp := api.BrowserSendResponse{
		Schema: api.SchemaVersion, ThreadID: thread.ID, OwnerMachine: owner,
		Action: req.Action, RequestID: req.RequestID,
	}
	answer := func(status int, outcome, detail string) (int, api.BrowserSendResponse) {
		resp.Outcome, resp.Detail = outcome, detail
		return status, resp
	}

	if req.Action == "send_headless" {
		status, err := d.startHeadlessTurn(thread, text, "", req.Model)
		if err != nil {
			// A headless turn either started or it did not: the decision is
			// synchronous, so `uncertain` never applies here. A live pane or an
			// in-flight turn is refused_busy; every other refusal (no resumable
			// conversation, a failed inbox or mode resolution) is refused_dead
			// with the daemon's own reason, and nothing was delivered.
			if status == http.StatusConflict {
				return answer(status, api.BrowserSendRefusedBusy, err.Error())
			}
			return answer(status, api.BrowserSendRefusedDead, err.Error())
		}
		return answer(http.StatusOK, api.BrowserSendSent, "headless turn started; completion is observed separately")
	}

	resolve := func() (string, string, bool, error) {
		loc, found, ferr := d.tmux.FindPaneByThreadID(thread.ID)
		if ferr != nil || !found {
			return "", "", false, ferr
		}
		return loc.Pane, loc.Session, true, nil
	}
	if _, _, found, ferr := resolve(); ferr != nil {
		return answer(http.StatusInternalServerError, api.BrowserSendUncertain, ferr.Error())
	} else if !found {
		return answer(http.StatusConflict, api.BrowserSendRefusedDead,
			"thread has no live pane; resume it, or use send_headless for a turn without one")
	}

	preq := pasteRequest{
		thread: thread,
		// Verbatim: the browser send does not expand @blob() references. Browser
		// attachments go through the bounded transfer subsystem, which must stay
		// the only browser path that names owner-side files.
		text:    text,
		guard:   d.pasteGuardFor(nil, nil),
		sender:  "browser send",
		resolve: resolve,
	}
	out, err := d.pasteByPolicy(r.Context(), preq, onTyping, 0)
	if err != nil {
		var typing errTyping
		if errors.As(err, &typing) {
			resp.InputAgoSec = int64(typing.ago / time.Second)
			return answer(http.StatusConflict, api.BrowserSendSkippedTyping, err.Error())
		}
		return answer(browserSendFailureStatus(err), browserSendFailureOutcome(err), err.Error())
	}
	resp.InputAgoSec = int64(out.InputAgo / time.Second)
	resp.DeadlineUnix = out.DeadlineUnix
	switch {
	case out.Sent:
		return answer(http.StatusOK, api.BrowserSendSent, "delivered to the live pane; the agent's turn is observed separately")
	case out.Deferred:
		return answer(http.StatusOK, api.BrowserSendDeferred,
			"pane in use; queued and delivered when it is quiet, or failed and flagged at the deadline")
	default:
		return answer(http.StatusConflict, api.BrowserSendSkippedTyping, "pane in use; nothing was delivered")
	}
}

// browserSendFailureOutcome classifies a delivery error by TYPE (see
// deliveryerr.go), never by its message text: rewording an error a human reads
// must not be able to change a machine-readable outcome a caller acts on.
//
// Only a typed deliveryRefused — a site with positive evidence that nothing was
// written — becomes `refused_dead`. deliveryUncertain and every unclassified
// error become `uncertain`, which is the safe direction: PI_DELIVERY forbids
// retrying an uncertain submission, and inventing a clean refusal would invite a
// resend of a message the agent may already be answering.
func browserSendFailureOutcome(err error) string {
	var refused deliveryRefused
	if errors.As(err, &refused) {
		return api.BrowserSendRefusedDead
	}
	return api.BrowserSendUncertain
}

func browserSendFailureStatus(err error) int {
	if browserSendFailureOutcome(err) == api.BrowserSendRefusedDead {
		return http.StatusConflict
	}
	return http.StatusBadGateway
}
