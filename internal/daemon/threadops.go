package daemon

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"errors"
	"github.com/lukastk/sesh/internal/agents"
	"net/http"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/store"
)

func (d *Daemon) routesThreadOps(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/threads/rename", d.handleThreadRename)
	mux.HandleFunc("POST /v1/threads/reparent", d.handleThreadReparent)
	mux.HandleFunc("POST /v1/threads/notify", d.handleThreadNotify)
	mux.HandleFunc("POST /v1/threads/hold", d.handleThreadHold)
	mux.HandleFunc("GET /v1/threads/transcript", d.handleThreadTranscript)
	mux.HandleFunc("POST /v1/threads/import", d.handleThreadImport)
	mux.HandleFunc("POST /v1/threads/meta", d.handleThreadMeta)
	mux.HandleFunc("POST /v1/threads/tag", d.handleThreadTag)
	mux.HandleFunc("POST /v1/threads/archive", d.handleThreadArchive)
	mux.HandleFunc("POST /v1/threads/stop", d.handleThreadStop)
	mux.HandleFunc("POST /v1/threads/delete", d.handleThreadDelete)
}

// handleThreadStop ends the thread's runtime — kills the thread's PANE (which
// kills the agent) — but KEEPS the record, which becomes a normal dead thread
// (resumable via `resume`). This is the runtime half of the old `kill`; dropping
// the record is a separate `delete`.
//
// It targets the pane (resolved from the @sesh-thread-id marker), NOT the
// session: a session may host several threads (their own windows, or splits in
// one window), and killing the session would take the siblings with it. When
// the thread owns its whole session (1 pane / 1 window) tmux tears the empty
// session down anyway, so the common case is unchanged. Stopping a thread whose
// pane is already gone is not an error (the runtime was already down) — it is
// idempotent.
func (d *Daemon) handleThreadStop(w http.ResponseWriter, r *http.Request) {
	var req api.StopThreadRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "stop: id is required")
		return
	}
	thread, err := d.store.GetThread(req.ID)
	if err != nil {
		d.threadOpErr(w, err)
		return
	}
	// SHELL THREAD: stopping one kills its whole tmux SESSION, which is a far
	// wider blast radius than an agent thread's single pane — every window, every
	// pane, including any OTHER threads' agent panes living in that session. So
	// it refuses unless forced, and the refusal NAMES the agents that would die.
	if thread.AgentKind == api.ShellAgentKind {
		sess, live, serr := d.tmux.FindSessionByShellID(thread.ID)
		if serr != nil {
			writeError(w, http.StatusInternalServerError, serr.Error())
			return
		}
		if live {
			if hosted := hostedAgentThreads(sess); len(hosted) > 0 && !req.Force {
				writeError(w, http.StatusConflict, fmt.Sprintf(
					"stop: shell thread %s hosts agent panes for %s — killing its session kills them too. Re-run with --force, or stop those threads first.",
					thread.ID, strings.Join(hosted, ", ")))
				return
			}
			if err := d.tmux.KillSession(sess.Name); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		d.respondThread(w, req.ID)
		return
	}

	loc, found, err := d.tmux.FindPaneByThreadID(thread.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if found {
		if err := d.tmux.KillPane(loc.Pane); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema": api.SchemaVersion, "stopped": req.ID})
}

func (d *Daemon) handleThreadRename(w http.ResponseWriter, r *http.Request) {
	var req api.RenameThreadRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// An empty name is allowed — it makes the thread NAMELESS (and clears a divider's
	// label to a bare rule), symmetric with `thread new --name ""`. Only the id is required.
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "rename: id is required")
		return
	}
	if err := d.store.RenameThread(req.ID, req.Name); err != nil {
		d.threadOpErr(w, err)
		return
	}
	d.respondThread(w, req.ID)
}

