package config

import "testing"

// TestLoadTUIMouseScroll covers the [tui] mouse-wheel sensitivity keys, including that
// they coexist with [[tui.views]] / [[tui.column_color]] (the shape myrig ships), that
// unset defaults to 1, and that a negative is a loud error.
func TestLoadTUIMouseScroll(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, `
[tui]
mouse_scroll_v = 3
mouse_scroll_h = 2

[[tui.views]]
name   = "ticketed"
filter = "ticketed and not archived"

[[tui.column_color]]
name  = "cwd"
color = "green"
`)
	c, err := LoadTUI(home)
	if err != nil {
		t.Fatal(err)
	}
	if c == nil {
		t.Fatal("nil TUI config")
	}
	if c.ScrollV() != 3 || c.ScrollH() != 2 {
		t.Errorf("ScrollV/H = %d/%d, want 3/2", c.ScrollV(), c.ScrollH())
	}
	if len(c.Views) != 1 || len(c.ColumnColors) != 1 {
		t.Errorf("views/colors did not coexist with [tui] scalars: %d views, %d colors", len(c.Views), len(c.ColumnColors))
	}

	// Unset → defaults to 1.
	home2 := t.TempDir()
	writeConfig(t, home2, "[tui]\ncolumns = [\"name\"]\n")
	c2, err := LoadTUI(home2)
	if err != nil {
		t.Fatal(err)
	}
	if c2.ScrollV() != 1 || c2.ScrollH() != 1 {
		t.Errorf("unset scroll should default to 1/1, got %d/%d", c2.ScrollV(), c2.ScrollH())
	}

	// Negative is a loud error.
	home3 := t.TempDir()
	writeConfig(t, home3, "[tui]\nmouse_scroll_v = -1\n")
	if _, err := LoadTUI(home3); err == nil {
		t.Errorf("a negative mouse_scroll_v should be a loud error")
	}
}

// TestLoadTUIAllMachines covers the [tui] all_machines default (makes the TUI a
// cross-machine view without --all-machines / the shell wrapper).
func TestLoadTUIAllMachines(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "[tui]\nall_machines = true\n")
	c, err := LoadTUI(home)
	if err != nil {
		t.Fatal(err)
	}
	if c == nil || !c.AllMachines {
		t.Fatalf("all_machines = true not parsed: %+v", c)
	}
	// Unset → false (self-only default).
	home2 := t.TempDir()
	writeConfig(t, home2, "[tui]\ncolumns = [\"name\"]\n")
	c2, err := LoadTUI(home2)
	if err != nil {
		t.Fatal(err)
	}
	if c2.AllMachines {
		t.Errorf("all_machines should default to false when unset")
	}
}

// TestLoadTUIShowOffline covers the [tui] show_offline default (whether an OFFLINE
// machine's stale threads are shown by default; unset → hidden).
func TestLoadTUIShowOffline(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "[tui]\nshow_offline = true\n")
	c, err := LoadTUI(home)
	if err != nil {
		t.Fatal(err)
	}
	if c == nil || !c.ShowOffline {
		t.Fatalf("show_offline = true not parsed: %+v", c)
	}
	// Unset → false (offline threads hidden by default).
	home2 := t.TempDir()
	writeConfig(t, home2, "[tui]\ncolumns = [\"name\"]\n")
	c2, err := LoadTUI(home2)
	if err != nil {
		t.Fatal(err)
	}
	if c2.ShowOffline {
		t.Errorf("show_offline should default to false when unset")
	}
}
