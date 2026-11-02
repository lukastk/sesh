package api

// Agent models (schema 58). A thread's REGISTERED model is Thread.Model — what
// sesh passes as --model on spawn, resume and every headless turn. Each harness
// also has an optional DEFAULT model, the `model` key of [spawn.<agent>] in the
// owning daemon's config.toml, given to (and recorded on) a thread created
// without --model.

// ModelDefaultsResponse is GET /v1/models/defaults and the reply to
// POST /v1/models/default: every agent's configured default (empty = none, the
// harness picks its own).
type ModelDefaultsResponse struct {
	Schema  int    `json:"schema"`
	Machine string `json:"machine"`
	// ConfigPath is the config.toml path the daemon reads (possibly a symlink).
	ConfigPath string `json:"config_path"`
	// LocalConfigPath is this machine's config.local.toml overlay, when present.
	// A default it sets wins over config.toml's (and set-default refuses to write
	// one it would shadow).
	LocalConfigPath string `json:"local_config_path,omitempty"`
	// WrotePath is the file a write actually changed — the symlink's target when
	// config.toml is a link. Set only on the POST reply.
	WrotePath string            `json:"wrote_path,omitempty"`
	Defaults  map[string]string `json:"defaults"`
}

// SetModelDefaultRequest is POST /v1/models/default. An empty Model removes the
// agent's default.
type SetModelDefaultRequest struct {
	Agent string `json:"agent"`
	Model string `json:"model"`
}

// ModelOption is one model a harness reports it can use on the owning machine.
type ModelOption struct {
	// ID is what --model accepts, in the harness's own spelling.
	ID          string `json:"id"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
	// Resolved is the concrete model an alias stands for (claude `opus` ->
	// `claude-opus-5-5`); empty when ID is already concrete.
	Resolved string `json:"resolved,omitempty"`
}

// ModelListResponse is GET /v1/models?agent=<a>: the models the harness itself
// reports on this machine (claude's SDK initialize handshake, `codex debug
// models`, `pi --list-models`). Sesh curates nothing.
type ModelListResponse struct {
	Schema  int    `json:"schema"`
	Machine string `json:"machine"`
	Agent   string `json:"agent"`
	// HarnessDefault is the model the harness runs given no --model, when it says
	// (claude does; codex and pi do not report one).
	HarnessDefault string        `json:"harness_default,omitempty"`
	Models         []ModelOption `json:"models"`
	// FetchedAtUnix is when the harness was asked; the daemon reuses a recent
	// answer unless the request says refresh=1.
	FetchedAtUnix int64 `json:"fetched_at_unix"`
}

// SetThreadModelRequest is POST /v1/threads/model: change a thread's registered
// model and, with Apply, the model its live agent is running.
type SetThreadModelRequest struct {
	ID    string `json:"id"`
	Model string `json:"model"`
	// Clear removes the registered model (the harness's default from then on).
	// Exclusive with Model and with Apply.
	Clear bool `json:"clear,omitempty"`
	// Apply also switches the live agent, if there is one: claude's pane is
	// restarted in place on the new model (its conversation resumes); codex is
	// sent `/model <m>` (session-only); pi/mypi switch over pi-rpc-socket
	// (session-only). With no live agent the record alone changes.
	Apply bool `json:"apply,omitempty"`
	// Force lets Apply act on a thread that is mid-turn. For claude that kills
	// the turn in flight.
	Force bool `json:"force,omitempty"`
}

// Outcomes of POST /v1/threads/model.
const (
	ModelRecorded  = "recorded"  // record only (no Apply)
	ModelNextTurn  = "next-turn" // Apply, but no live agent: the next spawn/resume/turn uses it
	ModelSwitched  = "switched"  // the live agent switched in-session (codex, pi)
	ModelRestarted = "restarted" // the live claude pane was restarted on the new model
)

type SetThreadModelResponse struct {
	Schema  int    `json:"schema"`
	Thread  Thread `json:"thread"`
	Outcome string `json:"outcome"`
	// Detail says what happened in words (what the agent confirmed, or why only
	// the record changed).
	Detail string `json:"detail"`
}
