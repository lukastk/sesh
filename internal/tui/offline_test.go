package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/api"
)

// machineReachable: self (and this client's own machine) is always reachable; a peer
// carries its mesh reachability; a machine absent from the mesh view is not blocked.
func TestMachineReachable(t *testing.T) {
	m := Model{
		machine: "mymain",
		machines: []api.MachineView{
			{Machine: "mymain", Self: true, Reachable: true},
			{Machine: "macbook", Reachable: true},
			{Machine: "macstudio", Reachable: false},
		},
	}
	cases := []struct {
		machine string
		want    bool
	}{
		{"", true},           // no machine id
		{"mymain", true},     // self
		{"macbook", true},    // reachable peer
		{"macstudio", false}, // offline peer
		{"unknown", true},    // not in the mesh -> don't block
	}
	for _, c := range cases {
		if got := m.machineReachable(c.machine); got != c.want {
			t.Errorf("machineReachable(%q) = %v, want %v", c.machine, got, c.want)
		}
	}
}

// requiresReachableOwner must cover EVERY normal-mode key whose action routes to the
// owning machine, and none of the read-only/navigation keys. This guards against the
// gate silently drifting out of sync with handleKey (the freeze would come back for a
// newly-added routed key). If you add an owner-routed key to handleKey, add it here too.
func TestRequiresReachableOwnerCoversActions(t *testing.T) {
	// Keys that shell out `sesh <verb> --machine <owner>` (mutate or enter a thread).
	routed := []string{"enter", "a", "d", "x", "f", "r", "t", "T", "P", "v", "p", "u", "m", "D", "h", "H", "n", "K"}
	for _, k := range routed {
		if !requiresReachableOwner(k) {
			t.Errorf("owner-routed key %q not gated by requiresReachableOwner", k)
		}
	}
	// Keys that never touch the owner — gating them would wrongly block offline browsing.
	local := []string{"up", "down", "k", "j", "ctrl+j", "ctrl+k", "ctrl+h", "ctrl+l",
		"left", "right", "/", "tab", "i", "w", "I", "o", "y", "R", "q", "esc"}
	for _, k := range local {
		if requiresReachableOwner(k) {
			t.Errorf("read-only key %q must NOT be gated (breaks offline browsing/navigation)", k)
		}
	}
}

