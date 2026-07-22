package daemon

// The eventer (PARITY_ROADMAP B1): the daemon's third observation loop. Every
// tick it builds the MERGED mesh view — its own maintainer state plus every
// peer's replicated snapshot — diffs it against the previous tick, and emits
// an Event per observed change into the hook runner. Observation is
// deliberately mesh-wide (v1's observer-bound design): the daemon you sit at
// fires your hooks for a thread going idle ANYWHERE, with no cross-machine
// plumbing in the hook.
//
// The first tick is a BASELINE: nothing is emitted (a daemon restart must not
// re-announce every existing thread as created).

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/lukastk/sesh/internal/api"
)

const eventerTick = 1 * time.Second

type eventer struct {
	d      *Daemon
	runner *hookRunner

	mu       sync.Mutex
	prev     map[string]api.ThreadSnapshot
	baseline bool // true once the first tick has seeded prev

	// attachFlip records, per thread, when THIS observer last saw the thread's
	// attachment axis change (either direction). Observer-local by design — it
	// needs no wire change, and "the user just navigated onto this session" is
	// only meaningful to the machine whose hooks are about to fire. Absent =
	// no flip observed since daemon start.
	attachFlip map[string]time.Time

	stop chan struct{}
	done chan struct{}
}

func newEventer(d *Daemon, runner *hookRunner) *eventer {
	return &eventer{d: d, runner: runner, attachFlip: map[string]time.Time{}, stop: make(chan struct{}), done: make(chan struct{})}
}

func (e *eventer) run() {
	defer close(e.done)
	t := time.NewTicker(eventerTick)
	defer t.Stop()
	for {
		select {
		case <-e.stop:
			return
		case <-t.C:
			e.tick()
		}
	}
}

func (e *eventer) shutdown() {
	close(e.stop)
	<-e.done
}

// merged builds the current mesh-wide view: local maintainer snapshots + every
// cached peer snapshot, keyed by thread id.
func (e *eventer) merged() map[string]api.ThreadSnapshot {
	out := map[string]api.ThreadSnapshot{}
	for _, sn := range e.d.maint.snapshot().Threads {
		out[sn.ID] = sn
	}
	if rows, err := e.d.store.LoadPeerSnapshots(); err == nil {
		for _, r := range rows {
			var threads []api.ThreadSnapshot
			if json.Unmarshal([]byte(r.Payload), &threads) != nil {
				continue // a stale/undecodable cache row: skip, resync fixes it
			}
			for _, sn := range threads {
				out[sn.ID] = sn
			}
		}
	}
	return out
}

func (e *eventer) tick() {
	cur := e.merged()
	e.mu.Lock()
	prev, seeded := e.prev, e.baseline
	e.prev, e.baseline = cur, true
	e.mu.Unlock()
	if !seeded {
		return // baseline tick: observe, emit nothing
	}

	for id, now := range cur {
		was, existed := prev[id]
		if !existed {
			e.runner.handle(e.decorate(Event{Type: "thread_created", Snap: now}))
			continue
		}
		// Record the attachment flip BEFORE emitting this tick's events, so an
		// event caused by the flip itself (nav → redraw → busy edge) already
		// carries AttachmentChangedAgo ≈ 0.
		if was.Attachment != now.Attachment && was.Attachment != "" && now.Attachment != "" {
			e.attachFlip[id] = time.Now()
		}
		if was.Busy != now.Busy && was.Busy != "" && now.Busy != "" {
			e.runner.handle(e.decorate(Event{Type: "busy_changed", Snap: now, From: string(was.Busy), To: string(now.Busy)}))
			if now.Busy == api.BusyIdle {
				// The turn-delivery engine (C3): owner-side, guarded per edge.
				go e.d.deliverSubscriptions(now)
			}
		}
		if was.Head != now.Head && was.Head != "" && now.Head != "" {
			e.runner.handle(e.decorate(Event{Type: "head_changed", Snap: now, From: string(was.Head), To: string(now.Head)}))
		}
		if !was.Archived && now.Archived {
			e.runner.handle(e.decorate(Event{Type: "thread_archived", Snap: now}))
		}
		if was.Archived && !now.Archived {
			e.runner.handle(e.decorate(Event{Type: "thread_unarchived", Snap: now}))
		}
		if was.Name != now.Name {
			e.runner.handle(e.decorate(Event{Type: "thread_renamed", Snap: now}))
		}
	}
	for id, was := range prev {
		if _, still := cur[id]; !still {
			delete(e.attachFlip, id)
			e.runner.handle(e.decorate(Event{Type: "thread_deleted", Snap: was}))
		}
	}
}

// decorate stamps the observer-computed age fields onto an event: seconds since
// the newest input on a client attached to the thread's session (from the
// owner-stamped snapshot field) and seconds since this observer saw the
// attachment axis flip. -1 = unknown; Env() omits unknowns so a hook's numeric
// test on an empty var never mis-reads 0 ("just now").
func (e *eventer) decorate(ev Event) Event {
	ev.AttachedActivityAgo, ev.AttachmentChangedAgo = -1, -1
	if ev.Snap.AttachedActivityUnix > 0 {
		ev.AttachedActivityAgo = max(time.Now().Unix()-ev.Snap.AttachedActivityUnix, 0)
	}
	if t, ok := e.attachFlip[ev.Snap.ID]; ok {
		ev.AttachmentChangedAgo = int64(time.Since(t).Seconds())
	}
	return ev
}
