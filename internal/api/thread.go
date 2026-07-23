package api

// VirtualAgentKind is the agent_kind of a VIRTUAL thread — a pure grouping node
// in the thread tree with NO agent, pane, or transcript. It exists so threads
// can be parented under something that is not (yet) a real thread; all record
// machinery (tree, reparent, hold inheritance, tags, archive, mesh sync) applies
// unchanged. It is deliberately NOT a valid agents.Kind: every agent-shaped code
// path that parses the kind refuses it loudly, so an unguarded path fails closed
// instead of doing something plausible-but-wrong. `thread realize` converts a
// virtual thread in place into a real (never-started headless) one.
const VirtualAgentKind = "virtual"

// DividerAgentKind is the agent_kind of a DIVIDER — a purely visual node the TUI
// renders as a horizontal rule (with an optional label) to separate groups of
// manually-ordered threads. Like a virtual thread it has NO agent, pane, or
// transcript, and is NOT a valid agents.Kind (every agent-shaped path refuses it
// loudly). Unlike a virtual thread it is childless and lives ONLY in the pinned
// block — it always carries a PinOrder and cannot be un-pinned (delete it to
// remove it).
const DividerAgentKind = "divider"

// NonAgentKind reports whether an agent_kind is one of the non-agent node kinds
// (virtual grouping node, divider) — records that have no conversation and refuse
// every agent-shaped operation.
func NonAgentKind(kind string) bool {
	return kind == VirtualAgentKind || kind == DividerAgentKind
}

// Thread is the persistent thread record. Pane and runtime state are NOT stored
// here — they are resolved live (see ThreadStatusResponse).
type Thread struct {
	ID            string   `json:"id"`
	Machine       string   `json:"machine"`
	SessionName   string   `json:"session_name"`
	Cwd           string   `json:"cwd"`
	AgentKind     string   `json:"agent_kind"`
	Name          string   `json:"name"`
	Tags          []string `json:"tags"`
	CreatedAtUnix int64    `json:"created_at_unix"`
	// Parent is the parent thread's id ('' = root) — the tree the TUI renders.
	Parent string `json:"parent,omitempty"`
	// Notify gates the user's notification hooks for this thread (hooks receive
	// SESH_NOTIFY=0/1; the daemon never decides what a notification IS).
	Notify bool `json:"notify"`
	// Meta is arbitrary per-thread KV ([[tui.views]] meta.<key> predicates,
	// scripts). Mutated via thread meta set/unset.
	Meta map[string]string `json:"meta,omitempty"`
	// AgentSessionID is the agent's own conversation id (captured at/after spawn;
	// what makes resume possible).
	AgentSessionID string `json:"agent_session_id,omitempty"`
	// HeadlessStarted is true once the CONVERSATION has begun — a headed spawn sets
	// it at launch, a headless thread on its first turn. It picks resume-vs-create
	// semantics for headless turns; it is NOT a headless/headful mode bit (that is
	// inferred from runtime — see Activity).
	HeadlessStarted bool `json:"headless_started,omitempty"`
	// Archived hides the thread from the active list (record kept).
	Archived bool `json:"archived,omitempty"`
	// ArchivedAtUnix is the unix time the thread was most recently archived; 0
	// while un-archived. Stamped by the OWNING daemon on the archive transition,
	// preserved across an idempotent re-archive, cleared to 0 on un-archive. The
	// TUI's archived view orders by it (most recently archived first).
	ArchivedAtUnix int64 `json:"archived_at_unix,omitempty"`
	// Model is the agent model pinned to this thread (opaque pass-through, e.g.
	// "haiku", "anthropic/claude-opus-4-8", "gpt-5.5"). '' = the agent's own
	// default. Applied on headed spawn, resume, and every headless turn; a per-turn
	// override (send-headless --model) does NOT change it.
	Model string `json:"model,omitempty"`
	// OnHoldUntilUnix parks a thread until a future instant: while now < this value
	// the thread is "on hold" and hidden from the default active view. 0 = not on
	// hold. The caller supplies the absolute instant (the TUI computes "start of
	// tomorrow" for a plain hold, or parses an explicit date); the OWNING daemon
	// derives the live OnHold flag against ITS clock. Auto-expires — once the instant
	// passes the thread silently returns to the active view (no explicit unhold).
	OnHoldUntilUnix int64 `json:"on_hold_until_unix,omitempty"`
	// PinOrder is the manual-ordering sort key. nil = not pinned (the thread sits in
	// the auto-sorted block); non-nil = pinned, rendered ABOVE the auto block ordered
	// by this key ascending. Only TOP-LEVEL (parentless) threads may be pinned; a
	// divider always carries one. The value is a fractional float computed CLIENT-SIDE
	// from the merged cross-machine view (the daemon is a pure setter, like hold), so
	// pinning/reordering is a single write to one owner and never renumbers siblings
	// (which may live on offline machines). Cleared when the thread is archived or
	// reparented under another thread.
	PinOrder *float64 `json:"pin_order,omitempty"`
	// Flagged marks the thread as needing the user's attention (schema 44). Set
	// AUTOMATICALLY by the owning daemon when a turn ends or the agent stalls on
	// a question/approval while the session is unattended (in-agent reporter
	// events; the busy→idle heuristic edge only for agents opted in via [flags]),
	// or MANUALLY (thread flag / the TUI key). NEVER auto-cleared — a flag stays
	// until the user unflags it (Lukas 2026-07-23; explicitness over convenience).
	Flagged bool `json:"flagged,omitempty"`
	// FlagReason optionally says WHY the thread auto-flagged (e.g. the question
	// claude asked). Cleared with the flag. Empty for manual flags.
	FlagReason string `json:"flag_reason,omitempty"`
	// FlagDisabled suppresses AUTO-flagging for this thread (e.g. children a
	// parent thread monitors — their turn ends are the parent's business).
	// Manually flagging a flag-disabled thread RE-ENABLES flagging and flags it
	// (one simple rule — no auto-vs-manual provenance bit).
	FlagDisabled bool `json:"flag_disabled,omitempty"`
}

