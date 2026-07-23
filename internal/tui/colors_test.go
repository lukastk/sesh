package tui

import "testing"

func TestResolveColumnColorsDefaults(t *testing.T) {
	// No config → the built-in defaults: NAME blue, CWD green, TKT! red, nothing else.
	got, err := ResolveColumnColors(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got[ColName]; !ok {
		t.Errorf("NAME should be coloured by default")
	}
	if _, ok := got[ColCwd]; !ok {
		t.Errorf("CWD should be coloured by default")
	}
	if _, ok := got[ColTicketInput]; !ok {
		t.Errorf("TKT! (ticket_input) should be coloured by default")
	}
	if _, ok := got[ColMachine]; ok {
		t.Errorf("MACHINE should have no default colour")
	}
}

func TestResolveColumnColorsOverrideAndClear(t *testing.T) {
	// Override CWD, clear NAME (empty colour removes the default), add MACHINE.
	got, err := ResolveColumnColors([]ColumnColorSpec{
		{Name: "cwd", Color: "red"},
		{Name: "name", Color: ""},
		{Name: "machine", Color: "#ff8800"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got[ColName]; ok {
		t.Errorf("NAME colour should have been cleared")
	}
	if _, ok := got[ColCwd]; !ok {
		t.Errorf("CWD colour should remain (overridden)")
	}
	if _, ok := got[ColMachine]; !ok {
		t.Errorf("MACHINE colour should have been added")
	}
}

func TestResolveColumnColorsLoud(t *testing.T) {
	if _, err := ResolveColumnColors([]ColumnColorSpec{{Name: "bogus", Color: "green"}}); err == nil {
		t.Errorf("unknown column must be loud")
	}
	if _, err := ResolveColumnColors([]ColumnColorSpec{{Name: "cwd", Color: "not-a-color"}}); err == nil {
		t.Errorf("bad colour must be loud")
	}
	if _, err := ResolveColumnColors([]ColumnColorSpec{{Name: "", Color: "green"}}); err == nil {
		t.Errorf("missing name must be loud")
	}
}

func TestResolveGlyphColorsDefaults(t *testing.T) {
	// No config → both running-state glyphs coloured (bright green).
	got, err := ResolveGlyphColors(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{GlyphBusy, GlyphDescendant} {
		if _, ok := got[name]; !ok {
			t.Errorf("%s should be coloured by default", name)
		}
	}
}

func TestResolveGlyphColorsOverrideAndClear(t *testing.T) {
	got, err := ResolveGlyphColors([]GlyphColorSpec{
		{Name: "busy", Color: "2"},
		{Name: "descendant", Color: ""}, // empty colour clears the default
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got[GlyphBusy]; !ok {
		t.Errorf("busy colour should remain (overridden)")
	}
	if _, ok := got[GlyphDescendant]; ok {
		t.Errorf("descendant colour should have been cleared")
	}
}

func TestResolveGlyphColorsLoud(t *testing.T) {
	if _, err := ResolveGlyphColors([]GlyphColorSpec{{Name: "bogus", Color: "green"}}); err == nil {
		t.Errorf("unknown glyph must be loud")
	}
	if _, err := ResolveGlyphColors([]GlyphColorSpec{{Name: "busy", Color: "not-a-color"}}); err == nil {
		t.Errorf("bad colour must be loud")
	}
	if _, err := ResolveGlyphColors([]GlyphColorSpec{{Name: "", Color: "green"}}); err == nil {
		t.Errorf("missing name must be loud")
	}
}

func TestParseColor(t *testing.T) {
	for _, ok := range []string{"green", "Blue", "magenta", "0", "255", "#ff8800", "12"} {
		if _, err := parseColor(ok); err != nil {
			t.Errorf("parseColor(%q) should succeed: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "chartreuse", "256", "-1", "#fff", "#gggggg", "0xff"} {
		if _, err := parseColor(bad); err == nil {
			t.Errorf("parseColor(%q) should be loud", bad)
		}
	}
}
