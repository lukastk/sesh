package daemon

import (
	"net/http"

	"github.com/lukastk/sesh/internal/api"
)

func (d *Daemon) routesHeadful(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/threads/headful", d.handleThreadHeadful)
}

// handleThreadHeadful is `thread headful` — under the unified model it IS the
// revive operation (an idle thread gains a pane resuming its conversation),
// identical to `thread resume`; both verbs are kept because both readings are
// natural ("promote this headless thread" / "resume this dead thread").
func (d *Daemon) handleThreadHeadful(w http.ResponseWriter, r *http.Request) {
	var req api.ThreadHeadfulRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "headful: id is required")
		return
	}
	d.reviveThread(w, req.ID)
}