// The live runtime state of a thread is two ORTHOGONAL axes, each from a
// distinct signal (per the Phase 3b design decision; see _dev/SPEC.md §3):
//
//   - Activity   from pane content-diff (working/waiting), the headless turn
//     registry (working), and pane/turn absence (idle)
//   - Attachment from `tmux list-clients`
//
// They are orthogonal because a detached agent can still be working, and a
// not-currently-viewed idle agent still needs input. ticket needs-input is
// derived from Activity == waiting REGARDLESS of attachment.

// The thread's runtime state is TWO ORTHOGONAL AXES (plus Attachment below) —
// never a fused enum (the old activity values "waiting"/"idle" each secretly
// encoded a (head,busy) pair, and "working" erased the head axis entirely):
//
//	head — the FORM of the runtime:
//	         headful  = a live tmux pane runs the agent
//	         headless = no pane (a turn process may or may not be in flight)
//	busy — whether a turn is executing right now:
//	         busy = a live pane that is actively changing (content-diff), OR a
//	                headless turn process in flight (the turn registry)
//	         idle = not executing: a pane at its prompt (headful) or no
//	                runtime at all (headless)
//
// The four states: headful·busy (pane mid-turn), headful·idle (agent at its
// prompt, blocked on the human), headless·busy (turn in flight — nothing to
// enter), headless·idle (no runtime — revive with resume/headful, or run a
// turn with send-headless). Both axes are strings so an unrecognized value
// from a version-skewed peer renders as a loud "?" on exactly that axis.

// Head is the runtime-form axis.
type Head string

const (
	Headful  Head = "headful"
	Headless Head = "headless"
)

// Busy is the execution axis.
type Busy string

const (
	BusyBusy Busy = "busy"
	BusyIdle Busy = "idle"
)

// Attachment is whether any tmux client is attached to the thread's session.
type Attachment string

const (
	Attached Attachment = "attached"
	Detached Attachment = "detached"
)

