package tui

import (
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/api"
)

// row builds a ThreadRow with a parent link and a busy state for the descendant tests.
func descRow(id, parent string, busy api.Busy) api.ThreadRow {
	return api.ThreadRow{
		Thread: api.Thread{ID: id, Name: id, Parent: parent},
		Head:   api.Headful,
		Busy:   busy,
	}
}

// A running grandchild marks BOTH the parent and the grandparent; an all-idle
// subtree marks nobody; a running node never marks itself.
func TestDescendantsRunning(t *testing.T) {
	rows := []api.ThreadRow{
		descRow("gp", "", api.BusyIdle),         // grandparent — idle
		descRow("p", "gp", api.BusyIdle),        // parent — idle
		descRow("kid", "p", api.BusyBusy),       // grandchild — RUNNING
		descRow("lonely", "", api.BusyIdle),     // unrelated root, idle
		descRow("kid2", "lonely", api.BusyIdle), // idle child of lonely
	}
	got := descendantsRunning(rows)
	for _, want := range []string{"gp", "p"} {
		if !got[want] {
			t.Errorf("%q should be marked (a descendant is running): %v", want, got)
		}
	}
	for _, no := range []string{"kid", "lonely", "kid2"} {
		if got[no] {
			t.Errorf("%q must NOT be marked (it is the running node itself or has no running descendant)", no)
		}
	}
}

// A cycle on the wire (the daemon forbids it, but never trust the wire) must not
// hang the ancestor walk.
func TestDescendantsRunningCycleSafe(t *testing.T) {
	rows := []api.ThreadRow{
		descRow("a", "b", api.BusyBusy),
		descRow("b", "a", api.BusyIdle),
	}
	_ = descendantsRunning(rows) // must terminate
}

// The rendered gutter shows the ↓ descendant glyph on the parent of a running
// child and NOT on a thread whose subtree is quiet.
func TestViewRendersDescendantGlyph(t *testing.T) {
	m := Model{rows: []api.ThreadRow{
		descRow("boss", "", api.BusyIdle),
		descRow("worker", "boss", api.BusyBusy),
		descRow("solo", "", api.BusyIdle),
	}}
	m.columns = append([]string(nil), DefaultColumns...)
	m.defaultExpand = true
	view := m.View()
	bossLine := rowLineLocal(view, "boss")
	if !strings.Contains(bossLine, DescendantGlyph(true)) {
		t.Errorf("boss row missing the descendant-running glyph %q: %q", DescendantGlyph(true), bossLine)
	}
	soloLine := rowLineLocal(view, "solo")
	if strings.Contains(soloLine, DescendantGlyph(true)) {
		t.Errorf("solo row wrongly shows the descendant-running glyph: %q", soloLine)
	}
	if !strings.Contains(view, "HBD") {
		t.Errorf("gutter header should read HBD: %q", view)
	}
}

func rowLineLocal(view, name string) string {
	for _, l := range strings.Split(view, "\n") {
		if strings.Contains(l, name) {
			return l
		}
	}
	return ""
}
