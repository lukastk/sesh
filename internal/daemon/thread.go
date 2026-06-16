package daemon

import (
	"errors"
	"fmt"
	"github.com/lukastk/sesh/internal/config"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/store"
	"github.com/lukastk/sesh/internal/tmux"
)

func (d *Daemon) routesThreads(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/threads", d.handleThreadNew)
	mux.HandleFunc("GET /v1/threads", d.handleThreadList)
	mux.HandleFunc("GET /v1/threads/pane", d.handleThreadPane)
	mux.HandleFunc("GET /v1/threads/capture", d.handleThreadCapture)
	mux.HandleFunc("GET /v1/threads/status", d.handleThreadStatus)
	mux.HandleFunc("POST /v1/threads/send", d.handleThreadSend)
}

// handleThreadSend delivers a message into a headed thread's LIVE pane (resolved
// from the marker) and submits it. A thread with no live pane is dead — sending
// is a loud error, not a silent no-op.
func (d *Daemon) handleThreadSend(w http.ResponseWriter, r *http.Request) {
	var req api.ThreadSendRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == "" || req.Text == "" {
		writeError(w, http.StatusBadRequest, "thread send: id and text are required")
		return
	}
	if _, err := d.store.GetThread(req.ID); err != nil {
		if errors.Is(err, store.ErrThreadNotFound) {
			writeError(w, http.StatusNotFound, "thread not found: "+req.ID)
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	loc, found, err := d.tmux.FindPaneByThreadID(req.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusConflict, "thread has no live pane (dead); cannot send")
		return
	}
	// Expand @blob(…) references to absolute paths; an unknown blob is a loud 400.
	text, err := d.expandPrompt(req.Text)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := d.tmux.SendText(loc.Pane, text, true); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema": api.SchemaVersion, "sent": req.ID})
}

// handleThreadNew spawns a headed thread: a real agent in a real tmux session,
// the pane stamped with the thread's @sesh-thread-id marker, SESH_THREAD_ID
// injected into the pane environment. Headless lands in a later phase and is an
// explicit NOT IMPLEMENTED error here (never a silent headed fallback).
func (d *Daemon) handleThreadNew(w http.ResponseWriter, r *http.Request) {
	var req api.NewThreadRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// A leading ~ is resolved against THIS (the owner) daemon's home, so a
	// portable ~-relative cwd from a remote client (e.g. the Obsidian plugin
	// deploying onto this machine) lands at the right absolute path on the
	// machine that actually runs the agent. Done once, up front, so every
	// spawn path (headed/headless/fork) sees the expanded cwd.
	req.Cwd = expandHomeCwd(req.Cwd)
	kind, err := agents.ParseKind(req.Agent)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// An empty name is allowed (a nameless thread): the session name comes from a
	// [[session_name]] rule or the cwd, not the name; sanitizeName falls back to
	// "thread" for the default sesh_<name>. The name is display-only otherwise.
	// cwd is required (absolute) for every mode EXCEPT --into-pane, which inherits
	// the existing pane's cwd when none is given.
	if req.IntoPane == "" {
		if req.Cwd == "" || !strings.HasPrefix(req.Cwd, "/") {
			writeError(w, http.StatusBadRequest, "thread: cwd must be an absolute path")
			return
		}
	} else if req.Cwd != "" && !strings.HasPrefix(req.Cwd, "/") {
		writeError(w, http.StatusBadRequest, "thread: cwd must be an absolute path")
		return
	}
	if req.Parent != "" {
		if _, err := d.store.GetThread(req.Parent); err != nil {
			writeError(w, http.StatusBadRequest, "thread: parent "+req.Parent+" does not exist")
			return
		}
	}
	if req.ForkFrom != "" {
		d.newForkedThread(w, kind, req)
		return
	}
	if req.Headless {
		d.newHeadlessThread(w, kind, req)
		return
	}
	// Placement modes are headed-only and mutually exclusive.
	if nSet(req.IntoSession != "", req.IntoWindow != "", req.IntoPane != "") > 1 {
		writeError(w, http.StatusBadRequest, "thread: --into-session, --into-window and --into-pane are mutually exclusive")
		return
	}
	if req.IntoPane != "" {
		d.newThreadIntoPane(w, kind, req)
		return
	}

	id := uuid.NewString()

	env := map[string]string{agents.EnvThreadID: id}
	if err := d.prepCodexEnv(kind, env, req.Cwd); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Capture the agent session id at spawn for pi/claude (what resume needs);
	// codex mints its own on the first turn and is discovered later.
	agentSessionID := ""
	if kind == agents.Pi || kind == agents.Claude {
		agentSessionID = uuid.NewString()
	}

	mode, err := d.resolveSpawnMode(kind, req.Mode)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	command := agents.HeadedCommand(kind, agentSessionID, mode, d.spawn.ArgsFor(string(kind)))

	// Resolve placement -> the thread's session_name + its (marked) pane. teardown
	// undoes ONLY what THIS spawn created (its own session, or its window/split
	// pane), so threads sharing a session are never harmed.
	var session, pane string
	teardown := func() {}
	switch {
	case req.IntoSession != "":
		if !d.tmux.HasSession(req.IntoSession) {
			writeError(w, http.StatusNotFound, "thread: --into-session "+req.IntoSession+" does not exist")
			return
		}
		session = req.IntoSession
		p, werr := d.tmux.CreateWindowCmd(session, req.Cwd, env, command)
		if werr != nil {
			writeError(w, http.StatusInternalServerError, werr.Error())
			return
		}
		pane = p
		teardown = func() { d.tmux.KillPane(pane) } //nolint:errcheck
	case req.IntoWindow != "":
		p, serr := d.tmux.SplitWindowCmd(req.IntoWindow, req.Cwd, env, command)
		if serr != nil {
			writeError(w, http.StatusInternalServerError, serr.Error())
			return
		}
		pane = p
		teardown = func() { d.tmux.KillPane(pane) } //nolint:errcheck
		info, found, ferr := d.tmux.FindPaneByID(pane)
		if ferr != nil || !found {
			teardown()
			writeError(w, http.StatusInternalServerError, "thread: cannot resolve the session of split pane "+pane)
			return
		}
		session = info.Session
	default:
		s, serr := d.sessionNameFor(req.Cwd, id, req.Name)
		if serr != nil {
			writeError(w, http.StatusInternalServerError, serr.Error())
			return
		}
		if d.tmux.HasSession(s) {
			writeError(w, http.StatusConflict, "thread: tmux session "+s+" already exists")
			return
		}
		if err := d.tmux.CreateSessionCmd(s, req.Cwd, env, command); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		session = s
		teardown = func() { d.tmux.KillSession(session) } //nolint:errcheck
		p, perr := d.tmux.SessionFirstPane(session)
		if perr != nil {
			teardown()
			writeError(w, http.StatusInternalServerError, perr.Error())
			return
		}
		pane = p
	}

	// Stamp the marker on the thread's pane so it is resolvable at runtime
	// (never stored).
	if err := d.tmux.SetPaneThreadID(pane, id); err != nil {
		teardown()
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	thread := api.Thread{
		ID:             id,
		Machine:        d.cfg.Machine,
		SessionName:    session,
		Cwd:            req.Cwd,
		AgentKind:      string(kind),
		Name:           req.Name,
		Tags:           []string{},
		CreatedAtUnix:  time.Now().Unix(),
		AgentSessionID: agentSessionID,
		Parent:         req.Parent,
		Notify:         d.defaults.NotifyDefault(),
		// A headed spawn BEGINS the conversation (the agent launches with this
		// session id) — so a later headless turn on the idle thread must RESUME,
		// not create. See api.Thread.HeadlessStarted.
		HeadlessStarted: true,
	}
	if err := d.store.InsertThread(thread); err != nil {
		teardown() // keep store and runtime consistent
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if req.Msg != "" {
		go d.sendWhenReady(thread, req.Msg)
	}
	writeJSON(w, http.StatusOK, api.ThreadResponse{Schema: api.SchemaVersion, Thread: thread})
}

// nSet counts the true booleans (placement modes are mutually exclusive).
func nSet(bs ...bool) int {
	n := 0
	for _, b := range bs {
		if b {
			n++
		}
	}
	return n
}

// newThreadIntoPane implements `thread new --into-pane`: register-then-exec. The
// daemon does NOT spawn — it binds the thread to an EXISTING shell pane (record +
// pre-minted session id + pane marker) and returns the exact agent command + env
// for the caller to exec in place, so the agent becomes that pane's own process.
// The pane must be a live work-server pane holding no agent and no thread.
func (d *Daemon) newThreadIntoPane(w http.ResponseWriter, kind agents.Kind, req api.NewThreadRequest) {
	info, found, err := d.tmux.FindPaneByID(req.IntoPane)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "thread: --into-pane "+req.IntoPane+" is not on sesh's work server (placement is work-server-only)")
		return
	}
	if info.ThreadID != "" {
		writeError(w, http.StatusConflict, "thread: pane "+req.IntoPane+" already belongs to thread "+info.ThreadID)
		return
	}
	if agent, running := tmux.AgentUnderPane(info.PanePID); running {
		writeError(w, http.StatusConflict, "thread: pane "+req.IntoPane+" already has a "+agent.Kind+" running — use `thread adopt` to manage it")
		return
	}

	cwd := req.Cwd
	if cwd == "" {
		cwd = info.Cwd
	}
	id := uuid.NewString()
	env := map[string]string{agents.EnvThreadID: id}
	if err := d.prepCodexEnv(kind, env, cwd); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	agentSessionID := ""
	if kind == agents.Pi || kind == agents.Claude {
		agentSessionID = uuid.NewString()
	}
	mode, err := d.resolveSpawnMode(kind, req.Mode)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	command := agents.HeadedCommand(kind, agentSessionID, mode, d.spawn.ArgsFor(string(kind)))

	thread := api.Thread{
		ID:              id,
		Machine:         d.cfg.Machine,
		SessionName:     info.Session,
		Cwd:             cwd,
		AgentKind:       string(kind),
		Name:            req.Name,
		Tags:            []string{},
		CreatedAtUnix:   time.Now().Unix(),
		AgentSessionID:  agentSessionID,
		Parent:          req.Parent,
		Notify:          d.defaults.NotifyDefault(),
		HeadlessStarted: true,
	}
	// Insert before stamping (no stamped-but-rowless pane on failure).
	if err := d.store.InsertThread(thread); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := d.tmux.SetPaneThreadID(info.Pane, id); err != nil {
		d.store.DeleteThread(id) //nolint:errcheck
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.ThreadResponse{
		Schema:        api.SchemaVersion,
		Thread:        thread,
		LaunchCommand: command,
		LaunchEnv:     env,
	})
}

// sendWhenReady delivers a spawn's --msg once the agent is READY: the pane's
// agent process is up and the TUI has rendered (≥3 non-blank lines) — the
// blank-pane race that loses keystrokes never happens. Loud log on timeout.
func (d *Daemon) sendWhenReady(th api.Thread, text string) {
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		loc, found, err := d.tmux.FindPaneByThreadID(th.ID)
		if err == nil && found {
			if agent, running := tmux.AgentUnderPane(loc.PanePID); running && agent.Kind == th.AgentKind {
				if cap, cerr := d.tmux.CapturePane(loc.Pane); cerr == nil && nonBlank(cap) >= 3 {
					if serr := d.tmux.SendText(loc.Pane, text, true); serr != nil {
						log.Printf("thread %s: --msg delivery failed: %v", th.ID, serr)
					}
					return
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	log.Printf("thread %s: --msg never delivered (agent not ready within 90s)", th.ID)
}

func nonBlank(s string) int {
	n := 0
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n
}

func (d *Daemon) handleThreadList(w http.ResponseWriter, r *http.Request) {
	includeArchived := r.URL.Query().Get("archived") == "1"
	threads, err := d.store.ListThreads(includeArchived)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := api.ThreadListResponse{Schema: api.SchemaVersion}
	if r.URL.Query().Get("all-machines") == "1" {
		// Mesh fan-out: this machine's threads + every reachable peer's.
		threads, resp.Unreachable = d.fanOutThreads(threads, includeArchived)
	}
	if threads == nil {
		threads = []api.Thread{}
	}
	resp.Threads = threads
	writeJSON(w, http.StatusOK, resp)
}

func (d *Daemon) handleThreadPane(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "thread pane: id is required")
		return
	}
	loc, found, err := d.tmux.FindPaneByThreadID(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.ResolvePaneResponse{Schema: api.SchemaVersion, Found: found, Pane: loc})
}

// handleThreadCapture returns the live text of a thread's pane (capture-pane). A
// thread with no live pane is dead — capturing is a loud 409, never an empty string.
func (d *Daemon) handleThreadCapture(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "thread capture: id is required")
		return
	}
	lines := 0
	if s := r.URL.Query().Get("lines"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "thread capture: lines must be a non-negative integer")
			return
		}
		lines = n
	}
	if _, err := d.store.GetThread(id); err != nil {
		if errors.Is(err, store.ErrThreadNotFound) {
			writeError(w, http.StatusNotFound, "thread not found: "+id)
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	loc, found, err := d.tmux.FindPaneByThreadID(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusConflict, "thread has no live pane (dead); cannot capture")
		return
	}
	content, err := d.tmux.CapturePaneLines(loc.Pane, lines)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.ThreadCaptureResponse{
		Schema:  api.SchemaVersion,
		ID:      id,
		Lines:   lines,
		Content: content,
	})
}