// StateAuthority says WHICH mechanism decided a headful thread's busy axis: an
// in-agent reporter ("reported" — exact, via POST /v1/threads/report-state) or
// the pane content-diff heuristic ("heuristic" — the floor). Omitted = unknown:
// a pre-43 peer's row, a headless thread (its busy comes from the daemon-owned
// turn registry, which needs no authority label), or the grid's on-demand
// fallback path (which never runs the rolling probe). Degradation from reported
// to heuristic must always be VISIBLE through this field, never silent. See
// _dev/STATE_AUTHORITY.md (schema 43).
type StateAuthority string

const (
	AuthorityReported  StateAuthority = "reported"
	AuthorityHeuristic StateAuthority = "heuristic"
)

// NewThreadRequest is the body of POST /v1/threads.
type NewThreadRequest struct {
	Agent    string `json:"agent"`              // claude | codex | pi
	Name     string `json:"name"`               // thread name (also seeds session name)
	Cwd      string `json:"cwd"`                // absolute start dir
	Headless bool   `json:"headless,omitempty"` // headless (no window) vs headed
	Parent   string `json:"parent,omitempty"`   // parent thread id ('' = root); must exist
	// ForkFrom branches an EXISTING thread's conversation: the source
	// transcript's prefix (through MessageID assistant turns; 0 = all) is
	// copied under a fresh agent session id and the new thread resumes from
	// that point. Headless-born; the source is untouched.
	ForkFrom  string `json:"fork_from,omitempty"`
	MessageID int    `json:"message_id,omitempty"`
	// Mode overrides the [spawn] launch mode for this spawn (yolo|default|
	// sandbox; '' = the config default).
	Mode string `json:"mode,omitempty"`
	// Model pins the agent model for this thread (opaque pass-through; '' = the
	// agent's default). Stored on the record and applied to every later spawn/turn.
	Model string `json:"model,omitempty"`
	// Msg, for headed spawns, is an initial prompt sent once the agent is
	// READY (the daemon waits for the pane asynchronously — never the blank-
	// pane race; a delivery failure is loud in the daemon log).
	Msg string `json:"msg,omitempty"`
	// Placement (headed only; mutually exclusive; empty = own new session).
	// A session may host many threads — runtime identity is the pane's marker.
	//   IntoSession: add the thread as a new WINDOW of an existing session.
	//   IntoWindow:  split target (pane/window) — the thread is a new pane beside it.
	//   IntoPane:    register-then-exec — bind the thread to an EXISTING shell pane
	//                and return LaunchCommand/LaunchEnv for the caller to exec in
	//                place (the daemon does NOT spawn). The pane must hold no agent.
	IntoSession string `json:"into_session,omitempty"`
	IntoWindow  string `json:"into_window,omitempty"`
	IntoPane    string `json:"into_pane,omitempty"`
	// Virtual creates a VIRTUAL thread — a pure grouping node (agent_kind
	// "virtual", no agent/pane/transcript) for parenting other threads under.
	// Mutually exclusive with every agent-shaped field (agent, headless,
	// fork_from, placement, msg, mode, model) — all refused loudly. Cwd is
	// OPTIONAL here (kept as the default for a later realize).
	Virtual bool `json:"virtual,omitempty"`
	// Divider creates a DIVIDER node — a visual horizontal rule in the pinned
	// block (agent_kind "divider", no agent/pane/transcript/children). Mutually
	// exclusive with every agent-shaped field (all refused loudly); Cwd is ignored.
	// Name is the optional label. A divider is created straight into the manual
	// order, so PinOrder MUST be set (the client computes it from the merged view).
	Divider bool `json:"divider,omitempty"`
	// PinOrder places a newly-created DIVIDER in the manual order (the client-side
	// fractional key). Ignored for non-divider spawns (a normal thread is pinned
	// after creation via POST /v1/threads/pin).
	PinOrder *float64 `json:"pin_order,omitempty"`
}

