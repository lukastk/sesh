package daemon

// Thread env (schema 62, api/env.go): the owner keeps a thread's variables in its
// env files (agents/threadenv.go), every launch and headless turn starts with them,
// and a running agent follows a change where its harness allows. restartAgent
// (setmodel.go) is what applies one everywhere else.

import (
	"fmt"
	"log"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/tmux"
)

func (d *Daemon) routesThreadEnv(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/threads/env", d.handleThreadEnvGet)
	mux.HandleFunc("POST /v1/threads/env", d.handleThreadEnvSet)
	mux.HandleFunc("POST /v1/threads/restart", d.handleThreadRestart)
}

func validateThreadEnv(vars map[string]string) error {
	size := 0
	for k, v := range vars {
		if err := agents.ValidateEnvKey(k); err != nil {
			return err
		}
		if err := agents.ValidateEnvValue(k, v); err != nil {
			return err
		}
		size += len(k) + len(v)
	}
	if size > api.MaxThreadEnvBytes {
		return fmt.Errorf("env: the variables are %d bytes; the limit is %d", size, api.MaxThreadEnvBytes)
	}
	return nil
}

func (d *Daemon) handleThreadEnvGet(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "env: id is required")
		return
	}
	if _, err := d.store.GetThread(id); err != nil {
		writeStoreThreadError(w, id, err)
		return
	}
	e, err := agents.ReadThreadEnv(d.cfg.Home, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.ThreadEnvResponse{Schema: api.SchemaVersion, ID: id, Machine: d.cfg.Machine,
		Vars: e.Vars, Hash: agents.ThreadEnvHash(e.Vars)})
}

func (d *Daemon) handleThreadEnvSet(w http.ResponseWriter, r *http.Request) {
	var req api.SetThreadEnvRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "env: id is required")
		return
	}
	if req.Replace && len(req.Unset) > 0 {
		writeError(w, http.StatusBadRequest, "env: replace and unset are exclusive")
		return
	}
	for _, k := range req.Unset {
		if _, both := req.Set[k]; both {
			writeError(w, http.StatusBadRequest, "env: "+k+" is both set and unset")
			return
		}
	}
	if err := validateThreadEnv(req.Set); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	th, err := d.store.GetThread(req.ID)
	if err != nil {
		writeStoreThreadError(w, req.ID, err)
		return
	}
	if !contextCapable(th.AgentKind) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("env: thread %s is a %s thread, which runs no agent", req.ID, th.AgentKind))
		return
	}

	d.threadEnvMu.Lock()
	defer d.threadEnvMu.Unlock()
	cur, err := agents.ReadThreadEnv(d.cfg.Home, req.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	hash := agents.ThreadEnvHash(cur.Vars)
	if req.ExpectHash != "" && req.ExpectHash != hash && !(req.ExpectHash == "none" && hash == "") {
		writeError(w, http.StatusConflict, "env: the thread's variables changed since they were read (someone else saved them) — reload them and edit again")
		return
	}
	next := map[string]string{}
	if !req.Replace {
		maps.Copy(next, cur.Vars)
	}
	maps.Copy(next, req.Set)
	for _, k := range req.Unset {
		if _, had := next[k]; !had {
			writeError(w, http.StatusBadRequest, "env: "+k+" is not set on this thread")
			return
		}
		delete(next, k)
	}
	if err := validateThreadEnv(next); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if maps.Equal(cur.Vars, next) {
		writeJSON(w, http.StatusOK, api.SetThreadEnvResponse{Schema: api.SchemaVersion, Thread: th, Hash: hash, Detail: "unchanged"})
		return
	}
	removed := cur.Removed
	for k := range cur.Vars {
		if _, kept := next[k]; !kept && !slices.Contains(removed, k) {
			removed = append(removed, k)
		}
	}
	removed = slices.DeleteFunc(removed, func(k string) bool { _, back := next[k]; return back })
	if err := agents.WriteThreadEnv(d.cfg.Home, req.ID, agents.ThreadEnv{Vars: next, Removed: removed}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := d.store.SetThreadEnvCount(req.ID, len(next)); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	th.EnvCount = len(next)
	outcome, detail := d.threadEnvOutcome(th)
	writeJSON(w, http.StatusOK, api.SetThreadEnvResponse{Schema: api.SchemaVersion, Thread: th, Hash: agents.ThreadEnvHash(next),
		Changed: true, Outcome: outcome, Detail: detail})
}

// threadEnvOutcome says how a change reaches th's agent.
func (d *Daemon) threadEnvOutcome(th api.Thread) (string, string) {
	const restart = "; `sesh thread restart` applies it"
	const processNote = "; the agent process and its MCP servers keep their old environment until `sesh thread restart`"
	loc, found, err := d.tmux.FindPaneByThreadID(th.ID)
	if err != nil {
		log.Printf("env: find pane of %s: %v", th.ID, err)
	}
	var proc tmux.AgentProc
	live := false
	if found {
		proc, live = tmux.AgentUnderPane(loc.PanePID)
	}
	if !live {
		if d.turnInFlight(th.ID) {
			return api.EnvNextLaunch, "the headless turn in flight keeps its environment; the next turn has the change"
		}
		return api.EnvNextLaunch, "the next launch or headless turn has the change"
	}
	switch agents.Family(agents.Kind(th.AgentKind)) {
	case agents.Claude:
		// Only a claude launched with the hook can follow: adopted, or launched by a
		// daemon older than thread env.
		if !strings.Contains(proc.Command, "env hook") {
			return api.EnvRestartRequired, "this claude was not launched with sesh's env hook" + restart
		}
		return api.EnvLive, "claude's shell commands have the change from the next command" + processNote
	case agents.Pi:
		return api.EnvLive, "pi's shell commands have the change from its next turn" + processNote
	default:
		return api.EnvRestartRequired, "a running " + th.AgentKind + " cannot take a new environment" + restart
	}
}

func (d *Daemon) handleThreadRestart(w http.ResponseWriter, r *http.Request) {
	var req api.RestartThreadRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "restart: id is required")
		return
	}
	th, err := d.store.GetThread(req.ID)
	if err != nil {
		writeStoreThreadError(w, req.ID, err)
		return
	}
	kind, err := agents.ParseKind(th.AgentKind)
	if err != nil {
		writeError(w, http.StatusBadRequest, "restart: thread "+req.ID+" is a "+th.AgentKind+" thread, which runs no agent")
		return
	}
	loc, found, err := d.tmux.FindPaneByThreadID(th.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	live := false
	if found {
		_, live = tmux.AgentUnderPane(loc.PanePID)
	}
	if !live {
		writeError(w, http.StatusConflict, "restart: thread "+req.ID+" has no running agent; the next launch or headless turn starts fresh anyway (`sesh thread resume`)")
		return
	}
	if snap, ok := d.maint.stateOf(th.ID); ok && snap.Busy == api.BusyBusy && !req.Force {
		writeError(w, http.StatusConflict, "restart: thread "+req.ID+" is mid-turn; wait for it to finish, or pass --force (the restart KILLS the turn in flight)")
		return
	}
	if err := d.restartAgent(th, kind, loc.Pane, th.Model); err != nil {
		writeError(w, http.StatusConflict, "restart: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.RestartThreadResponse{Schema: api.SchemaVersion, Thread: th,
		Detail: "the " + th.AgentKind + " pane was restarted in place; its conversation resumed"})
}

