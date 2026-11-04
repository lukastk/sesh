package api

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// A thread's MCP arguments (schema 63; design record: _dev/THREAD_MCP_ARGS.md).
// Thread.McpArgs maps an MCP server's name to the arguments this thread adds to
// that server's global entry, in order. The owner applies them every time the
// harness starts: spawn, resume, a restart in place, and every headless turn.
//
// They live on the thread record, so they replicate to peers and are copied by
// backups. They are for paths and switches, never for secrets.

// SetThreadMcpArgsRequest is POST /v1/threads/mcp-args: replace the argument list
// of each server in Set and remove each server in Unset. Other servers are kept.
// A running agent keeps the arguments it started with.
type SetThreadMcpArgsRequest struct {
	ID    string              `json:"id"`
	Set   map[string][]string `json:"set,omitempty"`
	Unset []string            `json:"unset,omitempty"`
}

var mcpServerName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ValidateMcpServerName refuses a name that cannot be one TOML key segment in
// codex's `-c mcp_servers.<server>.args=…`, the strictest of the three harnesses.
func ValidateMcpServerName(server string) error {
	if !mcpServerName.MatchString(server) {
		return fmt.Errorf("mcp-arg: invalid server name %q (letters, digits, - and _ only)", server)
	}
	return nil
}

// ValidateMcpArgs checks a server → arguments map as it arrives from any client.
func ValidateMcpArgs(servers map[string][]string) error {
	for server, args := range servers {
		if err := ValidateMcpServerName(server); err != nil {
			return err
		}
		if len(args) == 0 {
			return fmt.Errorf("mcp-arg: server %q has no arguments (use unset to remove it)", server)
		}
		for _, arg := range args {
			if arg == "" {
				return fmt.Errorf("mcp-arg: server %q has an empty argument", server)
			}
			if strings.ContainsRune(arg, 0) {
				return fmt.Errorf("mcp-arg: an argument of server %q contains a NUL byte", server)
			}
		}
	}
	return nil
}

// ParseMcpArgs turns repeated <server>=<arg> values into a server → arguments map,
// keeping their order per server. The first = separates them, so an argument may
// itself contain one (soliton=--agent-file=/path).
func ParseMcpArgs(values []string) (map[string][]string, error) {
	out := map[string][]string{}
	for _, value := range values {
		server, arg, ok := strings.Cut(value, "=")
		if !ok {
			return nil, fmt.Errorf("mcp-arg: %q is not <server>=<arg>", value)
		}
		out[server] = append(out[server], arg)
	}
	if len(out) == 0 {
		return nil, errors.New("mcp-arg: nothing given")
	}
	if err := ValidateMcpArgs(out); err != nil {
		return nil, err
	}
	return out, nil
}

// SortedMcpServers returns the map's server names in order, so that launch
// arguments built from it are deterministic.
func SortedMcpServers(servers map[string][]string) []string {
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