// PinThreadRequest is the body of POST /v1/threads/pin. PinOrder non-nil PINS or
// repositions the thread at that manual-order key; nil UNPINS it (removes the
// manual ordering). The caller supplies the absolute float — the daemon is a pure
// setter (the fractional math is client-side, over the merged cross-machine view).
// Refused loudly for a thread that has a parent (only top-level threads can be
// pinned) and for un-pinning a divider (delete it instead).
type PinThreadRequest struct {
	ID       string   `json:"id"`
	PinOrder *float64 `json:"pin_order,omitempty"`
}

// RealizeThreadRequest is the body of POST /v1/threads/realize: convert a
// VIRTUAL thread in place into a real one. The result is exactly a fresh
// never-started headless thread (agent kind set, session id pre-minted for
// pi/claude, no conversation until the first turn) — id, children, tags, holds
// and ticket bindings all survive because nothing but the record changes.
type RealizeThreadRequest struct {
	ID    string `json:"id"`
	Agent string `json:"agent"` // claude | codex | pi (never "virtual")
	// Cwd ('' = keep the record's stored cwd). A cwd must exist by realize time
	// (agents need one); missing both is a loud refusal.
	Cwd string `json:"cwd,omitempty"`
	// Model pins the agent model (opaque pass-through; '' = the agent's default).
	Model string `json:"model,omitempty"`
}

// ReparentThreadRequest re-parents a thread (” = make it a root).
type ReparentThreadRequest struct {
	ID     string `json:"id"`
	Parent string `json:"parent"`
}

// ThreadResponse wraps a single thread. For an `--into-pane` (register-then-exec)
// spawn the daemon does NOT launch the agent — it returns the exact command and
// env for the caller to exec in place; LaunchCommand is empty for every other spawn.
type ThreadResponse struct {
	Schema        int               `json:"schema"`
	Thread        Thread            `json:"thread"`
	LaunchCommand string            `json:"launch_command,omitempty"`
	LaunchEnv     map[string]string `json:"launch_env,omitempty"`
}

// ThreadListResponse is returned by GET /v1/threads.
type ThreadListResponse struct {
	Schema  int      `json:"schema"`
	Threads []Thread `json:"threads"`
	// Unreachable lists peer machines that could not be reached during an
	// all-machines fan-out (offline peers are an expected state, not an error —
	// but they are reported, never silently dropped).
	Unreachable []string `json:"unreachable,omitempty"`
}

// ThreadSendRequest is the body of POST /v1/threads/send (headful send: deliver
// a message into the thread's live pane and submit it).
type ThreadSendRequest struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	// Mode overrides the [spawn] mode for a HEADLESS turn ('' = config).
	Mode string `json:"mode,omitempty"`
	// Model overrides the thread's pinned model for THIS headless turn only ('' =
	// use the thread's stored model). The thread record is not changed.
	Model string `json:"model,omitempty"`
}

// RenameThreadRequest is the body of POST /v1/threads/rename.
type RenameThreadRequest struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// TagThreadRequest is the body of POST /v1/threads/tag: add and/or remove tags.
type TagThreadRequest struct {
	ID     string   `json:"id"`
	Add    []string `json:"add,omitempty"`
	Remove []string `json:"remove,omitempty"`
}

// ArchiveThreadRequest is the body of POST /v1/threads/archive.
type ArchiveThreadRequest struct {
	ID       string `json:"id"`
	Archived bool   `json:"archived"`
}

// StopThreadRequest is the body of POST /v1/threads/stop (end the runtime —
// agent + tmux session — but KEEP the record, which becomes a dead, resumable
// thread).
type StopThreadRequest struct {
	ID string `json:"id"`
}

// DeleteThreadRequest is the body of POST /v1/threads/delete (drop the record).
// Delete refuses a thread whose runtime is still LIVE unless Force is set —
// deleting a live thread orphans its agent (record gone, process still running).
type DeleteThreadRequest struct {
	ID    string `json:"id"`
	Force bool   `json:"force"`
}

// ThreadResumeRequest is the body of POST /v1/threads/resume (revive a dead
// headed thread).
type ThreadResumeRequest struct {
	ID string `json:"id"`
}

