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
	// Editor is the command used to edit a ticket's text field in the tickets view
	// (K). Overridden by `sesh tui --editor`; falls back to $EDITOR when unset.
	Editor string `toml:"editor"`
	// ColumnMoves reposition INDIVIDUAL columns relative to an anchor, applied
	// on top of the base set (the default, or `columns`) — so you can move one
	// column without enumerating them all.
	//
	//	[[tui.column]]
	//	name   = "notify"
	//	before = "machine"
	ColumnMoves []ColumnMove `toml:"column"`
	// ColumnColors tint individual columns. Built-in defaults (NAME blue, CWD
	// green, TKT! red) apply unless overridden; an entry with an empty colour
	// clears one.
	//
	//	[[tui.column_color]]
	//	name  = "cwd"
	//	color = "green"   # a name, a 0-255 number, or #rrggbb
	ColumnColors []ColumnColor `toml:"column_color"`
	// GlyphColors tint the state gutter's attention glyphs. Built-in defaults
	// (busy ▶ / descendant ↓ bright green) apply unless overridden; an entry with
	// an empty colour clears one. Valid names: busy, descendant.
	//
	//	[[tui.glyph_color]]
	//	name  = "busy"
	//	color = "2"       # a name, a 0-255 number, or #rrggbb
	GlyphColors []GlyphColor `toml:"glyph_color"`
	// MaxColumnWidths caps each column at a maximum render width (the default: a
	// long name/cwd is truncated so it can't blow out the layout; the `w` key toggles
	// it per session). Set false to disable the cap entirely so every column grows to
	// its content. Pointer so unset ≠ false — unset means the built-in default (on).
	//
	//	[tui]
	//	max_column_widths = false
	MaxColumnWidths *bool `toml:"max_column_widths"`
	// ColumnWidths override the built-in per-column max width used when the cap is on
	// (full-width NAME/CWD/TKT-NAME default to 40/40/30; a fixed column's reserved
	// width). Only consulted while the cap is on.
	//
	//	[[tui.column_width]]
	//	name = "name"
	//	max  = 60
	ColumnWidths []ColumnWidth `toml:"column_width"`
	// Keys rebind the TUI's commands. Every action lives in a command registry
	// (`p` opens the palette, `?` lists them); only the frequent ones carry a key
	// by default. An entry's FIRST appearance replaces that command's default
	// keys, further entries for it ADD keys, and an empty key UNBINDS it (leaving
	// it palette-only) — the same empty-clears convention as column_color /
	// glyph_color. A configured key wins over a default that held it. Unknown
	// command ids, unusable key names and two entries fighting over one key are
	// all LOUD errors (see the TUI's ResolveKeymap). ctrl+c always quits and
	// cannot be rebound.
	//
	//	[[tui.key]]
	//	command = "fork"
	//	key     = "F"
	//
	//	[[tui.key]]
	//	command = "delete"
	//	key     = ""      # palette-only
	Keys []KeyBinding `toml:"key"`
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
	// MouseScrollV / MouseScrollH set mouse-wheel SENSITIVITY: how many wheel
	// notches it takes to move one row (vertical) or pan one column (horizontal).
	// 1 (the default) moves on every notch; higher = LESS sensitive (dampens fast
	// trackpad scrolling). 0/unset = 1; negatives are a loud error.
	//
	//	[tui]
	//	mouse_scroll_v = 3
	//	mouse_scroll_h = 2
	MouseScrollV int `toml:"mouse_scroll_v"`
	MouseScrollH int `toml:"mouse_scroll_h"`
	// AllMachines makes `sesh tui` default to the cross-machine view (every mesh
	// machine, not just self) — the same as passing --all-machines. Lets the popup
	// bindings launch the TUI WITHOUT the shell wrapper that used to add the flag.
	//
	//	[tui]
	//	all_machines = true
	AllMachines bool `toml:"all_machines"`
	// ShowOffline makes `sesh tui` show the last-known threads of OFFLINE mesh machines
	// by default (same as pressing `o` / passing --show-offline). Default (unset/false)
	// hides them — their owner is unreachable, so they can't be entered or mutated.
	//
	//	[tui]
	//	show_offline = true
	ShowOffline bool `toml:"show_offline"`
}

// ScrollV / ScrollH resolve the effective wheel divisor (unset/0 → 1).
func (c *TUIConfig) ScrollV() int { return scrollDiv(c.MouseScrollV) }
func (c *TUIConfig) ScrollH() int { return scrollDiv(c.MouseScrollH) }

func scrollDiv(v int) int {
	if v < 1 {
		return 1
	}
	return v
}

// TUIView is one custom view definition.
type TUIView struct {
	Name   string `toml:"name"`
	Filter string `toml:"filter"`
	// Position, when >0, places this view at that 1-based slot in the Tab/picker
	// order (interleaved with the built-ins active/on hold/archived/all) — e.g.
	// position=2 puts it second, right after `active`. Omitted/0 = appended after
	// the built-ins (the default). Multiple positioned views insert in ascending
	// position order.
	Position int `toml:"position"`
}

// ColumnColor tints one column. Color is a name (green/blue/…), a 0-255 palette
// number, or a #rrggbb hex; empty clears the column's colour (incl. a default).
type ColumnColor struct {
	Name  string `toml:"name"`
	Color string `toml:"color"`
}

// GlyphColor tints one gutter glyph (busy ▶ / descendant ↓). Color is a name, a
// 0-255 palette number, or a #rrggbb hex; empty clears the glyph's colour
// (incl. a default). Validated by the TUI's ResolveGlyphColors.
type GlyphColor struct {
	Name  string `toml:"name"`
	Color string `toml:"color"`
}

