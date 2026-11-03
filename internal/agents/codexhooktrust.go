package agents

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Codex runs a hook only once it is TRUSTED: `[hooks.state."<key>"] trusted_hash`
// in the codex config must equal the hash codex computes for the hook's
// declaration. The hash is codex's own normalised-TOML digest, so sesh does not
// compute it: it asks codex (`codex app-server`, JSON-RPC `hooks/list`, which
// reports each hook's key, currentHash and trustStatus) and writes what codex
// reported — the same record codex's own trust prompt writes.

// CodexHookState is one hook as `hooks/list` reports it.
type CodexHookState struct {
	Key         string `json:"key"`
	Source      string `json:"source"`
	EventName   string `json:"eventName"`
	Command     string `json:"command"`
	Enabled     bool   `json:"enabled"`
	CurrentHash string `json:"currentHash"`
	TrustStatus string `json:"trustStatus"`
}

// ProbeCodexHooks starts `codex app-server <extra…>` with CODEX_HOME=codexHome and
// returns the hooks it reports.
func ProbeCodexHooks(ctx context.Context, codexHome string, extra []string) ([]CodexHookState, error) {
	cmd := exec.CommandContext(ctx, Executable(Codex), append([]string{"app-server"}, extra...)...)
	cmd.Env = append(os.Environ(), "CODEX_HOME="+codexHome)
	cmd.Dir = codexHome
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("codex hooks: start codex app-server: %w", err)
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	for _, m := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"sesh","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"hooks/list","params":{}}`,
	} {
		if _, err := io.WriteString(stdin, m+"\n"); err != nil {
			return nil, fmt.Errorf("codex hooks: write to codex app-server: %w", err)
		}
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var msg struct {
			ID     *int `json:"id"`
			Result *struct {
				Data []struct {
					Hooks  []CodexHookState `json:"hooks"`
					Errors []any            `json:"errors"`
				} `json:"data"`
			} `json:"result"`
			Error any `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) != nil || msg.ID == nil || *msg.ID != 2 {
			continue
		}
		if msg.Error != nil {
			return nil, fmt.Errorf("codex hooks: hooks/list failed: %v", msg.Error)
		}
		if msg.Result == nil {
			return nil, errors.New("codex hooks: hooks/list returned no result")
		}
		var out []CodexHookState
		for _, d := range msg.Result.Data {
			if len(d.Errors) > 0 {
				return nil, fmt.Errorf("codex hooks: hooks/list reported errors: %v", d.Errors)
			}
			out = append(out, d.Hooks...)
		}
		return out, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("codex hooks: codex app-server did not answer hooks/list in time (%v); stderr: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil, fmt.Errorf("codex hooks: codex app-server exited before answering hooks/list (does this codex support hooks?); stderr: %s", strings.TrimSpace(stderr.String()))
}

// codexHookStateHeader matches a `[hooks.state."<key>"]` header line.
func codexHookStateHeader(key string) *regexp.Regexp {
	return regexp.MustCompile(`^\s*\[\s*hooks\s*\.\s*state\s*\.\s*"` + regexp.QuoteMeta(key) + `"\s*\]\s*(#.*)?$`)
}

var tomlHeaderLine = regexp.MustCompile(`^\s*\[`)
var trustedHashLine = regexp.MustCompile(`^\s*trusted_hash\s*=`)

// SetCodexHookTrust records `trusted_hash = <hash>` for hook <key> in
// <codexHome>/config.toml — replacing a stale value in place, or appending the
// table. Every other line is kept byte for byte.
func SetCodexHookTrust(codexHome, key, hash string) error {
	if strings.ContainsAny(key, "\"\\\n") || strings.ContainsAny(hash, "\"\\\n") {
		return fmt.Errorf("codex hooks: refusing to write an unexpected key/hash %q/%q", key, hash)
	}
	path := filepath.Join(codexHome, "config.toml")
	if target, err := filepath.EvalSymlinks(path); err == nil {
		path = target
	}
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("codex hooks: read config: %w", err)
	}
	value := `trusted_hash = "` + hash + `"`
	lines := strings.SplitAfter(string(raw), "\n")
	header := codexHookStateHeader(key)
	edited := ""
	for i, l := range lines {
		if !header.MatchString(strings.TrimRight(l, "\r\n")) {
			continue
		}
		end := len(lines)
		for j := i + 1; j < len(lines); j++ {
			if tomlHeaderLine.MatchString(lines[j]) {
				end = j
				break
			}
		}
		replaced := false
		for j := i + 1; j < end; j++ {
			if trustedHashLine.MatchString(lines[j]) {
				lines[j] = value + "\n"
				replaced = true
				break
			}
		}
		if !replaced {
			if !strings.HasSuffix(lines[i], "\n") {
				lines[i] += "\n"
			}
			lines[i] += value + "\n"
		}
		edited = strings.Join(lines, "")
		break
	}
	if edited == "" {
		edited = string(raw)
		if edited != "" && !strings.HasSuffix(edited, "\n") {
			edited += "\n"
		}
		edited += "\n[hooks.state.\"" + key + "\"]\n" + value + "\n"
	}
	if edited == string(raw) {
		return nil
	}
	mode := os.FileMode(0o600)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("codex hooks: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config.toml.*")
	if err != nil {
		return fmt.Errorf("codex hooks: write config: %w", err)
	}
	name := tmp.Name()
	_, werr := tmp.WriteString(edited)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(name, mode)
	}
	if werr == nil {
		werr = os.Rename(name, path)
	}
	if werr != nil {
		_ = os.Remove(name)
		return fmt.Errorf("codex hooks: write config: %w", werr)
	}
	return nil
}

