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
