package tui

// Per-column foreground colours (G4). A column can be tinted via
// [[tui.column_color]] in config.toml ({ name = "cwd", color = "green" }). Two
// built-in defaults ship (NAME blue, CWD green, Lukas's choice); a config entry
// for the same column overrides it, and an entry with an empty colour clears it.
// Colours are applied at render time to non-selected cells only — a selected row
// is reverse-video (the dominant cue) and a filter match recolours its own runes,
// so per-column colour yields to both (see renderCells). Unknown column names and
// unparseable colours are LOUD errors at startup, never a silently-dropped colour.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ColumnColorSpec is one [[tui.column_color]] entry. An empty Color clears the
// column's colour (including a built-in default).
type ColumnColorSpec struct {
	Name  string
	Color string
}

// DefaultColumnColors are the built-in per-column colours. Overridable/clearable
// per column via [[tui.column_color]].
var DefaultColumnColors = []ColumnColorSpec{
	{Name: ColName, Color: "blue"},
	{Name: ColCwd, Color: "green"},
}

// namedColors maps the basic ANSI colour names to their palette numbers.
var namedColors = map[string]string{
	"black": "0", "red": "1", "green": "2", "yellow": "3",
	"blue": "4", "magenta": "5", "cyan": "6", "white": "7",
	"gray": "8", "grey": "8", "brightblack": "8",
	"brightred": "9", "brightgreen": "10", "brightyellow": "11",
	"brightblue": "12", "brightmagenta": "13", "brightcyan": "14", "brightwhite": "15",
}

// parseColor resolves a colour string to a lipgloss colour. Accepts a known name,
// a 0..255 palette number, or a #rrggbb hex. Anything else is a LOUD error.
func parseColor(s string) (lipgloss.Color, error) {
	v := strings.ToLower(strings.TrimSpace(s))
	if v == "" {
		return "", fmt.Errorf("empty colour")
	}
	if n, ok := namedColors[v]; ok {
		return lipgloss.Color(n), nil
	}
	if strings.HasPrefix(v, "#") {
		if len(v) == 7 {
			if _, err := strconv.ParseUint(v[1:], 16, 32); err == nil {
				return lipgloss.Color(v), nil
			}
		}
		return "", fmt.Errorf("bad hex colour %q (want #rrggbb)", s)
	}
	if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 255 {
		return lipgloss.Color(v), nil
	}
	return "", fmt.Errorf("unknown colour %q (use a name like green/blue, a 0-255 number, or #rrggbb)", s)
}

// ResolveColumnColors builds the per-column style map: the built-in defaults
// overlaid by the config entries (an entry with an empty colour clears that
// column). Unknown column names and unparseable colours are LOUD errors.
func ResolveColumnColors(cfg []ColumnColorSpec) (map[string]lipgloss.Style, error) {
	known := map[string]bool{}
	for _, c := range colOrder {
		known[c.name] = true
	}
	// Start from the defaults (name→colour string), so config can clear them.
	colors := map[string]string{}
	for _, d := range DefaultColumnColors {
		colors[d.Name] = d.Color
	}
	for _, e := range cfg {
		name := strings.ToLower(strings.TrimSpace(e.Name))
		if name == "" {
			return nil, fmt.Errorf("[[tui.column_color]]: name is required")
		}
		if !known[name] {
			return nil, fmt.Errorf("[[tui.column_color]] %q: unknown column (valid: %s)", name, strings.Join(ValidColumnNames(), ", "))
		}
		if strings.TrimSpace(e.Color) == "" {
			delete(colors, name) // explicit clear
			continue
		}
		colors[name] = e.Color
	}
	out := map[string]lipgloss.Style{}
	for name, cstr := range colors {
		col, err := parseColor(cstr)
		if err != nil {
			return nil, fmt.Errorf("[[tui.column_color]] %q: %w", name, err)
		}
		out[name] = lipgloss.NewStyle().Foreground(col)
	}
	return out, nil
}
