package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/lukastk/sesh/internal/api"
)

// TestArchiveInstantAndUndo (H54): `a` archives WITHOUT a confirm popup; a
// confirmed archive pushes the undo stack + notes the U hint; U pops LIFO and
// emits the unarchive command; U on an empty stack is a gentle note; U toward
// an OFFLINE owner is a loud error and keeps the entry (nothing half-done).
func TestArchiveInstantAndUndo(t *testing.T) {
	row := api.ThreadRow{Thread: api.Thread{ID: "tid-1", Machine: "m1", Name: "worker"}}
	m := Model{machine: "m1", rows: []api.ThreadRow{row}}

	// `a` returns an action command immediately — no confirm state opened.
	mm, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = mm.(Model)
	if m.confirming != confirmNone {
		t.Fatalf("a opened a confirm popup — archive must be instant")
	}
	if cmd == nil {
		t.Fatalf("a produced no archive command")
	}

	// A CONFIRMED archive (the actionMsg carrying its undo entry) pushes the
	// stack and shows the U hint.
	entry := &archiveUndoEntry{id: "tid-1", machine: "m1", name: "worker"}
	mm, _ = m.Update(actionMsg{id: "tid-1", undoArchive: entry})
	m = mm.(Model)
	if len(m.archiveUndo) != 1 {
		t.Fatalf("confirmed archive did not push the undo stack")
	}
	if !strings.Contains(m.note, "U to undo") {
		t.Fatalf("note %q missing the undo hint", m.note)
	}

	// U pops and emits the unarchive command.
	mm, cmd = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("U")})
	m = mm.(Model)
	if len(m.archiveUndo) != 0 {
		t.Fatalf("U did not pop the stack")
	}
	if cmd == nil {
		t.Fatalf("U produced no unarchive command")
	}
	if !strings.Contains(m.note, "un-archived") {
		t.Fatalf("note %q missing the un-archive feedback", m.note)
	}

	// Empty stack: a gentle note, no command.
	mm, cmd = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("U")})
	m = mm.(Model)
	if cmd != nil || !strings.Contains(m.note, "nothing to un-archive") {
		t.Fatalf("U on an empty stack: cmd=%v note=%q", cmd, m.note)
	}

	// THREE archives → THREE U's, restored in LIFO order (newest first).
	for _, name := range []string{"one", "two", "three"} {
		mm, _ = m.Update(actionMsg{id: "tid-" + name, undoArchive: &archiveUndoEntry{id: "tid-" + name, machine: "m1", name: name}})
		m = mm.(Model)
	}
	if len(m.archiveUndo) != 3 {
		t.Fatalf("stack depth = %d, want 3", len(m.archiveUndo))
	}
	for _, want := range []string{"three", "two", "one"} {
		mm, cmd = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("U")})
		m = mm.(Model)
		if cmd == nil || !strings.Contains(m.note, `un-archived "`+want+`"`) {
			t.Fatalf("LIFO undo: note %q, want un-archived %q (cmd=%v)", m.note, want, cmd)
		}
	}
	if len(m.archiveUndo) != 0 {
		t.Fatalf("stack not empty after three undos")
	}

	// An entry whose OWNER is offline: loud error, entry kept for later.
	m.archiveUndo = []archiveUndoEntry{{id: "tid-2", machine: "peer", name: "far"}}
	m.machines = []api.MachineView{{Machine: "peer", Reachable: false}}
	mm, cmd = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("U")})
	m = mm.(Model)
	if cmd != nil || m.ActionErr() == nil || len(m.archiveUndo) != 1 {
		t.Fatalf("offline undo: cmd=%v err=%v stack=%d — want refused loudly, entry kept", cmd, m.ActionErr(), len(m.archiveUndo))
	}
}
