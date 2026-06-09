package tui

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestTUIImportsAreThinClient mechanically enforces the structural rule that makes
// "no fixtures for state" true: the TUI may import internal/client and internal/api
// but NOT internal/store or daemon internals. So its only possible source of state
// is the real HTTP+JSON surface — a hard-coded render can't reach into the DB.
func TestTUIImportsAreThinClient(t *testing.T) {
	forbidden := []string{
		"github.com/lukastk/sesh/internal/store",
		"github.com/lukastk/sesh/internal/daemon",
		"github.com/lukastk/sesh/internal/tmux",
		"github.com/lukastk/sesh/internal/agents",
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, imp := range af.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			for _, bad := range forbidden {
				if p == bad || strings.HasPrefix(p, bad+"/") {
					t.Errorf("%s imports forbidden package %q — the TUI must be a thin client over internal/client only", f, p)
				}
			}
		}
	}
}
