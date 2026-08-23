package daemon

// SHELL THREADS: a thread record whose runtime is a tmux SESSION rather than an
// agent pane. Its durable content is its working directory — reviving it means
// `new-session -c <cwd>`, the way reviving an agent thread means resuming a
// conversation. See _dev/SHELL.md.
//
// Runtime identity is the SESSION-scoped @sesh-shell-id marker (tmux.ShellIDOption),
// which is a DIFFERENT tmux key from the pane-scoped @sesh-thread-id on purpose:
// tmux user options inherit down during format expansion, so one shared key would
// make every unmarked pane in a shell thread's session report that thread's id.

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/store"
)

func (d *Daemon) routesShell(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/shells", d.handleShellNew)
	mux.HandleFunc("POST /v1/shells/promote", d.handleShellPromote)
	mux.HandleFunc("GET /v1/shells/sessions", d.handleShellSessions)
	mux.HandleFunc("GET /v1/shells/info", d.handleShellInfo)
}

// shellNameFor derives a shell thread's name from its cwd when the caller gave
// none: the cwd's base, which for a boxyard box is already the box's index name.
// Never empty — an unnamed row is indistinguishable from every other unnamed row.
func shellNameFor(cwd string) string {
	base := filepath.Base(strings.TrimRight(cwd, string(filepath.Separator)))
	if base == "" || base == "." || base == string(filepath.Separator) {
		return "shell"
	}
	return base
}

// findShellByCwdName returns a NON-ARCHIVED shell thread on this machine with
// the given (cwd, name) — the idempotency key of the `shell enter` flow.
//
// More than one match is a LOUD error, not a guess: two shells in one cwd are
// legal (two tasks in one box) but they must be distinguishable, so `shell new`
// refuses a duplicate name and this can only fire on data that predates that
// rule or was made out of band.
func (d *Daemon) findShellByCwdName(cwd, name string) (api.Thread, bool, error) {
	threads, err := d.store.ListThreads(false)
	if err != nil {
		return api.Thread{}, false, err
	}
	var hits []api.Thread
	for _, th := range threads {
		if th.AgentKind == api.ShellAgentKind && th.Cwd == cwd && th.Name == name {
			hits = append(hits, th)
		}
	}
	switch len(hits) {
	case 0:
		return api.Thread{}, false, nil
	case 1:
		return hits[0], true, nil
	default:
		ids := make([]string, 0, len(hits))
		for _, h := range hits {
			ids = append(ids, h.ID)
		}
		return api.Thread{}, false, fmt.Errorf(
			"more than one non-archived shell thread has cwd %s and name %q (%s) — address one explicitly with --id",
			cwd, name, strings.Join(ids, ", "))
	}
}

// startShellSession creates the tmux session for a shell thread in its cwd and
// stamps the marker. The stamp is what makes the session THIS thread's runtime,
// so a failure to stamp tears the session back down rather than leaving an
// unattributable session behind.
func (d *Daemon) startShellSession(thread api.Thread) error {
	if thread.Cwd == "" {
		return fmt.Errorf("shell %s has no cwd — nothing to root a session in", thread.ID)
	}
	if _, err := os.Stat(thread.Cwd); err != nil {
		return fmt.Errorf("shell %s: cwd %s is not there on %s: %w", thread.ID, thread.Cwd, d.cfg.Machine, err)
	}
	name := thread.SessionName
	// A tmux session name collision is COSMETIC (identity is the marker, not the
	// name), so suffix rather than refuse — but say so, since the rendered name
	// will not be the one that was asked for.
	if d.tmux.HasSession(name) {
		for i := 2; ; i++ {
			cand := fmt.Sprintf("%s-%d", name, i)
			if !d.tmux.HasSession(cand) {
				log.Printf("shell %s: tmux session %q already exists on this server; using %q instead (identity is the @sesh-shell-id marker, not the name)", thread.ID, name, cand)
				name = cand
				break
			}
		}
	}
	if err := d.tmux.CreateSession(name, thread.Cwd, d.spawnEnv(thread.ID)); err != nil {
		return err
	}
	if err := d.tmux.StampSessionShellID(name, thread.ID); err != nil {
		// Unattributable session: tear it down rather than leave a ghost that
		// looks like it belongs to this thread but cannot be resolved to it.
		if kerr := d.tmux.KillSession(name); kerr != nil {
			log.Printf("shell %s: stamping %q failed (%v) AND tearing it down failed (%v) — that session is now an untracked ghost", thread.ID, name, err, kerr)
		}
		return fmt.Errorf("stamp shell marker on %q: %w", name, err)
	}
	if name != thread.SessionName {
		if err := d.store.SetThreadSessionName(thread.ID, name); err != nil {
			return fmt.Errorf("record actual session name %q: %w", name, err)
		}
	}
	return nil
}

