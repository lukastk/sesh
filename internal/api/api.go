// Package api defines the versioned, machine-readable client-facing contract
// between the sesh CLI/TUI (and future Obsidian plugin) and the daemon. Output
// is a contract: every response carries the schema version so clients can
// detect drift.
package api

// SchemaVersion is the version of the client-facing JSON schema. Bump on any
// breaking change to a response shape.
// 2: the unified thread model — Thread.headless dropped (headless/headful is
// inferred runtime, not a stored mode); Activity value "dead" renamed "idle".
// 3: the two-axes state model — `activity` REPLACED by the orthogonal
// `head` ("headful"|"headless") and `busy` ("busy"|"idle") on
// status/row/snapshot.
// 4: `tickets_open` added to row/snapshot (open = not done/dropped).
// 5: `parent` added to the thread record (parent/child trees).
const SchemaVersion = 5

// StatusResponse is returned by GET /v1/status.
type StatusResponse struct {
	Schema        int    `json:"schema"`
	Machine       string `json:"machine"`
	PID           int    `json:"pid"`
	Version       string `json:"version"`
	StartedAtUnix int64  `json:"started_at_unix"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	DBPath        string `json:"db_path"`
	SocketPath    string `json:"socket_path"`
	SchemaVersion int    `json:"schema_version"`
}

// ErrorResponse is the uniform error body.
type ErrorResponse struct {
	Schema int    `json:"schema"`
	Error  string `json:"error"`
}

// --- hooks (PARITY_ROADMAP B1) ---

// HookInfo is one configured hook + its runtime mute state.
type HookInfo struct {
	Name    string `json:"name"`
	Event   string `json:"event"`
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
	Agent   string `json:"agent,omitempty"`
	Machine string `json:"machine,omitempty"`
	Tag     string `json:"tag,omitempty"`
	Command string `json:"command"`
	Muted   bool   `json:"muted"`
}

// HooksListResponse is GET /v1/hooks.
type HooksListResponse struct {
	Schema int        `json:"schema"`
	Hooks  []HookInfo `json:"hooks"`
}

// HookMuteRequest is POST /v1/hooks/mute (enable = muted false).
type HookMuteRequest struct {
	Name  string `json:"name"`
	Muted bool   `json:"muted"`
}

// HookTestRequest is POST /v1/hooks/test — run a hook synchronously with a
// synthetic (or a real thread's) event.
type HookTestRequest struct {
	Name     string `json:"name"`
	ThreadID string `json:"thread_id,omitempty"`
}

// HookTestResponse carries the synchronous run's combined output.
type HookTestResponse struct {
	Schema int    `json:"schema"`
	Name   string `json:"name"`
	Output string `json:"output"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
}
