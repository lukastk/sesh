package tui

import (
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/api"
)

func TestCwdScopeExactAndDescendants(t *testing.T) {
	rows := []api.ThreadRow{
		{Thread: api.Thread{ID: "exact", Cwd: "/home/linux/mysetup/sesh"}, CwdRel: "~/mysetup/sesh"},
		{Thread: api.Thread{ID: "child", Cwd: "/Users/mac/mysetup/sesh/internal/tui"}, CwdRel: "~/mysetup/sesh/internal/tui"},
		{Thread: api.Thread{ID: "sibling-prefix", Cwd: "/home/linux/mysetup/sesh-ui"}, CwdRel: "~/mysetup/sesh-ui"},
		{Thread: api.Thread{ID: "empty"}},
	}

	exact := cwdScope{root: "~/mysetup/sesh"}
	wantExact := map[string]bool{"exact": true}
	for _, row := range rows {
		if got := exact.admits(row); got != wantExact[row.ID] {
			t.Errorf("exact scope admits(%s) = %v, want %v", row.ID, got, wantExact[row.ID])
		}
	}

	tree := cwdScope{root: "~/mysetup/sesh", descendants: true}
	wantTree := map[string]bool{"exact": true, "child": true}
	for _, row := range rows {
		if got := tree.admits(row); got != wantTree[row.ID] {
			t.Errorf("tree scope admits(%s) = %v, want %v", row.ID, got, wantTree[row.ID])
		}
	}
}

func TestCwdScopeAbsoluteOutsideHome(t *testing.T) {
	scope := cwdScope{root: "/srv/work", descendants: true}
	for _, tc := range []struct {
		cwd string
		ok  bool
	}{
		{"/srv/work", true},
		{"/srv/work/sub", true},
		{"/srv/worker", false},
		{"/srv", false},
	} {
		row := api.ThreadRow{Thread: api.Thread{Cwd: tc.cwd}, CwdRel: "~/irrelevant"}
		if got := scope.admits(row); got != tc.ok {
			t.Errorf("absolute tree scope admits(%q) = %v, want %v", tc.cwd, got, tc.ok)
		}
	}
}

func TestCwdScopeIsOrthogonalToViews(t *testing.T) {
	in := api.ThreadSnapshot{Thread: api.Thread{ID: "in", Name: "in", Machine: "m", Cwd: "/home/u/proj", Archived: true}, CwdRel: "~/proj"}
	out := api.ThreadSnapshot{Thread: api.Thread{ID: "out", Name: "out", Machine: "m", Cwd: "/home/u/other", Archived: true}, CwdRel: "~/other"}
	mesh := []api.MachineView{{Machine: "m", Self: true, Reachable: true, Threads: []api.ThreadSnapshot{in, out}}}
	scope := &cwdScope{root: "~/proj"}

	// `all` must include archived/on-hold state, but the launch scope still excludes
	// the unrelated directory. A later view switch applies a different view predicate
	// without dropping that directory boundary.
	rows, _ := flattenMeshRows(mesh, ViewAll, nil, scope, false, true, "")
	if len(rows) != 1 || rows[0].ID != "in" {
		t.Fatalf("all view under exact scope = %v, want only in", ids(rows))
	}
	rows, _ = flattenMeshRows(mesh, ViewActive, nil, scope, false, true, "")
	if len(rows) != 0 {
		t.Fatalf("active view should hide the scoped archived row, got %v", ids(rows))
	}
}

func TestWithInitialView(t *testing.T) {
	pred, err := CompilePredicate("flagged")
	if err != nil {
		t.Fatal(err)
	}
	m := Model{}.WithViews([]customView{{name: "flagged", pred: pred}})
	m, err = m.WithInitialView("all")
	if err != nil || m.CurrentView() != ViewAll {
		t.Fatalf("WithInitialView(all) = view %v, err %v", m.CurrentView(), err)
	}
	m, err = m.WithInitialView("flagged")
	if err != nil || m.viewName() != "flagged" {
		t.Fatalf("WithInitialView(flagged) = %q, err %v", m.viewName(), err)
	}
	if _, err := m.WithInitialView("missing"); err == nil || !strings.Contains(err.Error(), "valid:") {
		t.Fatalf("unknown initial view should be loud with valid names, got %v", err)
	}
}

func TestWithCwdScopeRejectsCallerRelativePath(t *testing.T) {
	if _, err := (Model{}).WithCwdScope("relative/path", false); err == nil {
		t.Fatal("relative launch scope must be rejected rather than interpreted against the daemon cwd")
	}
}