// ThreadHeadfulRequest is the body of POST /v1/threads/headful (promote a live
// headless thread into a headed tmux pane, resuming its conversation).
type ThreadHeadfulRequest struct {
	ID string `json:"id"`
}

// PaneLocator is a resolved live pane for a thread.
type PaneLocator struct {
	Session string `json:"session"`
	Window  int    `json:"window"`
	Pane    string `json:"pane"`
	PanePID int    `json:"pane_pid"`
}

// ResolvePaneResponse is returned by GET /v1/threads/{id}/pane. Found is false
// when no live pane bears the thread's marker (the thread is dead).
type ResolvePaneResponse struct {
	Schema int         `json:"schema"`
	Found  bool        `json:"found"`
	Pane   PaneLocator `json:"pane,omitempty"`
}

// ThreadCaptureResponse returns the captured text of a thread's live pane
// (GET /v1/threads/capture?id=&lines=). Lines echoes the request (0 = the visible
// area only; N>0 = the last N lines including scrollback).
type ThreadCaptureResponse struct {
	Schema  int    `json:"schema"`
	ID      string `json:"id"`
	Lines   int    `json:"lines"`
	Content string `json:"content"`
}

// ThreadStatusResponse is the live runtime status of a thread: the two
// orthogonal axes plus the raw signals they derive from.
type ThreadStatusResponse struct {
	Schema       int        `json:"schema"`
	ID           string     `json:"id"`
	Head         Head       `json:"head"`
	Busy         Busy       `json:"busy"`
	Attachment   Attachment `json:"attachment"`
	AgentRunning bool       `json:"agent_running"`
	Clients      int        `json:"clients"`
	Pane         string     `json:"pane,omitempty"`
}

// NeedsInput is the derived "the human is blocking the agent" view: a live pane
// whose agent is at its prompt (headful·idle), regardless of attachment.
func (s ThreadStatusResponse) NeedsInput() bool { return s.Head == Headful && s.Busy == BusyIdle }

// ThreadRow is a thread plus its live runtime status — the unit the TUI grid
// renders. The status is computed live (never stored).
type ThreadRow struct {
	Thread
	Head       Head       `json:"head"`
	Busy       Busy       `json:"busy"`
	Attachment Attachment `json:"attachment"`
	// TicketsOpen is the number of bound, still-open tickets (not done/dropped)
	// — the TUI's `ticketed` predicate and TICKETS column read it.
	TicketsOpen int `json:"tickets_open"`
	// TicketName is the newest open ticket's name (the TKT-NAME column); '' if none.
	TicketName string `json:"ticket_name,omitempty"`
	// TicketNeedsInput is true when ANY bound open ticket of this thread needs input
	// (an active ticket on a headful·idle thread) — the TKT-! column.
	TicketNeedsInput bool `json:"ticket_needs_input,omitempty"`
	// CwdRel mirrors ThreadSnapshot.CwdRel: Cwd ~-relative to the OWNING machine's
	// home, so the CWD column / cwd_label rules render correctly cross-machine.
	CwdRel string `json:"cwd_rel,omitempty"`
	// OnHold is the live "on hold right now" flag — OnHoldEffectiveUnix > the OWNING
	// daemon's clock. The owner derives it (only it can compare against its own now).
	// The default view hides on-hold rows; the `on hold` view shows them.
	OnHold bool `json:"on_hold,omitempty"`
	// OnHoldEffectiveUnix is the EFFECTIVE hold deadline: max(this thread's own
	// OnHoldUntilUnix, every same-machine ANCESTOR's own) so a child INHERITS a parent's
	// hold (a held parent parks its whole subtree). Derived per tick by the owner; 0 =
	// not held. OnHoldUntilUnix stays the thread's OWN editable value (what `hold`/`H`
	// set/clear); this is the inherited maximum the view/column read.
	OnHoldEffectiveUnix int64 `json:"on_hold_effective_unix,omitempty"`
	// StateAuthority is which mechanism decided Busy for a headful thread
	// (reported vs heuristic); omitted = unknown/not-applicable. Schema 43.
	// (The blocked/done overlays that briefly lived here in 43 were replaced
	// by the flagged system in 44: agent-stall state is daemon-internal now —
	// it feeds auto-flagging and the wait endpoint's settled condition, both
	// owner-local — and "finished while you weren't looking" became the
	// stored, manually-cleared Flagged field on the record.)
	StateAuthority StateAuthority `json:"state_authority,omitempty"`
}

