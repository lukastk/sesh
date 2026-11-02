package daemon

import (
	"log"
	"os"
	"sync"
	"time"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/api"
)

// The model observer keeps every local agent thread's OBSERVED model — the model
// its transcript says it last ran on (agents.LastModel) — and the maintainer
// stamps it onto the thread's snapshot (ThreadSnapshot.ObservedModel), so it
// travels the mesh like the rest of the owner-derived state.
//
// It runs on its own slow loop, not the maintainer's 300 ms tick: resolving a
// transcript path can walk a directory tree (codex rollouts are found by
// walking ~/.codex/sessions), and the observed model changes at most once per
// turn. Per thread it caches the resolved path and only re-reads a file whose
// size or mtime moved — appended bytes alone when it only grew.
const (
	modelObserveInterval = 3 * time.Second
	// modelPathRecheck bounds how stale a cached path may get: claude moves a
	// conversation to a new leaf file on compaction, and a not-yet-written
	// transcript (no turn yet) has to be looked for again.
	modelPathRecheck = 30 * time.Second
)

type observedFile struct {
	sessionID, cwd string // what the path was resolved from; a change re-resolves
	path           string // '' = not on disk yet
	resolvedAt     time.Time
	size           int64
	mtime          time.Time
	scanned        int64
	model          string
}

type modelObserver struct {
	d     *Daemon
	files map[string]*observedFile // observer goroutine only

	mu    sync.RWMutex
	model map[string]string // published: thread id -> observed model

	stop, done chan struct{}
	started    bool
}

func newModelObserver(d *Daemon) *modelObserver {
	return &modelObserver{d: d, files: map[string]*observedFile{}, model: map[string]string{},
		stop: make(chan struct{}), done: make(chan struct{})}
}

// observed is a thread's observed model (” = none yet, or not an agent thread).
func (o *modelObserver) observed(id string) string {
	if o == nil {
		return ""
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.model[id]
}

func (o *modelObserver) start() {
	o.started = true
	go func() {
		defer close(o.done)
		t := time.NewTicker(modelObserveInterval)
		defer t.Stop()
		o.sweep()
		for {
			select {
			case <-o.stop:
				return
			case <-t.C:
				o.sweep()
			}
		}
	}()
}

func (o *modelObserver) stopAndWait() {
	if o == nil || !o.started {
		return
	}
	close(o.stop)
	<-o.done
}

func observable(kind string) bool {
	switch agents.Kind(kind) {
	case agents.Claude, agents.Codex, agents.Pi, agents.MyPi:
		return true
	}
	return false
}

func (o *modelObserver) sweep() {
	threads, err := o.d.store.ListThreads(true)
	if err != nil {
		return // transient store error: try again next sweep
	}
	homes := agents.ResolveHomes(o.d.cfg.CodexHome)
	now := time.Now()
	next := make(map[string]string, len(threads))
	seen := make(map[string]bool, len(threads))
	for _, th := range threads {
		if !observable(th.AgentKind) || th.AgentSessionID == "" {
			continue
		}
		seen[th.ID] = true
		f := o.files[th.ID]
		if f == nil || f.sessionID != th.AgentSessionID || f.cwd != th.Cwd {
			f = &observedFile{sessionID: th.AgentSessionID, cwd: th.Cwd}
			o.files[th.ID] = f
		}
		o.refresh(th, f, homes, now)
		if f.model != "" {
			next[th.ID] = f.model
		}
	}
	for id := range o.files {
		if !seen[id] {
			delete(o.files, id)
		}
	}
	o.mu.Lock()
	o.model = next
	o.mu.Unlock()
}

func (o *modelObserver) refresh(th api.Thread, f *observedFile, homes agents.Homes, now time.Time) {
	kind := agents.Kind(th.AgentKind)
	if f.path == "" || now.Sub(f.resolvedAt) >= modelPathRecheck {
		path, found, err := agents.TranscriptPath(kind, th.AgentSessionID, th.Cwd, homes)
		if err != nil {
			log.Printf("observed model %s: resolve transcript: %v", th.ID, err)
			return
		}
		f.resolvedAt = now
		if !found {
			path = ""
		}
		if path != f.path {
			*f = observedFile{sessionID: f.sessionID, cwd: f.cwd, path: path, resolvedAt: now}
		}
	}
	if f.path == "" {
		return // no turn yet: no observed model
	}
	fi, err := os.Stat(f.path)
	if err != nil {
		*f = observedFile{sessionID: f.sessionID, cwd: f.cwd} // vanished: resolve again next sweep
		return
	}
	if fi.Size() == f.size && fi.ModTime().Equal(f.mtime) {
		return
	}
	since := int64(0)
	if f.scanned > 0 && fi.Size() >= f.size && fi.Size() >= f.scanned {
		since = f.scanned // only grew: read what was appended
	}
	scan, err := agents.LastModel(kind, f.path, since)
	if err != nil {
		log.Printf("observed model %s: read %s: %v", th.ID, f.path, err)
		f.size, f.scanned = 0, 0 // re-read whole next sweep
		return
	}
	switch {
	case scan.Found:
		f.model = scan.Model
	case since == 0:
		f.model = "" // a whole-file read that found none: there is no observed model
	}
	f.size, f.mtime, f.scanned = fi.Size(), fi.ModTime(), scan.Scanned
}
