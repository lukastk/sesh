package daemon

// Idle stop (schema 66, _dev/IDLE_STOP.md): the owning daemon stops its own
// headful agent threads once they have been idle for longer than their limit
// — the machine's [idle_stop] after, or the thread's own idle_stop override.
// "Stop" is exactly `thread stop` for an agent thread: the pane dies, the
// record (flags, archive state, tags, holds, parentage) is untouched, and
// `thread resume` brings the conversation back. Nothing crosses the mesh: each
// daemon decides for the threads it owns, under its own config.
//
// The idle clock is the maintainer's published state: LastActiveUnix (the last
// pane change, or a tick of reported busy — including background work, issue
// #24) and AttachedActivityUnix (the last INPUT from an attached client; mere
// attachment is not activity, because cockpit clients stay attached). Both are
// runtime state, so a daemon restart restarts every clock: that can only DELAY
// a stop, never cause an early one.
//
// A third input closes the gap those leave: the maintainer counts its FIRST
// capture of a pane as a baseline, not a change, so a pane that has not changed
// since the daemon first saw it (an agent resting at its prompt since launch,
// or every pane after a daemon restart) publishes no LastActiveUnix at all. The
// sweep therefore remembers when it first saw each thread live (firstSeen) and
// counts that as activity. It is kept here rather than in LastActiveUnix, which
// other readers (the browser's row "updated" time) show to the user.

import (
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/lukastk/sesh/internal/api"
)

// idleStopVerdict decides one thread. Pure: the truth table lives in
// idlestop_test.go. snap/haveSnap are the maintainer's published state for the
// thread. It returns the limit that applies (0 = none) and how long the thread
// has been idle (0 when not measured).
func idleStopVerdict(th api.Thread, snap api.ThreadSnapshot, haveSnap bool, firstSeen time.Time, machineAfter time.Duration, now time.Time) (stop bool, reason string, limit, idleFor time.Duration) {
	if !api.HasConversation(th.AgentKind) {
		// A shell thread's busy is pinned idle (a long build would look idle) and
		// stopping one kills its whole session, including any agent panes inside
		// it; virtual nodes and dividers have no runtime at all.
		return false, "not an agent thread (" + th.AgentKind + ")", 0, 0
	}
	never, override, err := api.ParseIdleStopOverride(th.IdleStop)
	if err != nil {
		// The daemon validates every write, so this is a corrupt row or a peer
		// that wrote something newer. Keep the thread and say so.
		return false, "invalid idle_stop override: " + err.Error(), 0, 0
	}
	if never {
		return false, "idle_stop = never", 0, 0
	}
	limit = machineAfter
	if override > 0 {
		limit = override
	}
	if limit <= 0 {
		return false, "no idle limit ([idle_stop] after unset and no override)", 0, 0
	}
	if !haveSnap {
		return false, "not observed yet", limit, 0
	}
	if snap.Head != api.Headful {
		return false, "headless (nothing to stop)", limit, 0
	}
	if snap.Busy == api.BusyBusy {
		return false, "busy", limit, 0
	}
	if snap.Background != nil && len(snap.Background.Wakeups) > 0 {
		// In-session wake-ups live inside the agent process: stopping it would
		// silently cancel them.
		return false, fmt.Sprintf("%d pending in-session wake-up(s)", len(snap.Background.Wakeups)), limit, 0
	}
	last := snap.LastActiveUnix
	if snap.AttachedActivityUnix > last {
		last = snap.AttachedActivityUnix
	}
	if !firstSeen.IsZero() && firstSeen.Unix() > last {
		last = firstSeen.Unix()
	}
	if last <= 0 {
		return false, "no activity reading yet", limit, 0
	}
	idleFor = now.Sub(time.Unix(last, 0))
	if idleFor < limit {
		return false, fmt.Sprintf("idle %s of %s", roundIdle(idleFor), limit), limit, idleFor
	}
	return true, fmt.Sprintf("idle %s (limit %s)", roundIdle(idleFor), limit), limit, idleFor
}

func roundIdle(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	if d < time.Minute {
		return d.Round(time.Second)
	}
	return d.Round(time.Minute)
}

// idleStopInterval is how often the sweep runs: a quarter of the machine
// limit, between 1s and 30s, so a stop lands at most ~25% (or 30s) after the
// limit. 30s when the machine has no limit (per-thread overrides only).
func idleStopInterval(after time.Duration) time.Duration {
	if after <= 0 {
		return 30 * time.Second
	}
	iv := after / 4
	if iv < time.Second {
		iv = time.Second
	}
	if iv > 30*time.Second {
		iv = 30 * time.Second
	}
	return iv
}

// idleSeen is the sweep's record of when it first saw each thread live (see the
// file comment). Entries are dropped when a thread is no longer headful, so a
// revived thread's clock starts at its revival.
type idleSeen struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

