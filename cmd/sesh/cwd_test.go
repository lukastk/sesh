package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAbsCwd proves feature 4: a relative --cwd is expanded to an absolute path
// against the invocation directory, so the daemon's "cwd must be absolute"
// contract is met without the user spelling out the full path. A leading ~ is
// deliberately passed THROUGH unchanged — it means "the OWNER machine's home"
// and is resolved by the owning daemon (expandHomeCwd), so a ~-relative cwd
// stays portable across a cross-machine spawn instead of baking in the local home.
func TestAbsCwd(t *testing.T) {
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
		{"~", "~"},                   // bare ~ → untouched (owner daemon resolves it)
		{"~/dev/proj", "~/dev/proj"}, // ~/ prefix → untouched (portable cross-machine)
	}
	for _, c := range cases {
		got, err := absCwd(c.in)
		if err != nil {
			t.Fatalf("absCwd(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("absCwd(%q) = %q, want %q", c.in, got, c.want)
		}
		// Relative inputs must come out absolute; ~-inputs intentionally do not.
		if c.in != "" && !strings.HasPrefix(c.in, "~") && !filepath.IsAbs(got) {
			t.Errorf("absCwd(%q) = %q is not absolute", c.in, got)
		}
	}
}