func (d *Daemon) handleShellNew(w http.ResponseWriter, r *http.Request) {
	var req api.NewShellRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Cwd == "" {
		writeError(w, http.StatusBadRequest, "shell new: --cwd is required (a shell thread's working directory IS its durable content)")
		return
	}
	// A ~-relative cwd is expanded against THIS machine's home — that is what
	// makes `--machine X --cwd ~/dev/box` portable (the caller's home may differ
	// from the owner's). Same treatment as thread new / realize / adopt.
	req.Cwd = expandHomeCwd(req.Cwd)
	if !filepath.IsAbs(req.Cwd) {
		writeError(w, http.StatusBadRequest, "shell new: --cwd must be absolute or ~-relative, got "+req.Cwd)
		return
	}
	cwd := filepath.Clean(req.Cwd)
	name := req.Name
	if name == "" {
		name = shellNameFor(cwd)
	}
	if req.Parent != "" {
		if _, err := d.store.GetThread(req.Parent); err != nil {
			writeError(w, http.StatusBadRequest, "shell new: parent thread not found: "+req.Parent)
			return
		}
	}

	existing, found, err := d.findShellByCwdName(cwd, name)
	if err != nil {
		writeError(w, http.StatusConflict, "shell new: "+err.Error())
		return
	}
	if found {
		if req.Idempotent {
			// The `shell enter` flow: enter the one that is already there. If it
			// is headless, revive it so the caller always gets a live session.
			if _, live, lerr := d.tmux.FindSessionByShellID(existing.ID); lerr == nil && !live {
				if serr := d.startShellSession(existing); serr != nil {
					writeError(w, http.StatusInternalServerError, "shell enter: restart session: "+serr.Error())
					return
				}
				existing, _ = d.store.GetThread(existing.ID)
			}
			writeJSON(w, http.StatusOK, api.ThreadResponse{Schema: api.SchemaVersion, Thread: existing})
			return
		}
		writeError(w, http.StatusConflict, fmt.Sprintf(
			"shell new: %s already has a shell thread named %q (%s). Several shells in one cwd are fine, but they need distinct names — pass --name, or use `shell enter` to enter that one.",
			cwd, name, existing.ID))
		return
	}

	id := uuid.NewString()
	sessionName := req.SessionName
	if sessionName == "" {
		sessionName, err = d.sessionNameFor(cwd, id, name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "shell new: derive session name: "+err.Error())
			return
		}
	}
	thread := api.Thread{
		ID:            id,
		Machine:       d.cfg.Machine,
		SessionName:   sessionName,
		Cwd:           cwd,
		AgentKind:     api.ShellAgentKind,
		Name:          name,
		Tags:          []string{},
		CreatedAtUnix: time.Now().Unix(),
		Parent:        req.Parent,
		Notify:        d.defaults.NotifyDefault(),
	}
	if err := d.store.InsertThread(thread); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !req.NoStart {
		if err := d.startShellSession(thread); err != nil {
			// The record without its session is a headless shell thread, which is
			// a legitimate state — but the caller asked for a live one, so this is
			// loud, and the record is dropped so a failed create leaves nothing.
			if derr := d.store.DeleteThread(id); derr != nil {
				log.Printf("shell %s: session start failed (%v) and dropping the record failed too (%v)", id, err, derr)
			}
			writeError(w, http.StatusInternalServerError, "shell new: "+err.Error())
			return
		}
		thread, _ = d.store.GetThread(id)
	}
	writeJSON(w, http.StatusOK, api.ThreadResponse{Schema: api.SchemaVersion, Thread: thread})
}