// handleThreadTag adds and/or removes tags, preserving order and de-duplicating.
func (d *Daemon) handleThreadTag(w http.ResponseWriter, r *http.Request) {
	var req api.TagThreadRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	thread, err := d.store.GetThread(req.ID)
	if err != nil {
		d.threadOpErr(w, err)
		return
	}
	remove := map[string]bool{}
	for _, t := range req.Remove {
		remove[t] = true
	}
	seen := map[string]bool{}
	var tags []string
	for _, t := range thread.Tags {
		if remove[t] || seen[t] {
			continue
		}
		seen[t] = true
		tags = append(tags, t)
	}
	for _, t := range req.Add {
		if t == "" || remove[t] || seen[t] {
			continue
		}
		seen[t] = true
		tags = append(tags, t)
	}
	if err := d.store.SetThreadTags(req.ID, tags); err != nil {
		d.threadOpErr(w, err)
		return
	}
	d.respondThread(w, req.ID)
}

func (d *Daemon) handleThreadArchive(w http.ResponseWriter, r *http.Request) {
	var req api.ArchiveThreadRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "archive: id is required")
		return
	}
	// A divider can't be archived (archive clears pin_order, which a divider must
	// keep — a divider only exists in the pinned block). Delete it instead.
	if req.Archived {
		if th, err := d.store.GetThread(req.ID); err == nil && th.AgentKind == api.DividerAgentKind {
			writeError(w, http.StatusConflict,
				"archive: "+req.ID+" is a divider — delete it instead (sesh thread delete --id "+req.ID+")")
			return
		}
	}
	if err := d.store.SetThreadArchived(req.ID, req.Archived, time.Now().Unix()); err != nil {
		d.threadOpErr(w, err)
		return
	}
	d.respondThread(w, req.ID)
}

// handleThreadDelete drops the record only — the runtime (agent + tmux session)
// is left untouched. It is for forgetting a record, usually an already-dead one.
// Deleting a thread whose runtime is still LIVE orphans its agent (record gone,
// process still running), so delete refuses a live thread unless Force is set —
// the natural order is `stop` then `delete` (which is what `kill` was).
func (d *Daemon) handleThreadDelete(w http.ResponseWriter, r *http.Request) {
	var req api.DeleteThreadRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "delete: id is required")
		return
	}
	thread, err := d.store.GetThread(req.ID)
	if err != nil {
		d.threadOpErr(w, err)
		return
	}
	// A SHELL thread's liveness is its SESSION, not a pane — and the consequence
	// of a forced delete differs too: an orphaned agent is a running conversation
	// you have lost the handle to, whereas an "orphaned" session is merely a
	// ghost, fully listed in the shells viewer and re-promotable. The refusal
	// says that rather than borrowing the agent wording.
	if thread.AgentKind == api.ShellAgentKind {
		sess, live, err := d.tmux.FindSessionByShellID(req.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if live && !req.Force {
			writeError(w, http.StatusConflict,
				"delete: shell thread "+thread.SessionName+" still has a live tmux session; run `stop` first, or delete --force to drop the record and leave the session running as an untracked ghost (you can promote it again from the shells view)")
			return
		}
		if live {
			// UNSTAMP before dropping the record: a session left carrying a marker
			// whose record is gone classifies as `stale`, which is a bug state, not
			// a ghost. Loud on failure — the stale marker would otherwise be silent.
			if err := d.tmux.UnstampSessionShellID(sess.Name); err != nil {
				writeError(w, http.StatusInternalServerError,
					"delete: could not clear the @sesh-shell-id marker on session "+sess.Name+
						" (dropping the record now would leave a STALE marker): "+err.Error())
				return
			}
		}
	} else if !req.Force {
		if _, live, err := d.tmux.FindPaneByThreadID(req.ID); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		} else if live {
			writeError(w, http.StatusConflict,
				"delete: thread "+thread.SessionName+" is live (agent running); run `stop` first, or delete --force to drop the record and orphan the agent")
			return
		}
	}
	if err := d.store.DeleteThread(req.ID); err != nil {
		d.threadOpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema": api.SchemaVersion, "deleted": req.ID})
}

