package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

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

// keyMsg builds the tea.KeyMsg for a named key ("enter") or a rune key ("v").
func keyMsg(k string) tea.KeyMsg {
	if k == "enter" {
		return tea.KeyMsg{Type: tea.KeyEnter}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

// `v` opens the new-virtual-group name prompt; an empty submit cancels with no
// command (nothing shells out); the prompt label names the TARGET MACHINE (the
// selection is only a machine carrier), and a remote selection routes creation
// to that machine.
func TestNewVirtualGroupPrompt(t *testing.T) {
	m := Model{
		machine: "mymain",
		rows: []api.ThreadRow{{Thread: api.Thread{ID: "aaaa1111-0000-0000-0000-000000000000",
			Name: "some-thread", Machine: "macbook", AgentKind: "pi"}}},
		machines: []api.MachineView{
			{Machine: "mymain", Self: true, Reachable: true},
			{Machine: "macbook", Reachable: true},
		},
	}
	nm, _ := m.runCommand("new-virtual")
	m = nm.(Model)
	if m.prompting != promptNewVirtual {
		t.Fatalf("new-virtual did not open the prompt (got %v)", m.prompting)
	}
	if view := m.View(); !strings.Contains(view, `"macbook"`) {
		t.Errorf("prompt should name the target machine (the selected row's), view:\n%s", view)
	}
	// Empty submit cancels: prompt closes, no cmd returned.
	nm, cmd := m.Update(keyMsg("enter"))
	m = nm.(Model)
	if m.prompting != promptNone {
		t.Fatalf("empty submit did not close the prompt")
	}
	if cmd != nil {
		t.Fatalf("empty submit must not run anything")
	}
}

// With no selection the group is created locally — the prompt still opens and
// names the local machine.
func TestNewVirtualGroupPromptNoSelection(t *testing.T) {
	m := Model{machine: "mymain"}
	nm, _ := m.runCommand("new-virtual")
	m = nm.(Model)
	if m.prompting != promptNewVirtual {
		t.Fatalf("new-virtual with no selection should still open the prompt")
	}
	if view := m.View(); !strings.Contains(view, `"mymain"`) {
		t.Errorf("prompt should fall back to the local machine, view:\n%s", view)
	}
}

// `new-virtual` on an OFFLINE machine's row is refused by the reachability gate
// before the prompt opens (creating there would hang on the routing timeout).
func TestNewVirtualGroupOfflineRefused(t *testing.T) {
	m := Model{
		machine: "mymain",
		rows: []api.ThreadRow{{Thread: api.Thread{ID: "bbbb2222-0000-0000-0000-000000000000",
			Name: "stuck", Machine: "macstudio", AgentKind: "pi"}}},
		machines: []api.MachineView{
			{Machine: "mymain", Self: true, Reachable: true},
			{Machine: "macstudio", Reachable: false},
		},
	}
	nm, cmd := m.runCommand("new-virtual")
	m = nm.(Model)
	if m.prompting != promptNone || cmd != nil || m.ActionErr() == nil {
		t.Fatalf("new-virtual on an offline row must refuse instantly (prompting=%v cmd=%v err=%v)",
			m.prompting, cmd, m.ActionErr())
	}
}
