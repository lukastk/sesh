package daemon

// The eventer (PARITY_ROADMAP B1, rebuilt by the mesh scale pass —
// _dev/MESH_SCALE.md C2): the daemon's change observer. It used to rebuild and
// diff the ENTIRE merged mesh every second — decoding every peer's cached
// threads from SQLite each tick, 147 ms/s on the phone with ~2,000 mostly-
// archived threads. It is now DIFF-FED: the mesh view emits (old, new) pairs
// at the moment a peer transition lands, and the local maintainer emits pairs
// from its publish path — so the eventer does ZERO work when nothing changed,
// O(changed rows) when something did, and can never MISS an edge shorter than
// a polling tick (the property [[hooks]] used to pin the mesh cadence for).
//
// Baseline semantics are unchanged: state present at daemon start (the seeded
// view, the maintainer's first sweep) is absorbed silently — a restart never
// re-announces existing threads.

import (
	"sync"
	"time"

	"github.com/lukastk/sesh/internal/api"
)

type eventer struct {
	d      *Daemon
	runner *hookRunner

	// attachFlip records, per thread, when THIS observer last saw the thread's
	// attachment axis change (either direction). Observer-local by design — it
	// needs no wire change, and "the user just navigated onto this session" is
	// only meaningful to the machine whose hooks are about to fire. Absent =
	// no flip observed since daemon start.
	mu         sync.Mutex
	attachFlip map[string]time.Time
}

func newEventer(d *Daemon, runner *hookRunner) *eventer {
	return &eventer{d: d, runner: runner, attachFlip: map[string]time.Time{}}
}

// observe consumes a batch of thread transitions and fires the corresponding
// events. Safe for concurrent callers (per-peer sync goroutines + the
// maintainer's probe pool); hook commands themselves run in their own
// goroutines via the runner.
func (e *eventer) observe(changes []snapChange) {
	for _, ch := range changes {
		switch {
		case ch.old == nil && ch.new == nil:
			continue
		case ch.old == nil:
			e.runner.handle(e.decorate(Event{Type: "thread_created", Snap: *ch.new}))
		case ch.new == nil:
			e.mu.Lock()
			delete(e.attachFlip, ch.old.ID)
			e.mu.Unlock()
			e.runner.handle(e.decorate(Event{Type: "thread_deleted", Snap: *ch.old}))
		default:
			e.observePair(*ch.old, *ch.new)
		}
	}
}

func (e *eventer) observePair(was, now api.ThreadSnapshot) {
	// Record the attachment flip BEFORE emitting this pair's events, so an
	// event caused by the flip itself (nav → redraw → busy edge) already
	// carries AttachmentChangedAgo ≈ 0.
	if was.Attachment != now.Attachment && was.Attachment != "" && now.Attachment != "" {
		e.mu.Lock()
		e.attachFlip[now.ID] = time.Now()
		e.mu.Unlock()
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
	// The flag flipping (schema 44): to=flagged is "this thread needs the
	// user" — the toast edge (auto-flags fire it on unattended turn ends
	// and question stalls; manual flags fire it too). Mesh-wide like every
	// event (the flag is a record field riding the snapshot).
	if was.Flagged != now.Flagged {
		from, to := "unflagged", "flagged"
		if was.Flagged {
			from, to = "flagged", "unflagged"
		}
		e.runner.handle(e.decorate(Event{Type: "flag_changed", Snap: now, From: from, To: to}))
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
	e.mu.Lock()
	t, ok := e.attachFlip[ev.Snap.ID]
	e.mu.Unlock()
	if ok {
		ev.AttachmentChangedAgo = int64(time.Since(t).Seconds())
	}
	return ev
}
