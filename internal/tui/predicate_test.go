package tui

import (
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/api"
)

func row(mut func(*api.ThreadRow)) api.ThreadRow {
	r := api.ThreadRow{
		Thread: api.Thread{ID: "abc12345-0000", Name: "web", Machine: "mymain",
			AgentKind: "pi", Cwd: "/home/u/dev/box", Tags: []string{"jack", "p3"}},
		Head: api.Headful, Busy: api.BusyIdle, Attachment: api.Detached,
	}
	if mut != nil {
		mut(&r)
	}
	return r
}

func TestPredicateAtomsAndOps(t *testing.T) {
	cases := []struct {
		src  string
		mut  func(*api.ThreadRow)
		want bool
	}{
		{"headful", nil, true},
		{"headless", nil, false},
		{"idle", nil, true},
		{"busy", nil, false},
		{"busy", func(r *api.ThreadRow) { r.Busy = api.BusyBusy }, true},
		{"attached", nil, false},
		{"detached", nil, true},
		{"archived", nil, false},
		{"archived", func(r *api.ThreadRow) { r.Archived = true }, true},
		{"onhold", nil, false},
		{"onhold", func(r *api.ThreadRow) { r.OnHold = true }, true},
		{"not onhold", nil, true},
		{"onhold == true", func(r *api.ThreadRow) { r.OnHold = true }, true},
		{"ticketed", nil, false},
		{"ticketed", func(r *api.ThreadRow) { r.TicketsOpen = 2 }, true},
		{"agent == pi", nil, true},
		{"agent != pi", nil, false},
		{"machine == mymain and headful", nil, true},
		{"machine == other or agent == pi", nil, true},
		{"not (busy or archived)", nil, true},
		{`name ~ "^w"`, nil, true},
		{`cwd !~ "scratch"`, nil, true},
		{"tags == jack", nil, true},  // any-of semantics
		{"tags == nope", nil, false},
		{"tags != p3", nil, false},
		{"tickets == 0", nil, true},
		{"tickets != 0", func(r *api.ThreadRow) { r.TicketsOpen = 1 }, true},
		{"ticketed and not archived", func(r *api.ThreadRow) { r.TicketsOpen = 1 }, true},
		{"HEADFUL", nil, true}, // keywords are case-insensitive
	}
	for _, c := range cases {
		p, err := CompilePredicate(c.src)
		if err != nil {
			t.Fatalf("compile %q: %v", c.src, err)
		}
		if got := p.Eval(row(c.mut)); got != c.want {
			t.Errorf("%q = %v, want %v", c.src, got, c.want)
		}
	}
}

func TestPredicateLoudCompileErrors(t *testing.T) {
	for _, src := range []string{
		"",                 // empty
		"   ",              // blank
		"agent",            // lone selector, no operator
		"bogus == x",       // unknown selector
		"frobnicate",       // unknown atom
		"agent == ",        // missing operand
		"(busy",            // unclosed paren
		"busy extra",       // trailing junk
		`name ~ "["`,       // bad regex
		"agent = pi",       // single =
	} {
		if _, err := CompilePredicate(src); err == nil {
			t.Errorf("%q compiled silently (must be loud)", src)
		}
	}
	// The unknown-selector error teaches the valid set.
	_, err := CompilePredicate("bogus == x")
	if err == nil || !strings.Contains(err.Error(), "tickets") {
		t.Errorf("unknown-selector error should list valid selectors: %v", err)
	}
}

func TestCompileViewsLoud(t *testing.T) {
	if _, err := CompileViews([]ViewSpec{{Name: "x", Filter: "bogus =="}}); err == nil {
		t.Fatalf("broken view filter compiled silently")
	}
	if _, err := CompileViews([]ViewSpec{{Name: "", Filter: "busy"}}); err == nil {
		t.Fatalf("nameless view compiled silently")
	}
	vs, err := CompileViews([]ViewSpec{{Name: "tk", Filter: "ticketed"}})
	if err != nil || len(vs) != 1 || vs[0].name != "tk" {
		t.Fatalf("valid view failed: %v %v", vs, err)
	}
}
