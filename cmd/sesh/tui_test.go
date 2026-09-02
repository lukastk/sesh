package main

import (
	"path/filepath"
	"testing"
)

func TestNormalizeTUICwdScope(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "home", "lukas")
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"home", home, "~"},
		{"bare tilde", "~", "~"},
		{"inside home", filepath.Join(home, "mysetup", "sesh"), filepath.Join("~", "mysetup", "sesh")},
		{"outside home", filepath.Join(string(filepath.Separator), "srv", "work"), filepath.Join(string(filepath.Separator), "srv", "work")},
		{"home prefix is not containment", filepath.Join(string(filepath.Separator), "home", "lukastk", "work"), filepath.Join(string(filepath.Separator), "home", "lukastk", "work")},
		{"tilde", "~/mysetup/sesh", filepath.Join("~", "mysetup", "sesh")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeTUICwdScope(tc.in, home)
			if err != nil {
				t.Fatalf("normalizeTUICwdScope(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("normalizeTUICwdScope(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeTUICwdScopeRefusesUnsupportedHomeSyntax(t *testing.T) {
	if _, err := normalizeTUICwdScope("~other/work", "/home/lukas"); err == nil {
		t.Fatal("~other must be refused rather than interpreted as a relative directory")
	}
}

func TestTUICwdScopeFlagConflictsAreLoud(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"both scopes", []string{"--cwd", ".", "--cwd-tree", "."}},
		{"scope and cursor", []string{"--cwd", ".", "--cursor"}},
		{"explicit empty scope", []string{"--cwd", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := runTUI(tc.args); err == nil {
				t.Fatalf("runTUI(%v) should refuse before launching", tc.args)
			}
		})
	}
}
