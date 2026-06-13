package api

// Tmux runtime model. The full address of any running process is
// (machine, socket, session, window, pane); these types carry it in JSON.

// TmuxPane is one pane and what is running in it.
type TmuxPane struct {
	Pane     string `json:"pane"`  // tmux pane id, e.g. "%3"
	Index    int    `json:"index"` // pane index within its window
	Active   bool   `json:"active"`
	PID      int    `json:"pid"`     // pane's foreground process group leader
	Command  string `json:"command"` // pane_current_command
	Title    string `json:"title"`
	Path     string `json:"path"`                // pane_current_path
	ThreadID string `json:"thread_id,omitempty"` // @sesh-thread-id marker, if any
}

// TmuxWindow is one window and its panes.
type TmuxWindow struct {
	Index  int        `json:"index"`
	Name   string     `json:"name"`
	Active bool       `json:"active"`
	Panes  []TmuxPane `json:"panes"`
}

// TmuxSession is one session and its windows.
type TmuxSession struct {
	Machine  string       `json:"machine"`
	Socket   string       `json:"socket"`
	Name     string       `json:"name"`
	Attached bool         `json:"attached"`
	Windows  []TmuxWindow `json:"windows"`
}

// TmuxInfoResponse is returned by GET /v1/tmux/info.
type TmuxInfoResponse struct {
	Schema   int           `json:"schema"`
	Sessions []TmuxSession `json:"sessions"`
}

// TmuxCurrentResponse is the resolved locator of the calling terminal
// (client-side; uses the caller's $TMUX/$TMUX_PANE).
type TmuxCurrentResponse struct {
	Schema   int    `json:"schema"`
	Machine  string `json:"machine"`
	Socket   string `json:"socket"`
	Session  string `json:"session"`
	Window   int    `json:"window"`
	Pane     string `json:"pane"`
	ThreadID string `json:"thread_id,omitempty"`
}

// CreateSessionRequest is the body of POST /v1/tmux/sessions.
type CreateSessionRequest struct {
	Name string            `json:"name"`
	Dir  string            `json:"dir,omitempty"`
	Env  map[string]string `json:"env,omitempty"`
}

// CreateSessionResponse is its reply.
type CreateSessionResponse struct {
	Schema  int    `json:"schema"`
	Session string `json:"session"`
}

// CreatePaneRequest is the body of POST /v1/tmux/panes. Target is a tmux target
// (session, window, or pane).
type CreatePaneRequest struct {
	Target string `json:"target"`
	Dir    string `json:"dir,omitempty"`
}

// CreatePaneResponse is its reply.
type CreatePaneResponse struct {
	Schema int    `json:"schema"`
	Pane   string `json:"pane"`
}

// SendTextRequest is the body of POST /v1/tmux/send-text.
type SendTextRequest struct {
	Target string `json:"target"`
	Text   string `json:"text"`
	// Enter, if true, sends a trailing Enter key after the text.
	Enter bool `json:"enter"`
}

// NavRequest is the body of POST /v1/tmux/nav: the INNER half of `tmux nav`
// performed in-process by the daemon that owns the work tmux server (so an http
// peer needs no ssh hop). Session is the target session; Origin is the machine
// whose master is navigating (selects that master's marker client — see
// tmux.MasterClientMarker). The outer master-window select stays on the caller.
type NavRequest struct {
	Session string `json:"session"`
	Origin  string `json:"origin"`
	// ThreadID, when set, makes the inner switch land on the WINDOW holding that
	// thread's @sesh-thread-id pane (resolved on the daemon's own work server), not
	// the session's last-active window. Empty = a plain session-level switch.
	ThreadID string `json:"thread_id,omitempty"`
	// Window, when non-nil, is an EXPLICIT window index to land on (prefix+L replaying a
	// recorded location) — it overrides ThreadID resolution. A POINTER (not an int) so a
	// pre-window client that omits it is unambiguously "unset" rather than window 0.
	Window *int `json:"window,omitempty"`
}

// MasterCurrentResponse returns what the daemon's work server is currently showing
// the origin master's window — GET /v1/tmux/master-current?origin=X. ThreadID is the
// active pane's thread ("" for a plain-shell pane); Session is that client's session
// name (for the prefix+a tmux-session preselect). Both "" means no live master client.
type MasterCurrentResponse struct {
	Schema   int    `json:"schema"`
	ThreadID string `json:"thread_id"`
	Session  string `json:"session"`
	// Window is the window index the marker client is currently on (-1 when there's
	// nothing to resolve). It lets the cockpit record a full (machine, session, window)
	// location for prefix+L's "last window" toggle, not just the session.
	Window int `json:"window"`
}

// StageFileRequest is the body of POST /v1/tmux/stage-file. Content is the raw
// file bytes (base64 via Go's encoding/json []byte handling).
type StageFileRequest struct {
	Name    string `json:"name"`
	Content []byte `json:"content"`
}

// StageFileResponse returns the staged path on the daemon's machine.
type StageFileResponse struct {
	Schema int    `json:"schema"`
	Path   string `json:"path"`
}
