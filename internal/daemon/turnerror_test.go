package daemon

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lukastk/sesh/internal/api"
)

func TestTurnErrorTextScrubsThreadEnvValues(t *testing.T) {
	got := turnErrorText(errors.New("exited (2): bad key hunter2 for hunter2"), map[string]string{"KEY": "hunter2", "EMPTY": ""})
	if strings.Contains(got, "hunter2") || !strings.Contains(got, "exited (2)") {
		t.Errorf("got %q", got)
	}
}

func TestTurnErrorTextIsCappedOnACharacter(t *testing.T) {
	got := turnErrorText(errors.New(strings.Repeat("é", maxTurnErrorBytes)), nil)
	if len(got) > maxTurnErrorBytes+len("…") || !utf8.ValidString(got) || !strings.HasSuffix(got, "…") {
		t.Errorf("%d bytes, valid=%t", len(got), utf8.ValidString(got))
	}
}

// A maintained snapshot is up to a tick old. The overlay is what makes an await
// straight after a send wait for the turn, and see how it ended.
func TestOverlayTurnsIsCurrentWhereTheSnapshotIsStale(t *testing.T) {
	d := &Daemon{hlInFlight: map[string]bool{"running": true}, hlTurnError: map[string]string{"failed": "it broke", "retried": ""}}
	stale := func(id, text string) api.ThreadSnapshot {
		return api.ThreadSnapshot{Thread: api.Thread{ID: id, TurnError: text}, Busy: api.BusyIdle}
	}
	threads := []api.ThreadSnapshot{stale("running", ""), stale("failed", ""), stale("retried", "old"), stale("other", "kept")}
	d.overlayTurns(threads)
	if threads[0].Busy != api.BusyBusy {
		t.Errorf("a running turn reads %s", threads[0].Busy)
	}
	if threads[1].TurnError != "it broke" {
		t.Errorf("a failed turn reads %q", threads[1].TurnError)
	}
	if threads[2].TurnError != "" {
		t.Errorf("a new turn still shows %q", threads[2].TurnError)
	}
	if threads[3].TurnError != "kept" || threads[3].Busy != api.BusyIdle {
		t.Errorf("a thread this process has not run changed: %+v", threads[3])
	}
}
