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

// A fork is headless-born: POST /v1/threads {fork_from} writes the branched
// transcript and records a thread whose next turn resumes it. Nothing is spawned,
// so the spawn-shaped fields cannot be honored — and sesh#18 was that they were
// ACCEPTED instead: a routed `thread new --fork-from … --msg` answered with a
// created thread (headless_started true) and no turn ever ran, with nothing logged.
// The gate lives at the daemon seam so every client gets it, not just the CLI.
func TestForkRefusesSpawnShapedFields(t *testing.T) {
	d := newBrowserTestDaemon(t, "self", "")
	src := api.Thread{
		ID: uuid.NewString(), Machine: d.cfg.Machine, SessionName: "headless-src",
		Cwd: t.TempDir(), AgentKind: "pi", Tags: []string{}, CreatedAtUnix: time.Now().Unix(),
		AgentSessionID: uuid.NewString(),
	}
	if err := d.store.InsertThread(src); err != nil {
		t.Fatal(err)
	}
	before, err := d.store.ListThreads(true)
	if err != nil {
		t.Fatal(err)
	}

	post := func(t *testing.T, req api.NewThreadRequest) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		d.handleThreadNew(rec, httptest.NewRequest(http.MethodPost, "/v1/threads", strings.NewReader(forkReqJSON(t, req))))
		return rec
	}

	for _, tc := range []struct {
		name string
		req  api.NewThreadRequest
		want string
	}{
		{"msg", api.NewThreadRequest{ForkFrom: src.ID, Cwd: src.Cwd, Msg: "do the thing"}, "--msg is not delivered to a fork"},
		{"into-session", api.NewThreadRequest{ForkFrom: src.ID, Cwd: src.Cwd, IntoSession: "sesh_x"}, "placement"},
		{"into-window", api.NewThreadRequest{ForkFrom: src.ID, Cwd: src.Cwd, IntoWindow: "sesh_x:0"}, "placement"},
		{"into-pane", api.NewThreadRequest{ForkFrom: src.ID, Cwd: src.Cwd, IntoPane: "%7"}, "placement"},
		{"mode", api.NewThreadRequest{ForkFrom: src.ID, Cwd: src.Cwd, Mode: "yolo"}, "spawn-time mode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := post(t, tc.req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d %s, want 400 (silently dropping the field is sesh#18)", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Errorf("body = %s, want it to explain %q", rec.Body.String(), tc.want)
			}
			// The refusal must come BEFORE any side effect: no thread recorded, and
			// no branched transcript written.
			after, err := d.store.ListThreads(true)
			if err != nil {
				t.Fatal(err)
			}
			if len(after) != len(before) {
				t.Errorf("thread count %d -> %d: the refusal left a record behind", len(before), len(after))
			}
		})
	}

	// The gate is SPECIFIC: the same fork with none of those fields gets past it and
	// fails on its own merits (this source has no transcript on disk), so the
	// refusals above cannot be a blanket "forks are broken".
	rec := post(t, api.NewThreadRequest{ForkFrom: src.ID, Cwd: src.Cwd, Name: "plain"})
	if rec.Code == http.StatusBadRequest && strings.Contains(rec.Body.String(), "HEADLESS-born") {
		t.Fatalf("a plain fork hit a headless-born refusal: %s", rec.Body.String())
	}
	if rec.Code != http.StatusConflict {
		t.Logf("plain fork (no transcript on disk) = %d %s", rec.Code, rec.Body.String())
	}
}

func forkReqJSON(t *testing.T, req api.NewThreadRequest) string {
	t.Helper()
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