// NeedsInput is the derived needs-input view for a row (headful·idle).
func (r ThreadRow) NeedsInput() bool { return r.Head == Headful && r.Busy == BusyIdle }

// ThreadGridResponse is returned by GET /v1/threads/grid: every thread with its
// live status, optionally fanned out across the mesh.
type ThreadGridResponse struct {
	Schema      int         `json:"schema"`
	Rows        []ThreadRow `json:"rows"`
	Unreachable []string    `json:"unreachable,omitempty"`
}

// ThreadSnapshot is the unit of mesh replication: a thread record plus its live
// state, self-contained so a client renders it with no extra round-trip. Produced
// by the daemon's background state maintainer (never an on-demand probe). See
// _dev/MESH.md.
type ThreadSnapshot struct {
	Thread
	Head       Head       `json:"head"`
	Busy       Busy       `json:"busy"`
	Attachment Attachment `json:"attachment"`
	// AttachedActivityUnix is the newest tmux client_activity (last INPUT from a
	// client, the OWNING machine's clock) among clients attached to the thread's
	// session; 0 = detached or unknown (e.g. a pre-42 peer). Lets a notify hook
	// tell "the user is driving this session" from "a cockpit client is merely
	// parked on it" — raw attachment cannot (schema 42).
	AttachedActivityUnix int64  `json:"attached_activity_unix,omitempty"`
	TicketsOpen          int    `json:"tickets_open"`
	TicketName           string `json:"ticket_name,omitempty"`        // newest open ticket's name (TKT-NAME column)
	TicketNeedsInput     bool   `json:"ticket_needs_input,omitempty"` // any active ticket on a headful·idle thread
	AgentRunning         bool   `json:"agent_running"`
	LastActiveUnix       int64  `json:"last_active_unix"` // last pane change / turn completion
	// CwdRel is Cwd rendered ~-relative to the OWNING machine's home, stamped by
	// that machine's maintainer (the home is owner data the viewer cannot know). A
	// viewer applies its own [[cwd_label]] rules to this, so the CWD column labels
	// correctly even cross-machine — without it, a viewer with a different home
	// shows the raw absolute path. '' only if the owner's home is unknown.
	CwdRel string `json:"cwd_rel,omitempty"`
	// OnHold is the live "on hold right now" flag (OnHoldEffectiveUnix > the owning
	// daemon's clock), stamped by that machine's maintainer so a cross-machine viewer
	// need not (and cannot reliably) compare against the owner's clock itself.
	OnHold bool `json:"on_hold,omitempty"`
	// OnHoldEffectiveUnix is the effective hold deadline including inherited holds —
	// max(own, same-machine ancestors' own). See ThreadRow.OnHoldEffectiveUnix.
	OnHoldEffectiveUnix int64 `json:"on_hold_effective_unix,omitempty"`
	// StateAuthority is which mechanism decided Busy for this headful thread
	// (reported vs heuristic); omitted = unknown/not-applicable (headless, a
	// pre-43 peer). Stamped by the owning maintainer. Schema 43. (The 43-era
	// blocked/done snapshot overlays were replaced by the stored Flagged
	// record field in 44 — see Thread.Flagged.)
	StateAuthority StateAuthority `json:"state_authority,omitempty"`
}