func (d *Daemon) respondThread(w http.ResponseWriter, id string) {
	thread, err := d.store.GetThread(id)
	if err != nil {
		d.threadOpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, api.ThreadResponse{Schema: api.SchemaVersion, Thread: thread})
}

func (d *Daemon) threadOpErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrThreadNotFound) {
		writeError(w, http.StatusNotFound, "thread not found")
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}

// handleThreadReparent re-parents a thread (” = root). The new parent must
// exist, and the chain from it must not loop back through the thread (a cycle
// would hang every tree walk) — both are loud refusals.
func (d *Daemon) handleThreadReparent(w http.ResponseWriter, r *http.Request) {
	var req api.ReparentThreadRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "reparent: id is required")
		return
	}
	if _, err := d.store.GetThread(req.ID); err != nil {
		writeError(w, http.StatusNotFound, "reparent: thread "+req.ID+" not found")
		return
	}
	if req.Parent != "" {
		if req.Parent == req.ID {
			writeError(w, http.StatusBadRequest, "reparent: a thread cannot be its own parent")
			return
		}
		seen := map[string]bool{req.ID: true}
		for p := req.Parent; p != ""; {
			if seen[p] {
				writeError(w, http.StatusBadRequest, "reparent: would create a cycle through "+p)
				return
			}
			seen[p] = true
			th, err := d.store.GetThread(p)
			if err != nil {
				writeError(w, http.StatusBadRequest, "reparent: parent "+p+" does not exist")
				return
			}
			p = th.Parent
		}
	}
	if err := d.store.SetThreadParent(req.ID, req.Parent); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	th, err := d.store.GetThread(req.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.ThreadResponse{Schema: api.SchemaVersion, Thread: th})
}

// handleThreadNotify toggles a thread's notification gate.
func (d *Daemon) handleThreadNotify(w http.ResponseWriter, r *http.Request) {
	var req api.NotifyThreadRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "notify: id is required")
		return
	}
	if err := d.store.SetThreadNotify(req.ID, req.On); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	th, err := d.store.GetThread(req.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.ThreadResponse{Schema: api.SchemaVersion, Thread: th})
}

