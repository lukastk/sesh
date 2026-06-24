package api

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
	// Model is the agent model pinned to this thread (opaque pass-through, e.g.
	// "haiku", "anthropic/claude-opus-4-8", "gpt-5.5"). '' = the agent's own
	// default. Applied on headed spawn, resume, and every headless turn; a per-turn
	// override (send-headless --model) does NOT change it.
	Model string `json:"model,omitempty"`
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
}

// ReparentThreadRequest re-parents a thread ('' = make it a root).
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
	Head           Head       `json:"head"`
	Busy           Busy       `json:"busy"`
	Attachment     Attachment `json:"attachment"`
	TicketsOpen      int        `json:"tickets_open"`
	TicketName       string     `json:"ticket_name,omitempty"`        // newest open ticket's name (TKT-NAME column)
	TicketNeedsInput bool       `json:"ticket_needs_input,omitempty"` // any active ticket on a headful·idle thread
	AgentRunning     bool       `json:"agent_running"`
	LastActiveUnix   int64      `json:"last_active_unix"` // last pane change / turn completion
	// CwdRel is Cwd rendered ~-relative to the OWNING machine's home, stamped by
	// that machine's maintainer (the home is owner data the viewer cannot know). A
	// viewer applies its own [[cwd_label]] rules to this, so the CWD column labels
	// correctly even cross-machine — without it, a viewer with a different home
	// shows the raw absolute path. '' only if the owner's home is unknown.
	CwdRel string `json:"cwd_rel,omitempty"`
}

// MachineSnapshot is one machine's full live thread state, returned by
// GET /v1/snapshot — a pure read of the maintained state.
type MachineSnapshot struct {
	Schema          int              `json:"schema"`
	Machine         string           `json:"machine"`
	GeneratedAtUnix int64            `json:"generated_at_unix"`
	Threads         []ThreadSnapshot `json:"threads"`
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

// MetaThreadRequest sets ('' value = deletes) one meta key.
type MetaThreadRequest struct {
	ID    string `json:"id"`
	Key   string `json:"key"`
	Value string `json:"value"`
}