// startThreadEnv prepares thread id's env files for a launch and returns env.sh's
// path. Every launch gets the file, variables or not: a claude only watches a file
// that exists, and one set later has to reach it. A launch starts from the current
// variables, so the removals recorded for the previous process are done with.
func (d *Daemon) startThreadEnv(id string) (string, error) {
	d.threadEnvMu.Lock()
	defer d.threadEnvMu.Unlock()
	e, err := agents.ReadThreadEnv(d.cfg.Home, id)
	if err != nil {
		return "", err
	}
	if err := agents.WriteThreadEnv(d.cfg.Home, id, agents.ThreadEnv{Vars: e.Vars}); err != nil {
		return "", err
	}
	return agents.ThreadEnvScript(d.cfg.Home, id)
}

// withThreadEnv is command, sourcing the thread's env.sh first if it has variables.
// A thread without any launches on the same line as ever.
func (d *Daemon) withThreadEnv(id, command string) (string, error) {
	e, err := agents.ReadThreadEnv(d.cfg.Home, id)
	if err != nil {
		return "", err
	}
	if len(e.Vars) == 0 {
		return command, nil
	}
	script, err := agents.ThreadEnvScript(d.cfg.Home, id)
	if err != nil {
		return "", err
	}
	return agents.WithThreadEnv(script, command), nil
}

// seedThreadEnv writes a new thread's starting variables and returns how many: what
// it inherits, then req.Env over them. A fork continues its source, a delegate's
// worker its caller, and a child its parent, so they keep working in the same
// environment unless the caller opts out.
func (d *Daemon) seedThreadEnv(id string, req api.NewThreadRequest) (int, error) {
	vars := map[string]string{}
	if !req.NoInheritEnv {
		from := req.ForkFrom
		if from == "" {
			from = req.EnvFrom
		}
		if from == "" {
			from = req.Parent
		}
		if from != "" {
			src, err := agents.ReadThreadEnv(d.cfg.Home, from)
			if err != nil {
				return 0, err
			}
			maps.Copy(vars, src.Vars)
		}
	}
	maps.Copy(vars, req.Env)
	if len(vars) == 0 {
		return 0, nil
	}
	if err := agents.WriteThreadEnv(d.cfg.Home, id, agents.ThreadEnv{Vars: vars}); err != nil {
		return 0, err
	}
	return len(vars), nil
}

// dropUnrecordedThreadEnv removes the files of a spawn that left no record: its env
// files and its generated MCP config.
func (d *Daemon) dropUnrecordedThreadEnv(id string, recorded bool) {
	if recorded {
		return
	}
	if err := agents.RemoveThreadEnv(d.cfg.Home, id); err != nil {
		log.Printf("thread %s: remove the env files of a failed spawn: %v", id, err)
	}
	if err := agents.RemoveMcpConfig(d.cfg.Home, id); err != nil {
		log.Printf("thread %s: remove the MCP config of a failed spawn: %v", id, err)
	}
}
