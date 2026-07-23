package tui

// Per-column foreground colours (G4). A column can be tinted via
// [[tui.column_color]] in config.toml ({ name = "cwd", color = "green" }). Three
// built-in defaults ship (NAME blue, CWD green, TKT! red — Lukas's choice); a
// config entry for the same column overrides it, and an entry with an empty
// colour clears it.
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
	{Name: ColTicketInput, Color: "red"}, // the TKT! "!" = a ticket needs input
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

// Gutter glyph names tintable via [[tui.glyph_color]]. These are the state
// gutter's ATTENTION glyphs — busy = ▶ (this thread is executing a turn),
// descendant = ↓ (a child/grandchild is). The tint applies only when the glyph
// is in its active state (idle `·`/blank stays plain) and only on non-selected
// rows (reverse video is the selected row's dominant cue, as with columns).
const (
	GlyphBusy       = "busy"
	GlyphDescendant = "descendant"
	// GlyphFlag tints the ⚑ needs-attention flag (schema 44); the ⌀
	// flag-disabled marker stays plain (it is a suppression state, not an
	// attention state).
	GlyphFlag = "flag"
)

// validGlyphNames lists the tintable glyphs, in gutter order (for error messages).
var validGlyphNames = []string{GlyphBusy, GlyphDescendant, GlyphFlag}

// GlyphColorSpec is one [[tui.glyph_color]] entry. An empty Color clears the
// glyph's colour (including a built-in default).
type GlyphColorSpec struct {
	Name  string
	Color string
}

// DefaultGlyphColors are the built-in glyph colours: the running-state glyphs
// bright green so live activity pops out of the grid. Overridable/clearable per
// glyph via [[tui.glyph_color]].
var DefaultGlyphColors = []GlyphColorSpec{
	{Name: GlyphBusy, Color: "10"},
	{Name: GlyphDescendant, Color: "10"},
	// The flag is an attention marker — red by default.
	{Name: GlyphFlag, Color: "9"},
}

// ResolveGlyphColors builds the per-glyph style map: the built-in defaults
// overlaid by the config entries (an entry with an empty colour clears that
// glyph). Unknown glyph names and unparseable colours are LOUD errors.
func ResolveGlyphColors(cfg []GlyphColorSpec) (map[string]lipgloss.Style, error) {
	known := map[string]bool{}
	for _, n := range validGlyphNames {
		known[n] = true
	}
	colors := map[string]string{}
	for _, d := range DefaultGlyphColors {
		colors[d.Name] = d.Color
	}
	for _, e := range cfg {
		name := strings.ToLower(strings.TrimSpace(e.Name))
		if name == "" {
			return nil, fmt.Errorf("[[tui.glyph_color]]: name is required")
		}
		if !known[name] {
			return nil, fmt.Errorf("[[tui.glyph_color]] %q: unknown glyph (valid: %s)", name, strings.Join(validGlyphNames, ", "))
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
			return nil, fmt.Errorf("[[tui.glyph_color]] %q: %w", name, err)
		}
		out[name] = lipgloss.NewStyle().Foreground(col)
	}
	return out, nil
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
