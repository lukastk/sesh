package daemon

// GET /v1/browser/tui-config (capability browser.tui-config-v1): the hub's own
// `sesh tui` configuration, resolved exactly as `sesh tui` resolves it on this
// machine, so a browser grid shows the same columns, views, keys and colours from
// the same ~/.sesh/config.toml. A broken [tui] section is the same loud error the
// TUI would print at startup.

import (
	"net/http"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
	"github.com/lukastk/sesh/internal/tui"
)

func (d *Daemon) handleBrowserTUIConfig(w http.ResponseWriter, r *http.Request) {
	if !onlyQuery(w, r) {
		return
	}
	out, err := browserTUIConfig(d.cfg.Home)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "tui config: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func browserTUIConfig(home string) (api.BrowserTUIConfig, error) {
	tcfg, err := config.LoadTUI(home)
	if err != nil {
		return api.BrowserTUIConfig{}, err
	}
	if tcfg == nil {
		tcfg = &config.TUIConfig{}
	}
	out := api.BrowserTUIConfig{Schema: api.SchemaVersion, MaxColumnWidths: tcfg.MaxColumnWidthsDefault(), ShowOffline: tcfg.ShowOffline,
		ViewRing: append([]string{}, tcfg.ViewRing...), ColumnColors: map[string]string{}, GlyphColors: map[string]string{}, ColumnWidths: map[string]int{}}

	// Columns: [tui] columns (or the default) with the [[tui.column]] moves applied.
	cols, err := tui.ResolveColumns(tcfg.Columns)
	if err != nil {
		return out, err
	}
	var moves []tui.ColumnMove
	for _, mv := range tcfg.ColumnMoves {
		moves = append(moves, tui.ColumnMove{Name: mv.Name, After: mv.After, Before: mv.Before, Position: mv.Position})
	}
	if len(moves) > 0 {
		if cols, err = tui.ApplyColumnMoves(cols, moves); err != nil {
			return out, err
		}
	}
	out.Columns = cols

	// Views in display order; a custom view carries its filter source.
	var specs []tui.ViewSpec
	filters := map[string]string{}
	for _, v := range tcfg.Views {
		specs = append(specs, tui.ViewSpec{Name: v.Name, Filter: v.Filter, Position: v.Position})
		filters[v.Name] = v.Filter
	}
	compiled, err := tui.CompileViews(specs)
	if err != nil {
		return out, err
	}
	for _, name := range tui.ViewNames(compiled) {
		out.Views = append(out.Views, api.BrowserTUIView{Name: name, Filter: filters[name]})
	}

	// Keys: validated like the TUI's keymap, then passed as written.
	var keySpecs []tui.KeySpec
	for _, k := range tcfg.Keys {
		keySpecs = append(keySpecs, tui.KeySpec{Command: k.Command, Key: k.Key})
		out.Keys = append(out.Keys, api.BrowserTUIKey{Command: k.Command, Key: k.Key})
	}
	if _, err := tui.ResolveKeymap(keySpecs); err != nil {
		return out, err
	}

	// Colours: the built-in defaults overlaid by the config (an empty colour clears one).
	for _, c := range tui.DefaultColumnColors {
		out.ColumnColors[c.Name] = c.Color
	}
	var colorSpecs []tui.ColumnColorSpec
	for _, c := range tcfg.ColumnColors {
		colorSpecs = append(colorSpecs, tui.ColumnColorSpec{Name: c.Name, Color: c.Color})
		out.ColumnColors[c.Name] = c.Color
	}
	if _, err := tui.ResolveColumnColors(colorSpecs); err != nil {
		return out, err
	}
	for _, g := range tui.DefaultGlyphColors {
		out.GlyphColors[g.Name] = g.Color
	}
	var glyphSpecs []tui.GlyphColorSpec
	for _, g := range tcfg.GlyphColors {
		glyphSpecs = append(glyphSpecs, tui.GlyphColorSpec{Name: g.Name, Color: g.Color})
		out.GlyphColors[g.Name] = g.Color
	}
	if _, err := tui.ResolveGlyphColors(glyphSpecs); err != nil {
		return out, err
	}
	for name, color := range out.ColumnColors {
		if color == "" {
			delete(out.ColumnColors, name)
		}
	}
	for name, color := range out.GlyphColors {
		if color == "" {
			delete(out.GlyphColors, name)
		}
	}
	var widthSpecs []tui.ColumnWidthSpec
	for _, cw := range tcfg.ColumnWidths {
		widthSpecs = append(widthSpecs, tui.ColumnWidthSpec{Name: cw.Name, Max: cw.Max})
		out.ColumnWidths[cw.Name] = cw.Max
	}
	if _, err := tui.ResolveColumnWidths(widthSpecs); err != nil {
		return out, err
	}
	return out, nil
}
