package daemon

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/store"
)

// TestWaitConditionMet pins the `until` vocabulary, including the settled =
// idle-or-blocked rule (a blocked thread reads busy on the execution axis, so
// a bare idle wait would sit out an approval prompt forever).
func TestWaitConditionMet(t *testing.T) {
	busy := api.ThreadSnapshot{Busy: api.BusyBusy}
	idle := api.ThreadSnapshot{Busy: api.BusyIdle}
	blocked := api.ThreadSnapshot{Busy: api.BusyBusy, Blocked: true}

	cases := []struct {
		until string
		snap  api.ThreadSnapshot
		want  bool
	}{
		{"busy", busy, true}, {"busy", idle, false},
		{"idle", idle, true}, {"idle", busy, false}, {"idle", blocked, false},
		{"blocked", blocked, true}, {"blocked", busy, false},
		{"settled", idle, true}, {"settled", blocked, true}, {"settled", busy, false},
	}
	for _, tc := range cases {
		got, err := waitConditionMet(tc.until, tc.snap)
		if err != nil || got != tc.want {
			t.Errorf("waitConditionMet(%s, %+v) = %v/%v, want %v", tc.until, tc.snap.Busy, got, err, tc.want)
		}
	}
	if _, err := waitConditionMet("running", api.ThreadSnapshot{}); err == nil {
		t.Error("unknown until must be a loud error")
	}
}

// TestHandleThreadWait exercises the bounded server-owned wait: loud refusals,
// instant success when the condition already holds, and a reached=false 200
// (NOT an error — the client loop decides) when the bound expires.
func TestHandleThreadWait(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "sesh.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	d := &Daemon{store: st}
	d.maint = newMaintainer(d)
	if err := st.InsertThread(api.Thread{ID: "tid-w", Machine: "test", SessionName: "w", AgentKind: "pi"}); err != nil {
		t.Fatalf("InsertThread: %v", err)
	}
	// Seed a published snapshot the handler's poll reads.
	ls := &liveState{}
	d.maint.st["tid-w"] = ls
	d.maint.publish(ls, api.ThreadSnapshot{Thread: api.Thread{ID: "tid-w"}, Head: api.Headful, Busy: api.BusyIdle})

	get := func(q string) (int, api.ThreadWaitResponse) {
		t.Helper()
		rec := httptest.NewRecorder()
		d.handleThreadWait(rec, httptest.NewRequest("GET", "/v1/threads/wait?"+q, nil))
		var out api.ThreadWaitResponse
		if rec.Code == 200 {
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatalf("bad body: %v", err)
			}
		}
		return rec.Code, out
	}

	if code, _ := get("until=idle&timeout_ms=100"); code != 400 {
		t.Fatalf("missing id: code %d, want 400", code)
	}
	if code, _ := get("id=tid-w&until=running&timeout_ms=100"); code != 400 {
		t.Fatalf("bad until: code %d, want 400", code)
	}
	if code, _ := get("id=nope&until=idle&timeout_ms=100"); code != 404 {
		t.Fatalf("unknown thread: code %d, want 404", code)
	}

	// Condition already met: returns reached immediately (well under the bound).
	start := time.Now()
	code, resp := get("id=tid-w&until=idle&timeout_ms=5000")
	if code != 200 || !resp.Reached || resp.Busy != api.BusyIdle {
		t.Fatalf("met wait: code=%d reached=%v busy=%s", code, resp.Reached, resp.Busy)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("met wait took %s — should return on the first poll", time.Since(start))
	}

	// Condition not met within the bound: 200 with reached=false + the state.
	code, resp = get("id=tid-w&until=busy&timeout_ms=300")
	if code != 200 || resp.Reached || resp.Busy != api.BusyIdle {
		t.Fatalf("unmet wait: code=%d reached=%v busy=%s, want 200/false/idle", code, resp.Reached, resp.Busy)
	}
}
