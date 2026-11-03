package api

// Thread CONTEXT (schema 60): a block of text the user attaches to one thread,
// which the thread's agent receives as standing instructions — independent of the
// thread's working directory (AGENTS.md / CLAUDE.md cover that).
//
// The text lives in the OWNING daemon's store. It reaches the agent through each
// harness's own extension point, asked fresh, so a change applies without a
// restart:
//
//   - claude: SessionStart + UserPromptSubmit hooks (passed with --settings on
//     every sesh launch) return it as additionalContext;
//   - codex: the same two hooks, passed with -c on every sesh launch and trusted
//     by the daemon in the codex config;
//   - pi / mypi: the sesh-agent-state extension puts it in the system prompt
//     before every turn.
//
// Claude and codex record an injection as a message in the conversation, so the
// owner remembers which version each agent session last received and injects
// again only when it changed (or when the history lost it: a new, cleared or
// compacted session). See POST /v1/threads/context/deliver.

// MaxThreadContextBytes bounds a thread's context. Claude Code inlines at most
// 10,000 characters of hook context and replaces anything longer with a 2 KB
// preview plus a file path (which the model then treats as untrusted), and codex's
// default hook-context budget is ~2,500 tokens; 8,000 bytes plus sesh's wrapper fits
// both, so a context that is accepted reaches every harness whole.
const MaxThreadContextBytes = 8000

// ThreadContextResponse is GET /v1/threads/context?id=<id>.
type ThreadContextResponse struct {
	Schema  int    `json:"schema"`
	ID      string `json:"id"`
	Machine string `json:"machine"`
	// Text is the context; empty = the thread has none.
	Text string `json:"text"`
	// Hash identifies this version of the text (empty when there is none). Pass it
	// back as SetThreadContextRequest.ExpectHash to refuse a lost update.
	Hash string `json:"hash,omitempty"`
	// UpdatedAtUnix is when the text was last set (0 when there is none).
	UpdatedAtUnix int64 `json:"updated_at_unix,omitempty"`
}

// SetThreadContextRequest is POST /v1/threads/context: set (Text) or remove
// (Clear) a thread's context. An empty Text without Clear is refused, so a blank
// value can never remove a context by accident.
type SetThreadContextRequest struct {
	ID    string `json:"id"`
	Text  string `json:"text,omitempty"`
	Clear bool   `json:"clear,omitempty"`
	// ExpectHash, when set, makes the write conditional: it is refused (409) unless
	// the thread's current context hash equals it. "none" expects no context. An
	// editor uses it so a context changed meanwhile is not silently overwritten.
	ExpectHash string `json:"expect_hash,omitempty"`
}

// SetThreadContextResponse is the reply to POST /v1/threads/context.
type SetThreadContextResponse struct {
	Schema int    `json:"schema"`
	Thread Thread `json:"thread"`
	// Hash is the new version's hash (empty after a clear).
	Hash string `json:"hash,omitempty"`
	// Changed is false when the write left the context as it was.
	Changed bool `json:"changed"`
	// Detail says when the agent will see the change.
	Detail string `json:"detail"`
}

// Hook events a context delivery is asked for.
const (
	// ContextEventSessionStart is the harness's SessionStart hook; Source says why.
	ContextEventSessionStart = "SessionStart"
	// ContextEventPrompt is the harness's UserPromptSubmit hook.
	ContextEventPrompt = "UserPromptSubmit"
	// ContextEventTurn is pi's before_agent_start: pi rebuilds its system prompt
	// for every run, so the context is returned every time.
	ContextEventTurn = "turn"
)

// ThreadContextDeliverRequest is POST /v1/threads/context/deliver: an agent's
// hook (on this machine) asking what context to inject now. The CLI has already
// established the thread with a VERIFIED identity (pane marker or headless-turn
// tree); the daemon additionally checks the session against the thread's record.
type ThreadContextDeliverRequest struct {
	ID    string `json:"id"`
	Agent string `json:"agent"`
	// SessionID is the agent's own session id, from the hook's input.
	SessionID string `json:"session_id"`
	Event     string `json:"event"`
	// Source is a SessionStart's reason: startup, resume, clear or compact.
	Source string `json:"source,omitempty"`
}

// ThreadContextDeliverResponse says what to inject. For claude/codex Inject is
// the whole additionalContext ("" = inject nothing). For pi, System is the
// system-prompt addendum for this run ("" = none) and Inject is a one-off notice
// that the context changed ("" = none).
type ThreadContextDeliverResponse struct {
	Schema int    `json:"schema"`
	Inject string `json:"inject,omitempty"`
	System string `json:"system,omitempty"`
	// Hash is the version delivered (empty when the thread has no context).
	Hash string `json:"hash,omitempty"`
	// Skipped explains an empty answer for a thread that HAS context (session
	// mismatch, already delivered) — for diagnostics; never shown to the agent.
	Skipped string `json:"skipped,omitempty"`
}