// EnsureCodexContextHooksTrusted makes codex trust sesh's context hooks in
// codexHome: probe, record any missing/stale trust, probe again, and fail loudly
// unless both hooks then report trusted and enabled.
func EnsureCodexContextHooksTrusted(ctx context.Context, codexHome string, hookArgs []string) error {
	probe := func() (map[string]CodexHookState, error) {
		hooks, err := ProbeCodexHooks(ctx, codexHome, hookArgs)
		if err != nil {
			return nil, err
		}
		got := map[string]CodexHookState{}
		for _, h := range hooks {
			got[h.Key] = h
		}
		return got, nil
	}
	got, err := probe()
	if err != nil {
		return err
	}
	wrote := false
	for _, e := range CodexContextHookEvents {
		h, ok := got[e.TrustKey]
		if !ok {
			return fmt.Errorf("codex hooks: codex does not report sesh's %s hook (key %s) — its hook keys changed?", e.Event, e.TrustKey)
		}
		if h.TrustStatus == "trusted" {
			continue
		}
		if h.CurrentHash == "" {
			return fmt.Errorf("codex hooks: codex reports no hash for sesh's %s hook", e.Event)
		}
		if err := SetCodexHookTrust(codexHome, e.TrustKey, h.CurrentHash); err != nil {
			return err
		}
		wrote = true
	}
	if wrote {
		if got, err = probe(); err != nil {
			return err
		}
	}
	for _, e := range CodexContextHookEvents {
		h := got[e.TrustKey]
		if h.TrustStatus != "trusted" || !h.Enabled {
			return fmt.Errorf("codex hooks: sesh's %s hook is %s (enabled=%v) in %s after recording its trust — codex will not run it", e.Event, h.TrustStatus, h.Enabled, codexHome)
		}
	}
	return nil
}

// CodexContextHooksRecorded reports whether codexHome's config still holds a
// trusted_hash for each context hook (a cheap re-check between probes).
func CodexContextHooksRecorded(codexHome string) bool {
	raw, err := os.ReadFile(filepath.Join(codexHome, "config.toml"))
	if err != nil {
		return false
	}
	lines := strings.Split(string(raw), "\n")
	for _, e := range CodexContextHookEvents {
		header := codexHookStateHeader(e.TrustKey)
		found := false
		for i, l := range lines {
			if header.MatchString(l) {
				for j := i + 1; j < len(lines) && !tomlHeaderLine.MatchString(lines[j]); j++ {
					if trustedHashLine.MatchString(lines[j]) {
						found = true
					}
				}
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// CodexProbeTimeout bounds one trust check (two codex app-server starts).
const CodexProbeTimeout = 45 * time.Second
