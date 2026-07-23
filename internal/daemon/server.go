package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/lukastk/sesh/internal/api"
)

func (d *Daemon) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", d.handleHealth)
	mux.HandleFunc("GET /v1/status", d.handleStatus)
	mux.HandleFunc("GET /v1/snapshot", d.handleSnapshot)
	mux.HandleFunc("GET /v1/mesh", d.handleMesh)
	mux.HandleFunc("POST /v1/shutdown", d.handleShutdown)
	mux.HandleFunc("GET /v1/tmux/info", d.handleTmuxInfo)
	mux.HandleFunc("POST /v1/tmux/sessions", d.handleTmuxCreateSession)
	mux.HandleFunc("POST /v1/tmux/kill-session", d.handleTmuxKillSession)
	mux.HandleFunc("POST /v1/tmux/panes", d.handleTmuxCreatePane)
	mux.HandleFunc("POST /v1/tmux/send-text", d.handleTmuxSendText)
	mux.HandleFunc("POST /v1/tmux/nav", d.handleTmuxNav)
	mux.HandleFunc("GET /v1/tmux/master-current", d.handleTmuxMasterCurrent)
	mux.HandleFunc("POST /v1/tmux/stage-file", d.handleTmuxStageFile)
	d.routesThreads(mux)
	d.routesThreadOps(mux)
	d.routesReportState(mux)
	d.routesWait(mux)
	d.routesFlag(mux)
	d.routesResume(mux)
	d.routesRealize(mux)
	d.routesPin(mux)
	d.routesGrid(mux)
	d.routesHeadless(mux)
	d.routesHeadful(mux)
	d.routesRPC(mux)
	d.routesTerminal(mux)
	d.routesMaster(mux)
	d.routesTickets(mux)
	d.routesBlobs(mux)
	d.routesFs(mux)
	d.routesPlugins(mux)
	d.routesUIConfig(mux)
	d.routesSubscriptions(mux)
	d.routesPeers(mux)
	d.routesAdopt(mux)
	d.routesDoctor(mux)
	mux.HandleFunc("GET /v1/hooks", d.handleHooksList)
	mux.HandleFunc("POST /v1/hooks/mute", d.handleHooksMute)
	mux.HandleFunc("POST /v1/hooks/test", d.handleHooksTest)
	return mux
}

func (d *Daemon) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"schema": api.SchemaVersion, "ok": true})
}

func (d *Daemon) handleStatus(w http.ResponseWriter, r *http.Request) {
	schemaVer, err := d.store.SchemaVersion()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := api.StatusResponse{
		Schema:        api.SchemaVersion,
		Machine:       d.cfg.Machine,
		PID:           os.Getpid(),
		Version:       Version,
		StartedAtUnix: d.started.Unix(),
		UptimeSeconds: int64(time.Since(d.started).Seconds()),
		DBPath:        d.cfg.DBPath(),
		SocketPath:    d.cfg.SocketPath(),
		SchemaVersion: schemaVer,
		TmuxSocket:    d.cfg.TmuxSocket,
		MasterSocket:  d.cfg.MasterSocket,
		Home:          d.cfg.Home,
		MeshCadence:   d.mesh.cadence(),
	}
	writeJSON(w, http.StatusOK, resp)
}

func (d *Daemon) handleShutdown(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"schema": api.SchemaVersion, "shutting_down": true})
	// Shut down asynchronously so this response can flush first.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		d.Shutdown(ctx)
	}()
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, api.ErrorResponse{Schema: api.SchemaVersion, Error: msg})
}
