package daemon

// The flagged system's manual surface (api schema 44, ticket df4fb07a): POST
// /v1/threads/flag applies one of on|off|disable|enable. Auto-flagging lives
// in the maintainer (autoflag.go); this endpoint is the user's toggle.

import (
	"net/http"

	"github.com/lukastk/sesh/internal/api"
)

func (d *Daemon) routesFlag(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/threads/flag", d.handleThreadFlag)
}

// heuristicFlagAllowed: may a HEURISTIC busy→idle edge auto-flag this agent
// kind? ([flags] heuristic_agents; reported edges always flag.)
func (d *Daemon) heuristicFlagAllowed(agentKind string) bool {
	return d.flags.HeuristicAgents[agentKind]
}

func (d *Daemon) handleThreadFlag(w http.ResponseWriter, r *http.Request) {
	var req api.FlagThreadRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch req.Action {
	case api.FlagOn, api.FlagOff, api.FlagDisable, api.FlagEnable:
	default:
		writeError(w, http.StatusBadRequest, "flag: unknown action "+req.Action+" (want on|off|disable|enable)")
		return
	}
	th, err := d.store.GetThread(req.ID)
	if err != nil {
		d.threadOpErr(w, err)
		return
	}
	// A divider is a visual rule — flagging it is meaningless. Virtual groups
	// ARE flaggable (a user may mark a whole group for attention manually).
	if th.AgentKind == api.DividerAgentKind {
		writeError(w, http.StatusConflict, "flag: "+th.ID+" is a divider — it can't need attention")
		return
	}
	if err := d.store.SetThreadFlagAction(req.ID, req.Action, ""); err != nil {
		d.threadOpErr(w, err)
		return
	}
	d.respondThread(w, req.ID)
}
