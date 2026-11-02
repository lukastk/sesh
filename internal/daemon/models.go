package daemon

import (
	"net/http"
	"sync"

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
