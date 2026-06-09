package agents

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// codex shows a per-directory "Do you trust the contents of this directory?"
// prompt at spawn for any cwd not in its trusted set, and that prompt eats the
// first keystrokes sent to the pane. Trust is persisted in codex's config as
//
//	[projects."<dir>"]
//	trust_level = "trusted"
//
// EnsureCodexTrust idempotently adds that entry to <codexHome>/config.toml so a
// sesh-spawned codex thread comes up at a clean input prompt. codexHome is
// codex's home dir (CODEX_HOME, default ~/.codex). This is the same trust the
// user would grant by answering the prompt once; sesh grants it up front for
// dirs it spawns agents in.
func EnsureCodexTrust(codexHome, cwd string) error {
	if codexHome == "" || cwd == "" {
		return fmt.Errorf("codex trust: empty codexHome or cwd")
	}
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		return fmt.Errorf("codex trust: %w", err)
	}
	cfgPath := filepath.Join(codexHome, "config.toml")

	existing, err := os.ReadFile(cfgPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("codex trust: read config: %w", err)
	}
	header := fmt.Sprintf("[projects.%q]", cwd)
	if strings.Contains(string(existing), header) {
		return nil // already trusted
	}

	entry := fmt.Sprintf("\n%s\ntrust_level = \"trusted\"\n", header)
	f, err := os.OpenFile(cfgPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("codex trust: open config: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(entry); err != nil {
		return fmt.Errorf("codex trust: write config: %w", err)
	}
	return nil
}

// CodexHome resolves codex's home dir: the configured override if set, else the
// codex default ~/.codex.
func CodexHome(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	uh, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("codex home: %w", err)
	}
	return filepath.Join(uh, ".codex"), nil
}
