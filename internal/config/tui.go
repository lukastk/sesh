package config

import (
	"errors"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// TUIConfig is the [tui] table in <SESH_HOME>/config.toml — user defaults for
// the TUI (mechanism stays in sesh; the FILE is dotfiles-owned policy, like
// [[session_name]]).
//
//	[tui]
//	columns = ["machine", "agent", "name", "cwd", "tags"]
type TUIConfig struct {
	Columns []string `toml:"columns"`
	// ExpandChildren makes tree nodes start EXPANDED (default false: children
	// start collapsed under their parent, per v1).
	ExpandChildren bool `toml:"expand_children"`
	// Views are the custom Tab-cycle views: a name + a predicate over the
	// thread rows (compiled by the TUI; a broken filter is loud at startup).
	//
	//	[[tui.views]]
	//	name   = "ticketed"
	//	filter = "ticketed and not archived"
	Views []TUIView `toml:"views"`
}

// TUIView is one custom view definition.
type TUIView struct {
	Name   string `toml:"name"`
	Filter string `toml:"filter"`
}

type tuiConfigFile struct {
	TUI *TUIConfig `toml:"tui"`
}

// LoadTUI reads the [tui] table from <home>/config.toml. Missing file or
// missing table = (nil, nil) — the built-in defaults apply. A present-but-
// broken file is a LOUD error (parity with LoadNaming: a misconfiguration must
// never silently fall back).
func LoadTUI(home string) (*TUIConfig, error) {
	raw, err := os.ReadFile(ConfigPath(home))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("config: read %s: %w", ConfigPath(home), err)
	}
	var f tuiConfigFile
	if err := toml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", ConfigPath(home), err)
	}
	return f.TUI, nil
}

// Defaults is the [defaults] table — record-creation defaults the daemon
// applies (policy knobs, dotfiles-owned like the rest of config.toml).
type Defaults struct {
	// Notifications is the notify gate new threads start with. nil = true.
	Notifications *bool `toml:"notifications"`
}

type defaultsFile struct {
	Defaults *Defaults `toml:"defaults"`
}

// LoadDefaults reads the [defaults] table. Missing file/table = all defaults.
func LoadDefaults(home string) (Defaults, error) {
	raw, err := os.ReadFile(ConfigPath(home))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Defaults{}, nil
		}
		return Defaults{}, fmt.Errorf("config: read %s: %w", ConfigPath(home), err)
	}
	var f defaultsFile
	if err := toml.Unmarshal(raw, &f); err != nil {
		return Defaults{}, fmt.Errorf("config: parse %s: %w", ConfigPath(home), err)
	}
	if f.Defaults == nil {
		return Defaults{}, nil
	}
	return *f.Defaults, nil
}

// NotifyDefault resolves the notification default (unset = true).
func (d Defaults) NotifyDefault() bool {
	if d.Notifications == nil {
		return true
	}
	return *d.Notifications
}
