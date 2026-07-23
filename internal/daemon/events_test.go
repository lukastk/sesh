package daemon

import (
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
)

// TestEventEnv pins the hook-env contract: every SESH_* variable a hook
// command can rely on, including SESH_ATTACHMENT (added so a notify hook can
// skip attached threads — a user typing into a pane or navigating onto it
// latches the content-diff busy probe, and the settle back to idle is
// indistinguishable from a finished turn without the attachment axis).
func TestEventEnv(t *testing.T) {
	ev := Event{
		Type:                 "busy_changed",
		From:                 "busy",
		To:                   "idle",
		AttachedActivityAgo:  42,
		AttachmentChangedAgo: 7,
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
			Head:           api.Headful,
			Busy:           api.BusyIdle,
			Attachment:     api.Attached,
			StateAuthority: api.AuthorityReported,
			Blocked:        true,
			BlockedReason:  "needs permission to use Bash",
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
		"SESH_HEAD":                   string(api.Headful),
		"SESH_BUSY":                   string(api.BusyIdle),
		"SESH_ATTACHMENT":             "attached",
		"SESH_ATTACHED_ACTIVITY_AGO":  "42",
		"SESH_ATTACHMENT_CHANGED_AGO": "7",
		"SESH_NOTIFY":                 "1",
		"SESH_BLOCKED":                "1",
		"SESH_BLOCKED_REASON":         "needs permission to use Bash",
		"SESH_STATE_AUTHORITY":        "reported",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("Env()[%q] = %q, want %q", k, env[k], v)
		}
	}
	if len(env) != len(want) {
		t.Errorf("Env() has %d entries, want %d — a new variable must be added to this contract test", len(env), len(want))
	}

	// The other attachment value and the gate's off state; an unblocked thread
	// with no authority/reason must OMIT the presence-gated vars (a hook tests
	// presence — absence must never read as a value).
	ev.Snap.Attachment = api.Detached
	ev.Snap.Notify = false
	ev.Snap.Blocked = false
	ev.Snap.BlockedReason = ""
	ev.Snap.StateAuthority = ""
	env = ev.Env()
	if env["SESH_ATTACHMENT"] != "detached" {
		t.Errorf("SESH_ATTACHMENT = %q, want detached", env["SESH_ATTACHMENT"])
	}
	if env["SESH_NOTIFY"] != "0" {
		t.Errorf("SESH_NOTIFY = %q, want 0", env["SESH_NOTIFY"])
	}
	if env["SESH_BLOCKED"] != "0" {
		t.Errorf("SESH_BLOCKED = %q, want 0", env["SESH_BLOCKED"])
	}
	if _, present := env["SESH_BLOCKED_REASON"]; present {
		t.Error("SESH_BLOCKED_REASON must be absent when there is no reason")
	}
	if _, present := env["SESH_STATE_AUTHORITY"]; present {
		t.Error("SESH_STATE_AUTHORITY must be absent when the authority is unknown")
	}

	// Unknown ages (-1) must be OMITTED, not exported as a number — a hook's
	// numeric comparison on an empty var fails open (notifies); on "0" or "-1"
	// it would wrongly suppress.
	ev.AttachedActivityAgo, ev.AttachmentChangedAgo = -1, -1
	env = ev.Env()
	for _, k := range []string{"SESH_ATTACHED_ACTIVITY_AGO", "SESH_ATTACHMENT_CHANGED_AGO"} {
		if v, present := env[k]; present {
			t.Errorf("%s = %q, want ABSENT when unknown", k, v)
		}
	}
}

// TestEventerDecorate covers the observer-side age computation: activity age
// from the owner-stamped snapshot unix (clamped at 0 for small clock skew),
// flip age from the eventer's attachFlip record, -1 (unknown) when absent.
func TestEventerDecorate(t *testing.T) {
	e := &eventer{attachFlip: map[string]time.Time{}}

	// Nothing known: both unknown.
	ev := e.decorate(Event{Snap: api.ThreadSnapshot{Thread: api.Thread{ID: "a"}}})
	if ev.AttachedActivityAgo != -1 || ev.AttachmentChangedAgo != -1 {
		t.Fatalf("bare event: got ago %d/%d, want -1/-1", ev.AttachedActivityAgo, ev.AttachmentChangedAgo)
	}

	// Activity stamped 100s ago; flip seen 5s ago.
	e.attachFlip["a"] = time.Now().Add(-5 * time.Second)
	ev = e.decorate(Event{Snap: api.ThreadSnapshot{
		Thread:               api.Thread{ID: "a"},
		AttachedActivityUnix: time.Now().Unix() - 100,
	}})
	if ev.AttachedActivityAgo < 99 || ev.AttachedActivityAgo > 102 {
		t.Errorf("activity ago = %d, want ~100", ev.AttachedActivityAgo)
	}
	if ev.AttachmentChangedAgo < 4 || ev.AttachmentChangedAgo > 7 {
		t.Errorf("flip ago = %d, want ~5", ev.AttachmentChangedAgo)
	}

	// An owner clock slightly ahead of ours must clamp to 0, not go negative
	// (negative would be omitted from the env = read as unknown).
	ev = e.decorate(Event{Snap: api.ThreadSnapshot{
		Thread:               api.Thread{ID: "b"},
		AttachedActivityUnix: time.Now().Unix() + 3,
	}})
	if ev.AttachedActivityAgo != 0 {
		t.Errorf("future activity ago = %d, want clamped 0", ev.AttachedActivityAgo)
	}
}
