package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Claude Code shows a per-workspace "Quick safety check: Is this a project you
// created or one you trust?" dialog at startup for any cwd it has not been told
// to trust, and that dialog eats the first keystrokes sent to the pane (Enter
// selects "No, exit" and the agent is gone). Trust is persisted in claude's
// GLOBAL config file as
//
//	{"projects": {"<dir>": {"hasTrustDialogAccepted": true, ...}}}
//
// and claude's own error text names that key as the non-interactive way to
// grant it ("set projects[<dir>].hasTrustDialogAccepted: true in
// ~/.claude.json"). --dangerously-skip-permissions does NOT bypass the dialog.
//
// Since Claude Code 2.1.27x the lookup walks the cwd's ancestors only up to the
// enclosing GIT ROOT (measured against 2.1.273; 2.1.228 walked to `/`), so a
// freshly cloned box under a trusted home is a NEW workspace and prompts every
// time — ticket 4b069b88. EnsureTrust is the claude twin of EnsureCodexTrust:
// sesh grants the trust up front for the dirs it spawns agents in, so a spawned
// thread comes up at a clean input prompt and a queued `thread send` lands in
// the agent, not in a dialog.
//
// The file is claude's live state (700+ project records, history, account),
// rewritten by every running claude session, so the edit is minimal and safe:
//   - only projects[<cwd>].hasTrustDialogAccepted is touched; every other byte
//     of the document is carried through as raw JSON (no float64 round-trip of
//     ids/timestamps, no key reordering inside untouched records);
//   - a document that does not parse is an ERROR, never overwritten — a torn
//     or corrupt file must not be replaced by an empty one;
//   - the write is temp-file + rename, mode 0600, the same atomic shape claude
//     itself uses (its own crash leftovers are `.claude.json.tmp.<pid>.<hex>`),
//     so a concurrent claude can never observe a half-written file;
//   - already-trusted is a pure read: nothing is written.
//
// The realpath of cwd is seeded too when it differs (claude keys a git repo by
// its CANONICAL root, and the ancestor walk compares the as-given cwd against
// that): both keys name the same directory, and one redundant entry is what
// claude itself would write on accept.

// GlobalConfigPath returns claude's global config file: $CLAUDE_CONFIG_DIR/.claude.json
// when CLAUDE_CONFIG_DIR is set, else $HOME/.claude.json (a SIBLING of ~/.claude,
// not inside it — unlike the transcripts, which live under Homes.Claude).
func GlobalConfigPath() (string, error) {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, ".claude.json"), nil
	}
	uh, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("claude trust: user home: %w", err)
	}
	return filepath.Join(uh, ".claude.json"), nil
}

// TrustKeys returns the project keys EnsureTrust seeds for cwd: the cleaned
// absolute path, plus its symlink-resolved form when that differs. cwd must be
// absolute and must exist (a spawn into a missing dir fails here, loudly, rather
// than after the pane is up).
func TrustKeys(cwd string) ([]string, error) {
	if !filepath.IsAbs(cwd) {
		return nil, fmt.Errorf("claude trust: cwd %q is not absolute", cwd)
	}
	clean := filepath.Clean(cwd)
	real, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return nil, fmt.Errorf("claude trust: resolve cwd: %w", err)
	}
	if real == clean {
		return []string{clean}, nil
	}
	return []string{clean, real}, nil
}

// EnsureTrust idempotently marks cwd as trusted in the claude global config at
// configPath (see GlobalConfigPath). A missing file is created holding only the
// trust entry. Returns without writing when every key is already trusted.
func EnsureTrust(configPath, cwd string) error {
	if configPath == "" {
		return errors.New("claude trust: empty config path")
	}
	keys, err := TrustKeys(cwd)
	if err != nil {
		return err
	}

	raw, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("claude trust: read %s: %w", configPath, err)
	}

	// Top level and the projects map are decoded to RAW members so untouched
	// content round-trips verbatim; only the target entries are opened.
	top := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &top); err != nil {
			return fmt.Errorf("claude trust: %s is not a JSON object (refusing to overwrite it): %w", configPath, err)
		}
	}
	projects := map[string]json.RawMessage{}
	if p, ok := top["projects"]; ok && len(bytes.TrimSpace(p)) > 0 && string(bytes.TrimSpace(p)) != "null" {
		if err := json.Unmarshal(p, &projects); err != nil {
			return fmt.Errorf("claude trust: %s: \"projects\" is not a JSON object (refusing to overwrite it): %w", configPath, err)
		}
	}

	changed := false
	for _, key := range keys {
		entry := map[string]json.RawMessage{}
		if e, ok := projects[key]; ok && len(bytes.TrimSpace(e)) > 0 && string(bytes.TrimSpace(e)) != "null" {
			if err := json.Unmarshal(e, &entry); err != nil {
				return fmt.Errorf("claude trust: %s: projects[%q] is not a JSON object (refusing to overwrite it): %w", configPath, key, err)
			}
		}
		if string(bytes.TrimSpace(entry["hasTrustDialogAccepted"])) == "true" {
			continue // already trusted
		}
		entry["hasTrustDialogAccepted"] = json.RawMessage("true")
		enc, err := json.Marshal(entry)
		if err != nil {
			return fmt.Errorf("claude trust: encode projects[%q]: %w", key, err)
		}
		projects[key] = enc
		changed = true
	}
	if !changed {
		return nil
	}

	encProjects, err := json.Marshal(projects)
	if err != nil {
		return fmt.Errorf("claude trust: encode projects: %w", err)
	}
	top["projects"] = encProjects
	out, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return fmt.Errorf("claude trust: encode config: %w", err)
	}
	return writeAtomic(configPath, out)
}

// writeAtomic writes data to path via a same-directory temp file + rename, so a
// reader never sees a partial document. Mode 0600 (claude's own choice for this
// file — it holds account details).
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("claude trust: mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".claude.json.sesh-*")
	if err != nil {
		return fmt.Errorf("claude trust: temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { os.Remove(tmpName) }
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("claude trust: chmod temp: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("claude trust: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("claude trust: close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("claude trust: rename into %s: %w", path, err)
	}
	return nil
}
