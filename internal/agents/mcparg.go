package agents

// Per-thread MCP arguments (api/mcparg.go, _dev/THREAD_MCP_ARGS.md). A harness has
// one global entry per MCP server, installed once per machine. A thread adds
// arguments to that entry for its own processes only, and the global entry is
// never written. Per harness, the thread's arguments are APPENDED to the global
// entry's:
//
//   - claude: --mcp-config=<generated file>. A same-named entry there replaces the
//     global entry whole (measured: its env did not survive), so the file repeats
//     the global command and args. It is not allowed to repeat an env block, because
//     generated files hold no secrets: a global entry with one is refused.
//   - codex: -c mcp_servers.<server>.args=[…]. The override replaces the args array
//     (measured) and leaves the rest of the entry, so it repeats the global args.
//   - pi / mypi: --sesh-mcp-args=<json>, read by the sesh-agent-state extension
//     (integrations/pi/sesh-agent-state/mcp.ts), which owns the lookup of the global
//     entry in the agent dir's mcp.json. The `=` form is required: pi takes a
//     following word as an extension flag's value only if it does not start with -,
//     and pi keeps only the last of a repeated flag, hence one JSON value.
//
// Every argument is a separate argv element: headless turns exec it directly and
// headed launch lines pass it through ShellEscape.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/lukastk/sesh/internal/api"
)

// PiMcpArgsFlag is the extension flag carrying a thread's arguments to pi and mypi.
const PiMcpArgsFlag = "sesh-mcp-args"

const (
	// McpDirName is the directory under SESH_HOME with one folder per thread.
	McpDirName    = "thread-mcp"
	mcpConfigName = "mcp.json"
)