// KeyBinding binds one TUI command to one key. Command is the command's stable
// id (as listed by the `?` popup / the command palette); Key is a bubbletea key
// string ("f", "ctrl+f", "up", "alt+enter"), and empty unbinds the command.
type KeyBinding struct {
	Command string `toml:"command"`
	Key     string `toml:"key"`
}

// ColumnWidth overrides one column's max render width (used when the width cap is
// on). Max must be >= 1 (validated by the TUI's ResolveColumnWidths, which also
// checks the name is a known column).
type ColumnWidth struct {
	Name string `toml:"name"`
	Max  int    `toml:"max"`
}

// MaxColumnWidthsDefault resolves the max-column-width cap default (unset = on).
func (c *TUIConfig) MaxColumnWidthsDefault() bool {
	if c == nil || c.MaxColumnWidths == nil {
		return true
	}
	return *c.MaxColumnWidths
}

// ColumnMove repositions one column, applied over the base set. Exactly one
// of After/Before (relative to an anchor column) or Position (absolute,
// 1-based: position 1 = the first column) is set. A column not already in the
// base set is INSERTED at that position (so you can add e.g. `created` without
// re-listing the set).
type ColumnMove struct {
	Name     string `toml:"name"`
	After    string `toml:"after"`
	Before   string `toml:"before"`
	Position int    `toml:"position"`
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
	if f.TUI != nil {
		if f.TUI.MouseScrollV < 0 || f.TUI.MouseScrollH < 0 {
			return nil, fmt.Errorf("config: [tui] mouse_scroll_v / mouse_scroll_h must be >= 1 (1 = move every notch, higher = less sensitive)")
		}
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

// --- [spawn] (PARITY_ROADMAP E3) ---

// SpawnConfig is the [spawn] table (+ [spawn.<agent>] overrides): the DEFAULT
// behavior for launching agents. Modes:
//
//	yolo    — bypass permissions (claude --dangerously-skip-permissions,
//	          codex --dangerously-bypass-approvals-and-sandbox; pi is always
//	          full-access, so yolo ≡ default for it)
//	default — the agent's own defaults (prompts)
//	sandbox — restricted (codex --sandbox read-only; claude headless default-
//	          deny; pi: IMPOSSIBLE — loud)
//
// Args is a literal extra-argv escape hatch for agent-specific settings.
type SpawnConfig struct {
	Mode string   `toml:"mode"`
	Args []string `toml:"args"`
}

type spawnFile struct {
	Spawn map[string]any `toml:"spawn"`
}

// SpawnModes are the valid [spawn] modes.
var SpawnModes = []string{"yolo", "default", "sandbox"}

// Spawn is the resolved per-agent spawn policy.
type Spawn struct {
	global SpawnConfig
	agents map[string]SpawnConfig
}

// LoadSpawn reads [spawn] + [spawn.<agent>]. Missing = zero policy (mode
// "default"). Unknown modes, or sandbox configured for pi, refuse LOUDLY.
func LoadSpawn(home string) (Spawn, error) {
	out := Spawn{agents: map[string]SpawnConfig{}}
	raw, err := os.ReadFile(ConfigPath(home))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, nil
		}
		return out, fmt.Errorf("config: read %s: %w", ConfigPath(home), err)
	}
	var f spawnFile
	if err := toml.Unmarshal(raw, &f); err != nil {
		return out, fmt.Errorf("config: parse %s: %w", ConfigPath(home), err)
	}
	decode := func(v any, into *SpawnConfig, name string) error {
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("config: [spawn%s] must be a table", name)
		}
		if mode, ok := m["mode"].(string); ok {
			into.Mode = mode
		}
		if args, ok := m["args"].([]any); ok {
			for _, a := range args {
				s, ok := a.(string)
				if !ok {
					return fmt.Errorf("config: [spawn%s] args must be strings", name)
				}
				into.Args = append(into.Args, s)
			}
		}
		return nil
	}
	for k, v := range f.Spawn {
		switch k {
		case "mode":
			if s, ok := v.(string); ok {
				out.global.Mode = s
			}
		case "args":
			if err := decode(map[string]any{"args": v}, &out.global, ""); err != nil {
				return out, err
			}
		case "claude", "codex", "pi":
			var sc SpawnConfig
			if err := decode(v, &sc, "."+k); err != nil {
				return out, err
			}
			out.agents[k] = sc
		default:
			return out, fmt.Errorf("config: [spawn] unknown key %q (mode, args, or an agent table)", k)
		}
	}
	for agent := range map[string]bool{"": true, "claude": true, "codex": true, "pi": true} {
		mode := out.ModeFor(agent)
		valid := mode == ""
		for _, m := range SpawnModes {
			if mode == m {
				valid = true
			}
		}
		if !valid {
			return out, fmt.Errorf("config: [spawn] unknown mode %q (valid: %v)", mode, SpawnModes)
		}
	}
	if out.ModeFor("pi") == "sandbox" {
		return out, fmt.Errorf("config: [spawn] pi cannot be sandboxed (pi has no permission system) — yolo/default only")
	}
	return out, nil
}

// ModeFor resolves an agent's mode ([spawn.<agent>] wins over [spawn]; '' =
// default).
func (s Spawn) ModeFor(agent string) string {
	if a, ok := s.agents[agent]; ok && a.Mode != "" {
		return a.Mode
	}
	return s.global.Mode
}

// ArgsFor resolves an agent's extra args (global + per-agent, in that order).
func (s Spawn) ArgsFor(agent string) []string {
	out := append([]string(nil), s.global.Args...)
	if a, ok := s.agents[agent]; ok {
		out = append(out, a.Args...)
	}
	return out
}
