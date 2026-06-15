package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/lukastk/sesh/internal/api"
)

// TestSuccessfulActionSchedulesDelayedReconcile locks in the fix for "the TUI
// doesn't refresh immediately after reparent": a successful action reconciles
// NOW and once more shortly after, because the immediate fetch can outrun the
// maintainer's snapshot republish (a no-optimistic-patch structural change like
// reparent-to-root would otherwise wait for the next 3s poll).
//
// Invoking a tea.Batch command yields a tea.BatchMsg (the child cmds, NOT run),
// so we can assert the schedule without executing the fetch (which needs a live
// client).
func TestSuccessfulActionSchedulesDelayedReconcile(t *testing.T) {
	m := Model{rows: []api.ThreadRow{{Thread: api.Thread{ID: "t1", Name: "x"}}}}

	// A reparent-style success: err == nil, no optimistic patch.
	_, cmd := m.Update(actionMsg{id: "t1", preselect: "t1"})
	if cmd == nil {
		t.Fatal("successful action returned no command (want immediate + delayed reconcile)")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("want a tea.BatchMsg (fetch + delayed reconcile), got %T", cmd())
	}
	if len(batch) != 2 {
		t.Fatalf("want 2 batched commands (fetch + reconcileAfter), got %d", len(batch))
	}

	// The reconcileMsg the delayed tick produces triggers another fetch (and is a
	// one-shot — it must NOT re-arm the poll, which would multiply the poll rate).
	_, cmd = m.Update(reconcileMsg{})
	if cmd == nil {
		t.Fatal("reconcileMsg returned no fetch command")
	}
}