// Pressing an owner-routed key on a thread whose machine is OFFLINE refuses instantly:
// a loud actionErr, NO command (so nothing shells out to hang on the routing timeout),
// and no confirm/prompt popup opens. This is the freeze fix — proven by the ABSENCE of
// a returned tea.Cmd (the old code returned a blocking exec cmd).
func TestOfflineActionRefusedInstantly(t *testing.T) {
	offlineRow := api.ThreadRow{Thread: api.Thread{ID: "beef1234", Name: "stuck", Machine: "macstudio"}}
	base := func() Model {
		return Model{
			machine: "mymain",
			rows:    []api.ThreadRow{offlineRow},
			machines: []api.MachineView{
				{Machine: "mymain", Self: true, Reachable: true},
				{Machine: "macstudio", Reachable: false},
			},
		}
	}
	// Every owner-routed key must be refused with no side effects.
	for _, k := range []string{"enter", "a", "d", "x", "f", "r", "t", "T", "P", "v", "p", "u", "m", "D", "h", "H", "n", "K"} {
		m := base()
		var key tea.KeyMsg
		if k == "enter" {
			key = tea.KeyMsg{Type: tea.KeyEnter}
		} else {
			key = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		mm, cmd := m.handleKey(key)
		got := mm.(Model)
		if cmd != nil {
			t.Errorf("key %q on offline thread returned a non-nil cmd (would shell out and hang)", k)
		}
		if got.actionErr == nil {
			t.Errorf("key %q on offline thread did not set a loud actionErr", k)
		}
		// No popup/prompt should have opened.
		if got.confirming != confirmNone || got.prompting != promptNone || got.tagPopup || got.ticketMode != ticketNone {
			t.Errorf("key %q on offline thread opened a popup/prompt (should refuse before that)", k)
		}
	}
}

// The SAME keys on a REACHABLE thread are NOT refused by the gate (they proceed to their
// action — a returned cmd or an opened popup — so the gate only bites offline threads).
func TestReachableActionNotBlocked(t *testing.T) {
	row := api.ThreadRow{Thread: api.Thread{ID: "cafe5678", Name: "live", Machine: "macbook", Tags: []string{"x"}}}
	base := func() Model {
		return Model{
			machine: "mymain",
			rows:    []api.ThreadRow{row},
			machines: []api.MachineView{
				{Machine: "mymain", Self: true, Reachable: true},
				{Machine: "macbook", Reachable: true},
			},
		}
	}
	// A representative spread: direct-action keys return a cmd; popup keys open a popup.
	// None should set actionErr from the offline gate.
	for _, k := range []string{"a", "r", "P", "T"} {
		m := base()
		mm, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
		got := mm.(Model)
		if got.actionErr != nil {
			t.Errorf("key %q on a reachable thread was wrongly refused: %v", k, got.actionErr)
		}
	}
}

// flattenMeshRows hides an OFFLINE peer's last-known threads by default, and reveals them
// when hideOffline is off (the `o` toggle). Self and reachable peers are unaffected.
func TestFlattenHidesOfflineMachines(t *testing.T) {
	mesh := []api.MachineView{
		{Machine: "mymain", Self: true, Reachable: true, Threads: []api.ThreadSnapshot{
			{Thread: api.Thread{ID: "s1", Name: "self-a", Machine: "mymain"}},
		}},
		{Machine: "macbook", Reachable: true, Threads: []api.ThreadSnapshot{
			{Thread: api.Thread{ID: "b1", Name: "book-a", Machine: "macbook"}},
		}},
		{Machine: "macstudio", Reachable: false, Threads: []api.ThreadSnapshot{
			{Thread: api.Thread{ID: "z1", Name: "studio-a", Machine: "macstudio"}},
			{Thread: api.Thread{ID: "z2", Name: "studio-b", Machine: "macstudio"}},
		}},
	}

	// hideOffline=true (default): macstudio's two stale threads are dropped; self + the
	// reachable peer remain.
	rows, _ := flattenMeshRows(mesh, ViewActive, nil, true /*all*/, true /*hideOffline*/, "")
	if hasRow(rows, "z1") || hasRow(rows, "z2") {
		t.Errorf("offline machine's threads should be hidden by default: %v", ids(rows))
	}
	if !hasRow(rows, "s1") || !hasRow(rows, "b1") {
		t.Errorf("self/reachable-peer threads must remain: %v", ids(rows))
	}

	// hideOffline=false (after `o`): they reappear.
	rows2, _ := flattenMeshRows(mesh, ViewActive, nil, true, false, "")
	if !hasRow(rows2, "z1") || !hasRow(rows2, "z2") {
		t.Errorf("offline machine's threads should show when hideOffline is off: %v", ids(rows2))
	}

	// Self is NEVER hidden even if (impossibly) marked unreachable — hideOffline only
	// gates peers.
	selfUnreach := []api.MachineView{
		{Machine: "mymain", Self: true, Reachable: false, Threads: []api.ThreadSnapshot{
			{Thread: api.Thread{ID: "s1", Name: "self-a", Machine: "mymain"}},
		}},
	}
	rows3, _ := flattenMeshRows(selfUnreach, ViewActive, nil, true, true, "")
	if !hasRow(rows3, "s1") {
		t.Errorf("self must never be hidden by the offline filter")
	}
}

// Pressing `o` flips hideOffline (the per-session toggle).
func TestOfflineToggleKey(t *testing.T) {
	m := Model{hideOffline: true}
	mm, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("o")})
	if mm.(Model).hideOffline {
		t.Errorf("`o` should toggle hideOffline true->false")
	}
	m2 := Model{hideOffline: false}
	mm2, _ := m2.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("o")})
	if !mm2.(Model).hideOffline {
		t.Errorf("`o` should toggle hideOffline false->true")
	}
}

func ids(rows []api.ThreadRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}