// handleThreadHold writes a thread's hold state: the absolute on-hold-until
// instant, the absolute release-until instant, or neither (both 0 = clear). The
// daemon is a pure setter — "on hold right now" / "released right now" are derived
// live against its clock by the maintainer/grid, so a past instant stores fine and
// simply reads as lapsed. Both instants at once is REFUSED: held-and-released has
// no meaning, and silently picking one would be the plausible-but-wrong class.
//
// The reply carries the EFFECTIVE deadline after the write plus the ancestor that
// dominates it, because only the owner can compute inheritance (it spans that
// machine's whole record set). Without it a caller clearing a child's hold could
// not tell that an ancestor still parks it — which is exactly how "hold cleared"
// came to be printed for a thread that stayed on hold.
func (d *Daemon) handleThreadHold(w http.ResponseWriter, r *http.Request) {
	var req api.HoldThreadRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "hold: id is required")
		return
	}
	if req.OnHoldUntilUnix != 0 && req.ReleaseUntilUnix != 0 {
		writeError(w, http.StatusBadRequest,
			"hold: on_hold_until_unix and release_until_unix are mutually exclusive — a thread is held, released, or neither")
		return
	}
	if err := d.store.SetThreadHold(req.ID, req.OnHoldUntilUnix, req.ReleaseUntilUnix); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	th, err := d.store.GetThread(req.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := api.HoldThreadResponse{Schema: api.SchemaVersion, Thread: th}
	// Resolve the outcome over the FULL record set (archived included — a child's
	// hold can be inherited through an archived ancestor). A read failure here must
	// not fail the write that already landed: report the write, omit the derived
	// part rather than inventing a reassuring one.
	if all, err := d.store.ListThreads(true); err == nil {
		now := time.Now().Unix()
		resp.OnHoldEffectiveUnix = effectiveHolds(all, now)[req.ID]
		if dom := holdDominator(all, req.ID, now); dom != "" {
			resp.HeldByID = dom
			for _, t := range all {
				if t.ID == dom {
					resp.HeldByName = t.Name
					break
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleThreadTranscript is the owner-side transcript read (D0): the raw
// lines (?tail=N for the last N) + the last assistant reply with its monotone
// count. Transcript content is NOT mesh-replicated — remote reads route here.
func (d *Daemon) handleThreadTranscript(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "transcript: id is required")
		return
	}
	th, err := d.store.GetThread(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if conversationGate(w, th, "transcript") {
		return
	}
	tail := -1
	if t := r.URL.Query().Get("tail"); t != "" {
		n, perr := strconv.Atoi(t)
		if perr != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "transcript: bad tail "+t)
			return
		}
		tail = n
	}
	homes := agents.ResolveHomes(d.cfg.CodexHome)
	kind := agents.Kind(th.AgentKind)
	lines, err := agents.ReadTranscript(kind, th.AgentSessionID, th.Cwd, homes, tail)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	path, _, _ := agents.TranscriptPath(kind, th.AgentSessionID, th.Cwd, homes)
	reply, count, err := agents.LastReply(kind, th.AgentSessionID, th.Cwd, homes)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.TranscriptResponse{
		Schema: api.SchemaVersion, ID: id, Path: path,
		Lines: lines, LastReply: reply, ReplyCount: count,
	})
}

// handleThreadMeta sets (” value = deletes) one meta key.
func (d *Daemon) handleThreadMeta(w http.ResponseWriter, r *http.Request) {
	var req api.MetaThreadRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == "" || req.Key == "" {
		writeError(w, http.StatusBadRequest, "meta: id and key are required")
		return
	}
	if err := d.store.SetThreadMetaKey(req.ID, req.Key, req.Value); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	th, err := d.store.GetThread(req.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.ThreadResponse{Schema: api.SchemaVersion, Thread: th})
}

// handleThreadImport inserts a pre-built thread record (v1 migration). No
// agent is spawned — the record points at an existing on-disk conversation
// (resume materializes it). A duplicate id is a loud conflict (re-import is
// guarded client-side, but the store is the source of truth).
func (d *Daemon) handleThreadImport(w http.ResponseWriter, r *http.Request) {
	var th api.Thread
	if err := decodeJSON(r, &th); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if th.ID == "" || th.AgentKind == "" {
		writeError(w, http.StatusBadRequest, "import: id and agent are required")
		return
	}
	if _, err := d.store.GetThread(th.ID); err == nil {
		writeError(w, http.StatusConflict, "import: thread "+th.ID+" already exists")
		return
	}
	if th.Machine == "" {
		th.Machine = d.cfg.Machine
	}
	if th.Tags == nil {
		th.Tags = []string{}
	}
	if err := d.store.InsertThread(th); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// InsertThread persists the record's archived_at but lands the archived FLAG at 0
	// (the flag column is written by SetThreadArchived); flip it for an imported archived
	// thread. Passing the record's OWN archived_at as `now` preserves it verbatim (the
	// CASE keeps an existing value); a pre-37 record carries 0, which stays 0 (no archive
	// time ever existed — it sorts to the bottom of the archived view, by design).
	if th.Archived {
		if err := d.store.SetThreadArchived(th.ID, true, th.ArchivedAtUnix); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, api.ThreadResponse{Schema: api.SchemaVersion, Thread: th})
}

// hostedAgentThreads lists the ids of agent threads whose panes live in a
// session — what a shell thread's kill would take down with it.
func hostedAgentThreads(sess api.TmuxSession) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range sess.Windows {
		for _, p := range w.Panes {
			if p.ThreadID != "" && !seen[p.ThreadID] {
				seen[p.ThreadID] = true
				out = append(out, p.ThreadID)
			}
		}
	}
	return out
}
