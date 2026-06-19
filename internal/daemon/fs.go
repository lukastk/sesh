package daemon

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
)

func (d *Daemon) routesFs(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/fs/list", d.handleFsList)
}

// handleFsList lists the immediate SUBDIRECTORIES of an allow-listed, home-rooted
// path on THIS daemon's host. It is a deliberately generic, policy-free primitive
// (no boxyard/mysetup knowledge): the Obsidian new-thread modal uses it to fill the
// box (~/dev) and mysetup (~/mysetup) cwd pickers on platforms with no local
// filesystem access (mobile), and it is reachable cross-machine over the peer's
// transport so you enumerate the machine you are deploying to.
//
// SECURITY: the resolved path must lie within the daemon user's home dir. A path
// outside home — or one that escapes via "../" — is refused LOUDLY (403), never a
// silent empty listing. Only directories are returned (the pickers want dirs);
// symlinks are not followed (parity with the plugin's old withFileTypes read).
func (d *Daemon) handleFsList(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("path")
	if raw == "" {
		writeError(w, http.StatusBadRequest, "fs list: path is required")
		return
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		writeError(w, http.StatusInternalServerError, "fs list: cannot resolve home dir")
		return
	}
	home = filepath.Clean(home)
	// expandHomeCwd turns a leading ~ into the home dir; a non-~ path passes through.
	abs := expandHomeCwd(raw)
	if !filepath.IsAbs(abs) {
		writeError(w, http.StatusBadRequest, "fs list: path must be absolute or ~-rooted: "+raw)
		return
	}
	abs = filepath.Clean(abs)
	// Allow-list: the cleaned path must be the home dir or strictly under it. This
	// also rejects "~/../etc" (cleans to outside home) — no silent traversal.
	if abs != home && !strings.HasPrefix(abs, home+string(os.PathSeparator)) {
		writeError(w, http.StatusForbidden, "fs list: path is outside the home dir: "+raw)
		return
	}
	ents, err := os.ReadDir(abs)
	if err != nil {
		writeError(w, http.StatusNotFound, "fs list: "+err.Error())
		return
	}
	out := make([]api.FsEntry, 0, len(ents))
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		full := filepath.Join(abs, e.Name())
		out = append(out, api.FsEntry{Name: e.Name(), Path: config.TildeRelative(full, home)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, api.FsListResponse{
		Schema:  api.SchemaVersion,
		Path:    config.TildeRelative(abs, home),
		Entries: out,
	})
}
