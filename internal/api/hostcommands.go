package api

// Host commands (schema 74, capability host-commands-v1): named commands declared
// per machine in <SESH_HOME>/host-commands/*.toml, run by that machine's daemon on
// request. See internal/config/hostcommands.go for the manifest. Runs are
// asynchronous because every sesh hop (CLI client, /v1/route, the browser hub)
// has a 15 s HTTP timeout and a command may take minutes: POST
// /v1/host-commands/run starts one and returns its id; GET
// /v1/host-commands/run?id=&wait_ms= long-polls it to the end.

// HostCommandArg is one value a command accepts.
type HostCommandArg struct {
	Name        string `json:"name"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
	AllowDash   bool   `json:"allow_dash,omitempty"`
}

// HostCommand is one declared command. Argv is shown for transparency; it is
// never taken from a client.
type HostCommand struct {
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Argv        []string         `json:"argv"`
	TimeoutMs   int64            `json:"timeout_ms"`
	Args        []HostCommandArg `json:"args,omitempty"`
}

// HostCommandProvider is one manifest file; Name is its file stem.
type HostCommandProvider struct {
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Commands    []HostCommand `json:"commands"`
}

// HostCommandsResponse is GET /v1/host-commands: this daemon's providers.
type HostCommandsResponse struct {
	Schema    int                   `json:"schema"`
	Machine   string                `json:"machine"`
	Providers []HostCommandProvider `json:"providers"`
}

// HostCommandRunRequest is the POST /v1/host-commands/run body. RequestID is a
// caller-minted UUID: a start retried with the same id returns the original run
// instead of running the command twice. Args holds a value for each declared arg
// the caller supplies; an undeclared name or a missing required one is a 400.
type HostCommandRunRequest struct {
	Provider  string            `json:"provider"`
	Command   string            `json:"command"`
	Args      map[string]string `json:"args,omitempty"`
	RequestID string            `json:"request_id"`
}

// Host command run states.
const (
	HostCommandRunning = "running"
	HostCommandDone    = "done"
)

// HostCommandRun is a run's state: the POST's response (normally running) and
// the GET's. When State is done, ExitCode is the command's exit status (nonzero
// is the command's own failure, reported, not an HTTP error) unless Error is set:
// it could not be started, was killed at its timeout, or the daemon was
// shutting down — then ExitCode is -1. Each output stream is capped at 1 MiB;
// the truncation flags say whether the cap cut it.
type HostCommandRun struct {
	Schema          int    `json:"schema"`
	RunID           string `json:"run_id"`
	RequestID       string `json:"request_id"`
	Machine         string `json:"machine"`
	Provider        string `json:"provider"`
	Command         string `json:"command"`
	State           string `json:"state"`
	StartedAtUnix   int64  `json:"started_at_unix"`
	DurationMs      int64  `json:"duration_ms,omitempty"`
	ExitCode        int    `json:"exit_code"`
	Error           string `json:"error,omitempty"`
	Stdout          string `json:"stdout,omitempty"`
	Stderr          string `json:"stderr,omitempty"`
	StdoutTruncated bool   `json:"stdout_truncated,omitempty"`
	StderrTruncated bool   `json:"stderr_truncated,omitempty"`
	Replayed        bool   `json:"replayed,omitempty"`
}
