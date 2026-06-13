package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/tmux"
)

// handleTmuxMasterCurrent serves GET /v1/tmux/master-current?origin=X: the thread
// the origin master's window is CURRENTLY showing on THIS machine's work server.
// Resolved in-process (the daemon owns the socket + reads its own marker), so it's a
// single fast call the TUI makes asynchronously — never blocking master prefix+s.
func (d *Daemon) handleTmuxMasterCurrent(w http.ResponseWriter, r *http.Request) {
	origin := r.URL.Query().Get("origin")
	if origin == "" {
		writeError(w, http.StatusBadRequest, "master-current: origin is required")
		return
	}
	session, tid, window, err := d.tmux.MarkerClientCurrent(tmux.MasterClientMarker(d.cfg.Home, origin))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.MasterCurrentResponse{Schema: api.SchemaVersion, ThreadID: tid, Session: session, Window: window})
}

// handleTmuxNav serves POST /v1/tmux/nav: the INNER switch-client, run in-process
// against THIS daemon's own work tmux server. This is what lets an http peer's nav
// skip the ssh hop entirely — the remote daemon already owns the socket and can read
// its own master-client marker. Identical logic to the local/ssh InnerSwitchScript,
// just executed here.
func (d *Daemon) handleTmuxNav(w http.ResponseWriter, r *http.Request) {
	var req api.NavRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Session == "" || req.Origin == "" {
		writeError(w, http.StatusBadRequest, "nav: session and origin are required")
		return
	}
	windowStr := ""
	if req.Window != nil && *req.Window >= 0 {
		windowStr = strconv.Itoa(*req.Window)
	}
	script := tmux.InnerSwitchScript(d.cfg.TmuxSocket, req.Session, req.ThreadID, windowStr, tmux.MasterClientMarker(d.cfg.Home, req.Origin))
	if out, err := exec.Command("sh", "-c", script).CombinedOutput(); err != nil {
		writeError(w, http.StatusConflict, fmt.Sprintf("nav inner switch: %v: %s", err, out))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema": api.SchemaVersion, "switched": true})
}

func (d *Daemon) handleTmuxInfo(w http.ResponseWriter, r *http.Request) {
	sessions, err := d.tmux.Info(d.cfg.Machine)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if filter := r.URL.Query().Get("session"); filter != "" {
		filtered := sessions[:0]
		for _, s := range sessions {
			if s.Name == filter {
				filtered = append(filtered, s)
			}
		}
		sessions = filtered
	}
	writeJSON(w, http.StatusOK, api.TmuxInfoResponse{Schema: api.SchemaVersion, Sessions: sessions})
}

func (d *Daemon) handleTmuxCreateSession(w http.ResponseWriter, r *http.Request) {
	var req api.CreateSessionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := d.tmux.CreateSession(req.Name, req.Dir, req.Env); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.CreateSessionResponse{Schema: api.SchemaVersion, Session: req.Name})
}

func (d *Daemon) handleTmuxCreatePane(w http.ResponseWriter, r *http.Request) {
	var req api.CreatePaneRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	pane, err := d.tmux.CreatePane(req.Target, req.Dir)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.CreatePaneResponse{Schema: api.SchemaVersion, Pane: pane})
}

func (d *Daemon) handleTmuxSendText(w http.ResponseWriter, r *http.Request) {
	var req api.SendTextRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := d.tmux.SendText(req.Target, req.Text, req.Enter); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema": api.SchemaVersion, "sent": true})
}

// handleTmuxStageFile writes the posted bytes into a per-daemon staging dir and
// returns the absolute path on this machine. This works identically for local
// and (later) mesh-forwarded remote staging: bytes in, path out.
func (d *Daemon) handleTmuxStageFile(w http.ResponseWriter, r *http.Request) {
	var req api.StageFileRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "stage-file: empty name")
		return
	}
	stageDir := filepath.Join(d.cfg.Home, "staged")
	if err := os.MkdirAll(stageDir, 0o700); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Keep only the base name to avoid path traversal from the requested name.
	dest := filepath.Join(stageDir, filepath.Base(req.Name))
	if err := os.WriteFile(dest, req.Content, 0o600); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.StageFileResponse{Schema: api.SchemaVersion, Path: dest})
}

func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("decode request body: %w", err)
	}
	return nil
}