// MachineSnapshot is one machine's live thread state, returned by
// GET /v1/snapshot — a pure read of the maintained state. Normally Threads is the
// FULL set; a schema-41 daemon answering a valid `since=<cursor>` sets Delta and
// sends only the rows changed since that cursor (plus Removed ids), so a mesh
// sync round transfers what changed, not the whole machine.
type MachineSnapshot struct {
	Schema          int              `json:"schema"`
	Machine         string           `json:"machine"`
	GeneratedAtUnix int64            `json:"generated_at_unix"`
	Threads         []ThreadSnapshot `json:"threads"`
	// Delta (schema 41): Threads is the changed-rows set for the requested cursor,
	// not the full machine; Removed lists thread ids deleted since it.
	Delta   bool     `json:"delta,omitempty"`
	Removed []string `json:"removed,omitempty"`
	// Generation (schema 41) is the OPAQUE cursor for the next conditional fetch.
	// Present on every schema-41 response (full or delta); absent from a pre-41
	// daemon, which is how a syncer knows to stay on the ETag/304 flow.
	Generation string `json:"generation,omitempty"`
}

// MachineView is one machine's slice of the merged mesh view: its threads plus how
// fresh they are. Self is always fresh; a peer carries the cache's last-sync time
// and whether the most recent sync attempt reached it (offline → reachable=false,
// last-known threads retained).
type MachineView struct {
	Machine      string           `json:"machine"`
	Self         bool             `json:"self"`
	Reachable    bool             `json:"reachable"`
	SyncedAtUnix int64            `json:"synced_at_unix"`
	Threads      []ThreadSnapshot `json:"threads"`
}

// MeshSnapshot is the merged cross-machine view returned by GET /v1/mesh: this
// machine's live snapshot plus every peer's cached snapshot. Read locally (O(1)),
// offline-capable. See _dev/MESH.md.
type MeshSnapshot struct {
	Schema   int           `json:"schema"`
	Machines []MachineView `json:"machines"`
}

// HeadlessReplyResponse is returned by GET /v1/threads/headless-reply: whether a
// turn is still in flight, and the last completed reply (if any).
type HeadlessReplyResponse struct {
	Schema    int    `json:"schema"`
	ID        string `json:"id"`
	Working   bool   `json:"working"`
	HaveReply bool   `json:"have_reply"`
	Reply     string `json:"reply,omitempty"`
}

// NotifyThreadRequest toggles a thread's notification gate.
type NotifyThreadRequest struct {
	ID string `json:"id"`
	On bool   `json:"on"`
}

// HoldThreadRequest is the body of POST /v1/threads/hold: park the thread until
// OnHoldUntilUnix (0 = clear the hold). The caller supplies the ABSOLUTE instant
// (the TUI computes "start of tomorrow" for a plain hold, or parses an explicit
// date) — the daemon is a pure setter, mechanism not UX; "on hold right now" is
// derived live from this value vs the owning daemon's clock, so a past instant
// stores cleanly and simply reads as not-on-hold (auto-expiry).
type HoldThreadRequest struct {
	ID              string `json:"id"`
	OnHoldUntilUnix int64  `json:"on_hold_until_unix"`
}

// FlagThreadRequest is POST /v1/threads/flag (schema 44). Action semantics:
//   - "on":      flag the thread (manual). Also RE-ENABLES flagging if it was
//     disabled — one simple rule instead of an auto-vs-manual provenance bit.
//   - "off":     clear the flag (+ its reason). Flags NEVER auto-clear.
//   - "disable": suppress auto-flagging for this thread (parent-monitored
//     children); also clears any current flag.
//   - "enable":  re-allow auto-flagging (does not flag by itself).
type FlagThreadRequest struct {
	ID     string `json:"id"`
	Action string `json:"action"`
}

// FlagThreadRequest actions.
const (
	FlagOn      = "on"
	FlagOff     = "off"
	FlagDisable = "disable"
	FlagEnable  = "enable"
)

