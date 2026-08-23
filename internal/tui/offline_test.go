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

// requiresReachableOwner must cover EVERY command whose action routes to the owning
// machine, and none of the read-only/navigation ones. Keyed by COMMAND ID since keys
// are configurable — a gate keyed by key string would stop covering a rebound action.
//
// The classification is EXHAUSTIVE over the registry: a newly added command that is in
// neither list fails this test, so the gate cannot silently drift out of sync with the
// dispatch (the freeze would come back for the new command, from the palette too).
func TestRequiresReachableOwnerCoversActions(t *testing.T) {
	// Commands that shell out `sesh <verb> --machine <owner>` (mutate or enter a thread).
	routed := []string{"enter", "archive", "delete", "stop", "flag", "fork", "flag-gate",
		"rename", "tag-add", "tag-remove", "set-parent", "set-parent-uuid", "new-virtual",
		"pin", "unpin", "move-mode", "new-divider", "hold", "hold-until", "notify", "tickets"}
	for _, id := range routed {
		if _, ok := commandByID(id); !ok {
			t.Fatalf("routed command %q is not in the registry", id)
		}
		if !requiresReachableOwner(id) {
			t.Errorf("owner-routed command %q not gated by requiresReachableOwner", id)
		}
	}
	// Commands that never touch the owner — gating them would wrongly block offline browsing.
	local := []string{"cursor-up", "cursor-down", "scroll-up", "scroll-down", "fold", "unfold",
		"pan-left", "pan-right", "filter", "view-picker", "palette", "toggle-id",
		// goto-uuid only moves the cursor (and the view) — it never touches an owner,
		// and gating it would refuse a jump AWAY from an offline machine's row.
		"goto-uuid",
		"toggle-width-cap", "toggle-offline", "uuid", "details", "refresh", "help",
		"dismiss", "quit",
		// undo-archive routes, but its target comes from the undo STACK, not the
		// selection — the selection-keyed gate would check the WRONG machine. It
		// checks its own entry's machine and refuses there instead (H54).
		"undo-archive"}
	for _, id := range local {
		if _, ok := commandByID(id); !ok {
			t.Fatalf("read-only command %q is not in the registry", id)
		}
		if requiresReachableOwner(id) {
			t.Errorf("read-only command %q must NOT be gated (breaks offline browsing/navigation)", id)
		}
	}
	classified := map[string]bool{}
	for _, id := range append(append([]string(nil), routed...), local...) {
		classified[id] = true
	}
	for _, c := range Commands() {
		if !classified[c.ID] {
			t.Errorf("command %q is in neither list — classify it (routed => add to requiresReachableOwner)", c.ID)
		}
	}
}

// ownerRoutedCommands is the routed set, for the offline-gate tests below.
var ownerRoutedCommands = []string{"enter", "archive", "delete", "stop", "flag", "fork",
	"flag-gate", "rename", "tag-add", "tag-remove", "set-parent", "set-parent-uuid",
	"new-virtual", "pin", "unpin", "move-mode", "new-divider", "hold", "hold-until",
	"notify", "tickets"}

// Running an owner-routed command on a thread whose machine is OFFLINE refuses instantly:
// a loud actionErr, NO command (so nothing shells out to hang on the routing timeout),
// and no confirm/prompt/picker popup opens. This is the freeze fix — proven by the ABSENCE
// of a returned tea.Cmd (the old code returned a blocking exec cmd). Driven through
// runCommand, so it covers palette invocation as well as key presses.
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
	for _, id := range ownerRoutedCommands {
		m := base()
		mm, cmd := m.runCommand(id)
		got := mm.(Model)
		if cmd != nil {
			t.Errorf("command %q on offline thread returned a non-nil cmd (would shell out and hang)", id)
		}
		if got.actionErr == nil {
			t.Errorf("command %q on offline thread did not set a loud actionErr", id)
		}
		// No popup/prompt should have opened.
		if got.confirming != confirmNone || got.prompting != promptNone || got.tagPopup ||
			got.ticketMode != ticketNone || got.parentPick {
			t.Errorf("command %q on offline thread opened a popup/prompt (should refuse before that)", id)
		}
	}
}

// The gate also bites when the command is reached by its KEY (the other entry point):
// a bound owner-routed key on an offline row refuses with no cmd and no popup.
func TestOfflineKeyRefusedInstantly(t *testing.T) {
	offlineRow := api.ThreadRow{Thread: api.Thread{ID: "beef1234", Name: "stuck", Machine: "macstudio"}}
	m := Model{
		machine: "mymain",
		rows:    []api.ThreadRow{offlineRow},
		machines: []api.MachineView{
			{Machine: "mymain", Self: true, Reachable: true},
			{Machine: "macstudio", Reachable: false},
		},
	}
	for _, k := range []string{"a", "f", "x", "r", "h", "n", "u", "m", "K"} {
		mm, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
		got := mm.(Model)
		if cmd != nil || got.actionErr == nil {
			t.Errorf("key %q on offline thread must refuse instantly (cmd=%v err=%v)", k, cmd != nil, got.actionErr)
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

// The toggle-offline command flips hideOffline (the per-session toggle). It carries no
// default key since 2026-08 — the palette is how you reach it.
func TestOfflineToggleCommand(t *testing.T) {
	m := Model{hideOffline: true}
	mm, _ := m.runCommand("toggle-offline")
	if mm.(Model).hideOffline {
		t.Errorf("toggle-offline should flip hideOffline true->false")
	}
	m2 := Model{hideOffline: false}
	mm2, _ := m2.runCommand("toggle-offline")
	if !mm2.(Model).hideOffline {
		t.Errorf("toggle-offline should flip hideOffline false->true")
	}
}

func ids(rows []api.ThreadRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}
