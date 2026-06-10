// Package api defines the versioned, machine-readable client-facing contract
// between the sesh CLI/TUI (and future Obsidian plugin) and the daemon. Output
// is a contract: every response carries the schema version so clients can
// detect drift.
package api

// SchemaVersion is the version of the client-facing JSON schema. Bump on any
// breaking change to a response shape.
// 2: the unified thread model — Thread.headless dropped (headless/headful is
// inferred runtime, not a stored mode); Activity value "dead" renamed "idle".
const SchemaVersion = 2

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
