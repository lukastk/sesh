package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/lukastk/sesh/internal/api"
)

func postArchive(t *testing.T, d *Daemon, req api.ArchiveThreadRequest) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	d.handleThreadArchive(rec, httptest.NewRequest(http.MethodPost, "/v1/threads/archive", strings.NewReader(string(b))))
	return rec
}

func insertArchiveGateThread(t *testing.T, d *Daemon, name string) api.Thread {
	t.Helper()
	th := api.Thread{
		ID: uuid.NewString(), Machine: d.cfg.Machine, SessionName: "headless-" + name,
		Cwd: t.TempDir(), AgentKind: "pi", Name: name, Tags: []string{},
		CreatedAtUnix: time.Now().Unix(), AgentSessionID: uuid.NewString(),
	}
	if err := d.store.InsertThread(th); err != nil {
		t.Fatal(err)
	}
	return th
}

func archivedNow(t *testing.T, d *Daemon, id string) bool {
	t.Helper()
	th, err := d.store.GetThread(id)
	if err != nil {
		t.Fatal(err)
	}
	return th.Archived
}

// Archiving parks a FINISHED thread (schema 57). A flag is output nobody has read
// and a runtime is a turn that may still be producing some, so either one refuses —
// and the refusal has to name what is unmet, because archiving hides the thread from
// the list where it would otherwise have been looked at.
func TestArchiveGateRefusesFlaggedAndRunning(t *testing.T) {
	d := newBrowserTestDaemon(t, "self", "")

	// Finished: no flag, no runtime. Archives, and un-archives again.
	done := insertArchiveGateThread(t, d, "finished")
	if rec := postArchive(t, d, api.ArchiveThreadRequest{ID: done.ID, Archived: true}); rec.Code != http.StatusOK {
		t.Fatalf("archiving a finished thread = %d %s", rec.Code, rec.Body.String())
	}
	if !archivedNow(t, d, done.ID) {
		t.Fatal("finished thread not archived")
	}
	if rec := postArchive(t, d, api.ArchiveThreadRequest{ID: done.ID, Archived: false}); rec.Code != http.StatusOK {
		t.Fatalf("un-archive = %d %s (un-archiving is never gated)", rec.Code, rec.Body.String())
	}

	// Flagged: refused, naming the flag, its reason and the way to clear it.
	flagged := insertArchiveGateThread(t, d, "flagged")
	if err := d.store.SetThreadFlagAction(flagged.ID, "on", "turn ended"); err != nil {
		t.Fatal(err)
	}
	rec := postArchive(t, d, api.ArchiveThreadRequest{ID: flagged.ID, Archived: true})
	if rec.Code != http.StatusConflict {
		t.Fatalf("archiving a flagged thread = %d %s, want 409", rec.Code, rec.Body.String())
	}
	for _, want := range []string{"flagged", "turn ended", "thread flag --off", "--force"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("refusal %s is missing %q", rec.Body.String(), want)
		}
	}
	if archivedNow(t, d, flagged.ID) {
		t.Error("the refused thread was archived anyway")
	}

	// A headless turn IN FLIGHT is a live runtime, by the same reading `thread
	// status` uses — refused while it holds the slot, allowed once it is released.
	running := insertArchiveGateThread(t, d, "running")
	d.hlMu.Lock()
	d.hlInFlight[running.ID] = true
	d.hlMu.Unlock()
	rec = postArchive(t, d, api.ArchiveThreadRequest{ID: running.ID, Archived: true})
	if rec.Code != http.StatusConflict {
		t.Fatalf("archiving a thread with a turn in flight = %d %s, want 409", rec.Code, rec.Body.String())
	}
	for _, want := range []string{"headless turn is in flight", "thread stop"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("refusal %s is missing %q", rec.Body.String(), want)
		}
	}

	// Both unmet: ONE refusal that names both, so clearing them is a single pass.
	if err := d.store.SetThreadFlagAction(running.ID, "on", ""); err != nil {
		t.Fatal(err)
	}
	rec = postArchive(t, d, api.ArchiveThreadRequest{ID: running.ID, Archived: true})
	body := rec.Body.String()
	if !strings.Contains(body, "flagged") || !strings.Contains(body, "in flight") {
		t.Errorf("refusal names only one unmet condition: %s", body)
	}

	// Force parks it as it is — what a schedule's --on-turn-end policy and
	// `delegate`'s worker rely on.
	if rec := postArchive(t, d, api.ArchiveThreadRequest{ID: running.ID, Archived: true, Force: true}); rec.Code != http.StatusOK {
		t.Fatalf("forced archive = %d %s", rec.Code, rec.Body.String())
	}
	if !archivedNow(t, d, running.ID) {
		t.Error("forced archive did not park the thread")
	}
	d.hlMu.Lock()
	delete(d.hlInFlight, running.ID)
	d.hlMu.Unlock()
}
