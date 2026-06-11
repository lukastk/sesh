package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAbsCwd proves feature 4: a relative (or ~) --cwd is expanded to an absolute
// path against the invocation directory, so the daemon's "cwd must be absolute"
// contract is met without the user spelling out the full path.
func TestAbsCwd(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct{ in, want string }{
		{"", ""},                         // empty stays empty (caller's required-check handles it)
		{"/already/abs", "/already/abs"}, // absolute passes through (cleaned)
		{".", wd},                        // relative → invocation dir
		{"sub/dir", filepath.Join(wd, "sub/dir")}, // nested relative
		{"../x", filepath.Join(filepath.Dir(wd), "x")},
		{"~", home},                        // bare ~ → home
		{"~/dev/proj", home + "/dev/proj"}, // ~/ prefix → home-relative
	}
	for _, c := range cases {
		got, err := absCwd(c.in)
		if err != nil {
			t.Fatalf("absCwd(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("absCwd(%q) = %q, want %q", c.in, got, c.want)
		}
		if c.in != "" && !filepath.IsAbs(got) {
			t.Errorf("absCwd(%q) = %q is not absolute", c.in, got)
		}
	}
}
