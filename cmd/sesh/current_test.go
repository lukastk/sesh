package main

import (
	"flag"
	"strings"
	"testing"
)

// TestGuardEmptyIDFlag proves the empty-selector footgun guard: an --id that is
// EXPLICITLY passed but empty (e.g. `--id "$X"` with $X unset) is a loud error,
// while an OMITTED --id is fine (that is the intended current-thread inference).
func TestGuardEmptyIDFlag(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{"omitted infers (no error)", []string{}, false},
		{"explicit empty errors", []string{"--id", ""}, true},
		{"explicit whitespace errors", []string{"--id", "   "}, true},
		{"explicit value is fine", []string{"--id", "abc123"}, false},
		{"id=form empty errors", []string{"--id="}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("t", flag.ContinueOnError)
			fs.String("id", "", "")
			if err := fs.Parse(tc.args); err != nil {
				t.Fatalf("parse: %v", err)
			}
			err := guardEmptyIDFlag(fs)
			if tc.wantErr && err == nil {
				t.Errorf("args %v: expected a loud error, got nil", tc.args)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("args %v: expected no error, got %v", tc.args, err)
			}
			if err != nil && !strings.Contains(err.Error(), "--id") {
				t.Errorf("error should name --id: %v", err)
			}
		})
	}
}

// TestGuardEmptyFlagNamed proves the guard generalizes to other selector flags
// (e.g. `hooks test --thread`).
func TestGuardEmptyFlagNamed(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.String("thread", "", "")
	if err := fs.Parse([]string{"--thread", ""}); err != nil {
		t.Fatal(err)
	}
	if err := guardEmptyFlag(fs, "thread"); err == nil {
		t.Errorf("explicit empty --thread should be a loud error")
	}

	fs2 := flag.NewFlagSet("t", flag.ContinueOnError)
	fs2.String("thread", "", "")
	if err := fs2.Parse([]string{}); err != nil {
		t.Fatal(err)
	}
	if err := guardEmptyFlag(fs2, "thread"); err != nil {
		t.Errorf("omitted --thread should be fine (synthetic default): %v", err)
	}
}

// TestGuardEmptyPositionalRef proves the positional twin (`sesh info ""`): an
// explicitly-supplied empty positional id is a loud error; an omitted one is fine.
func TestGuardEmptyPositionalRef(t *testing.T) {
	if err := guardEmptyPositionalRef(true, ""); err == nil {
		t.Errorf("supplied empty positional should be a loud error")
	}
	if err := guardEmptyPositionalRef(true, "   "); err == nil {
		t.Errorf("supplied whitespace positional should be a loud error")
	}
	if err := guardEmptyPositionalRef(false, ""); err != nil {
		t.Errorf("omitted positional should be fine (inference): %v", err)
	}
	if err := guardEmptyPositionalRef(true, "abc"); err != nil {
		t.Errorf("supplied non-empty positional should be fine: %v", err)
	}
}
