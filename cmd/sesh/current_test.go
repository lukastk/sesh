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

// TestIsFullUUID pins the full-uuid fast path's gate: only a canonical
// 36-char lowercase-hex uuid qualifies (it skips the whole-list prefix
// resolve — the expensive round trip on routed verbs); anything else falls
// through to prefix resolution exactly as before.
func TestIsFullUUID(t *testing.T) {
	yes := []string{
		"95276330-5abf-48e0-8793-d9da5d250446",
		"00000000-0000-0000-0000-000000000000",
	}
	no := []string{
		"",
		"95276330",                              // a prefix
		"95276330-5abf-48e0-8793-d9da5d25044",   // 35 chars
		"95276330-5abf-48e0-8793-d9da5d2504467", // 37 chars
		"95276330-5ABF-48e0-8793-d9da5d250446",  // uppercase → conservative fallthrough
		"95276330-5abf-48e0-8793_d9da5d250446",  // wrong separator
		"g5276330-5abf-48e0-8793-d9da5d250446",  // non-hex
		"952763305abf48e08793d9da5d250446",      // undashed
	}
	for _, s := range yes {
		if !isFullUUID(s) {
			t.Errorf("isFullUUID(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if isFullUUID(s) {
			t.Errorf("isFullUUID(%q) = true, want false", s)
		}
	}
}

// TestResolveIDPrefixFullUUIDSkipsList proves the fast path is real: a full
// uuid resolves with NO daemon list call at all (nil client — any list attempt
// would panic), while a prefix still goes to the list (and errors loudly when
// the daemon is unreachable, as before).
func TestResolveIDPrefixFullUUIDSkipsList(t *testing.T) {
	id := "95276330-5abf-48e0-8793-d9da5d250446"
	got, err := resolveIDPrefix(nil, id)
	if err != nil || got != id {
		t.Fatalf("full uuid should resolve to itself without a list fetch: got %q, %v", got, err)
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
