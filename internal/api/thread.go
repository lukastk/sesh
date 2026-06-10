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

// Activity is the thread's INFERRED runtime state (headless/headful is not a
// stored mode — a thread is whatever its runtime currently is):
//
//	working — a turn is in progress: a live pane that is actively changing, OR a
//	          headless turn process in flight
//	waiting — a live pane, byte-stable (agent at its prompt, awaiting input)
//	idle    — NO runtime at all (no pane, no turn): the unified state formerly
//	          split into "dead" (was headed) and headless-between-turns. An idle
//	          thread is a durable conversation that accepts EITHER revival verb:
//	          resume/headful (a pane) or send-headless (one turn).
type Activity string

const (
	ActivityWorking Activity = "working"
	ActivityWaiting Activity = "waiting"
	ActivityIdle    Activity = "idle"
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
}

// ThreadResponse wraps a single thread.
type ThreadResponse struct {
	Schema int    `json:"schema"`
	Thread Thread `json:"thread"`
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

// ThreadStatusResponse is the live runtime status of a thread: the two
// orthogonal axes plus the raw signals they derive from.
type ThreadStatusResponse struct {
	Schema       int        `json:"schema"`
	ID           string     `json:"id"`
	Activity     Activity   `json:"activity"`
	Attachment   Attachment `json:"attachment"`
	AgentRunning bool       `json:"agent_running"`
	Clients      int        `json:"clients"`
	Pane         string     `json:"pane,omitempty"`
}

// NeedsInput is the derived "the human is blocking the agent" view: the agent is
// idle (waiting) regardless of whether anyone is currently attached.
func (s ThreadStatusResponse) NeedsInput() bool { return s.Activity == ActivityWaiting }

// ThreadRow is a thread plus its live runtime status — the unit the TUI grid
// renders. The status is computed live (never stored).
type ThreadRow struct {
	Thread
	Activity   Activity   `json:"activity"`
	Attachment Attachment `json:"attachment"`
}

// NeedsInput is the derived needs-input view for a row (activity == waiting).
func (r ThreadRow) NeedsInput() bool { return r.Activity == ActivityWaiting }

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
	Activity       Activity   `json:"activity"`
	Attachment     Attachment `json:"attachment"`
	AgentRunning   bool       `json:"agent_running"`
	LastActiveUnix int64      `json:"last_active_unix"` // last pane change / turn completion
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
