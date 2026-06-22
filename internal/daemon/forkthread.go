package daemon

// Fork (PARITY_ROADMAP D3, v1's rewind-and-branch): POST /v1/threads with
// fork_from clones the source conversation's prefix (through message_id
// assistant turns; 0 = the whole transcript) under a FRESH agent session id,
// writes it at the agent's own location, and records a new HEADLESS thread
// that resumes from the branch point. The source is untouched (the 409
// "would fork" guard protects against ACCIDENTAL forks; this is the
// deliberate verb). Owner-side by construction: the source transcript lives
// on this machine's disk.

import (
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/fork"
)

func (d *Daemon) newForkedThread(w http.ResponseWriter, kind agents.Kind, req api.NewThreadRequest) {
	src, err := d.store.GetThread(req.ForkFrom)
	if err != nil {
		writeError(w, http.StatusNotFound, "fork: source thread "+req.ForkFrom+" not found on this machine")
		return
	}
	if src.AgentKind != string(kind) {
		writeError(w, http.StatusBadRequest, "fork: source is "+src.AgentKind+", not "+string(kind)+" (a fork stays the same agent)")
		return
	}
	homes := agents.ResolveHomes(d.cfg.CodexHome)
	// Fork from the CURRENT leaf of any claude compaction chain, not the stale stored
	// anchor: the transcript lines we read carry the LEAF's session id, so fork.Branch's
	// id rewrite (a string replace of oldID→newID on every line) must be given that same
	// leaf id, or it would find nothing to replace and write a transcript still bearing
	// the source's id. (codex/pi don't drift, so this is a no-op for them.)
	srcSession, err := agents.ResolveCurrentSession(kind, src.AgentSessionID, src.Cwd, homes)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "fork: resolve source session: "+err.Error())
		return
	}
	srcPath, found, err := agents.TranscriptPath(kind, srcSession, src.Cwd, homes)
	if err != nil || !found {
		writeError(w, http.StatusConflict, "fork: source thread has no transcript on disk (no turn yet)")
		return
	}
	lines, err := agents.ReadTranscript(kind, srcSession, src.Cwd, homes, -1)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	newSessionID := uuid.NewString()
	branched, err := fork.Branch(string(kind), lines, srcSession, newSessionID, req.MessageID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	dst, err := fork.DestPath(string(kind), homes.Claude, homes.Codex, homes.Pi, src.Cwd, newSessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	content := []byte{}
	for _, l := range branched {
		content = append(content, l...)
		content = append(content, '\n')
	}
	if err := os.WriteFile(dst, content, 0o644); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = srcPath // documented: never written

	// A fork stays the same conversation, so it inherits the source's pinned model
	// by default; an explicit --model on the fork overrides it.
	model := req.Model
	if model == "" {
		model = src.Model
	}
	id := uuid.NewString()
	thread := api.Thread{
		ID:             id,
		Machine:        d.cfg.Machine,
		SessionName:    "headless-" + id,
		Cwd:            src.Cwd,
		AgentKind:      string(kind),
		Name:           req.Name,
		Tags:           []string{},
		CreatedAtUnix:  time.Now().Unix(),
		AgentSessionID: newSessionID,
		Parent:         req.Parent,
		Notify:         d.defaults.NotifyDefault(),
		Model:          model,
		// The conversation EXISTS (the branched transcript): turns must RESUME.
		HeadlessStarted: true,
	}
	if err := d.store.InsertThread(thread); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.ThreadResponse{Schema: api.SchemaVersion, Thread: thread})
}
