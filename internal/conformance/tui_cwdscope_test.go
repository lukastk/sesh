package conformance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/matrix"
	"github.com/lukastk/sesh/internal/tui"
)

func init() {
	registerTUIClaim("cwd-launch-scope", claimCwdLaunchScope)
}

// claimCwdLaunchScope proves the two launch restrictions against rows read from a
// real daemon: exact CWD excludes a child; tree CWD adds the child but not a sibling
// whose string merely shares the prefix. Both start in `all` (so an archived exact
// row is present), while the configured view picker remains intact.
func claimCwdLaunchScope(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)

	base := t.TempDir()
	root := filepath.Join(base, "project")
	childDir := filepath.Join(root, "internal", "tui")
	prefixSibling := filepath.Join(base, "project-other")
	for _, dir := range []string{root, childDir, prefixSibling} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	exact := sb.newHeadlessThreadAt(t, "pi", "scope-exact", root)
	child := sb.newHeadlessThreadAt(t, "pi", "scope-child", childDir)
	sibling := sb.newHeadlessThreadAt(t, "pi", "scope-prefix-sibling", prefixSibling)
	if _, stderr, err := sb.Runner.Run(t, "thread", "archive", "--id", exact.ID); err != nil {
		t.Fatalf("archive exact row: %v\n%s", err, stderr)
	}

	views, err := tui.CompileViews([]tui.ViewSpec{{Name: "scope configured", Filter: "agent==pi"}})
	if err != nil {
		t.Fatal(err)
	}
	newModel := func(descendants bool) tui.Model {
		m := tui.New(sb.Home+"/daemon.sock", false).WithViews(views)
		m, err = m.WithCwdScope(root, descendants)
		if err != nil {
			t.Fatal(err)
		}
		m, err = m.WithInitialView("all")
		if err != nil {
			t.Fatal(err)
		}
		return m
	}

	exactModel := renderUntilCount(t, newModel(false), 1)
	if exactModel.CurrentView() != tui.ViewAll {
		t.Fatalf("exact launch opened view %v, want all", exactModel.CurrentView())
	}
	if got := modelRowNames(exactModel); len(got) != 1 || got[0] != exact.Name {
		t.Fatalf("exact launch rows = %v, want only %q (archived, proving initial all)", got, exact.Name)
	}
	// Tab opens the ordinary configured view picker; launch scoping must not replace
	// or collapse the user's view set.
	exactModel = runKey(t, exactModel, "tab")
	if view := exactModel.View(); !strings.Contains(view, "scope configured") || !strings.Contains(view, "all") {
		t.Fatalf("scoped launch lost configured/built-in views:\n%s", view)
	}
	exactModel = runKey(t, exactModel, "down") // all -> configured view
	exactModel = runKey(t, exactModel, "enter")
	exactModel = renderUntilCount(t, exactModel, 1)
	if view := exactModel.View(); !strings.Contains(view, "[scope configured]") {
		t.Fatalf("could not switch to configured view inside scope:\n%s", view)
	}
	if got := modelRowNames(exactModel); len(got) != 1 || got[0] != exact.Name {
		t.Fatalf("configured view escaped exact launch scope: %v", got)
	}

	treeModel := renderUntilCount(t, newModel(true), 2)
	got := modelRowNames(treeModel)
	if !containsString(got, exact.Name) || !containsString(got, child.Name) {
		t.Fatalf("tree launch rows = %v, want exact %q + child %q", got, exact.Name, child.Name)
	}
	if containsString(got, sibling.Name) {
		t.Fatalf("tree launch admitted prefix sibling %q: %v", sibling.Name, got)
	}
	// `/` remains the ordinary second-stage fuzzy filter inside the tree boundary.
	treeModel = runKey(t, treeModel, "/")
	treeModel = typeText(t, treeModel, "scope-child")
	view := treeModel.View()
	if !strings.Contains(view, child.Name) || strings.Contains(view, exact.Name) || strings.Contains(view, sibling.Name) {
		t.Fatalf("interactive filter did not narrow within tree scope:\n%s", view)
	}
}

func modelRowNames(m tui.Model) []string {
	out := make([]string, 0, len(m.Rows()))
	for _, row := range m.Rows() {
		out = append(out, row.Name)
	}
	return out
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
