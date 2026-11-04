package daemon

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/agents/claude"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/store"
)

// mcpLaunchArgs are the launch arguments carrying a thread's MCP arguments to its
// harness (agents/mcparg.go). Called for every start: launchArgs for a headed one,
// startHeadlessTurn for each turn. A thread without any gets no flag and, if it had
// a generated file, loses it.
func (d *Daemon) mcpLaunchArgs(kind agents.Kind, threadID string, servers map[string][]string) ([]string, error) {
	if len(servers) == 0 {
		if err := agents.RemoveMcpConfig(d.cfg.Home, threadID); err != nil {
			return nil, err
		}
		return nil, nil
	}
	switch agents.Family(kind) {
	case agents.Claude:
		globalConfig, err := claude.GlobalConfigPath()
		if err != nil {
			return nil, err
		}
		path, err := agents.WriteClaudeMcpConfig(d.cfg.Home, threadID, globalConfig, servers)
		if err != nil {
			return nil, err
		}
		// The = form: claude's --mcp-config is variadic, and would otherwise take
		// the argument after it, a headless turn's prompt included.
		return []string{"--mcp-config=" + path}, nil
	case agents.Codex:
		codexHome, err := agents.CodexHome(d.cfg.CodexHome)
		if err != nil {
			return nil, err
		}
		return agents.CodexMcpArgs(codexHome, servers)
	case agents.Pi:
		return agents.PiMcpArgs(servers)
	default:
		return nil, fmt.Errorf("NOT IMPLEMENTED: MCP arguments for agent %q", kind)
	}
}

func (d *Daemon) handleThreadMcpArgs(w http.ResponseWriter, r *http.Request) {
	var req api.SetThreadMcpArgsRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "mcp-arg: id is required")
		return
	}
	if len(req.Set) == 0 && len(req.Unset) == 0 {
		writeError(w, http.StatusBadRequest, "mcp-arg: nothing to set or unset")
		return
	}
	if err := api.ValidateMcpArgs(req.Set); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	for _, server := range req.Unset {
		if err := api.ValidateMcpServerName(server); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if _, both := req.Set[server]; both {
			writeError(w, http.StatusBadRequest, "mcp-arg: server "+server+" is both set and unset")
			return
		}
	}
	thread, err := d.store.UpdateThreadMcpArgs(req.ID, req.Set, req.Unset)
	var missing *store.McpServerNotSetError
	switch {
	case errors.As(err, &missing):
		writeError(w, http.StatusNotFound, err.Error())
	case err != nil:
		d.threadOpErr(w, err)
	default:
		writeJSON(w, http.StatusOK, api.ThreadResponse{Schema: api.SchemaVersion, Thread: thread})
	}
}