// ThreadWaitResponse is GET /v1/threads/wait?id=&until=&timeout_ms= — one
// server-owned bounded wait for a thread state (schema 43). The daemon polls
// its maintained state internally (~100ms) for up to timeout_ms (capped at
// 10s per request — clients loop to their own deadline, which keeps every
// transport's per-request timeout safe). reached=false after the bound is a
// normal outcome (200), not an error: the caller's loop decides when the
// OVERALL wait has failed. `until` vocabulary: busy | idle | blocked |
// settled (= idle or blocked — "the agent stopped running on its own").
type ThreadWaitResponse struct {
	Schema  int    `json:"schema"`
	ID      string `json:"id"`
	Reached bool   `json:"reached"`
	Head    Head   `json:"head"`
	Busy    Busy   `json:"busy"`
	Blocked bool   `json:"blocked,omitempty"`
	// LastActiveUnix rides along for the send --wait stall guard: delivered
	// keystrokes change the pane, which bumps it even before a turn latches.
	LastActiveUnix int64 `json:"last_active_unix,omitempty"`
}

// ReportStateRequest is POST /v1/threads/report-state — an in-agent reporter
// (a pi extension, a claude hook) tells the OWNING daemon a turn-lifecycle
// fact about its thread (schema 43; see _dev/STATE_AUTHORITY.md). Seq must
// be strictly increasing per thread: a stale/duplicate seq is refused loudly
// (409), never applied out of order — reports may race over the wire and a
// late-arriving turn_started must not overwrite the turn_ended after it.
type ReportStateRequest struct {
	ThreadID string `json:"thread_id"`
	// Source identifies the reporter (e.g. "sesh:pi-ext"), for diagnostics.
	Source string `json:"source"`
	// Event is one of the Report* constants below.
	Event string `json:"event"`
	Seq   int64  `json:"seq"`
	// Reason optionally describes a `blocked` event (e.g. the permission
	// prompt's message). Ignored for other events.
	Reason string `json:"reason,omitempty"`
}

// Reporter event vocabulary. `release` withdraws the reporter's authority
// (only a real agent QUIT should send it — see the pi runtime-rebind footnote
// in _dev/STATE_AUTHORITY.md); the thread then degrades to the heuristic floor.
// `blocked`/`unblocked` overlay the busy axis: the agent is MID-TURN but
// stalled on the human (an approval prompt, a question). turn_started and
// turn_ended both clear the blocked overlay — a new or finished turn is never
// still blocked. Since 44 the blocked state is DAEMON-INTERNAL (no snapshot
// field): it feeds the auto-flag trigger and the wait endpoint's
// blocked/settled conditions, both resolved on the owning daemon.
const (
	ReportTurnStarted = "turn_started"
	ReportTurnEnded   = "turn_ended"
	ReportBlocked     = "blocked"
	ReportUnblocked   = "unblocked"
	ReportRelease     = "release"
)

// AdoptThreadRequest brings an agent sesh didn't spawn under management.
//
// Two modes:
//   - PANE adopt (Pane set): a live agent running on the work server is brought
//     under management; detection is per agent, every ambiguity loud.
//   - HEADLESS adopt (Pane empty): an EXISTING conversation that is NOT running
//     anywhere (e.g. a claude transcript on disk) is registered as a durable
//     headless thread. There is no pane to inspect, so SessionID and AgentKind
//     must be supplied explicitly; the caller asserts both.
type AdoptThreadRequest struct {
	Pane string `json:"pane"`
	Name string `json:"name"`
	// SessionID, when set, is the agent's conversation id supplied EXPLICITLY by the
	// caller. In PANE adopt it's used when auto-detection can't find it (e.g. claude
	// launched with a bare `-r`, no id in argv) and bypasses per-agent detection (the
	// pane must still hold a live agent). In HEADLESS adopt it is REQUIRED — it is the
	// existing conversation the headless thread binds to.
	SessionID string `json:"session_id,omitempty"`
	// AgentKind (claude|codex|pi) is REQUIRED for HEADLESS adopt (no pane to detect
	// it) and must be empty for PANE adopt (the live pane's agent is authoritative).
	AgentKind string `json:"agent_kind,omitempty"`
	// Cwd is the headless thread's working directory (HEADLESS adopt only). The
	// owning daemon expands a leading ~ against its own home.
	Cwd string `json:"cwd,omitempty"`
}

// MetaThreadRequest sets (” value = deletes) one meta key.
type MetaThreadRequest struct {
	ID    string `json:"id"`
	Key   string `json:"key"`
	Value string `json:"value"`
}
