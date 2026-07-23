package tui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/lukastk/sesh/internal/api"
	"github.com/muesli/termenv"
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

// The running-state glyphs are TINTED (green) on non-selected rows — ▶ for the
// thread's own turn, ↓ for a descendant's — and left untinted inside the selected
// row (reverse video is the dominant cue, same rule as column colours). Rendered
// under a forced ANSI colour profile so the assertion is deterministic off-tty.
func TestViewTintsRunningGlyphs(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	defer lipgloss.SetColorProfile(prev)

	m := Model{rows: []api.ThreadRow{
		descRow("solo", "", api.BusyIdle),
		descRow("boss", "", api.BusyIdle),
		descRow("worker", "boss", api.BusyBusy),
	}}
	m.columns = append([]string(nil), DefaultColumns...)
	m.defaultExpand = true

	// tinted matches an SGR sequence immediately preceding the glyph — the shape
	// styleRunning.Render produces; a plain (or merely reverse-video-wrapped) glyph
	// does not match.
	tinted := func(glyph string) *regexp.Regexp {
		return regexp.MustCompile(`\x1b\[[0-9;]*m` + glyph)
	}

	view := m.View() // cursor 0 = solo; boss and worker are non-selected
	if line := rowLineLocal(view, "worker"); !tinted("▶").MatchString(line) {
		t.Errorf("busy row's ▶ should be tinted: %q", line)
	}
	if line := rowLineLocal(view, "boss"); !tinted("↓").MatchString(line) {
		t.Errorf("descendant-running row's ↓ should be tinted: %q", line)
	}

	m.cursor = 2 // select worker: its ▶ must lose the tint (reverse video wins)
	view = m.View()
	if line := rowLineLocal(view, "worker"); tinted("▶").MatchString(line) {
		t.Errorf("selected row's ▶ must NOT be tinted: %q", line)
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