func (d *Daemon) handleShellPromote(w http.ResponseWriter, r *http.Request) {
	var req api.PromoteShellRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Session == "" {
		writeError(w, http.StatusBadRequest, "shell promote: --session is required")
		return
	}
	sessions, err := d.tmux.Info(d.cfg.Machine)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var sess api.TmuxSession
	found := false
	for _, s := range sessions {
		if s.Name == req.Session {
			sess, found = s, true
			break
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "shell promote: no session named "+req.Session+
			" on "+d.cfg.Machine+"'s work server (promotion is work-server-only: every shell feature assumes that socket)")
		return
	}
	if sess.ShellID != "" {
		if th, gerr := d.store.GetThread(sess.ShellID); gerr == nil {
			writeError(w, http.StatusConflict, fmt.Sprintf(
				"shell promote: session %s is already shell thread %q (%s)", req.Session, th.Name, th.ID))
			return
		}
		// STALE marker: the record is gone but the stamp survived — a delete that
		// failed to unstamp. Re-promoting is the repair, but say so loudly.
		log.Printf("shell promote: session %s carried a STALE @sesh-shell-id %s (no such record) — re-promoting over it", req.Session, sess.ShellID)
	}
	if req.Parent != "" {
		if _, err := d.store.GetThread(req.Parent); err != nil {
			writeError(w, http.StatusBadRequest, "shell promote: parent thread not found: "+req.Parent)
			return
		}
	}

	name := req.Name
	if name == "" {
		name = sess.Name
	}
	// The session's START dir is the honest cwd — it is what `new-session -c` was
	// given and does NOT drift when a pane cds away (unlike pane_current_path).
	cwd := sess.Path
	if cwd == "" {
		// A pre-47 peer, or a tmux too old to report session_path. Fall back to
		// the active pane's path and say so — this CAN be wrong if the pane has
		// cd'd, and a silently wrong revival dir is exactly the kind of
		// plausible-but-wrong result that must never pass unremarked.
		for _, win := range sess.Windows {
			for _, p := range win.Panes {
				if p.Active && p.Path != "" {
					cwd = p.Path
				}
			}
		}
		log.Printf("shell promote: session %s reported no session_path; falling back to the active pane's cwd %q, which may have drifted from where the session was started", req.Session, cwd)
	}
	if cwd == "" {
		writeError(w, http.StatusConflict, "shell promote: cannot determine a working directory for session "+req.Session)
		return
	}

	id := uuid.NewString()
	thread := api.Thread{
		ID:            id,
		Machine:       d.cfg.Machine,
		SessionName:   sess.Name,
		Cwd:           cwd,
		AgentKind:     api.ShellAgentKind,
		Name:          name,
		Tags:          []string{},
		CreatedAtUnix: time.Now().Unix(),
		Parent:        req.Parent,
		Notify:        d.defaults.NotifyDefault(),
	}
	if err := d.store.InsertThread(thread); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := d.tmux.StampSessionShellID(sess.Name, id); err != nil {
		if derr := d.store.DeleteThread(id); derr != nil {
			log.Printf("shell promote: stamp failed (%v) and dropping the record failed too (%v)", err, derr)
		}
		writeError(w, http.StatusInternalServerError, "shell promote: stamp marker: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.ThreadResponse{Schema: api.SchemaVersion, Thread: thread})
}

// handleShellSessions lists every live session on this daemon's work server,
// classified. Deliberately NOT part of the mesh snapshot: a session's attached
// bit flips constantly, and folding this into the 1 Hz replicated snapshot would
// destroy delta sync's steady-state-empty property (H44). Callers fan out to it
// on demand.
func (d *Daemon) handleShellSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := d.tmux.Info(d.cfg.Machine)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]api.ShellSession, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, d.classifySession(s))
	}
	writeJSON(w, http.StatusOK, api.ShellSessionsResponse{
		Schema: api.SchemaVersion, Machine: d.cfg.Machine, Sessions: out,
	})
}

// classifySession is the whole classification rule, in one place:
//
//	shell — carries a marker resolving to a record (it IS a shell thread)
//	stale — carries a marker whose record is GONE (a delete that failed to
//	        unstamp; promotable, but a bug worth seeing)
//	agent — hosts at least one @sesh-thread-id-marked pane
//	ghost — none of the above: untracked, the promote target
//
// The pane marker read here is trustworthy ONLY because sesh never stamps
// @sesh-thread-id at session scope; a session-scoped value would be inherited by
// every unmarked pane and turn every ghost into a false `agent`.
func (d *Daemon) classifySession(s api.TmuxSession) api.ShellSession {
	out := api.ShellSession{
		Machine: s.Machine, Name: s.Name, Path: s.Path, Attached: s.Attached,
		Windows: len(s.Windows), Class: api.ShellClassGhost,
	}
	seen := map[string]bool{}
	for _, w := range s.Windows {
		out.Panes += len(w.Panes)
		for _, p := range w.Panes {
			if p.ThreadID != "" && !seen[p.ThreadID] {
				seen[p.ThreadID] = true
				out.AgentThreads = append(out.AgentThreads, p.ThreadID)
			}
		}
	}
	if len(out.AgentThreads) > 0 {
		out.Class = api.ShellClassAgent
	}
	// A shell marker DOMINATES the agent classification: a shell thread's session
	// may legitimately host agent threads (that is what happens when you start an
	// agent inside a box's shell session), and it is still that shell thread's
	// session.
	if s.ShellID != "" {
		out.ThreadID = s.ShellID
		if _, err := d.store.GetThread(s.ShellID); err == nil {
			out.Class = api.ShellClassShell
		} else {
			out.Class = api.ShellClassStale
			log.Printf("shell sessions: session %s carries @sesh-shell-id %s with no matching record (stale marker — a delete that failed to unstamp)", s.Name, s.ShellID)
		}
	}
	return out
}

