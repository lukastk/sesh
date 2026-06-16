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
// 6: `notify` added to the thread record (per-thread notification gate;
// hooks receive SESH_NOTIFY).
// 7: `meta` (arbitrary per-thread KV) added to the thread record.
// 8: `cwd_rel` added to row/snapshot — Cwd ~-relative to the OWNING machine's
// home (stamped by that machine's maintainer) so the TUI's CWD column / cwd_label
// rules render correctly cross-machine (the viewer cannot know a peer's home).
// 9: `window` added to master-current response + nav request — the cockpit records a
// full (machine, session, window) location for the prefix+L "last window" toggle.
// 10: thread placement — `into_session`/`into_window`/`into_pane` on the new-thread
// request (a session may host many threads); `launch_command`/`launch_env` on the
// thread response carry the register-then-exec command for `--into-pane`.
// 11: per-thread ticket summary on the row/snapshot — `ticket_name` (newest open
// ticket) + `ticket_needs_input` (any active ticket on a headful·idle thread) for
// the new TUI columns; tickets lost the `description` field (mechanism-only).
// 12: cross-machine ticket binding — `POST /v1/tickets/import` (land a ticket
// record on this daemon, preserving id, binding cleared, active→ready) + `POST
// /v1/tickets/unbind` (detach from thread, active→ready). A ticket is now relocated
// to its bound thread's machine so the live join stays co-located. Additive
// endpoints (no existing wire shape changed) → mixed-mesh safe during rollout.
// 13: `POST /v1/tmux/kill-session` (kill one work-server session by name, routed) —
// the mechanism behind myrig's kill-empty-sessions cleanup. Additive endpoint
// (no existing wire shape changed) → mixed-mesh safe.
// 14: content-addressed blob store — `/v1/blobs` (add/list/get/delete/path/expand)
// for files referenced from prompts by an @blob(<hex>) token (expanded to a path on
// send/copy); plus `POST /v1/tickets/move` (daemon-coordinated cross-machine ticket
// relocation that carries the prompt's referenced blobs). Additive endpoints →
// mixed-mesh safe.
// 15: mesh-wide ticket lookup — `GET /v1/tickets/find?id=` fans out across the mesh
// (tickets are per-daemon) and returns the ticket + its owning machine + bound-thread
// context in one call (powers the Obsidian ticket note as an API client). Plus
// `closed_at_unix` on the ticket record (set on done/dropped). Additive (new endpoint,
// new omitempty field) → mixed-mesh safe; a find fanning out to a pre-15 peer simply
// misses a ticket living there until that peer is upgraded.
// 16: `ticket send-prompt` gained `prepend` (SendPromptRequest, optional) — prepend the
// ticket's name + id to the delivered prompt (default from the [ticket] send_prepend
// config, overridable per call). Also multi-line prompts now deliver via bracketed paste
// (newlines preserved). Additive request field → mixed-mesh safe (a pre-16 daemon ignores
// `prepend` and never prepends).
const SchemaVersion = 17

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

// TranscriptResponse is GET /v1/threads/transcript?id=&tail= — the OWNER-side
// read of a thread conversation's raw transcript lines + the last assistant
// reply (with its monotone count, the dedup marker).
type TranscriptResponse struct {
	Schema     int      `json:"schema"`
	ID         string   `json:"id"`
	Path       string   `json:"path"`
	Lines      []string `json:"lines"`
	LastReply  string   `json:"last_reply"`
	ReplyCount int      `json:"reply_count"`
}

// --- subscriptions (PARITY_ROADMAP C3) ---

// SubscribeRequest creates/removes a delivery edge: the subscribee's
// completed turns are sent into the subscriber thread.
type SubscribeRequest struct {
	Subscriber string `json:"subscriber"`
	Subscribee string `json:"subscribee"`
	AllowCycle bool   `json:"allow_cycle,omitempty"`
}

// SubscriptionInfo is one edge.
type SubscriptionInfo struct {
	Subscriber string `json:"subscriber"`
	Subscribee string `json:"subscribee"`
}

// SubscriptionsResponse is GET /v1/subscriptions[?id=].
type SubscriptionsResponse struct {
	Schema        int                `json:"schema"`
	Subscriptions []SubscriptionInfo `json:"subscriptions"`
}

// --- doctor (PARITY_ROADMAP E2) ---

// DoctorCheck is one diagnostic line. Status: "ok" | "warn" | "fail".
type DoctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// DoctorResponse is GET /v1/doctor — the DAEMON-side checks (the daemon's own
// environment is what runs agents/tmux, and it differs from the caller's
// shell — the deploy-env failure class).
type DoctorResponse struct {
	Schema int           `json:"schema"`
	Checks []DoctorCheck `json:"checks"`
}
