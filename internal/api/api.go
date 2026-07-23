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
//
// Schema 31 adds `default_chat_view` to UIConfig — the chat surface the app opens a
// thread in by default (terminal|transcript|rpc; empty = the app falls back to terminal).
// Additive/omitempty, display-only preference (mixed-mesh safe).
//
// Schema 32 adds HEADLESS adopt: AdoptThreadRequest gains `agent_kind` + `cwd`, and an
// empty `pane` now means "register an existing, not-running conversation (session_id) as
// a durable headless thread" instead of erroring. Additive/omitempty request fields; a
// pre-32 daemon rejects a pane-less adopt loudly (400), never silently mis-handling it.
//
// Schema 33 adds `extra_keys` to UIConfig — the Android touch-keyboard extra-keys row
// layout (Termux-style), an opaque JSON string the app renders. Empty = no row (default).
// NOT omitempty (the Settings control gates on presence). Additive; mixed-mesh safe.
//
// Schema 34 adds thread HOLD: `on_hold_until_unix` on the thread record (park a thread
// until a future instant), `POST /v1/threads/hold` to set/clear it, and a derived
// `on_hold` flag on the row/snapshot (OnHoldUntilUnix > the owning daemon's clock,
// stamped by that machine's maintainer). The TUI's default view hides on-hold threads;
// a new `on hold` view shows them; auto-expires once the instant passes. Additive
// (new omitempty field + new derived bool + new endpoint) → mixed-mesh safe: a pre-34
// daemon 404s the hold route loudly and its snapshots omit on_hold (read as not-held)
// until upgraded.
//
// Schema 35 adds hold INHERITANCE: `on_hold_effective_unix` on the row/snapshot — the
// effective hold deadline = max(a thread's own on_hold_until, every same-machine ancestor's
// own), so a held parent parks its whole subtree. `on_hold` is now derived from the
// EFFECTIVE deadline (was the own deadline). on_hold_until_unix stays the thread's own
// editable value. Derived per tick by the owning daemon (cross-machine ancestry is NOT
// inherited — the daemon resolves only its own records). Additive omitempty field + a
// semantic widening of the existing on_hold bool → mixed-mesh safe (a pre-35 peer just
// reports non-inherited on_hold for its threads until upgraded).
//
// 36: GET /v1/tmux/master-current gains an optional `machine` query param — when set to a
// peer, the daemon RESOLVES IT ON THAT PEER over its own warm mesh connection (the
// peerRemoteClient http pool / an ssh hop) instead of the caller forking a `sesh … --machine`
// subprocess + a cold connection. This is what makes the TUI's master-cursor preselect fast
// for a remote active window (~120ms cold subprocess → ~the warm RTT). Additive + peer-safe:
// the routed peer is queried with NO machine (origin only), the same call pre-36 peers
// already serve; only the daemon the TUI talks to (its own machine) needs to be on 36, so a
// routine binary+restart on that machine suffices and the mesh need not be in lockstep.
//
// 37: threads.archived_at — the unix time a thread was most recently archived (0 while
// un-archived). Stamped by the OWNING daemon on the archive transition (preserved across an
// idempotent re-archive, cleared on un-archive); the TUI's archived view orders by it (most
// recently archived first). Additive/omitempty on the Thread record (flows through ThreadRow/
// ThreadSnapshot automatically) ⇒ mixed-mesh safe: a pre-37 peer omits it (its archived rows
// sort as archived_at=0, i.e. by the stable fallback) until upgraded.
//
// 38: VIRTUAL threads — a thread record with agent_kind "virtual" is a pure grouping
// node (no agent/pane/transcript) for parenting other threads under. NewThreadRequest
// gains `virtual`; POST /v1/threads/realize converts a virtual thread in place into a
// real never-started headless one (id/children/tags/holds preserved). Agent verbs on a
// virtual thread refuse loudly. Also: deleting ANY thread now promotes its children to
// the deleted thread's parent (no more dangling parent ids), with a store data-fix
// migration clearing historical danglers. Additive ⇒ mixed-mesh safe: only the OWNING
// daemon needs 38 (virtual threads can only be created there); a pre-38 viewer renders
// the kind string and its routed agent verbs hit the upgraded owner's loud refusal; a
// pre-38 daemon 404s the realize route loudly.
//
// 39: MANUAL THREAD ORDERING + DIVIDERS. Thread gains `pin_order` (*float64, nil =
// unpinned): a pinned top-level thread renders ABOVE the auto-sorted block ordered by
// this fractional key. A DIVIDER is a thread record with agent_kind "divider" (a visual
// rule; no agent/pane/transcript/children, always pinned) — NewThreadRequest gains
// `divider`+`pin_order`. POST /v1/threads/pin (PinThreadRequest) sets/clears a thread's
// pin_order; the daemon is a pure setter (the fractional math is client-side over the
// merged view). Pinning is cleared on archive/reparent-to-child; only top-level threads
// may be pinned; a divider can't be un-pinned. Additive/omitempty on the Thread record
// (flows through ThreadRow/ThreadSnapshot automatically) ⇒ mixed-mesh safe: only the
// OWNING daemon needs 39 (a pin/divider can only be created there); a pre-39 viewer omits
// pin_order (renders the thread unpinned) and its routed pin hits the upgraded owner's
// endpoint; a pre-39 daemon 404s the pin route loudly.
//
// 40: MESH-SYNC DATA RATIONING (GitHub issue #1 — the 1 Hz full-snapshot poll burned
// ~450 MB/hr of mobile data on the termux leaf). GET /v1/snapshot serves an ETag over
// its sorted threads payload and honors If-None-Match with a bodyless 304; the mesh
// sync fetches conditionally and idles to [mesh] idle_interval when nothing consumes
// the mesh view (no /v1/mesh read or all-machines fan-out in the active window, no
// [[hooks]] configured). StatusResponse gains `mesh_cadence`. Mixed-mesh safe:
// ETag/304 are plain HTTP conditionals (a pre-40 daemon ignores the header and serves
// the full 200; a pre-40 syncer sends no If-None-Match and gets the full 200);
// mesh_cadence is additive/omitempty. (An initial revision of 40 also slimmed
// archived-dead threads out of the peer-facing snapshot; REVERTED same-day — it made
// remote archived threads vanish from cached mesh views, and an optimization must
// never change what sesh shows. The invisible replacement is 41's delta sync.)
//
// 41: MESH DELTA SYNC (issue #1 follow-up, ticket 953ac79d). GET /v1/snapshot gains an
// optional `since=<cursor>` query param: with a valid cursor the response carries ONLY
// the thread rows changed since it (`delta:true`, `removed` ids for deletions) plus the
// next `generation` cursor — so a steady-state sync round costs ~a hundred bytes and a
// busy tick costs one row, while archived/idle threads replicate once and then never
// re-transfer. The cursor is OPAQUE ("<boot-epoch>:<counter>"); an unknown/stale cursor
// or a daemon restart (epoch mismatch) degrades to the FULL payload — never to wrong
// data. Purely additive and mixed-mesh safe: a pre-41 daemon ignores `since` and serves
// the full 200 (no `generation` ⇒ the client stays on the ETag/304 flow); a pre-41
// client never sends `since`. What the views SHOW is unchanged — this is transfer-layer
// only (the no-UX-tradeoffs rule from the 40 revert).
//
// 42: ATTACHED-CLIENT ACTIVITY (H47 follow-up). ThreadSnapshot gains
// `attached_activity_unix` — the newest tmux client_activity (last INPUT from a
// client, owner's clock) among clients attached to the thread's session, stamped by
// the owning maintainer's existing per-tick list-clients probe; 0/omitted = detached
// or unknown. Motivation: the notify hook's "is the user watching?" gate — raw
// attachment over-suppresses because cockpit clients PARK on sessions, so the hook
// needs "was there recent user INPUT" (plus the observer-local attachment-flip age,
// which needs no wire change). Additive/omitempty ⇒ mixed-mesh safe: a pre-42 peer's
// rows read 0 (unknown) and the hook fails open to notifying.
//
// 43: STATE AUTHORITY (issue #4, _dev/STATE_AUTHORITY.md). POST
// /v1/threads/report-state lets an in-agent reporter (a pi extension, a claude
// hook) report turn lifecycle (turn_started/turn_ended/release, per-thread
// monotonic seq) to the OWNING daemon; the maintainer then prefers the reported
// busy over the pane content-diff heuristic for that headful thread, bounded by
// pane liveness (pane/agent death clears the authority — a crashed agent can
// never pin busy). ThreadRow/ThreadSnapshot gain `state_authority`
// ("reported"|"heuristic", omitempty) so degradation to the heuristic floor is
// always visible, never silent. Additive (new endpoint + omitempty field) ⇒
// mixed-mesh safe: a pre-43 daemon 404s the route loudly (its threads simply
// stay heuristic) and a pre-43 viewer ignores the field.
//
// 44: FLAGGED replaces the 43-era done/blocked overlays (ticket df4fb07a —
// Lukas: "any agent that stops their turn, regardless of whether it is blocked
// or done, should be looked at"). Thread record gains STORED flagged +
// flag_reason + flag_disabled (store migration; persists across restarts,
// replicates like any record field). The OWNING daemon auto-flags on an
// unattended turn end (in-agent reporter edges for all three agents — codex
// via its notify hook, which is turn-end-only and therefore sufficient here —
// or the busy→idle heuristic edge for agents opted in via [flags]) and on an
// agent stalling on a question/approval while unattended; flags NEVER
// auto-clear (manual `thread flag --off` / the TUI key only). Manual flag-on
// re-enables a flag-disabled thread. POST /v1/threads/flag (on|off|disable|
// enable). The snapshot fields done/done_since_unix/blocked/blocked_reason are
// REMOVED (the blocked stall state became daemon-internal: it feeds
// auto-flagging and the wait endpoint's blocked/settled conditions, both
// owner-local); hook events done_changed/blocked_changed became flag_changed,
// env SESH_DONE/SESH_BLOCKED/SESH_BLOCKED_REASON became SESH_FLAGGED/
// SESH_FLAG_REASON. Mixed-mesh: additive fields + removed omitempty fields —
// a pre-44 viewer simply never renders flags (and its stale done/blocked
// never render on 44); a pre-44 daemon 404s the flag route loudly. The 43-era
// [[hooks]] events refuse a 44 daemon at start, so config + binary deploy
// together (myrig notify-flagged).
const SchemaVersion = 44

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
	// DefaultChatView is the chat surface the app opens a thread in by default
	// (terminal|transcript|rpc; empty = the app falls back to terminal). NOT omitempty:
	// the app gates its Settings control on the field being PRESENT (its "does this daemon
	// support it" signal, same as default_agent/default_machine) — omitempty would drop the
	// empty default from the GET and hide the control, so the field must always serialize.
	DefaultChatView string `json:"default_chat_view"`
	// ExtraKeys is the Android extra-keys row layout (Termux-style) as an opaque JSON string
	// the app renders (empty = no row). NOT omitempty — same present-to-advertise-support
	// contract as default_chat_view, so the Settings editor shows.
	ExtraKeys string `json:"extra_keys"`
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
	// MeshCadence (schema 40) is the mesh sync's current pace, so a peer showing
	// "synced 45s ago" is diagnosable as deliberate idling rather than degraded
	// sync: "active" (recent mesh demand), "idle" (backed off to idle_interval),
	// "hooks-pinned" ([[hooks]] configured — never idles), or "always" (idling
	// disabled via idle_interval = "0s").
	MeshCadence string `json:"mesh_cadence,omitempty"`
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
