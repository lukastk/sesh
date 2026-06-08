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
	Headless      bool     `json:"headless"`
	CreatedAtUnix int64    `json:"created_at_unix"`
}

// RuntimeState is the live, polled (never stored) thread state.
type RuntimeState string

const (
	StateWorking  RuntimeState = "working"  // agent attached and mid-turn
	StateWaiting  RuntimeState = "waiting"  // agent live and idle (awaiting input)
	StateDetached RuntimeState = "detached" // live pane exists but no client attached
	StateDead     RuntimeState = "dead"     // no live pane bears the thread's marker
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

// ThreadStatusResponse is the live runtime status of a thread.
type ThreadStatusResponse struct {
	Schema       int          `json:"schema"`
	ID           string       `json:"id"`
	State        RuntimeState `json:"state"`
	Attached     bool         `json:"attached"`
	AgentRunning bool         `json:"agent_running"`
	Pane         string       `json:"pane,omitempty"`
}
