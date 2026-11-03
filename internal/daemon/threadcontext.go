package daemon

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/store"
)

// Thread context (schema 60, api/context.go): the owner stores the text, and the
// thread's agent asks for it from its own hooks — claude/codex SessionStart and
// UserPromptSubmit, pi's before_agent_start — through POST
// /v1/threads/context/deliver. Asking at the hook, rather than passing the text
// at launch, is what makes a change apply to a running agent: claude and codex
// freeze launch-time instructions into the session at creation and ignore them on
// resume (claude also drops them at compaction). Claude also gets the text as an
// instruction file, the one form a rewind cannot remove (agents/contexthook.go).

func (d *Daemon) routesThreadContext(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/threads/context", d.handleThreadContextGet)
	mux.HandleFunc("POST /v1/threads/context", d.handleThreadContextSet)
	mux.HandleFunc("POST /v1/threads/context/deliver", d.handleThreadContextDeliver)
}

// contextCapable reports whether a thread kind runs an agent that can receive a
// context: every agent kind. Shell, virtual and divider threads run none.
func contextCapable(kind string) bool {
	_, err := agents.ParseKind(kind)
	return err == nil
}

func (d *Daemon) handleThreadContextGet(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "context: id is required")
		return
	}
	text, updated, err := d.store.GetThreadContext(id)
	if err != nil {
		writeStoreThreadError(w, id, err)
		return
	}
	writeJSON(w, http.StatusOK, api.ThreadContextResponse{Schema: api.SchemaVersion, ID: id, Machine: d.cfg.Machine,
		Text: text, Hash: store.ContextHashOf(text), UpdatedAtUnix: updated})
}