// The content-diff probe samples a pane repeatedly and declares working when
// change is SUSTAINED (>= activityChangedNeeded intervals), not a one-off. It
// early-exits the instant it has enough changes, so detecting a busy agent is
// fast; only confirming IDLE runs the full window. The window is long enough to
// catch the slowest working animation (codex's ~1s "thinking" timer), while
// requiring >1 change still rejects a single idle blip (a rotating hint, an MCP
// server finishing startup). When settled, all three agent TUIs are byte-stable.
const (
	activityMaxSamples    = 10                     // up to ~3s of confirming idle
	activityInterval      = 300 * time.Millisecond //
	activityChangedNeeded = 2                      // changed intervals => working
)

// handleThreadStatus resolves a thread's LIVE runtime state (never stored) as
// two ORTHOGONAL axes (Phase 3b decision; see _dev/SPEC.md §3):
//
//   - Activity   {working|waiting|dead}: dead = no live agent under a marked
//     pane; otherwise working/waiting from a pane content-diff probe.
//   - Attachment {attached|detached}: from `tmux list-clients`.
//
// They are independent: a detached agent can still be working, and an idle agent
// still needs input whether or not anyone is attached.
func (d *Daemon) handleThreadStatus(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "thread status: id is required")
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

	resp := api.ThreadStatusResponse{Schema: api.SchemaVersion, ID: id}

	loc, found, err := d.tmux.FindPaneByThreadID(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		// No pane: headless; busy iff a turn is in flight. Necessarily detached.
		resp.Head = api.Headless
		resp.Busy = api.BusyIdle
		if d.turnInFlight(id) {
			resp.Busy = api.BusyBusy
		}
		resp.Attachment = api.Detached
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.Head = api.Headful
	resp.Pane = loc.Pane

	// Attachment axis (independent of activity).
	clients, err := d.tmux.ClientCount(loc.Session)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Clients = clients
	resp.Attachment = api.Detached
	if clients > 0 {
		resp.Attachment = api.Attached
	}

	// Activity axis.
	agent, running := tmux.AgentUnderPane(loc.PanePID)
	resp.AgentRunning = running && agent.Kind == thread.AgentKind
	if !resp.AgentRunning {
		// A marked pane but no live agent of the right kind: no runtime —
		// headless·idle (revivable).
		resp.Head = api.Headless
		resp.Busy = api.BusyIdle
		writeJSON(w, http.StatusOK, resp)
		return
	}
	working, err := d.paneChanging(loc.Pane)
	if err != nil {
		// The pane vanished mid-probe (e.g. the session was killed concurrently).
		// An unreachable pane is no runtime, not a server error.
		resp.Head = api.Headless
		resp.Busy = api.BusyIdle
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.Busy = api.BusyIdle
	if working {
		resp.Busy = api.BusyBusy
	}
	writeJSON(w, http.StatusOK, resp)
}

// resolveState computes the two runtime axes for a thread — the signals ticket
// needs-input/needs-revival depend on. Shared with the status endpoint's logic.
func (d *Daemon) resolveState(thread api.Thread) (api.Head, api.Busy, error) {
	loc, found, err := d.tmux.FindPaneByThreadID(thread.ID)
	if err != nil {
		return "", "", err
	}
	if !found {
		if d.turnInFlight(thread.ID) {
			return api.Headless, api.BusyBusy, nil
		}
		return api.Headless, api.BusyIdle, nil
	}
	agent, running := tmux.AgentUnderPane(loc.PanePID)
	if !(running && agent.Kind == thread.AgentKind) {
		return api.Headless, api.BusyIdle, nil
	}
	working, err := d.paneChanging(loc.Pane)
	if err != nil {
		return api.Headless, api.BusyIdle, nil // pane vanished mid-probe => no runtime
	}
	if working {
		return api.Headful, api.BusyBusy, nil
	}
	return api.Headful, api.BusyIdle, nil
}

// paneChanging samples a pane's visible content several times and reports whether
// it is SUSTAINEDLY changing — the agent-agnostic "the agent is producing output /
// its TUI is animating a turn" signal that distinguishes working from waiting.
// A single idle blip (one changed interval) reads as waiting; a real turn (its
// spinner/output animating) changes a majority of intervals.
func (d *Daemon) paneChanging(pane string) (bool, error) {
	prev, err := d.tmux.CapturePane(pane)
	if err != nil {
		return false, err
	}
	changed := 0
	for i := 0; i < activityMaxSamples; i++ {
		time.Sleep(activityInterval)
		cur, err := d.tmux.CapturePane(pane)
		if err != nil {
			return false, err
		}
		if cur != prev {
			changed++
			if changed >= activityChangedNeeded {
				return true, nil // early exit: a busy agent is detected fast
			}
		}
		prev = cur
	}
	return false, nil
}

// expandHomeCwd expands a leading ~ (or ~/…) in a thread cwd against THIS
// daemon's home directory. A portable ~-relative path (the form the Obsidian
// plugin sends when deploying onto another machine) thus resolves against the
// OWNER's home — the home of the machine that runs the agent — not the
// caller's. A cwd with no leading ~ is returned unchanged; if the home can't be
// resolved the ~-path is left as-is so the caller's absolute-path check rejects
// it loudly rather than guessing.
func expandHomeCwd(cwd string) string {
	if cwd != "~" && !strings.HasPrefix(cwd, "~/") {
		return cwd
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return cwd
	}
	return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(cwd, "~"), "/"))
}

