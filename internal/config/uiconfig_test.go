package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadUIConfigDefaultsWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadUIConfig(dir)
	if err != nil {
		t.Fatalf("LoadUIConfig (no file): %v", err)
	}
	// The default is COLLAPSED parents (true), not the Go zero value (false).
	if !cfg.CollapseParents {
		t.Errorf("default collapse_parents = %v, want true", cfg.CollapseParents)
	}
}

func TestUIConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	// Save a non-default value, then load it back unchanged.
	if err := SaveUIConfig(dir, UIConfig{CollapseParents: false}); err != nil {
		t.Fatalf("SaveUIConfig: %v", err)
	}
	cfg, err := LoadUIConfig(dir)
	if err != nil {
		t.Fatalf("LoadUIConfig: %v", err)
	}
	if cfg.CollapseParents {
		t.Errorf("round-tripped collapse_parents = true, want false")
	}
	// An EXPLICIT false in the file must survive (the pointer form distinguishes
	// "set to false" from "absent → default true").
	raw, _ := os.ReadFile(UIConfigPath(dir))
	if len(raw) == 0 {
		t.Errorf("ui_config.toml is empty after save")
	}
}

func TestLoadUIConfigMalformedIsLoud(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ui_config.toml"), []byte("collapse_parents = "), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadUIConfig(dir); err == nil {
		t.Error("malformed ui_config.toml must be a loud error, not silently defaulted")
	}
}
