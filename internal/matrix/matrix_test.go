package matrix

import (
	"strings"
	"testing"
)

// These are unit tests of the harness spine itself (pure logic), not matrix
// cells. AGENTS.md explicitly allows tests outside the matrix; the per-cell
// expectation applies only to registered conformant features.

func TestExpectedCellsAgentic(t *testing.T) {
	f := Feature{
		ID:         "x",
		Agents:     AllAgents,
		Localities: AllLocalities,
	}
	got := f.ExpectedCells()
	if len(got) != 6 { // 3 agents x 2 localities
		t.Fatalf("want 6 expected cells, got %d: %v", len(got), got)
	}
}

func TestExpectedCellsAgnostic(t *testing.T) {
	f := Feature{ID: "x", Localities: []Locality{Local}}
	got := f.ExpectedCells()
	if len(got) != 1 || got[0].Agent != AgentAgnostic || got[0].Locality != Local {
		t.Fatalf("agnostic single-locality feature: got %v", got)
	}
}

func TestExpectedCellsExcludesNA(t *testing.T) {
	f := Feature{
		ID:         "x",
		Agents:     AllAgents,
		Localities: AllLocalities,
		NA:         []NAEntry{{Agent: Pi, Locality: Remote, Reason: "pi has no headless mode — by design"}},
	}
	got := f.ExpectedCells()
	if len(got) != 5 {
		t.Fatalf("want 5 expected cells after one N/A, got %d", len(got))
	}
	for _, c := range got {
		if c.Agent == Pi && c.Locality == Remote {
			t.Fatalf("N/A cell leaked into expected cells: %s", c.ID())
		}
	}
	if len(f.NACells()) != 1 {
		t.Fatalf("want 1 N/A cell, got %d", len(f.NACells()))
	}
	if reason, ok := f.NAReason(Pi, Remote); !ok || reason == "" {
		t.Fatal("NAReason should report the justification")
	}
}

func TestRegisterTestRejectsOffAxisCell(t *testing.T) {
	Register(Feature{ID: "_ut.offaxis", Localities: []Locality{Local}}) // agent-agnostic, local only
	defer mustPanic(t, "off-axis agent")
	// codex is off-axis for an agent-agnostic feature.
	RegisterTest("_ut.offaxis", Codex, Local, func(t *testing.T) {})
}

func TestRegisterTestRejectsUnknownFeature(t *testing.T) {
	defer mustPanic(t, "unknown feature")
	RegisterTest("_ut.does-not-exist", AgentAgnostic, Local, func(t *testing.T) {})
}

func TestRegisterRejectsNAWithoutReason(t *testing.T) {
	defer mustPanic(t, "missing N/A justification")
	Register(Feature{
		ID:         "_ut.badna",
		Localities: []Locality{Local},
		NA:         []NAEntry{{Agent: AgentAgnostic, Locality: Local}}, // no reason
	})
}

func TestMissingCellsDetectsUnbound(t *testing.T) {
	Register(Feature{ID: "_ut.missing", Localities: []Locality{Local, Remote}})
	missing := MissingCells()
	var found int
	for _, c := range missing {
		if c.Feature == "_ut.missing" {
			found++
		}
	}
	if found != 2 {
		t.Fatalf("want 2 missing cells for _ut.missing, got %d", found)
	}
}

func TestParseStatusRoundTrip(t *testing.T) {
	for _, s := range []Status{StatusPass, StatusFail, StatusSkip, StatusNA, StatusMissing, StatusNotRun} {
		if got := ParseStatus(s.String()); got != s {
			t.Errorf("round trip failed for %v: got %v", s, got)
		}
	}
}

func TestAllGreenGate(t *testing.T) {
	if (Counts{Pass: 5}).AllGreen() != true {
		t.Error("5 pass, nothing else should be all-green")
	}
	if (Counts{Pass: 5, Skip: 1}).AllGreen() != false {
		t.Error("any skip must block all-green")
	}
	if (Counts{}).AllGreen() != false {
		t.Error("zero passes is not all-green")
	}
}

func mustPanic(t *testing.T, wantSubstr string) {
	t.Helper()
	r := recover()
	if r == nil {
		t.Fatalf("expected panic containing %q, got none", wantSubstr)
	}
	if msg, ok := r.(string); ok && !strings.Contains(msg, wantSubstr) {
		// not fatal: panic message wording may vary; just log
		t.Logf("panic message %q did not contain %q", msg, wantSubstr)
	}
}
