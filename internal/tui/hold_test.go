package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/lukastk/sesh/internal/api"
)

// holdArgvModel builds a one-row model whose routed verbs run a REAL fake `sesh`
// on disk that logs its argv, so a test asserts the command actually issued rather
// than an internal flag. Which command an un-hold issues is the whole behaviour
// here: a clear cannot undercut an ancestor's hold, only a release can.
func holdArgvModel(t *testing.T, row api.ThreadRow) (Model, func() []string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "argv.log")
	bin := filepath.Join(dir, "sesh")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s ' \"$@\" >> %q\nprintf '\\n' >> %q\n", log, log)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	m := Model{
		machine:    row.Machine,
		rows:       []api.ThreadRow{row},
		machines:   reachableMachines(row.Machine),
		binaryPath: bin,
	}
	m.machines[0].Self = true
	return m, func() []string {
		b, err := os.ReadFile(log)
		if err != nil {
			return nil
		}
		var out []string
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				out = append(out, l)
			}
		}
		return out
	}
}

func runHoldCmd(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a command to be issued")
	}
	cmd()
}

// THE REPORTED BUG: `h` on a thread held only by an ANCESTOR used to decide on the
// thread's OWN deadline, which was 0 — so the "un-hold" key took the SET branch and
// parked it until tomorrow. Worse, that left behind an own hold that OUTLIVED the
// parent's, so releasing the parent no longer freed the child. The toggle now keys
// on the effective state and issues a RELEASE.
func TestHoldToggleReleasesInheritedHold(t *testing.T) {
	now := time.Now().Unix()
	row := api.ThreadRow{
		Thread:              api.Thread{ID: "child", Name: "child", Machine: "mymain"},
		OnHold:              true,
		OnHoldEffectiveUnix: now + 7200, // parked by an ancestor; own deadline is 0
	}
	m, argv := holdArgvModel(t, row)
	runHoldCmd(t, m.holdToggleSelected())

	got := argv()
	if len(got) != 1 {
		t.Fatalf("expected exactly one routed command, got %v", got)
	}
	if !strings.Contains(got[0], "--release") {
		t.Errorf("un-holding a thread held by its ANCESTOR must issue --release, got: %s", got[0])
	}
	if strings.Contains(got[0], "--clear") {
		t.Errorf("a clear cannot undercut an ancestor's hold: %s", got[0])
	}
	// The regression itself: the toggle must never issue a plain hold here.
	if strings.Contains(got[0], "--until-unix") && !strings.Contains(got[0], "--release") {
		t.Errorf("the un-hold key issued a HOLD — the reported bug: %s", got[0])
	}
}

// A thread held by its OWN deadline is un-held by clearing it: no release is
// needed (nothing dominates it), and a release would leave stale state behind.
func TestHoldToggleClearsOwnHold(t *testing.T) {
	now := time.Now().Unix()
	row := api.ThreadRow{
		Thread:              api.Thread{ID: "own", Name: "own", Machine: "mymain", OnHoldUntilUnix: now + 3600},
		OnHold:              true,
		OnHoldEffectiveUnix: now + 3600,
	}
	m, argv := holdArgvModel(t, row)
	runHoldCmd(t, m.holdToggleSelected())

	got := argv()
	if len(got) != 1 || !strings.Contains(got[0], "--clear") {
		t.Fatalf("un-holding an own-held thread should --clear, got %v", got)
	}
	if strings.Contains(got[0], "--release") {
		t.Errorf("no ancestor dominates this thread — a release is not needed: %s", got[0])
	}
}

// An un-held thread is parked until the start of tomorrow (the other half of the
// toggle, unchanged) — including a RELEASED one, since setting a hold zeroes the
// release, so `h` still means "held" / "not held".
func TestHoldToggleHoldsUnheldAndReleasedThreads(t *testing.T) {
	now := time.Now().Unix()
	for _, tc := range []struct {
		name string
		row  api.ThreadRow
	}{
		{"plain", api.ThreadRow{Thread: api.Thread{ID: "a", Name: "a", Machine: "mymain"}}},
		{"released", api.ThreadRow{Thread: api.Thread{ID: "b", Name: "b", Machine: "mymain", HoldReleaseUntilUnix: now + 3600}}},
	} {
		m, argv := holdArgvModel(t, tc.row)
		runHoldCmd(t, m.holdToggleSelected())
		got := argv()
		if len(got) != 1 || !strings.Contains(got[0], "--until-unix") {
			t.Fatalf("%s: an un-held thread should be parked, got %v", tc.name, got)
		}
		if strings.Contains(got[0], "--release") || strings.Contains(got[0], "--clear") {
			t.Errorf("%s: expected a plain hold, got: %s", tc.name, got[0])
		}
	}
}

// The `H` prompt's EMPTY input means "not held", so it follows the same rule as the
// toggle: a thread dominated by an ancestor needs a release, not a clear.
func TestHoldPromptEmptyReleasesInheritedHold(t *testing.T) {
	now := time.Now().Unix()
	row := api.ThreadRow{
		Thread:              api.Thread{ID: "child", Name: "child", Machine: "mymain"},
		OnHold:              true,
		OnHoldEffectiveUnix: now + 7200,
	}
	m, argv := holdArgvModel(t, row)
	mm, _ := m.runCommand("hold-until")
	m = mm.(Model)
	mm, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = mm.(Model)
	runHoldCmd(t, cmd)

	got := argv()
	if len(got) != 1 || !strings.Contains(got[0], "--release") {
		t.Fatalf("an empty H prompt on an inherited hold should --release, got %v", got)
	}
}

// The HOLD column distinguishes the three states, because a released thread is
// otherwise indistinguishable from an ordinary un-held one — leaving no way to see
// why a thread sits in the active view while its parent is parked.
func TestHoldColumnRendersReleaseAndInheritance(t *testing.T) {
	now := time.Now().Unix()
	future := now + 48*3600
	cell := colSpecByName(t, ColHold)
	m := &Model{}

	own := api.ThreadRow{Thread: api.Thread{OnHoldUntilUnix: future}, OnHold: true, OnHoldEffectiveUnix: future}
	if got := cell(m, own); strings.HasPrefix(got, "↑") || strings.HasPrefix(got, "~") || got == "" {
		t.Errorf("own hold should render a bare date, got %q", got)
	}
	inherited := api.ThreadRow{Thread: api.Thread{}, OnHold: true, OnHoldEffectiveUnix: future}
	if got := cell(m, inherited); !strings.HasPrefix(got, "↑") {
		t.Errorf("an inherited hold should render the ↑ marker, got %q", got)
	}
	released := api.ThreadRow{Thread: api.Thread{HoldReleaseUntilUnix: future}}
	if got := cell(m, released); !strings.HasPrefix(got, "~") {
		t.Errorf("a live release should render the ~ marker, got %q", got)
	}
	lapsed := api.ThreadRow{Thread: api.Thread{HoldReleaseUntilUnix: now - 3600}}
	if got := cell(m, lapsed); got != "" {
		t.Errorf("a LAPSED release must render nothing, got %q", got)
	}
}

// colSpecByName finds a column's cell renderer so a test can drive it directly.
func colSpecByName(t *testing.T, name string) func(*Model, api.ThreadRow) string {
	t.Helper()
	for _, c := range colOrder {
		if c.name == name {
			return c.cell
		}
	}
	t.Fatalf("no column named %q", name)
	return nil
}
