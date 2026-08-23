package api

// Shell threads: the wire types for a tracked tmux SESSION. See _dev/SHELL.md.
//
// A shell thread reuses api.Thread wholesale — machine, session_name, cwd, name,
// tags, parent, meta, archived, on_hold_until, pin_order, flagged, notify — with
// agent_kind ShellAgentKind. Nothing about the record is new, which is why this
// feature needed no store migration. The conversation-only fields
// (agent_session_id, headless_started, model) are meaningless for it and are
// refused rather than silently accepted.

// NewShellRequest is the body of POST /v1/shells.
type NewShellRequest struct {
	// Cwd is the session's working directory — a shell thread's DURABLE content,
	// the way a transcript is an agent thread's. Reviving the thread recreates
	// the session here. Required.
	Cwd string `json:"cwd"`
	// Name is the thread name; empty = derived from cwd by the [[session_name]] /
	// cwd_label rules, exactly as an agent thread's session name is.
	Name string `json:"name,omitempty"`
	// SessionName overrides the tmux session name (default: derived from Name).
	// Purely descriptive — identity is the @sesh-shell-id marker.
	SessionName string `json:"session_name,omitempty"`
	// Parent is the parent thread id ('' = root).
	Parent string `json:"parent,omitempty"`
	// NoStart records the place WITHOUT starting a session: the thread is born
	// headless and `thread resume` creates its session later.
	NoStart bool `json:"no_start,omitempty"`
	// Idempotent (the `shell enter` flow): if a NON-ARCHIVED shell thread already
	// exists on this machine with the same (cwd, name), return IT instead of
	// creating a second one. Without this, a duplicate name in one cwd is refused.
	Idempotent bool `json:"idempotent,omitempty"`
}

// PromoteShellRequest is the body of POST /v1/shells/promote: adopt an EXISTING,
// untracked tmux session as a shell thread. The session must live on this
// daemon's work server and must not already carry a shell marker.
type PromoteShellRequest struct {
	// Session is the tmux session name to promote. Required.
	Session string `json:"session"`
	// Name is the thread name; empty = derived from the session's own name.
	Name string `json:"name,omitempty"`
	// Parent is the parent thread id ('' = root).
	Parent string `json:"parent,omitempty"`
}

// ShellSessionClass is what a live tmux session IS to sesh.
const (
	// ShellClassShell: carries a live @sesh-shell-id resolving to a record.
	ShellClassShell = "shell"
	// ShellClassAgent: hosts agent-thread panes but is not itself tracked.
	ShellClassAgent = "agent"
	// ShellClassGhost: no sesh identity at all — the promote target.
	ShellClassGhost = "ghost"
	// ShellClassStale: carries a marker whose record is GONE. Treated as a ghost
	// for promotion, but reported distinctly and logged, because it means a
	// delete failed to unstamp and that is a bug worth seeing.
	ShellClassStale = "stale"
)

// ShellSession is one live tmux session, classified.
type ShellSession struct {
	Machine  string `json:"machine"`
	Name     string `json:"name"`
	Path     string `json:"path,omitempty"` // the session's START dir (#{session_path})
	Attached bool   `json:"attached"`
	Windows  int    `json:"windows"`
	Panes    int    `json:"panes"`
	Class    string `json:"class"`
	// ThreadID is the shell thread this session is the runtime of (class shell),
	// or the stale marker (class stale).
	ThreadID string `json:"thread_id,omitempty"`
	// AgentThreads are the ids of agent threads whose panes live in this session.
	AgentThreads []string `json:"agent_threads,omitempty"`
}

// ShellSessionsResponse is returned by GET /v1/shells/sessions.
type ShellSessionsResponse struct {
	Schema   int            `json:"schema"`
	Machine  string         `json:"machine"`
	Sessions []ShellSession `json:"sessions"`
}

// ShellInfoResponse is returned by GET /v1/shells/info?id=... — the shell
// thread's locator plus its live window/pane tree. It carries everything needed
// to drop to RAW TMUX for anything sesh does not wrap: sesh must not reimplement
// tmux, so this is the documented escape hatch.
type ShellInfoResponse struct {
	Schema  int    `json:"schema"`
	ID      string `json:"id"`
	Machine string `json:"machine"`
	Name    string `json:"name"`
	Cwd     string `json:"cwd"`
	// Session/Attached/Windows describe the LIVE session; Live is false when the
	// shell thread is headless (a remembered place with no session right now).
	Live     bool   `json:"live"`
	Session  string `json:"session,omitempty"`
	Attached bool   `json:"attached,omitempty"`
	Socket   string `json:"socket"`
	// SocketPath is the tmux socket's filesystem path on Machine.
	SocketPath string `json:"socket_path"`
	// TmuxPrefix is a ready-to-paste command prefix for raw tmux against this
	// session's server, e.g. "tmux -L sesh" locally or
	// "ssh-target macbook tmux -L sesh" for a peer.
	TmuxPrefix string       `json:"tmux_prefix"`
	Windows    []TmuxWindow `json:"windows,omitempty"`
}
