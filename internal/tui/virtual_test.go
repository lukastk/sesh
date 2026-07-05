package tui

import (
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/api"
)

func virtualRow(machine string) api.ThreadRow {
	return api.ThreadRow{
		Thread: api.Thread{ID: "beefcafe-0000-0000-0000-000000000000", Name: "project X",
			Machine: machine, AgentKind: api.VirtualAgentKind, SessionName: "virtual-beefcafe"},
		Head: api.Headless, Busy: api.BusyIdle,
	}
}

// Enter on a VIRTUAL thread is a loud warning, never a nav/revive (Lukas's
// decision: a warning, not a fold toggle). The refusal must not shell anything
// out — navSelected's returned command yields the actionMsg directly.
func TestEnterVirtualThreadWarns(t *testing.T) {
	m := Model{
		machine:  "mymain",
		rows:     []api.ThreadRow{virtualRow("mymain")},
		machines: []api.MachineView{{Machine: "mymain", Self: true, Reachable: true}},
	}
	cmd := m.navSelected()
	if cmd == nil {
		t.Fatal("navSelected returned no cmd")
	}
	msg, ok := cmd().(actionMsg)
	if !ok {
		t.Fatalf("want actionMsg, got %T", cmd())
	}
	if msg.err == nil {
		t.Fatal("entering a virtual thread must refuse loudly")
	}
	for _, want := range []string{"virtual", "realize"} {
		if !strings.Contains(msg.err.Error(), want) {
			t.Errorf("warning %q should mention %q", msg.err, want)
		}
	}
}

// The refusal is row-shaped, not machine-shaped: a REMOTE virtual row (owner
// reachable) warns the same way instead of routing a doomed resume.
func TestEnterRemoteVirtualThreadWarns(t *testing.T) {
	m := Model{
		machine: "mymain",
		rows:    []api.ThreadRow{virtualRow("macbook")},
		machines: []api.MachineView{
			{Machine: "mymain", Self: true, Reachable: true},
			{Machine: "macbook", Reachable: true},
		},
	}
	msg, ok := m.navSelected()().(actionMsg)
	if !ok || msg.err == nil || !strings.Contains(msg.err.Error(), "virtual") {
		t.Fatalf("remote virtual enter must warn, got %#v", msg)
	}
}

// Forking a virtual thread refuses with a clear message (the daemon would refuse
// too, but via the opaque "unknown agent" parse error).
func TestForkVirtualThreadWarns(t *testing.T) {
	m := Model{
		machine:  "mymain",
		rows:     []api.ThreadRow{virtualRow("mymain")},
		machines: []api.MachineView{{Machine: "mymain", Self: true, Reachable: true}},
	}
	msg, ok := m.forkSelected()().(actionMsg)
	if !ok || msg.err == nil {
		t.Fatalf("fork on virtual must refuse loudly, got %#v", msg)
	}
	if !strings.Contains(msg.err.Error(), "virtual") || !strings.Contains(msg.err.Error(), "realize") {
		t.Errorf("fork warning should name virtual + realize: %q", msg.err)
	}
}

// A virtual row renders the ◇ head glyph (distinct from headless ◌); the busy
// axis stays the normal idle dot. Non-virtual rows are untouched.
func TestVirtualHeadGlyph(t *testing.T) {
	if got := HeadGlyph(virtualRow("mymain")); got != "◇" {
		t.Fatalf("virtual head glyph: want ◇, got %q", got)
	}
	real := api.ThreadRow{Thread: api.Thread{AgentKind: "pi"}, Head: api.Headless}
	if got := HeadGlyph(real); got != "◌" {
		t.Fatalf("headless head glyph regressed: want ◌, got %q", got)
	}
	if got := BusyGlyph(virtualRow("mymain")); got != "·" {
		t.Fatalf("virtual busy glyph: want ·, got %q", got)
	}
}
