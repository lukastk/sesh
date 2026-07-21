package daemon

import (
	"testing"

	"github.com/lukastk/sesh/internal/api"
)

// TestEventEnv pins the hook-env contract: every SESH_* variable a hook
// command can rely on, including SESH_ATTACHMENT (added so a notify hook can
// skip attached threads — a user typing into a pane or navigating onto it
// latches the content-diff busy probe, and the settle back to idle is
// indistinguishable from a finished turn without the attachment axis).
func TestEventEnv(t *testing.T) {
	ev := Event{
		Type: "busy_changed",
		From: "busy",
		To:   "idle",
		Snap: api.ThreadSnapshot{
			Thread: api.Thread{
				ID:          "tid-1",
				Name:        "worker",
				AgentKind:   "pi",
				Machine:     "mbox",
				Cwd:         "/w",
				SessionName: "sesh_worker",
				Tags:        []string{"a", "b"},
				Notify:      true,
			},
			Head:       api.Headful,
			Busy:       api.BusyIdle,
			Attachment: api.Attached,
		},
	}
	env := ev.Env()
	want := map[string]string{
		"SESH_EVENT":       "busy_changed",
		"SESH_EVENT_FROM":  "busy",
		"SESH_EVENT_TO":    "idle",
		"SESH_THREAD_ID":   "tid-1",
		"SESH_THREAD_NAME": "worker",
		"SESH_AGENT":       "pi",
		"SESH_MACHINE":     "mbox",
		"SESH_CWD":         "/w",
		"SESH_SESSION":     "sesh_worker",
		"SESH_TAGS":        "a,b",
		"SESH_HEAD":        string(api.Headful),
		"SESH_BUSY":        string(api.BusyIdle),
		"SESH_ATTACHMENT":  "attached",
		"SESH_NOTIFY":      "1",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("Env()[%q] = %q, want %q", k, env[k], v)
		}
	}
	if len(env) != len(want) {
		t.Errorf("Env() has %d entries, want %d — a new variable must be added to this contract test", len(env), len(want))
	}

	// The other attachment value and the gate's off state.
	ev.Snap.Attachment = api.Detached
	ev.Snap.Notify = false
	env = ev.Env()
	if env["SESH_ATTACHMENT"] != "detached" {
		t.Errorf("SESH_ATTACHMENT = %q, want detached", env["SESH_ATTACHMENT"])
	}
	if env["SESH_NOTIFY"] != "0" {
		t.Errorf("SESH_NOTIFY = %q, want 0", env["SESH_NOTIFY"])
	}
}
