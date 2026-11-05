package api

// Thread ENV (schema 62): environment variables the user attaches to one thread.
//
// The values live in a 0600 file on the OWNING machine, never in the store: thread
// rows replicate to every peer and sesh.db is copied by backups, so a row carries
// only EnvCount and the values are fetched from the owner on demand. See
// agents/threadenv.go for the files and _dev/THREAD_ENV.md for what each harness
// was measured to do.
//
// Every launch and every headless turn starts with the thread's variables. A change
// also reaches a running agent's SHELL COMMANDS where the harness allows it: claude
// (a FileChanged hook) from the next command, pi (its extension sets process.env)
// from the next turn, and codex from the next command where the shell's startup
// sources the thread's env file (myrig's .zshenv does; `env set` asks the shell). On
// claude and codex the agent process itself and the MCP servers it already started
// keep the environment they launched with until `thread restart`.

// MaxThreadEnvBytes bounds a thread's variables, keys and values together. Linux
// refuses a single environment string over 128 KiB at exec, and a launch must not
// be able to fail that way.
const MaxThreadEnvBytes = 64 * 1024

// ThreadEnvResponse is GET /v1/threads/env?id=<id>.
type ThreadEnvResponse struct {
	Schema  int               `json:"schema"`
	ID      string            `json:"id"`
	Machine string            `json:"machine"`
	Vars    map[string]string `json:"vars"`
	// Hash identifies this set of variables (empty when there are none). Pass it
	// back as SetThreadEnvRequest.ExpectHash to refuse a lost update.
	Hash string `json:"hash,omitempty"`
}

// SetThreadEnvRequest is POST /v1/threads/env: Set then Unset applied to the
// thread's variables, or with Replace, Set becomes the whole set (empty = clear).
type SetThreadEnvRequest struct {
	ID      string            `json:"id"`
	Set     map[string]string `json:"set,omitempty"`
	Unset   []string          `json:"unset,omitempty"`
	Replace bool              `json:"replace,omitempty"`
	// ExpectHash, when set, makes the write conditional: refused (409) unless the
	// current hash equals it. "none" expects no variables.
	ExpectHash string `json:"expect_hash,omitempty"`
}

// How a change to a thread's variables reaches its agent.
const (
	// EnvLive: the running agent's shell commands pick it up on their own.
	EnvLive = "live"
	// EnvNextLaunch: nothing is running; the next launch or headless turn has it.
	EnvNextLaunch = "next-launch"
	// EnvRestartRequired: an agent is running that cannot pick it up.
	EnvRestartRequired = "restart-required"
)

// SetThreadEnvResponse is the reply to POST /v1/threads/env.
type SetThreadEnvResponse struct {
	Schema  int    `json:"schema"`
	Thread  Thread `json:"thread"`
	Hash    string `json:"hash,omitempty"`
	Changed bool   `json:"changed"`
	// Outcome is one of the Env* constants (absent when Changed is false).
	Outcome string `json:"outcome,omitempty"`
	// Detail says when the agent sees the change.
	Detail string `json:"detail"`
}

// RestartThreadRequest is POST /v1/threads/restart: replace the thread's running
// agent with a fresh process on the same conversation, in the same pane.
type RestartThreadRequest struct {
	ID string `json:"id"`
	// Force restarts a thread that is mid-turn, killing the turn in flight.
	Force bool `json:"force,omitempty"`
}

// RestartThreadResponse is the reply to POST /v1/threads/restart.
type RestartThreadResponse struct {
	Schema int    `json:"schema"`
	Thread Thread `json:"thread"`
	Detail string `json:"detail"`
}
