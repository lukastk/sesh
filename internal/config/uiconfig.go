package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

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
}

// DefaultUIConfig is what a missing file (or a missing key) resolves to.
func DefaultUIConfig() UIConfig {
	return UIConfig{
		CollapseParents: true,
		CwdRoots:        []string{"~/mysetup", "~/dev"},
	}
}

// UIConfigPath is <home>/ui_config.toml.
func UIConfigPath(home string) string { return filepath.Join(home, "ui_config.toml") }

// uiConfigFile mirrors UIConfig with POINTERS so an absent key falls back to the
// default rather than the Go zero value (collapse_parents defaults true, not false).
type uiConfigFile struct {
	CollapseParents *bool     `toml:"collapse_parents"`
	CwdRoots        *[]string `toml:"cwd_roots"`
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
