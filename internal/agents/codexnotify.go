package agents

// codex turn-end reporting (schema 44, the flagged system): codex has no
// in-agent hook surface for turn STARTS, but its `notify` config invokes a
// program on turn completion — exactly the auto-flag trigger. sesh embeds the
// reporter script, materializes it under SESH_HOME, and wires it into the
// codex config at spawn (same ensure-idempotently pattern as EnsureCodexTrust).
// The script self-gates on SESH_THREAD_ID and reports the no-authority
// turn-end event (a turn-end-only reporter must never claim busy authority).

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed codexnotify.sh
var codexNotifyScript []byte

// WriteCodexNotifyScript materializes the embedded reporter at
// <seshHome>/codex-notify.sh (0755) and returns its path. Idempotent —
// rewritten on every daemon start so upgrades propagate.
func WriteCodexNotifyScript(seshHome string) (string, error) {
	if seshHome == "" {
		return "", fmt.Errorf("codex notify: empty sesh home")
	}
	path := filepath.Join(seshHome, "codex-notify.sh")
	if err := os.WriteFile(path, codexNotifyScript, 0o755); err != nil {
		return "", fmt.Errorf("codex notify: write script: %w", err)
	}
	return path, nil
}

// EnsureCodexNotify wires `notify = ["<script>"]` into the codex config.
// notify is a TOP-LEVEL toml key, so it must be PREPENDED — appending would
// land it inside the last [projects.*] table. If the config already carries
// ANY notify setting (the user's own), it is left untouched: sesh must not
// clobber user config, and the cost is only that codex threads flag via the
// heuristic opt-in instead.
func EnsureCodexNotify(codexHome, scriptPath string) error {
	if codexHome == "" || scriptPath == "" {
		return fmt.Errorf("codex notify: empty codexHome or scriptPath")
	}
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		return fmt.Errorf("codex notify: %w", err)
	}
	cfgPath := filepath.Join(codexHome, "config.toml")
	existing, err := os.ReadFile(cfgPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("codex notify: read config: %w", err)
	}
	for _, line := range strings.Split(string(existing), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "notify") {
			return nil // a notify setting exists (ours or the user's) — never clobber
		}
	}
	entry := fmt.Sprintf("notify = [%q]\n", scriptPath)
	if err := os.WriteFile(cfgPath, append([]byte(entry), existing...), 0o600); err != nil {
		return fmt.Errorf("codex notify: write config: %w", err)
	}
	return nil
}