func (d *Daemon) handleShellInfo(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "shell info: id is required")
		return
	}
	thread, err := d.store.GetThread(id)
	if err != nil {
		if errors.Is(err, store.ErrThreadNotFound) {
			writeError(w, http.StatusNotFound, "thread not found: "+id)
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if thread.AgentKind != api.ShellAgentKind {
		writeError(w, http.StatusConflict, "shell info: "+id+" is a "+thread.AgentKind+" thread, not a shell thread")
		return
	}
	resp := api.ShellInfoResponse{
		Schema: api.SchemaVersion, ID: thread.ID, Machine: thread.Machine,
		Name: thread.Name, Cwd: thread.Cwd,
		Socket:     d.tmux.Socket(),
		SocketPath: d.tmux.SocketPath(),
		TmuxPrefix: "tmux -L " + d.tmux.Socket(),
	}
	sess, live, err := d.tmux.FindSessionByShellID(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if live {
		resp.Live, resp.Session, resp.Attached, resp.Windows = true, sess.Name, sess.Attached, sess.Windows
	}
	writeJSON(w, http.StatusOK, resp)
}

// shellSendTarget resolves which pane of a shell thread's session a send lands
// in: an explicit pane id, an explicit window's active pane, or (default) the
// session's active pane. An address that does not exist in THIS session is a
// loud error — never a fallback to "somewhere else in the session", which would
// put the text in a pane the caller did not ask for.
func shellSendTarget(sess api.TmuxSession, pane string, window *int) (string, error) {
	if pane != "" && window != nil {
		return "", fmt.Errorf("thread send: --pane and --window are alternatives; pass one")
	}
	if pane != "" {
		for _, w := range sess.Windows {
			for _, p := range w.Panes {
				if p.Pane == pane {
					return p.Pane, nil
				}
			}
		}
		return "", fmt.Errorf("thread send: pane %s is not in session %s (list them with `sesh shell panes`)", pane, sess.Name)
	}
	if window != nil {
		for _, w := range sess.Windows {
			if w.Index != *window {
				continue
			}
			for _, p := range w.Panes {
				if p.Active {
					return p.Pane, nil
				}
			}
			if len(w.Panes) > 0 {
				return w.Panes[0].Pane, nil
			}
			return "", fmt.Errorf("thread send: window %d of session %s has no panes", *window, sess.Name)
		}
		return "", fmt.Errorf("thread send: session %s has no window %d", sess.Name, *window)
	}
	for _, w := range sess.Windows {
		for _, p := range w.Panes {
			if p.Active {
				return p.Pane, nil
			}
		}
	}
	return "", fmt.Errorf("thread send: session %s has no active pane", sess.Name)
}

// hostingShellThread resolves the SHELL THREAD whose session hosts the placement
// target of a spawn, for `thread new --parent-shell`. Returns "" when the target
// session is not a shell thread's — a legitimate, common answer (most sessions
// are not), so the caller simply stays a root rather than failing.
//
// A spawn with NO placement makes its own new session, so nothing hosts it.
func (d *Daemon) hostingShellThread(req api.NewThreadRequest) (string, error) {
	sessions, err := d.tmux.Info(d.cfg.Machine)
	if err != nil {
		return "", err
	}
	// The target session name: given directly, or the session owning the target
	// pane/window.
	target := req.IntoSession
	if target == "" {
		paneOrWindow := req.IntoPane
		if paneOrWindow == "" {
			paneOrWindow = req.IntoWindow
		}
		if paneOrWindow == "" {
			return "", nil // no placement: this spawn opens its own session
		}
		for _, sess := range sessions {
			for _, win := range sess.Windows {
				for _, p := range win.Panes {
					if p.Pane == paneOrWindow {
						target = sess.Name
					}
				}
			}
			if target == "" && strings.HasPrefix(paneOrWindow, sess.Name+":") {
				target = sess.Name // a "session:window" target
			}
		}
		if target == "" {
			return "", fmt.Errorf("cannot resolve which session hosts %q", paneOrWindow)
		}
	}
	for _, sess := range sessions {
		if sess.Name != target || sess.ShellID == "" {
			continue
		}
		// A marker whose record is gone must not become a dangling parent id.
		if _, err := d.store.GetThread(sess.ShellID); err != nil {
			log.Printf("thread new --parent-shell: session %s carries a STALE @sesh-shell-id %s (no such record); leaving the new thread at root", target, sess.ShellID)
			return "", nil
		}
		return sess.ShellID, nil
	}
	return "", nil
}
