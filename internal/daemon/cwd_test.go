package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExpandHomeCwd proves the owner-side ~ expansion: a leading ~ (or ~/…) in a
// thread cwd resolves against THIS daemon's home — the form a remote client (the
// Obsidian plugin) sends so a picked path is portable onto another machine —
// while a non-~ path is returned untouched (the absolute-path check still applies).
func TestExpandHomeCwd(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct{ in, want string }{
		{"", ""},                                         // empty stays empty
		{"/abs/path", "/abs/path"},                       // absolute untouched
		{"rel/path", "rel/path"},                         // bare relative untouched (caller rejects it)
		{"~", home},                                      // bare ~ → home
		{"~/dev/proj", filepath.Join(home, "dev/proj")},  // ~/ prefix → home-relative
		{"~root/x", "~root/x"},                           // ~user form is NOT expanded (left as-is)
	}
	for _, c := range cases {
		if got := expandHomeCwd(c.in); got != c.want {
			t.Errorf("expandHomeCwd(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	// A ~-relative cwd must come out absolute (it's what the daemon then validates).
	if got := expandHomeCwd("~/x"); !strings.HasPrefix(got, "/") {
		t.Errorf("expandHomeCwd(~/x) = %q, want an absolute path", got)
	}
}
