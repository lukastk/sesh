package api

import (
	"fmt"
	"time"
)

// Idle stop (schema 66, _dev/IDLE_STOP.md): the owning daemon stops a headful
// agent thread that has been idle for longer than its limit — the machine's
// [idle_stop] after, or the thread's own IdleStop override.

// IdleStopNever is the override value that exempts a thread.
const IdleStopNever = "never"

// ParseIdleStopOverride validates a thread's idle_stop value: "" (use the
// machine's limit), "never", or a positive Go duration.
func ParseIdleStopOverride(v string) (never bool, limit time.Duration, err error) {
	switch v {
	case "":
		return false, 0, nil
	case IdleStopNever:
		return true, 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return false, 0, fmt.Errorf("idle_stop %q: want \"never\" or a duration like \"48h\": %w", v, err)
	}
	if d <= 0 {
		return false, 0, fmt.Errorf("idle_stop %q: the duration must be positive", v)
	}
	return false, d, nil
}

// SetIdleStopRequest is POST /v1/threads/idle-stop: set ('' = inherit) a
// thread's own idle-stop override.
type SetIdleStopRequest struct {
	ID    string `json:"id"`
	Value string `json:"value"`
}

// IdleStopPreview is GET /v1/idle-stop/preview: what the owning daemon's sweep
// would decide for each of its threads right now.
type IdleStopPreview struct {
	Schema  int    `json:"schema"`
	Machine string `json:"machine"`
	// After is the machine-wide limit ("" = none).
	After   string              `json:"after,omitempty"`
	Threads []IdleStopCandidate `json:"threads"`
}

// IdleStopCandidate is one thread's verdict.
type IdleStopCandidate struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// Stop is true when the sweep would stop the thread now.
	Stop bool `json:"stop"`
	// Reason says why it would be stopped, or why it is kept.
	Reason string `json:"reason"`
	// Limit is the limit that applies ("" = none), IdleSeconds how long the
	// thread has been idle (0 when not applicable).
	Limit       string `json:"limit,omitempty"`
	IdleSeconds int64  `json:"idle_seconds,omitempty"`
}
