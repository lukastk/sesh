package agents

import (
	"bufio"
	"encoding/json"
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

// DiscoverCodexSession finds the codex session id for a thread by walking codex's
// rollout files under <codexHome>/sessions and returning the id of the NEWEST
// rollout whose recorded cwd matches and which was created at/after afterUnix
// (the thread's spawn time). codex mints its id on the first turn, so this is how
// sesh recovers it for resume. Returns ("", false) if none — e.g. a codex thread
// that died before its first turn (a legitimate N/A for resume).
func DiscoverCodexSession(codexHome, cwd string, afterUnix int64) (string, bool, error) {
	root := filepath.Join(codexHome, "sessions")
	type cand struct {
		id   string
		name string // rollout file name (iso-ts prefixed -> sortable)
	}
	var best cand
	err := filepath.WalkDir(root, func(p string, dEnt os.DirEntry, walkErr error) error {
		if walkErr != nil || dEnt.IsDir() {
			return nil
		}
		name := dEnt.Name()
		if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
			return nil
		}
		info, err := dEnt.Info()
		if err != nil || info.ModTime().Unix() < afterUnix-2 {
			return nil
		}
		id, rcwd := codexRolloutMeta(p)
		if id == "" || rcwd != cwd {
			return nil
		}
		if name > best.name { // iso-ts prefix => lexical sort is chronological
			best = cand{id: id, name: name}
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return "", false, err
	}
	if best.id == "" {
		return "", false, nil
	}
	return best.id, true, nil
}

// codexRolloutMeta reads a rollout's session_meta (first line) for its id + cwd.
func codexRolloutMeta(path string) (id, cwd string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	if !sc.Scan() {
		return "", ""
	}
	var line struct {
		Type    string `json:"type"`
		Payload struct {
			ID  string `json:"id"`
			Cwd string `json:"cwd"`
		} `json:"payload"`
	}
	if json.Unmarshal(sc.Bytes(), &line) != nil || line.Type != "session_meta" {
		return "", ""
	}
	return line.Payload.ID, line.Payload.Cwd
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
