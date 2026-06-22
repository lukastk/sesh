package daemon

import (
	"net/http"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
)

// routesUIConfig serves the sesh-ui app's UI preferences, backed by
// <SESH_HOME>/ui_config.toml. GET reads it (defaults applied for missing keys),
// POST replaces it. On the shared router → automatically exposed over the TCP API
// behind the bearer token, so the app reaches it on whichever daemon it connects to.
func (d *Daemon) routesUIConfig(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/ui-config", d.handleUIConfigGet)
	mux.HandleFunc("POST /v1/ui-config", d.handleUIConfigSet)
}

func (d *Daemon) handleUIConfigGet(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadUIConfig(d.cfg.Home)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.UIConfigResponse{Schema: api.SchemaVersion, UIConfig: toAPIUIConfig(cfg)})
}

func (d *Daemon) handleUIConfigSet(w http.ResponseWriter, r *http.Request) {
	var in api.UIConfig
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cfg := fromAPIUIConfig(in)
	// A bad cwd_label regex is a client error (loud 400), not an IO failure.
	if err := config.ValidateUIConfig(cfg); err != nil {
		writeError(w, http.StatusBadRequest, "ui-config: "+err.Error())
		return
	}
	if err := config.SaveUIConfig(d.cfg.Home, cfg); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cfg, err := config.LoadUIConfig(d.cfg.Home)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.UIConfigResponse{Schema: api.SchemaVersion, UIConfig: toAPIUIConfig(cfg)})
}

func toAPIUIConfig(c config.UIConfig) api.UIConfig {
	out := api.UIConfig{CollapseParents: c.CollapseParents, CwdRoots: c.CwdRoots, TranscriptPrefetchSecs: c.TranscriptPrefetchSecs, MasterCommand: c.MasterCommand, DefaultAgent: c.DefaultAgent, DefaultMachine: c.DefaultMachine}
	for _, r := range c.CwdLabels {
		out.CwdLabels = append(out.CwdLabels, api.CwdLabelRule{Match: r.Match, Label: r.Label})
	}
	return out
}

func fromAPIUIConfig(c api.UIConfig) config.UIConfig {
	out := config.UIConfig{CollapseParents: c.CollapseParents, CwdRoots: c.CwdRoots, TranscriptPrefetchSecs: c.TranscriptPrefetchSecs, MasterCommand: c.MasterCommand, DefaultAgent: c.DefaultAgent, DefaultMachine: c.DefaultMachine}
	for _, r := range c.CwdLabels {
		out.CwdLabels = append(out.CwdLabels, config.UICwdLabelRule{Match: r.Match, Label: r.Label})
	}
	return out
}