func writeStoreThreadError(w http.ResponseWriter, id string, err error) {
	if errors.Is(err, store.ErrThreadNotFound) {
		writeError(w, http.StatusNotFound, "thread not found: "+id)
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}

// validateContextText refuses a context the harnesses could not receive whole.
func validateContextText(text string) error {
	switch {
	case !utf8.ValidString(text):
		return errors.New("context: the text is not valid UTF-8")
	case strings.ContainsRune(text, 0):
		return errors.New("context: the text contains a NUL byte")
	case len(text) > api.MaxThreadContextBytes:
		return fmt.Errorf("context: the text is %d bytes; the limit is %d (Claude Code inlines at most 10,000 characters of hook context and replaces longer context with a preview the model distrusts, and codex's default hook budget is ~2,500 tokens)", len(text), api.MaxThreadContextBytes)
	}
	return nil
}

func (d *Daemon) handleThreadContextSet(w http.ResponseWriter, r *http.Request) {
	var req api.SetThreadContextRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch {
	case req.ID == "":
		writeError(w, http.StatusBadRequest, "context: id is required")
		return
	case req.Clear && req.Text != "":
		writeError(w, http.StatusBadRequest, "context: text and clear are exclusive")
		return
	case !req.Clear && strings.TrimSpace(req.Text) == "":
		writeError(w, http.StatusBadRequest, "context: the text is empty — to remove the thread's context use `sesh context clear`")
		return
	}
	if err := validateContextText(req.Text); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	th, err := d.store.GetThread(req.ID)
	if err != nil {
		writeStoreThreadError(w, req.ID, err)
		return
	}
	if !contextCapable(th.AgentKind) && !req.Clear {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("context: thread %s is a %s thread, which runs no agent to receive a context", req.ID, th.AgentKind))
		return
	}
	changed, err := d.store.SetThreadContext(req.ID, req.Text, time.Now().Unix(), req.ExpectHash)
	if err != nil {
		if errors.Is(err, store.ErrContextConflict) {
			writeError(w, http.StatusConflict, "context: the thread's context changed since it was read (someone else saved it) — reload it and edit again")
			return
		}
		writeStoreThreadError(w, req.ID, err)
		return
	}
	th, err = d.store.GetThread(req.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if agents.Family(agents.Kind(th.AgentKind)) == agents.Claude {
		if _, err := d.writeContextFile(req.ID); err != nil {
			writeError(w, http.StatusInternalServerError, "context: stored, but the thread's context file was not updated: "+err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, api.SetThreadContextResponse{Schema: api.SchemaVersion, Thread: th, Hash: th.ContextHash,
		Changed: changed, Detail: contextDetail(th, req.Clear, changed)})
}

// writeContextFile renders thread id's claude context file from the store and
// returns its folder. Called on every change and before every claude launch, so
// the copy is rebuilt if it is missing or stale. A thread being created has no
// record yet, and so no context: an empty file.
func (d *Daemon) writeContextFile(id string) (string, error) {
	text, _, err := d.store.GetThreadContext(id)
	if err != nil && !errors.Is(err, store.ErrThreadNotFound) {
		return "", err
	}
	content := ""
	if text != "" {
		content = renderContext(id, store.ContextHashOf(text), text)
	}
	return agents.WriteContextFile(d.cfg.Home, id, content)
}

func contextDetail(th api.Thread, cleared, changed bool) string {
	if !changed {
		return "unchanged"
	}
	what := "set"
	if cleared {
		what = "removed"
	}
	if agents.Family(agents.Kind(th.AgentKind)) == agents.Pi {
		return what + "; the agent sees it from its next turn"
	}
	return what + "; the agent is told at its next prompt (or when it next starts)"
}

func (d *Daemon) handleThreadContextDeliver(w http.ResponseWriter, r *http.Request) {
	var req api.ThreadContextDeliverRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == "" || req.Agent == "" || req.Event == "" {
		writeError(w, http.StatusBadRequest, "context deliver: id, agent and event are required")
		return
	}
	kind, err := agents.ParseKind(req.Agent)
	if err != nil {
		writeError(w, http.StatusBadRequest, "context deliver: "+err.Error())
		return
	}
	resp := api.ThreadContextDeliverResponse{Schema: api.SchemaVersion}
	th, err := d.store.GetThread(req.ID)
	if errors.Is(err, store.ErrThreadNotFound) {
		// A pane marker naming a deleted thread: no thread, so no context — not a
		// hook error on every prompt of the agent still running in that pane.
		resp.Skipped = "no thread " + req.ID + " on this machine"
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if agents.Family(agents.Kind(th.AgentKind)) != agents.Family(kind) {
		resp.Skipped = fmt.Sprintf("the hook is %s's but thread %s runs %s", kind, th.ID, th.AgentKind)
		writeJSON(w, http.StatusOK, resp)
		return
	}
	// Serialise deliveries per daemon: a hook pair (SessionStart then
	// UserPromptSubmit) can arrive back to back, and each read-decide-record must
	// see the previous one's record.
	d.contextMu.Lock()
	defer d.contextMu.Unlock()
	text, _, err := d.store.GetThreadContext(th.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	dSess, dHash, err := d.store.ContextDelivery(th.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	in := deliveryInput{
		threadID: th.ID, family: agents.Family(kind), event: req.Event, source: req.Source,
		contextFile: req.ContextFile, headless: d.turnInFlight(th.ID),
		session: req.SessionID, recorded: th.AgentSessionID,
		deliveredSession: dSess, deliveredHash: dHash, text: text,
		leafOf: func() string {
			leaf, err := agents.ResolveCurrentSession(agents.Kind(th.AgentKind), th.AgentSessionID, th.Cwd, agents.ResolveHomes(d.cfg.CodexHome))
			if err != nil {
				return ""
			}
			return leaf
		},
	}
	out, err := decideDelivery(in)
	if err != nil {
		writeError(w, http.StatusBadRequest, "context deliver: "+err.Error())
		return
	}
	if out.record {
		if err := d.store.SetContextDelivery(th.ID, req.SessionID, out.hash); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if out.skipped != "" && text != "" {
		log.Printf("context: thread %s %s/%s session %s: not delivered — %s", th.ID, req.Event, req.Source, req.SessionID, out.skipped)
	}
	resp.Inject, resp.System, resp.Hash, resp.Skipped = out.inject, out.system, out.hash, out.skipped
	writeJSON(w, http.StatusOK, resp)
}

// deliveryInput is everything decideDelivery needs, gathered by the handler.
type deliveryInput struct {
	threadID string
	family   agents.Kind
	event    string
	source   string
	// contextFile: a claude launched with its context file, which claude itself
	// reloads at every session start.
	contextFile bool
	// headless: a headless turn is running. Each is a new process, so its codex
	// SessionStart(resume) is that turn starting, not a restart or a rewind.
	headless bool
	// session is the hook's agent session id; recorded the thread record's.
	session  string
	recorded string
	// deliveredSession/deliveredHash: which session last received which version.
	deliveredSession string
	deliveredHash    string
	text             string
	// leafOf resolves the recorded session's live leaf (claude's compaction
	// chain); asked only when the hook's session differs from the record.
	leafOf func() string
}

type deliveryDecision struct {
	inject, system string
	hash           string
	// record = write (session, hash) as the delivered state.
	record  bool
	skipped string
}

// decideDelivery is the delivery rule, kept pure so every branch is unit-tested.
//
// WHO may receive it: the hook's session must be the thread's own agent session —
// the recorded one, its claude compaction leaf, one this thread already served,
// or one a SessionStart(clear) just began. Anything else is another agent RUN
// INSIDE the thread (an agent's bash calling `claude -p`), which inherits the
// thread's pane and identity but is not the thread; it gets nothing. A codex
// thread before its first turn has no recorded session yet and is accepted.
//
// WHAT it receives:
//   - pi: the current context as the system-prompt addendum on every run, plus a
//     one-off notice in the run when the context changed within this session;
//   - claude/codex: the conversation keeps every injection, so a version is sent
//     once per session — at a SessionStart that begins a fresh history (startup,
//     clear, compact), or at the first hook of a session it has not reached, or
//     when it changed since that session last received it (a removal is told as
//     a notice);
//   - claude launched with its context file: claude loads that file itself at
//     every session start and keeps it through a rewind, so a SessionStart that
//     begins a history sends nothing and only records what the session now has.
//     The hook carries changes to the running process;
//   - headed codex: a rewind removes the injection, and fires SessionStart(resume),
//     so that event always re-sends. A restart fires it too, adding one copy.
func decideDelivery(in deliveryInput) (deliveryDecision, error) {
	var out deliveryDecision
	if in.session == "" {
		return out, errors.New("the hook carried no agent session id")
	}
	switch in.event {
	case api.ContextEventSessionStart, api.ContextEventPrompt:
		if in.family == agents.Pi {
			return out, fmt.Errorf("event %q is not a pi event (pi asks with %q)", in.event, api.ContextEventTurn)
		}
	case api.ContextEventTurn:
		if in.family != agents.Pi {
			return out, fmt.Errorf("event %q is pi's; %s asks with %s or %s", in.event, in.family, api.ContextEventSessionStart, api.ContextEventPrompt)
		}
	default:
		return out, fmt.Errorf("unknown event %q", in.event)
	}
	cur := store.ContextHashOf(in.text)
	fresh := in.event == api.ContextEventSessionStart && (in.source == "startup" || in.source == "clear" || in.source == "compact")
	loadsFile := in.family == agents.Claude && in.contextFile && in.event == api.ContextEventSessionStart
	codexRewind := in.family == agents.Codex && !in.headless && in.event == api.ContextEventSessionStart && in.source == "resume" && in.text != ""
	accepted := in.recorded == "" || in.session == in.recorded || in.session == in.deliveredSession ||
		(in.event == api.ContextEventSessionStart && in.source == "clear")
	if !accepted && in.leafOf != nil && in.session == in.leafOf() {
		accepted = true
	}
	if !accepted {
		out.skipped = fmt.Sprintf("session %s is not thread %s's agent session (%s) — another agent run inside the thread", in.session, in.threadID, in.recorded)
		return out, nil
	}
	out.hash = cur
	same := in.session == in.deliveredSession
	out.record = !same || in.deliveredHash != cur

	if in.family == agents.Pi {
		// pi wraps a system-prompt section in its own <sesh_thread_context> tag.
		if cur != "" {
			out.system = renderContextBody(in.threadID, cur, in.text)
		}
		if same && in.deliveredHash != cur {
			if cur != "" {
				out.inject = renderContext(in.threadID, cur, in.text)
			} else {
				out.inject = renderCleared(in.threadID)
			}
		}
		return out, nil
	}

	switch {
	case codexRewind:
		out.inject = renderContext(in.threadID, cur, in.text)
	case loadsFile && (fresh || !same):
		// The context file gives this history the current version.
	case fresh:
		// A history that holds no injection: give it the current version (if any).
		if cur != "" {
			out.inject = renderContext(in.threadID, cur, in.text)
		}
	case same && in.deliveredHash == cur:
		if cur != "" {
			out.skipped = "this version was already delivered to this session"
		}
	case same:
		// Changed since this session last received it.
		if cur != "" {
			out.inject = renderContext(in.threadID, cur, in.text)
		} else {
			out.inject = renderCleared(in.threadID)
		}
	default:
		// A session this thread has not served (a resumed fork, a new leaf).
		if cur != "" {
			out.inject = renderContext(in.threadID, cur, in.text)
		} else {
			out.record = false // nothing to say and nothing to remember: no write per prompt
		}
	}
	return out, nil
}

// renderContext wraps the user's text so the agent knows what it is and that it
// supersedes any earlier version still in its history.
func renderContext(threadID, hash, text string) string {
	return fmt.Sprintf("<sesh-thread-context thread=%q version=%q>\n%s\n</sesh-thread-context>", threadID, hash, renderContextBody(threadID, hash, text))
}

// renderContextBody is the wrapped text without the outer tag (pi's system-prompt
// section supplies its own).
func renderContextBody(threadID, hash, text string) string {
	return fmt.Sprintf("The user attached this context to sesh thread %s (`sesh context`, version %s). Treat it as standing instructions from the user for this thread. It replaces any earlier sesh thread context in this conversation.\n\n%s",
		threadID, hash, strings.TrimRight(text, "\n"))
}

// renderCleared tells the agent a context it was given no longer applies.
func renderCleared(threadID string) string {
	return fmt.Sprintf("<sesh-thread-context thread=%q removed=\"true\">\n"+
		"The user removed this sesh thread's custom context. Disregard any earlier sesh thread context in this conversation.\n"+
		"</sesh-thread-context>", threadID)
}
