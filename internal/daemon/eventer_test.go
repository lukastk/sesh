package daemon

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
)

// captureEventer builds an eventer whose runner records every (event, env)
// dispatched — hooks for EVERY event type, runCmd overridden (no subprocess).
// handle() dispatches async, so reads go through waitEvents.
type eventCapture struct {
	mu   sync.Mutex
	envs []map[string]string
}

func (c *eventCapture) snapshot() []map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]map[string]string(nil), c.envs...)
}

func (c *eventCapture) waitFor(t *testing.T, want int) []map[string]string {
	t.Helper()
	end := time.Now().Add(2 * time.Second)
	for time.Now().Before(end) {
		if got := c.snapshot(); len(got) >= want {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	got := c.snapshot()
	t.Fatalf("captured %d events, want %d: %v", len(got), want, got)
	return nil
}

func newCaptureEventer(d *Daemon) (*eventer, *eventCapture) {
	var hooks []config.Hook
	for _, ev := range config.ValidHookEvents {
		hooks = append(hooks, config.Hook{Name: "cap-" + ev, Event: ev, Command: "true"})
	}
	cap := &eventCapture{}
	runner := newHookRunner(hooks, nil)
	runner.runCmd = func(ctx context.Context, h config.Hook, env map[string]string) (string, error) {
		cap.mu.Lock()
		cap.envs = append(cap.envs, env)
		cap.mu.Unlock()
		return "", nil
	}
	return newEventer(d, runner), cap
}

func pairOf(was, now api.ThreadSnapshot) snapChange {
	return snapChange{old: &was, new: &now}
}

// TestObserveEmitsEdgeEvents: the diff-fed observe fires exactly the events
// the old poll-diff fired, with the same From/To and empty-string guards.
func TestObserveEmitsEdgeEvents(t *testing.T) {
	// Machine "peer" != d.cfg.Machine, so the busy->idle subscription delivery
	// path returns at its owner-guard without touching the nil store.
	d := &Daemon{cfg: config.Config{Machine: "self"}}
	e, cap := newCaptureEventer(d)

	base := api.ThreadSnapshot{Thread: api.Thread{ID: "t1", Machine: "peer", Name: "one"},
		Busy: api.BusyIdle, Head: api.Headless, Attachment: api.Detached}

	// Creation.
	n := base
	e.observe([]snapChange{{old: nil, new: &n}})
	got := cap.waitFor(t, 1)
	if got[0]["SESH_EVENT"] != "thread_created" || got[0]["SESH_THREAD_ID"] != "t1" {
		t.Fatalf("creation event = %v", got[0])
	}

	// Busy edge idle->busy.
	busy := base
	busy.Busy = api.BusyBusy
	e.observe([]snapChange{pairOf(base, busy)})
	got = cap.waitFor(t, 2)
	if got[1]["SESH_EVENT"] != "busy_changed" || got[1]["SESH_BUSY"] != "busy" {
		t.Fatalf("busy edge = %v", got[1])
	}

	// An empty-side busy value must NOT fire (the pre-42-peer guard).
	empty := base
	empty.Busy = ""
	e.observe([]snapChange{pairOf(empty, busy)})
	time.Sleep(50 * time.Millisecond)
	if len(cap.snapshot()) != 2 {
		t.Fatalf("empty-from busy edge fired an event")
	}

	// Flag + archive + rename in one pair: three events.
	was := busy
	now := busy
	now.Flagged = true
	now.Archived = true
	now.Name = "renamed"
	e.observe([]snapChange{pairOf(was, now)})
	got = cap.waitFor(t, 5)
	kinds := map[string]bool{}
	for _, env := range got[2:] {
		kinds[env["SESH_EVENT"]] = true
	}
	if !kinds["flag_changed"] || !kinds["thread_archived"] || !kinds["thread_renamed"] {
		t.Fatalf("compound pair fired %v", kinds)
	}

	// Deletion.
	e.observe([]snapChange{{old: &now, new: nil}})
	got = cap.waitFor(t, 6)
	if got[len(got)-1]["SESH_EVENT"] != "thread_deleted" {
		t.Fatalf("deletion event = %v", got[len(got)-1])
	}
}

// TestObserveAttachFlipStampsAges: an attachment flip is recorded BEFORE the
// same pair's events fire, so a busy edge caused by the nav itself carries
// SESH_ATTACHMENT_CHANGED_AGO ~ 0; a deletion clears the flip record.
func TestObserveAttachFlipStampsAges(t *testing.T) {
	d := &Daemon{cfg: config.Config{Machine: "self"}}
	e, cap := newCaptureEventer(d)

	was := api.ThreadSnapshot{Thread: api.Thread{ID: "t1", Machine: "peer"},
		Busy: api.BusyIdle, Attachment: api.Detached}
	now := was
	now.Attachment = api.Attached
	now.Busy = api.BusyBusy
	e.observe([]snapChange{pairOf(was, now)})
	got := cap.waitFor(t, 1)
	if got[0]["SESH_EVENT"] != "busy_changed" || got[0]["SESH_ATTACHMENT_CHANGED_AGO"] != "0" {
		t.Fatalf("flip-caused edge = %v, want SESH_ATTACHMENT_CHANGED_AGO=0", got[0])
	}

	// Deletion clears the flip; a later decorate knows nothing of it.
	e.observe([]snapChange{{old: &now, new: nil}})
	cap.waitFor(t, 2)
	ev := e.decorate(Event{Type: "busy_changed", Snap: now})
	if ev.AttachmentChangedAgo != -1 {
		t.Fatalf("flip survived deletion: %d", ev.AttachmentChangedAgo)
	}
}
