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
// 17: free-text `notes` on the ticket record + mesh-wide `GET /v1/tickets/list-all`
// (every ticket across the mesh, each stamped with its owning machine + bound-thread
// name) powering the Obsidian ticket browser / bulk reconcile. Additive → mixed-mesh safe.
// 18: `thread_parent` added to the list-all entry (the bound thread's parent id) for
// snapshot parity with the per-ticket find. Additive omitempty → mixed-mesh safe.
// 19: `thread_archived` added to the list-all entry (the bound thread is archived) so a
// ticket browser can surface open tickets stranded in archived threads without a
// per-thread round-trip. Additive omitempty → mixed-mesh safe.
// 20: daemon identity on GET /v1/status — `tmux_socket`/`master_socket`/`home`, so the
// TUI learns its machine + work/master sockets from the daemon rather than its own
// SESH_* env (lets a fast `zsh -fc` popup launch `sesh tui` with no shell wrapper).
// Additive omitempty → mixed-mesh safe (a pre-20 daemon just omits them; the client
// falls back to its env-derived config).
// 21: GET /v1/fs/list — a generic, policy-free filesystem primitive returning the
// immediate SUBDIRECTORIES of an allow-listed (home-rooted) path on the daemon's host.
// Additive new endpoint → mixed-mesh safe (a pre-21 daemon 404s the route). Powers the
// Obsidian new-thread modal's box (~/dev) and mysetup (~/mysetup) cwd pickers on
// platforms with no local filesystem access (mobile).
// 22: additive /v1/peers CRUD (GET list / POST add / POST remove) over the existing
// peers registry — lets a client (the GUI) manage the mesh without local file access.
// Additive → mixed-mesh safe (a pre-22 daemon 404s the new routes).
// 23: agent model selection — `model` on the thread record (the agent model pinned at
// `thread new --model`, applied on headed spawn, resume, and every headless turn) plus
// `model` on the new-thread + send request (`send-headless --model` overrides the thread
// model for one turn). Opaque pass-through string (claude/codex/pi each take `--model`);
// a bad model fails LOUDLY at the agent, no curated list. Additive omitempty fields →
// mixed-mesh safe (a pre-23 daemon ignores `model` and spawns with the agent's default).
//
// Schema 24 adds the UI-config endpoints (GET/POST /v1/ui-config, backed by
// <SESH_HOME>/ui_config.toml) so the sesh-ui app can read/write its UI preferences
// (e.g. collapse_parents) over the API. Purely additive — a pre-24 daemon simply
// lacks the route.
//
// Schema 25 adds `cwd_roots` to UIConfig — the new-thread modal's "default parent
// folders" quick cwd pick (~/mysetup, ~/dev), listed per machine via fs/list.
// Additive — a pre-25 daemon omits the field and the app falls back to its default.
//
// Schema 26 adds `cwd_labels` to UIConfig — match→label regex rules (same language as
// config.toml's [[cwd_label]]) the app applies to format new-thread picker entries
// (e.g. a ~/dev box index → "<boxname> <boxid>"). Additive; validated on save (loud 400).
//
// Schema 27 adds `transcript_prefetch_secs` to UIConfig — the app's background transcript
// prefetch interval (cache all non-archived transcripts so opening is instant). Additive.
//
// Schema 28 adds `master_command` to UIConfig + the GET /v1/master/terminal WS endpoint —
// the app's "Master" mode runs that configured command (e.g. mmt-start) in a pty over a
// WebSocket. Additive; the endpoint refuses loudly if master_command is unset.
//
// Schema 29 adds the plugin command-provider substrate: GET /v1/plugins (manifests at
// <SESH_HOME>/plugins/*.toml) + POST /v1/plugins/{name}/{capability} (run a list/action
// capability's command on this machine, routed cross-machine like fs/list). Additive;
// the first plugin is the shipped boxyard example (box groups in the picker + create-box).
//
// Schema 30 adds `default_agent` + `default_machine` to UIConfig — the agent_kind and
// machine the app's New-thread modal preselects (empty = the app's own fallback / the
// local daemon). Additive, display-only preferences.
const SchemaVersion = 30

// UIConfig is the sesh-ui app's UI preferences, stored in <SESH_HOME>/ui_config.toml
// and served over GET/POST /v1/ui-config. Typed settings sesh stores + serves but does
// not otherwise interpret; missing keys resolve to their defaults on read.
type UIConfig struct {
	// CollapseParents makes parent threads start collapsed in the app's thread tree.
	CollapseParents bool `json:"collapse_parents"`
	// CwdRoots are the new-thread "default parent folders" — the app lists each one's
	// subdirs (per target machine, via fs/list) as a quick cwd pick. Default ~/mysetup, ~/dev.
	CwdRoots []string `json:"cwd_roots"`
	// CwdLabels are match→label regex rules the app applies to format picker entries.
	CwdLabels []CwdLabelRule `json:"cwd_labels"`
	// TranscriptPrefetchSecs is the app's background transcript-prefetch interval (0=off).
	TranscriptPrefetchSecs int `json:"transcript_prefetch_secs"`
	// MasterCommand is the command the app's "Master" mode runs in a pty (e.g. mmt-start).
	MasterCommand string `json:"master_command"`
	// DefaultAgent is the agent_kind the New-thread modal preselects (empty = app fallback).
	DefaultAgent string `json:"default_agent"`
	// DefaultMachine is the machine the New-thread modal preselects (empty = local daemon).
	DefaultMachine string `json:"default_machine"`
}

// CwdLabelRule is one new-thread-picker display rule (regex match + template label).
type CwdLabelRule struct {
	Match string `json:"match"`
	Label string `json:"label"`
}

// UIConfigResponse is returned by GET and POST /v1/ui-config.
type UIConfigResponse struct {
	Schema   int      `json:"schema"`
	UIConfig UIConfig `json:"ui_config"`
}

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
	// Daemon IDENTITY (schema 20): the machine + tmux sockets + home this daemon
	// owns, so a client (the TUI) can learn them from the daemon instead of from
	// its own SESH_* env — making `sesh tui` work correctly even when launched
	// without the shell wrapper that exports those (e.g. a fast `zsh -fc` popup).
	TmuxSocket   string `json:"tmux_socket,omitempty"`
	MasterSocket string `json:"master_socket,omitempty"`
	Home         string `json:"home,omitempty"`
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
