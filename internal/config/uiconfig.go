package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/BurntSushi/toml"
)

// UIConfig is <SESH_HOME>/ui_config.toml — UI preferences for the sesh-ui app,
// served to clients over the API (GET/POST /v1/ui-config). The file lives in
// SESH_HOME so it follows the daemon a client connects to. sesh stores + serves
// these typed settings; it does not otherwise interpret them.
//
//	collapse_parents = true
type UIConfig struct {
	// CollapseParents makes parent threads start COLLAPSED in the app's thread
	// tree (children hidden until the parent is expanded). Default true.
	CollapseParents bool `toml:"collapse_parents" json:"collapse_parents"`
	// CwdRoots are the "default parent folders" the new-thread modal offers as a
	// quick cwd pick: the app lists the immediate subdirs of each (per target
	// machine, via GET /v1/fs/list) so you can spawn into a box (~/dev/<index>)
	// or a mysetup repo without browsing. ~-relative, so the same list works on
	// every machine (each daemon resolves ~ to its own home). Default ~/mysetup, ~/dev.
	CwdRoots []string `toml:"cwd_roots" json:"cwd_roots"`
	// CwdLabels format how a cwd entry is DISPLAYED in the new-thread quick-pick —
	// same rule language as config.toml's [[cwd_label]] (a Go RE2 `match` regex with
	// named groups + a `{placeholder}` `label` template; first match wins, no match →
	// the raw entry name). The default rule turns a ~/dev box index into
	// "<boxname> <boxid>". The app applies these to picker entries (normalizing the
	// Go `(?P<name>)` group syntax to JS `(?<name>)`).
	CwdLabels []UICwdLabelRule `toml:"cwd_label" json:"cwd_labels"`
	// TranscriptPrefetchSecs is how often (seconds) the app silently prefetches all
	// non-archived threads' transcripts in the background and caches them, so opening
	// a thread's transcript is instant. 0 disables. Default 10.
	TranscriptPrefetchSecs int `toml:"transcript_prefetch_secs" json:"transcript_prefetch_secs"`
	// MasterCommand is the shell command the app's "Master" mode runs in a pty over a
	// WebSocket — e.g. "mmt-start" (start+attach the master tmux). Run via `$SHELL -lc`
	// so shell functions/aliases resolve. Empty = the Master mode is unconfigured (the
	// endpoint refuses loudly). Per-machine: the app runs the TARGET daemon's command.
	MasterCommand string `toml:"master_command" json:"master_command"`
}

// UICwdLabelRule is one match→label rule for the new-thread picker display.
type UICwdLabelRule struct {
	Match string `toml:"match" json:"match"`
	Label string `toml:"label" json:"label"`
}

// DefaultUIConfig is what a missing file (or a missing key) resolves to.
func DefaultUIConfig() UIConfig {
	return UIConfig{
		CollapseParents: true,
		CwdRoots:        []string{"~/mysetup", "~/dev"},
		CwdLabels: []UICwdLabelRule{{
			Match: `^~/dev/[0-9]{8}_(?P<boxid>[a-z0-9]+)__(?P<boxname>[^/]+)$`,
			Label: `{boxname} <{boxid}>`,
		}},
		TranscriptPrefetchSecs: 10,
	}
}

// ValidateUIConfig checks the cwd_label rules compile and only reference known
// placeholders — LOUD, so a typo'd rule is refused at save, not silently dropped.
func ValidateUIConfig(cfg UIConfig) error {
	for i, r := range cfg.CwdLabels {
		if r.Match == "" || r.Label == "" {
			return fmt.Errorf("cwd_label rule %d: match and label are both required", i+1)
		}
		re, err := regexp.Compile(r.Match)
		if err != nil {
			return fmt.Errorf("cwd_label rule %d: bad match regex %q: %w", i+1, r.Match, err)
		}
		// Valid placeholders: the regex's named groups + {name} (raw entry) + {path} (~-path).
		valid := map[string]bool{"name": true, "path": true}
		for _, g := range re.SubexpNames() {
			if g != "" {
				valid[g] = true
			}
		}
		for _, m := range placeholderRe.FindAllStringSubmatch(r.Label, -1) {
			if !valid[m[1]] {
				return fmt.Errorf("cwd_label rule %d: label %q uses unknown placeholder {%s} (valid: the regex's named groups + {name} {path})", i+1, r.Label, m[1])
			}
		}
	}
	return nil
}

// UIConfigPath is <home>/ui_config.toml.
func UIConfigPath(home string) string { return filepath.Join(home, "ui_config.toml") }

// uiConfigFile mirrors UIConfig with POINTERS so an absent key falls back to the
// default rather than the Go zero value (collapse_parents defaults true, not false).
type uiConfigFile struct {
	CollapseParents        *bool             `toml:"collapse_parents"`
	CwdRoots               *[]string         `toml:"cwd_roots"`
	CwdLabels              *[]UICwdLabelRule `toml:"cwd_label"`
	TranscriptPrefetchSecs *int              `toml:"transcript_prefetch_secs"`
	MasterCommand          *string           `toml:"master_command"`
}

// LoadUIConfig reads <home>/ui_config.toml, applying defaults for any missing key.
// A missing file = all defaults. Malformed TOML is a LOUD error (never silently
// defaulted — that would mask a typo'd config).
func LoadUIConfig(home string) (UIConfig, error) {
	cfg := DefaultUIConfig()
	raw, err := os.ReadFile(UIConfigPath(home))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("config: read %s: %w", UIConfigPath(home), err)
	}
	var f uiConfigFile
	if err := toml.Unmarshal(raw, &f); err != nil {
		return cfg, fmt.Errorf("config: parse %s: %w", UIConfigPath(home), err)
	}
	if f.CollapseParents != nil {
		cfg.CollapseParents = *f.CollapseParents
	}
	// nil = key absent → keep the default roots; a present (even empty) list is honoured.
	if f.CwdRoots != nil {
		cfg.CwdRoots = *f.CwdRoots
	}
	if f.CwdLabels != nil {
		cfg.CwdLabels = *f.CwdLabels
	}
	// nil = absent → default interval; an explicit 0 (off) or any value is honoured.
	if f.TranscriptPrefetchSecs != nil {
		cfg.TranscriptPrefetchSecs = *f.TranscriptPrefetchSecs
	}
	if f.MasterCommand != nil {
		cfg.MasterCommand = *f.MasterCommand
	}
	// A malformed rule on disk is loud (parity with config.toml's cwd_label loader).
	if err := ValidateUIConfig(cfg); err != nil {
		return cfg, fmt.Errorf("config: %s: %w", UIConfigPath(home), err)
	}
	return cfg, nil
}

// SaveUIConfig writes the config to <home>/ui_config.toml with every key explicit,
// so the file round-trips and is human-editable.
func SaveUIConfig(home string, cfg UIConfig) error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return fmt.Errorf("config: encode ui_config: %w", err)
	}
	if err := os.WriteFile(UIConfigPath(home), buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("config: write %s: %w", UIConfigPath(home), err)
	}
	return nil
}
