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
	// Absent cwd_roots → the default parent folders, not an empty slice.
	if len(cfg.CwdRoots) != 2 || cfg.CwdRoots[0] != "~/mysetup" || cfg.CwdRoots[1] != "~/dev" {
		t.Errorf("default cwd_roots = %v, want [~/mysetup ~/dev]", cfg.CwdRoots)
	}
}

func TestUIConfigCwdRootsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := SaveUIConfig(dir, UIConfig{CollapseParents: true, CwdRoots: []string{"~/work", "~/dev", "~/src"}}); err != nil {
		t.Fatalf("SaveUIConfig: %v", err)
	}
	cfg, err := LoadUIConfig(dir)
	if err != nil {
		t.Fatalf("LoadUIConfig: %v", err)
	}
	if len(cfg.CwdRoots) != 3 || cfg.CwdRoots[0] != "~/work" || cfg.CwdRoots[2] != "~/src" {
		t.Errorf("round-tripped cwd_roots = %v, want [~/work ~/dev ~/src]", cfg.CwdRoots)
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

func TestUIConfigDefaultCwdLabel(t *testing.T) {
	cfg := DefaultUIConfig()
	if len(cfg.CwdLabels) != 1 {
		t.Fatalf("default cwd_labels = %d rules, want 1 (the box rule)", len(cfg.CwdLabels))
	}
	if err := ValidateUIConfig(cfg); err != nil {
		t.Errorf("default cwd_labels must validate: %v", err)
	}
}

func TestValidateUIConfigLoudOnBadRule(t *testing.T) {
	// Bad regex → loud.
	if err := ValidateUIConfig(UIConfig{CwdLabels: []UICwdLabelRule{{Match: "([unclosed", Label: "{x}"}}}); err == nil {
		t.Error("a bad match regex must be a loud error")
	}
	// Unknown placeholder (not a named group / {name} / {path}) → loud.
	if err := ValidateUIConfig(UIConfig{CwdLabels: []UICwdLabelRule{{Match: "^~/dev/(?P<a>.+)$", Label: "{nope}"}}}); err == nil {
		t.Error("an unknown label placeholder must be a loud error")
	}
	// A valid rule passes.
	if err := ValidateUIConfig(UIConfig{CwdLabels: []UICwdLabelRule{{Match: "^~/dev/(?P<boxname>.+)$", Label: "{boxname} ({name})"}}}); err != nil {
		t.Errorf("a valid rule must pass: %v", err)
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
