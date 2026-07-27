package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/lukastk/sesh/internal/api"
)

// TestSidebarEnterSupersedesInFlightFollow is the regression guard for the
// "I click a thread in the sidebar and it doesn't transition" bug (Lukas,
// 2026-07-27).
//
// The sidebar drives the cockpit's thread pane from TWO places: the ambient
// selection FOLLOW (a preview, fired on every selection move) and a user
// ENTER (click/Enter — an explicit command). Both shell out to `sesh tmux
// nav`, and the cockpit shows whichever nav lands LAST. Nothing sequenced
// them, so clicking while a follow was still running (routine after any
// trackpad scroll, and a cross-machine follow takes hundreds of ms) let the
// STALE PREVIEW land after the click and win — the click "didn't take" and
// the cockpit stayed on the thread the user had been on.
//
// The observable external effect is the SEQUENCE OF NAV COMMANDS issued, so
// that is what this asserts: a real fake `sesh` on disk records every --to
// target it is asked for, the follow is slow and the enter is fast (the exact
// losing interleaving), and the LAST nav the cockpit is told to make must be
// the thread the user clicked.
func TestSidebarEnterSupersedesInFlightFollow(t *testing.T) {
	// drive builds a sidebar whose follow nav takes followDelay, arrows onto
	// the follow target, clicks the OTHER row mid-flight, then runs the
	// resulting commands the way bubbletea's loop does — really concurrently,
	// feeding each result back in COMPLETION order — and returns the sequence
	// of nav targets the cockpit was actually asked for.
	drive := func(t *testing.T, followDelay string) []string {
		t.Helper()
		t.Setenv("TMUX", "") // intent writes must never reach the developer's live server

		dir := t.TempDir()
		logPath := filepath.Join(dir, "navs.log")
		bin := filepath.Join(dir, "fake-sesh")
		script := "#!/bin/sh\n" +
			"to=''; prev=''\n" +
			"for a in \"$@\"; do [ \"$prev\" = --to ] && to=$a; prev=$a; done\n" +
			"case \"$to\" in *slowpoke*) sleep " + followDelay + " ;; esac\n" +
			"echo \"$to\" >> " + logPath + "\n"
		if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}

		row := func(id string) api.ThreadRow {
			return api.ThreadRow{Thread: api.Thread{ID: id, Name: id, Machine: "peer", SessionName: id}, Head: api.Headful}
		}
		m := Model{
			sidebar: true, machine: "mymain", tmux: "/tmp/fake-tmux,1,0",
			followResolver: func() string { return "mymain" },
			machines: []api.MachineView{
				{Machine: "mymain", Self: true, Reachable: true},
				{Machine: "peer", Reachable: true},
			},
			rows:       []api.ThreadRow{row("slowpoke"), row("clicked")},
			binaryPath: bin,
			columns:    []string{ColName}, width: 38, height: 20, scrollDivV: 1, scrollDivH: 1,
		}

		// The user arrows onto "slowpoke" -> an ambient follow nav starts.
		m.cursor = 1
		m, followCmd := step(t, m, tea.KeyMsg{Type: tea.KeyUp})
		if followCmd == nil || !m.followInFlight {
			t.Fatalf("setup: moving onto a live row must fire a follow (cmd=%v inflight=%v)", followCmd, m.followInFlight)
		}
		// MID-FLIGHT the user clicks the other row.
		m, enterCmd := step(t, m, click(20, firstRowY+1))
		if m.Cursor() != 1 {
			t.Fatalf("setup: the click must select the clicked row, cursor=%d", m.Cursor())
		}

		// A faithful mini event loop: every command runs in its own goroutine
		// and is delivered the instant it finishes, so messages reach Update in
		// true COMPLETION order (batching them would hide the very interleaving
		// under test).
		msgs := make(chan tea.Msg, 32)
		outstanding := 0
		spawn := func(c tea.Cmd) {
			if c == nil {
				return
			}
			outstanding++
			go func() { msgs <- c() }()
		}
		spawn(followCmd)
		spawn(enterCmd)
		for outstanding > 0 {
			select {
			case msg := <-msgs:
				outstanding--
				var cmd tea.Cmd
				m, cmd = step(t, m, msg)
				spawn(cmd)
			case <-time.After(10 * time.Second):
				t.Fatal("the model never settled")
			}
		}
		b, err := os.ReadFile(logPath)
		if err != nil {
			return nil
		}
		return strings.Fields(strings.TrimSpace(string(b)))
	}

	// A normal follow (well inside enterQueueGrace): the click waits for it,
	// so the cockpit is told exactly twice — the preview it was already
	// showing, then the clicked thread. Before the fix this was
	// [clicked slowpoke clicked]: the stale preview landed ON TOP of the
	// click (the visible "it didn't take"), and only a THIRD, re-armed nav
	// eventually corrected it (the visible "it transitioned a moment later").
	t.Run("stale preview cannot overtake the click", func(t *testing.T) {
		got := drive(t, "0.15")
		want := []string{"peer:slowpoke", "peer:clicked"}
		if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("nav sequence = %v, want %v — the user's click must be the LAST nav, with no preview landing after it and no corrective re-nav", got, want)
		}
	})

	// ...but a STALLED follow must never swallow the click: past the grace the
	// enter goes out anyway (the old re-arm still corrects any late preview).
	t.Run("a stalled follow never blocks the click", func(t *testing.T) {
		got := drive(t, "2")
		if len(got) == 0 || got[0] != "peer:clicked" {
			t.Fatalf("nav sequence = %v — a click must go through within the grace even if the follow is stalled", got)
		}
	})
}