// sanitizeName maps a thread name to a tmux-safe session suffix. tmux session
// names may not contain ":" or "." and we avoid whitespace; everything else
// becomes "_".
// sessionNameFor resolves a thread's tmux session name: the user's
// [[session_name]] config policy when a rule matches the cwd, else the default
// sesh_<sanitized-name> convention. Policy errors (bad placeholder, empty
// expansion) are loud — never a silently wrong name.
func (d *Daemon) sessionNameFor(cwd, threadID, threadName string) (string, error) {
	home, _ := os.UserHomeDir()
	if name, matched, err := d.naming.SessionNameFor(cwd, threadID, threadName, home); err != nil {
		return "", err
	} else if matched {
		return name, nil
	}
	return "sesh_" + sanitizeName(threadName), nil
}

func sanitizeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	s := b.String()
	if s == "" {
		s = "thread"
	}
	return s
}

// resolveSpawnMode resolves the launch mode: the request's override (a CLI
// --yolo/--sandbox) wins over the [spawn] config. pi cannot be sandboxed —
// loud (yolo ≡ default for pi: it is always full-access).
func (d *Daemon) resolveSpawnMode(kind agents.Kind, override string) (string, error) {
	mode := override
	if mode == "" {
		mode = d.spawn.ModeFor(string(kind))
	}
	valid := mode == ""
	for _, m := range config.SpawnModes {
		if mode == m {
			valid = true
		}
	}
	if !valid {
		return "", fmt.Errorf("spawn: unknown mode %q (valid: %v)", mode, config.SpawnModes)
	}
	if kind == agents.Pi && mode == "sandbox" {
		return "", fmt.Errorf("spawn: pi cannot be sandboxed (no permission system) — yolo/default only")
	}
	return mode, nil
}