// McpDir is thread id's folder. The id is checked because the path is removed
// recursively: an empty or path-like id must never resolve outside it.
func McpDir(seshHome, threadID string) (string, error) {
	if seshHome == "" {
		return "", errors.New("mcp-arg: empty sesh home")
	}
	if threadID == "" || threadID == "." || threadID == ".." || strings.ContainsAny(threadID, `/\`) {
		return "", fmt.Errorf("mcp-arg: invalid thread id %q", threadID)
	}
	return filepath.Join(seshHome, McpDirName, threadID), nil
}

// RemoveMcpConfig removes thread id's folder. A thread that has none is not an error.
func RemoveMcpConfig(seshHome, threadID string) error {
	dir, err := McpDir(seshHome, threadID)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("mcp-arg: %w", err)
	}
	return nil
}

// claudeMcpEntry is what a generated file holds for one server.
type claudeMcpEntry struct {
	Type    string   `json:"type"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// claudeGlobalMcpEntry reads server's user-scope entry from claude's global config.
func claudeGlobalMcpEntry(globalConfig, server string) (claudeMcpEntry, error) {
	raw, err := os.ReadFile(globalConfig)
	if err != nil {
		return claudeMcpEntry{}, fmt.Errorf("mcp-arg: claude's global config: %w", err)
	}
	var cfg struct {
		McpServers map[string]struct {
			Type    string            `json:"type"`
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return claudeMcpEntry{}, fmt.Errorf("mcp-arg: %s: %w", globalConfig, err)
	}
	entry, ok := cfg.McpServers[server]
	switch {
	case !ok:
		return claudeMcpEntry{}, fmt.Errorf("mcp-arg: claude has no global MCP server %q in %s", server, globalConfig)
	case entry.Type != "" && entry.Type != "stdio":
		return claudeMcpEntry{}, fmt.Errorf("mcp-arg: claude's global MCP server %q is %s, and only a stdio server takes arguments", server, entry.Type)
	case entry.Command == "":
		return claudeMcpEntry{}, fmt.Errorf("mcp-arg: claude's global MCP server %q has no command", server)
	case len(entry.Env) > 0:
		return claudeMcpEntry{}, fmt.Errorf("mcp-arg: claude's global MCP server %q has an env block, which would have to be copied into a generated file; sesh writes no such values to disk, so move them out of the entry", server)
	}
	return claudeMcpEntry{Type: "stdio", Command: entry.Command, Args: entry.Args}, nil
}

// codexGlobalMcpArgs reads server's args from codex's config.toml.
func codexGlobalMcpArgs(codexHome, server string) ([]string, error) {
	path := filepath.Join(codexHome, "config.toml")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("mcp-arg: codex's config: %w", err)
	}
	var cfg struct {
		McpServers map[string]struct {
			Command string   `toml:"command"`
			Args    []string `toml:"args"`
			URL     string   `toml:"url"`
		} `toml:"mcp_servers"`
	}
	if _, err := toml.Decode(string(raw), &cfg); err != nil {
		return nil, fmt.Errorf("mcp-arg: %s: %w", path, err)
	}
	entry, ok := cfg.McpServers[server]
	switch {
	case !ok:
		return nil, fmt.Errorf("mcp-arg: codex has no global MCP server %q in %s", server, path)
	case entry.URL != "":
		return nil, fmt.Errorf("mcp-arg: codex's global MCP server %q is a URL server, and only a stdio server takes arguments", server)
	case entry.Command == "":
		return nil, fmt.Errorf("mcp-arg: codex's global MCP server %q has no command", server)
	}
	return entry.Args, nil
}

// CodexMcpArgs are the -c flags adding servers' arguments to one codex process.
func CodexMcpArgs(codexHome string, servers map[string][]string) ([]string, error) {
	var out []string
	for _, server := range api.SortedMcpServers(servers) {
		global, err := codexGlobalMcpArgs(codexHome, server)
		if err != nil {
			return nil, err
		}
		items := make([]string, 0, len(global)+len(servers[server]))
		for _, arg := range append(append([]string{}, global...), servers[server]...) {
			items = append(items, tomlString(arg))
		}
		out = append(out, "-c", "mcp_servers."+server+".args=["+strings.Join(items, ",")+"]")
	}
	return out, nil
}

// WriteClaudeMcpConfig writes thread id's generated file (0600, in a 0700 folder)
// and returns its path. It is rewritten at every launch, so a change to the global
// entry or to the thread's arguments applies at the next one.
func WriteClaudeMcpConfig(seshHome, threadID, globalConfig string, servers map[string][]string) (string, error) {
	entries := map[string]claudeMcpEntry{}
	for server, args := range servers {
		entry, err := claudeGlobalMcpEntry(globalConfig, server)
		if err != nil {
			return "", err
		}
		entry.Args = append(append([]string{}, entry.Args...), args...)
		entries[server] = entry
	}
	raw, err := json.MarshalIndent(map[string]any{"mcpServers": entries}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("mcp-arg: %w", err)
	}
	dir, err := McpDir(seshHome, threadID)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("mcp-arg: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("mcp-arg: %w", err)
	}
	// A unique temporary file: two launches of one thread may overlap. CreateTemp
	// makes it 0600, so the content is never readable by anyone else.
	tmp, err := os.CreateTemp(dir, mcpConfigName+".*")
	if err != nil {
		return "", fmt.Errorf("mcp-arg: %w", err)
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // fails once the rename has happened
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close() //nolint:errcheck
		return "", fmt.Errorf("mcp-arg: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("mcp-arg: %w", err)
	}
	path := filepath.Join(dir, mcpConfigName)
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", fmt.Errorf("mcp-arg: %w", err)
	}
	return path, nil
}

// PiMcpArgs is pi's and mypi's launch argument. One argv element, whatever it holds.
func PiMcpArgs(servers map[string][]string) ([]string, error) {
	raw, err := json.Marshal(servers)
	if err != nil {
		return nil, fmt.Errorf("mcp-arg: %w", err)
	}
	return []string{"--" + PiMcpArgsFlag + "=" + string(raw)}, nil
}