// observe returns when the thread was first seen live, recording now if it is
// new; a thread that is not live is forgotten (and returns zero).
func (s *idleSeen) observe(id string, live bool, now time.Time) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !live {
		delete(s.seen, id)
		return time.Time{}
	}
	if s.seen == nil {
		s.seen = map[string]time.Time{}
	}
	t, ok := s.seen[id]
	if !ok {
		s.seen[id] = now
		return now
	}
	return t
}

func (d *Daemon) startIdleStopper() {
	d.idleStopWG.Add(1)
	go func() {
		defer d.idleStopWG.Done()
		ticker := time.NewTicker(idleStopInterval(d.idleStop.After))
		defer ticker.Stop()
		for {
			select {
			case now := <-ticker.C:
				d.idleStopSweep(now)
			case <-d.idleStopStop:
				return
			}
		}
	}()
}

func (d *Daemon) stopIdleStopper() {
	select {
	case <-d.idleStopStop:
	default:
		close(d.idleStopStop)
	}
	d.idleStopWG.Wait()
}

// idleStopSweep evaluates every live (non-archived and archived alike: an
// archived-but-headful thread still holds a pane) thread this daemon owns and
// stops the ones past their limit.
func (d *Daemon) idleStopSweep(now time.Time) {
	threads, err := d.store.ListThreads(true)
	if err != nil {
		log.Printf("idle-stop: list threads: %v", err)
		return
	}
	for _, th := range threads {
		snap, ok := d.maint.stateOf(th.ID)
		first := d.idleSeen.observe(th.ID, ok && snap.Head == api.Headful, now)
		stop, reason, _, _ := idleStopVerdict(th, snap, ok, first, d.idleStop.After, now)
		if !stop {
			continue
		}
		if err := d.stopAgentPane(th.ID); err != nil {
			log.Printf("idle-stop: thread %s (%s): %s — stop FAILED: %v", th.ID, th.Name, reason, err)
			continue
		}
		log.Printf("idle-stop: stopped thread %s (%s): %s", th.ID, th.Name, reason)
		if err := d.store.SetThreadIdleStopped(th.ID, now.Unix(), reason); err != nil {
			log.Printf("idle-stop: thread %s: stopped, but recording why failed: %v", th.ID, err)
		}
	}
}

// clearIdleStopped forgets the last automatic stop as the thread starts again.
func (d *Daemon) clearIdleStopped(thread api.Thread) {
	if thread.IdleStoppedAtUnix == 0 && thread.IdleStoppedReason == "" {
		return
	}
	if err := d.store.SetThreadIdleStopped(thread.ID, 0, ""); err != nil {
		log.Printf("thread %s: the last idle stop could not be cleared: %v", thread.ID, err)
	}
}

func (d *Daemon) routesIdleStop(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/threads/idle-stop", d.handleSetIdleStop)
	mux.HandleFunc("GET /v1/idle-stop/preview", d.handleIdleStopPreview)
}

func (d *Daemon) handleSetIdleStop(w http.ResponseWriter, r *http.Request) {
	var req api.SetIdleStopRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "idle-stop: id is required")
		return
	}
	if _, _, err := api.ParseIdleStopOverride(req.Value); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	th, err := d.store.GetThread(req.ID)
	if err != nil {
		d.threadOpErr(w, err)
		return
	}
	if !api.HasConversation(th.AgentKind) {
		writeError(w, http.StatusConflict, fmt.Sprintf("idle-stop: thread %s is a %s node — idle-stop only applies to agent threads", th.ID, th.AgentKind))
		return
	}
	if err := d.store.SetThreadIdleStop(req.ID, req.Value); err != nil {
		d.threadOpErr(w, err)
		return
	}
	d.respondThread(w, req.ID)
}

func (d *Daemon) handleIdleStopPreview(w http.ResponseWriter, r *http.Request) {
	threads, err := d.store.ListThreads(true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	now := time.Now()
	resp := api.IdleStopPreview{Schema: api.SchemaVersion, Machine: d.cfg.Machine, Threads: []api.IdleStopCandidate{}}
	if d.idleStop.After > 0 {
		resp.After = d.idleStop.After.String()
	}
	for _, th := range threads {
		snap, ok := d.maint.stateOf(th.ID)
		if !ok || snap.Head != api.Headful {
			continue // nothing to stop: the preview lists live threads only
		}
		first := d.idleSeen.observe(th.ID, true, now)
		stop, reason, limit, idleFor := idleStopVerdict(th, snap, ok, first, d.idleStop.After, now)
		c := api.IdleStopCandidate{ID: th.ID, Name: th.Name, Stop: stop, Reason: reason, IdleSeconds: int64(idleFor / time.Second)}
		if limit > 0 {
			c.Limit = limit.String()
		}
		resp.Threads = append(resp.Threads, c)
	}
	writeJSON(w, http.StatusOK, resp)
}
