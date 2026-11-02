package daemon

import (
	"context"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
)

// Per-harness DEFAULT models (schema 58): the `model` key of [spawn.<agent>] in
// this daemon's config.toml. A thread created without --model is given the
// default and RECORDS it as its registered model, so a later change to the
// default never silently moves an existing thread to another model.
//
// The default is read FRESH at every thread creation rather than once at start
// (unlike the rest of [spawn]): the file may be edited by `sesh model
// set-default`, by hand, or arrive through a synced symlinked copy, and each of
// those should apply to the next thread without a daemon restart.

// configWriteMu serialises this daemon's own writes to config.toml so two
// concurrent set-default calls cannot interleave their read-edit-write.
var configWriteMu sync.Mutex

func (d *Daemon) routesModels(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/models/defaults", d.handleModelDefaults)
	mux.HandleFunc("POST /v1/models/default", d.handleSetModelDefault)
	mux.HandleFunc("GET /v1/models", d.handleModelList)
	mux.HandleFunc("POST /v1/threads/model", d.handleThreadModel)
}

// defaultModelFor is kind's configured default model, read fresh from
// config.toml (empty = none configured). A broken config is a loud error: the
// caller is creating a thread and must not guess which model it gets.
func (d *Daemon) defaultModelFor(kind agents.Kind) (string, error) {
	sp, err := config.LoadSpawn(d.cfg.Home)
	if err != nil {
		return "", err
	}
	return sp.ModelFor(string(kind)), nil
}

// withDefaultModel returns model, or kind's configured default when model is
// empty — writing an error response and returning ok=false when the default
// cannot be read.
func (d *Daemon) withDefaultModel(w http.ResponseWriter, kind agents.Kind, model string) (string, bool) {
	if model != "" {
		return model, true
	}
	def, err := d.defaultModelFor(kind)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "thread: read the default model: "+err.Error())
		return "", false
	}
	return def, true
}

func (d *Daemon) modelDefaults() (api.ModelDefaultsResponse, error) {
	sp, err := config.LoadSpawn(d.cfg.Home)
	if err != nil {
		return api.ModelDefaultsResponse{}, err
	}
	out := api.ModelDefaultsResponse{
		Schema:     api.SchemaVersion,
		Machine:    d.cfg.Machine,
		ConfigPath: config.ConfigPath(d.cfg.Home),
		Defaults:   map[string]string{},
	}
	if _, err := os.Stat(config.LocalConfigPath(d.cfg.Home)); err == nil {
		out.LocalConfigPath = config.LocalConfigPath(d.cfg.Home)
	}
	for _, a := range config.SpawnModelAgents {
		out.Defaults[a] = sp.ModelFor(a)
	}
	return out, nil
}

func (d *Daemon) handleModelDefaults(w http.ResponseWriter, _ *http.Request) {
	out, err := d.modelDefaults()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSetModelDefault writes (model != "") or removes (model == "") one
// agent's default in config.toml, through a symlink and preserving every other
// line — see config.SetSpawnModel. The model string is opaque, exactly like
// `thread new --model`: a bad one fails loudly at the agent when it is used.
func (d *Daemon) handleSetModelDefault(w http.ResponseWriter, r *http.Request) {
	var req api.SetModelDefaultRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	configWriteMu.Lock()
	wrote, err := config.SetSpawnModel(d.cfg.Home, req.Agent, req.Model)
	configWriteMu.Unlock()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := d.modelDefaults()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out.WrotePath = wrote
	writeJSON(w, http.StatusOK, out)
}

// modelListTTL is how long a harness's model list is reused. Asking claude costs
// a process start and a round trip to its backend (seconds), and the answer
// changes when the account's model access does — rarely. A picker opened twice
// in a row should not pay twice; `refresh=1` asks again.
const modelListTTL = 5 * time.Minute

var modelListCache = struct {
	sync.Mutex
	byAgent map[agents.Kind]api.ModelListResponse
}{byAgent: map[agents.Kind]api.ModelListResponse{}}

// handleModelList answers GET /v1/models?agent=<a>[&refresh=1] by asking the
// harness on THIS machine (see agents.ListModels): which models a harness can
// use is per-machine state (its credentials and config), so a picker for a
// thread asks that thread's owner.
func (d *Daemon) handleModelList(w http.ResponseWriter, r *http.Request) {
	kind, err := agents.ParseKind(r.URL.Query().Get("agent"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "models: "+err.Error())
		return
	}
	refresh := r.URL.Query().Get("refresh") == "1"
	modelListCache.Lock()
	cached, ok := modelListCache.byAgent[kind]
	modelListCache.Unlock()
	if ok && !refresh && time.Since(time.Unix(cached.FetchedAtUnix, 0)) < modelListTTL {
		writeJSON(w, http.StatusOK, cached)
		return
	}
	env := os.Environ()
	if kind == agents.Codex {
		codexHome, err := agents.CodexHome(d.cfg.CodexHome)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		env = append(env, "CODEX_HOME="+codexHome)
	}
	// Under the 15 s client/route-proxy timeout, so a hung harness is reported by
	// the daemon (naming the command) rather than as a bare client timeout.
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	list, err := agents.ListModels(ctx, kind, env, d.cfg.Home)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	out := api.ModelListResponse{
		Schema:         api.SchemaVersion,
		Machine:        d.cfg.Machine,
		Agent:          string(kind),
		HarnessDefault: list.HarnessDefault,
		Models:         make([]api.ModelOption, 0, len(list.Models)),
		FetchedAtUnix:  time.Now().Unix(),
	}
	for _, m := range list.Models {
		out.Models = append(out.Models, api.ModelOption{ID: m.ID, Label: m.Label, Description: m.Description, Resolved: m.Resolved})
	}
	modelListCache.Lock()
	modelListCache.byAgent[kind] = out
	modelListCache.Unlock()
	writeJSON(w, http.StatusOK, out)
}
