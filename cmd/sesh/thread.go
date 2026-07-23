package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/client"
	"github.com/lukastk/sesh/internal/config"
)

// absCwd expands a --cwd value to an absolute path relative to the process's
// working directory — i.e. WHERE the command was invoked — so a relative path
// just works (the daemon requires absolute). Empty stays empty (the caller's own
// required-check handles that).
//
// A leading ~ is left UNTOUCHED: ~ means "the OWNER machine's home", which only
// the owning daemon can resolve correctly (it expands ~ against its own home).
// Resolving it here would bake in the LOCAL home, which is wrong for a
// cross-machine (--machine) spawn — the exact bug a ~-relative cwd is meant to
// avoid. So a ~-relative cwd is portable across machines; a bare relative path
// (./x) still expands locally because it only has meaning where you typed it.
func absCwd(cwd string) (string, error) {
	if cwd == "" {
		return "", nil
	}
	if cwd == "~" || strings.HasPrefix(cwd, "~/") {
		return cwd, nil
	}
	return filepath.Abs(cwd)
}

// runThread implements `sesh thread <new|list|kill|pane>`. Mutations route to
// the local daemon (this machine is the thread's owner).
func runThread(args []string) error {
	if len(args) == 0 {
		return printGroupHelp("thread")
	}
	cfg := config.Load()
	sub, rest := args[0], args[1:]
	switch sub {
	case "new":
		return threadNew(cfg, rest)
	case "list":
		return threadList(cfg, rest)
	case "stop":
		return threadStop(cfg, rest)
	case "pane":
		return threadPane(cfg, rest)
	case "capture":
		return threadCapture(cfg, rest)
	case "status":
		return threadStatus(cfg, rest)
	case "send":
		return threadSend(cfg, rest)
	case "send-headless":
		return threadSendHeadless(cfg, rest)
	case "headless-reply":
		return threadHeadlessReply(cfg, rest)
	case "rename":
		return threadRename(cfg, rest)
	case "info":
		return runInfo(cfg, rest)
	case "adopt":
		return threadAdopt(cfg, rest)
	case "transcript":
		return threadTranscript(cfg, rest)
	case "notify":
		return threadNotify(cfg, rest)
	case "report-state":
		return threadReportState(cfg, rest)
	case "wait":
		return threadWait(cfg, rest)
	case "flag":
		return threadFlag(cfg, rest)
	case "hold":
		return threadHold(cfg, rest)
	case "pin":
		return threadPin(cfg, rest)
	case "unpin":
		return threadUnpin(cfg, rest)
	case "reparent":
		return threadReparent(cfg, rest)
	case "tag":
		return threadTag(cfg, rest)
	case "archive":
		return threadArchive(cfg, rest)
	case "delete":
		return threadDelete(cfg, rest)
	case "resume":
		return threadResume(cfg, rest)
	case "headful":
		return threadHeadful(cfg, rest)
	case "realize":
		return threadRealize(cfg, rest)
	case "grid":
		return threadGrid(cfg, rest)
	case "snapshot":
		return threadSnapshot(cfg, rest)
	default:
		return fmt.Errorf("unknown thread subcommand %q", sub)
	}
}

func threadRename(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("rename", flag.ContinueOnError)
	id := fs.String("id", "", "thread id (required)")
	name := fs.String("name", "", "new name (required; pass --name '' to clear the name / a divider's label)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// --name must be PROVIDED, but an explicit empty value is allowed (rename to
	// nameless / clear a divider's label) — so check presence, not emptiness.
	provided := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "name" {
			provided = true
		}
	})
	if !provided {
		return errors.New("thread rename: --name is required (pass --name '' to clear the name)")
	}
	rid, err := resolveIDFlag(cfg, fs, id)
	if err != nil {
		return err
	}
	*id = rid
	c := daemonClient(cfg)
	if err := c.ThreadRename(context.Background(), *id, *name); err != nil {
		return err
	}
	fmt.Printf("renamed %s -> %s\n", *id, *name)
	return nil
}

func threadTag(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("tag", flag.ContinueOnError)
	id := fs.String("id", "", "thread id (required)")
	var add, remove multiFlag
	fs.Var(&add, "add", "tag to add (repeatable)")
	fs.Var(&remove, "remove", "tag to remove (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(add) == 0 && len(remove) == 0 {
		return errors.New("thread tag: at least one --add/--remove required")
	}
	rid, err := resolveIDFlag(cfg, fs, id)
	if err != nil {
		return err
	}
	*id = rid
	c := daemonClient(cfg)
	if err := c.ThreadTag(context.Background(), *id, add, remove); err != nil {
		return err
	}
	fmt.Println("tagged", *id)
	return nil
}

func threadArchive(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("archive", flag.ContinueOnError)
	id := fs.String("id", "", "thread id (required)")
	unarchive := fs.Bool("unarchive", false, "unarchive instead")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rid, err := resolveIDFlag(cfg, fs, id)
	if err != nil {
		return err
	}
	*id = rid
	c := daemonClient(cfg)
	if err := c.ThreadArchive(context.Background(), *id, !*unarchive); err != nil {
		return err
	}
	if *unarchive {
		fmt.Println("unarchived", *id)
	} else {
		fmt.Println("archived", *id)
	}
	return nil
}

func threadDelete(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("delete", flag.ContinueOnError)
	id := fs.String("id", "", "thread id (required)")
	force := fs.Bool("force", false, "delete even if the runtime is live (orphans the agent)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return errors.New("thread delete: --id is required")
	}
	c := daemonClient(cfg)
	// Resolve an id PREFIX to the full uuid, like every other verb (the daemon's
	// delete is an exact-match lookup, so a bare prefix would 404). delete uses
	// resolveIDPrefix, NOT resolveThreadID: it must never INFER the current thread
	// from $SESH_THREAD_ID / the calling pane (destructive + ambient = footgun) —
	// an explicit prefix is fine, an omitted --id is the loud error above.
	rid, err := resolveIDPrefix(c, *id)
	if err != nil {
		return err
	}
	if err := c.ThreadDelete(context.Background(), rid, *force); err != nil {
		return err
	}
	fmt.Println("deleted", rid)
	return nil
}

// threadResume revives a dead headed thread. Exposed both as `sesh thread resume`
// and the top-level `sesh resume`.
func threadResume(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	id := fs.String("id", "", "thread id (required)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// Accept a positional id too (`sesh resume <id>`); reject an explicit-empty
	// --id / positional, infer the current thread only when both are omitted.
	rid, err := resolveIDOrPositional(cfg, fs)
	if err != nil {
		return err
	}
	*id = rid
	c := daemonClient(cfg)
	resp, err := c.ThreadResume(context.Background(), *id)
	if err != nil {
		return err
	}
	if *asJSON {
		return emitJSON(resp.Thread)
	}
	fmt.Printf("resumed %s (%s)\n", resp.Thread.ID, resp.Thread.SessionName)
	return nil
}

// threadHeadful promotes a live headless thread into a headed tmux pane (resuming its
// conversation). A turn in flight is rejected (409); a codex thread with no first turn
// yet is an explicit N/A.
func threadHeadful(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("headful", flag.ContinueOnError)
	id := fs.String("id", "", "thread id (required)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// Accept a positional id too (`sesh headful <id>`); reject an explicit-empty
	// --id / positional, infer the current thread only when both are omitted.
	rid, err := resolveIDOrPositional(cfg, fs)
	if err != nil {
		return err
	}
	*id = rid
	c := daemonClient(cfg)
	resp, err := c.ThreadHeadful(context.Background(), *id)
	if err != nil {
		return err
	}
	if *asJSON {
		return emitJSON(resp.Thread)
	}
	fmt.Printf("promoted %s to headed (%s)\n", resp.Thread.ID, resp.Thread.SessionName)
	return nil
}

// threadRealize converts a VIRTUAL grouping thread in place into a real,
// never-started headless thread (id/children/tags/holds preserved); enter it or
// send-headless afterwards to start the conversation. Explicit --id only (a
// prefix resolves; realize never infers the current thread — no agent ever runs
// inside a virtual thread, so an inferred id could only be a mistake).
func threadRealize(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("realize", flag.ContinueOnError)
	id := fs.String("id", "", "virtual thread id/prefix (required)")
	agent := fs.String("agent", "", "agent to realize as: claude|codex|pi (required)")
	cwd := fs.String("cwd", "", "start directory (default: the cwd stored at creation; required if none was)")
	model := fs.String("model", "", "agent model to pin to the thread (opaque pass-through; empty = the agent's default)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return errors.New("thread realize: --id is required")
	}
	if *agent == "" {
		return errors.New("thread realize: --agent is required")
	}
	if *cwd != "" {
		// Relative --cwd expands against the invocation dir; a leading ~ passes
		// through for the owner daemon to resolve (portable cross-machine).
		abs, err := absCwd(*cwd)
		if err != nil {
			return fmt.Errorf("thread realize: --cwd: %w", err)
		}
		*cwd = abs
	}
	c := daemonClient(cfg)
	rid, err := resolveIDPrefix(c, *id)
	if err != nil {
		return err
	}
	resp, err := c.ThreadRealize(context.Background(), api.RealizeThreadRequest{
		ID: rid, Agent: *agent, Cwd: *cwd, Model: *model,
	})
	if err != nil {
		return err
	}
	if *asJSON {
		return emitJSON(resp.Thread)
	}
	fmt.Printf("realized %s as %s (enter it or send-headless to start the conversation)\n", resp.Thread.ID, resp.Thread.AgentKind)
	return nil
}

func threadGrid(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("grid", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON (one row per line)")
	archived := fs.Bool("archived", false, "include archived threads")
	allMachines := fs.Bool("all-machines", false, "fan out across the mesh")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c := daemonClient(cfg)
	resp, err := c.ThreadGrid(context.Background(), *archived, *allMachines)
	if err != nil {
		return err
	}
	for _, m := range resp.Unreachable {
		fmt.Fprintf(os.Stderr, "warning: peer %s unreachable\n", m)
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		for _, row := range resp.Rows {
			if err := enc.Encode(row); err != nil {
				return err
			}
		}
		return nil
	}
	for _, row := range resp.Rows {
		fmt.Printf("%s\t%s\t%s\t%s\t%s\t%s\t%s\n", row.Head, row.Busy, row.Attachment, row.Machine, row.AgentKind, row.Name, row.ID)
	}
	return nil
}

func threadSnapshot(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("snapshot", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON (one row per line)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c := daemonClient(cfg)
	snap, err := c.Snapshot(context.Background())
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		for _, row := range snap.Threads {
			if err := enc.Encode(row); err != nil {
				return err
			}
		}
		return nil
	}
	for _, row := range snap.Threads {
		fmt.Printf("%s\t%s\t%s\t%s\t%s\t%s\n", row.Head, row.Busy, row.Attachment, row.AgentKind, row.Name, row.ID)
	}
	return nil
}

// multiFlag collects a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

func threadSendHeadless(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("send-headless", flag.ContinueOnError)
	id := fs.String("id", "", "thread id (required)")
	text := fs.String("text", "", "turn text (required)")
	model := fs.String("model", "", "override the thread's pinned model for THIS turn only (opaque pass-through; empty = the thread's model / agent default)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *text == "" {
		return errors.New("thread send-headless: --text is required")
	}
	rid, err := resolveIDFlag(cfg, fs, id)
	if err != nil {
		return err
	}
	*id = rid
	c := daemonClient(cfg)
	if err := c.ThreadSendHeadlessModel(context.Background(), *id, *text, *model); err != nil {
		return err
	}
	fmt.Println("turn started for", *id)
	return nil
}

func threadHeadlessReply(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("headless-reply", flag.ContinueOnError)
	id := fs.String("id", "", "thread id (required)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rid, err := resolveIDFlag(cfg, fs, id)
	if err != nil {
		return err
	}
	*id = rid
	c := daemonClient(cfg)
	resp, err := c.ThreadHeadlessReply(context.Background(), *id)
	if err != nil {
		return err
	}
	if *asJSON {
		return emitJSON(resp)
	}
	fmt.Printf("working=%t have_reply=%t\n", resp.Working, resp.HaveReply)
	if resp.HaveReply {
		fmt.Println(resp.Reply)
	}
	return nil
}

func threadSend(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	id := fs.String("id", "", "thread id (required)")
	text := fs.String("text", "", "message text (required)")
	wait := fs.Bool("wait", false, "block until the turn settles (idle or blocked)")
	timeout := fs.Duration("timeout", 0, "overall deadline for --wait (required with --wait)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *text == "" {
		return errors.New("thread send: --text is required")
	}
	if *wait && *timeout <= 0 {
		return errors.New("thread send: --wait requires --timeout")
	}
	if !*wait && *timeout != 0 {
		return errors.New("thread send: --timeout only applies with --wait")
	}
	rid, err := resolveIDFlag(cfg, fs, id)
	if err != nil {
		return err
	}
	*id = rid
	c := daemonClient(cfg)
	// For --wait's stall guard: the pre-send activity marker. Read BEFORE the
	// send so a delivered keystroke's pane change is observable as progress.
	var preActive int64
	preBusy := false
	if *wait {
		pre, werr := c.ThreadWait(context.Background(), rid, "busy", 0)
		if werr != nil {
			return werr
		}
		preActive, preBusy = pre.LastActiveUnix, pre.Reached
	}
	if err := c.ThreadSend(context.Background(), *id, *text); err != nil {
		return err
	}
	if !*wait {
		fmt.Println("sent", *id)
		return nil
	}
	// STALL GUARD (herdr's agent_prompt_stalled): a send from a non-busy state
	// must produce SOME observed change within 5s — a busy latch, or at least
	// the delivered keystrokes changing the pane (LastActiveUnix advancing).
	// Neither = the input likely never registered; failing fast beats waiting
	// out the whole timeout against a wedged pane. (From an already-busy
	// state the guard is skipped — completion of the ACTIVE turn may satisfy
	// the wait, same caveat as herdr.)
	if !preBusy {
		stalled := true
		stallDeadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(stallDeadline) {
			resp, werr := c.ThreadWait(context.Background(), rid, "busy",
				int(time.Until(stallDeadline).Milliseconds()))
			if werr != nil {
				return werr
			}
			if resp.Reached || resp.LastActiveUnix > preActive {
				stalled = false
				break
			}
		}
		if stalled {
			return fmt.Errorf("thread send: no state change within 5s of delivery — the input may not have registered (agent wedged, modal open?); thread %s still idle", rid)
		}
	}
	final, err := waitLoop(c, rid, "settled", *timeout)
	if err != nil {
		return err
	}
	state := string(final.Busy)
	if final.Blocked {
		state = "blocked"
	}
	fmt.Printf("sent %s; settled: %s\n", rid, state)
	return nil
}

// waitLoop drives bounded server-side waits until the condition or deadline.
func waitLoop(c *client.Client, id, until string, timeout time.Duration) (api.ThreadWaitResponse, error) {
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			resp, _ := c.ThreadWait(context.Background(), id, until, 0)
			state := string(resp.Busy)
			if resp.Blocked {
				state = "blocked"
			}
			return resp, fmt.Errorf("wait: thread %s did not reach %s within %s (last state: %s)", id, until, timeout, state)
		}
		resp, err := c.ThreadWait(context.Background(), id, until, int(remaining.Milliseconds()))
		if err != nil {
			return resp, err
		}
		if resp.Reached {
			return resp, nil
		}
	}
}

// threadWait blocks until a thread reaches a state (server-owned bounded
// polls under the hood; one routed hop for --machine).
func threadWait(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("wait", flag.ContinueOnError)
	id := fs.String("id", "", "thread id/prefix (default: the current thread)")
	until := fs.String("until", "", "target state: busy | idle | blocked | settled (required)")
	timeout := fs.Duration("timeout", 0, "overall deadline (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *until == "" {
		return errors.New("thread wait: --until is required")
	}
	if *timeout <= 0 {
		return errors.New("thread wait: --timeout is required")
	}
	rid, err := resolveIDFlag(cfg, fs, id)
	if err != nil {
		return err
	}
	final, err := waitLoop(daemonClient(cfg), rid, *until, *timeout)
	if err != nil {
		return err
	}
	state := string(final.Busy)
	if final.Blocked {
		state = "blocked"
	}
	fmt.Printf("%s reached %s (state: %s)\n", rid, *until, state)
	return nil
}

func threadStatus(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	id := fs.String("id", "", "thread id (required)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rid, err := resolveIDFlag(cfg, fs, id)
	if err != nil {
		return err
	}
	*id = rid
	c := daemonClient(cfg)
	resp, err := c.ThreadStatus(context.Background(), *id)
	if err != nil {
		return err
	}
	if *asJSON {
		return emitJSON(resp)
	}
	fmt.Printf("head:          %s\n", resp.Head)
	fmt.Printf("busy:          %s\n", resp.Busy)
	fmt.Printf("attachment:    %s (%d clients)\n", resp.Attachment, resp.Clients)
	fmt.Printf("agent_running: %t\n", resp.AgentRunning)
	fmt.Printf("needs_input:   %t\n", resp.NeedsInput())
	fmt.Printf("pane:          %s\n", resp.Pane)
	return nil
}

func threadNew(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	agent := fs.String("agent", "", "agent: claude|codex|pi (required)")
	name := fs.String("name", "", "thread name (optional; empty = a nameless thread)")
	cwd := fs.String("cwd", "", "start directory, relative or ~ ok (default: the current dir '.'; expanded against the invocation dir)")
	headless := fs.Bool("headless", false, "spawn headless (no window)")
	model := fs.String("model", "", "agent model to pin to the thread (opaque pass-through, e.g. haiku | anthropic/claude-opus-4-8 | gpt-5.5; empty = the agent's default)")
	parent := fs.String("parent", "", "parent thread id/prefix (default: the CURRENT thread when run inside one)")
	forkFrom := fs.String("fork-from", "", "branch this thread's conversation (default source: the current thread when only --fork is meaningful); agent/cwd default to the source's")
	yolo := fs.Bool("yolo", false, "launch with permissions bypassed (overrides [spawn] config)")
	sandboxF := fs.Bool("sandbox", false, "launch restricted (codex read-only; claude default-deny headless; pi: refused)")
	msg := fs.String("msg", "", "send this initial prompt once the agent is ready (headed spawns)")
	messageID := fs.Int("message-id", 0, "fork after the Nth assistant turn (0 = the whole conversation)")
	noParent := fs.Bool("no-parent", false, "force a root thread (suppress parent inference)")
	intoSession := fs.String("into-session", "", "place the thread as a new WINDOW of an existing session (shared session)")
	intoWindow := fs.String("into-window", "", "place the thread as a SPLIT pane of target (a pane id or session:window)")
	intoPane := fs.String("into-pane", "", "bind the thread to an EXISTING shell pane and run the agent in place (register-then-exec; with --exec)")
	doExec := fs.Bool("exec", false, "with --into-pane: replace THIS process with the agent (run it in the calling pane)")
	virtual := fs.Bool("virtual", false, "create a VIRTUAL thread — a pure grouping node with no agent (parent other threads under it; convert later with `thread realize`)")
	divider := fs.Bool("divider", false, "create a DIVIDER — a visual horizontal rule in the pinned block (--name is its optional label; placed at the top, reposition with `thread pin`)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *virtual && *divider {
		return errors.New("thread new: --virtual and --divider are mutually exclusive")
	}
	// A DIVIDER is a visual node with no agent — refuse every agent-shaped field
	// loudly (the daemon refuses too; catch them here before the request is built),
	// then create it at the top of the pinned block. --name is its optional label.
	if *divider {
		if *agent != "" {
			return errors.New("thread new: --divider takes no --agent (a divider has none)")
		}
		if *headless || *forkFrom != "" || *intoSession != "" || *intoWindow != "" ||
			*intoPane != "" || *msg != "" || *model != "" || *parent != "" {
			return errors.New("thread new: --divider takes no agent-shaped flags (it is a visual node — no agent, cwd, placement, or parent)")
		}
		return createDivider(cfg, *name, *asJSON)
	}
	if *virtual && *agent != "" {
		return errors.New("thread new: --virtual takes no --agent (a virtual thread has none; realize it later)")
	}
	var forkID string
	if *forkFrom != "" {
		rid, err := resolveThreadID(cfg, *forkFrom)
		if err != nil {
			return err
		}
		forkID = rid
		// Agent + cwd default to the source's (a fork stays the same
		// conversation); name still names the new thread.
		if *agent == "" || *cwd == "" {
			src, ok := lookupThread(daemonClient(cfg), forkID)
			if !ok {
				return fmt.Errorf("thread new: fork source %s not found", forkID)
			}
			if *agent == "" {
				*agent = src.AgentKind
			}
			if *cwd == "" {
				*cwd = src.Cwd
			}
		}
	}
	// Placement modes are mutually exclusive; --exec is for --into-pane only.
	if nSet(*intoSession != "", *intoWindow != "", *intoPane != "") > 1 {
		return errors.New("thread new: --into-session, --into-window and --into-pane are mutually exclusive")
	}
	if *doExec && *intoPane == "" {
		return errors.New("thread new: --exec requires --into-pane")
	}
	// --name is OPTIONAL (empty = a nameless thread). --agent is required —
	// except for a VIRTUAL thread, which by definition has none.
	if *agent == "" && !*virtual {
		return errors.New("thread new: --agent is required")
	}
	// --cwd defaults to the current dir ('.'); --into-pane inherits the pane's cwd
	// (leave it empty so the daemon takes the pane's path); a fork already defaulted
	// it to the source's cwd above; a VIRTUAL thread needs no cwd at all (an empty
	// one stays empty — baking the invocation dir into a grouping node would be
	// meaningless data; a cwd becomes required at realize time).
	if *cwd == "" && *intoPane == "" && !*virtual {
		*cwd = "."
	}
	if *cwd != "" {
		// Relative --cwd expands against the invocation dir (the daemon needs absolute).
		abs, err := absCwd(*cwd)
		if err != nil {
			return fmt.Errorf("thread new: --cwd: %w", err)
		}
		*cwd = abs
	}
	// Parent (v1 semantics, on the F1 resolver): an explicit --parent resolves
	// (prefix ok, loud unknown); otherwise a `new` run INSIDE a thread defaults
	// to it as parent. Inference failure here is a LEGITIMATE root, not an
	// error; --no-parent forces a root.
	resolvedParent := ""
	switch {
	case *noParent && *parent != "":
		return errors.New("thread new: --parent and --no-parent are mutually exclusive")
	case *parent != "":
		rp, err := resolveThreadID(cfg, *parent)
		if err != nil {
			return err
		}
		resolvedParent = rp
	case !*noParent:
		if rp, err := resolveThreadID(cfg, ""); err == nil {
			resolvedParent = rp
		}
	}
	if *yolo && *sandboxF {
		return errors.New("thread new: --yolo and --sandbox are mutually exclusive")
	}
	mode := ""
	if *yolo {
		mode = "yolo"
	}
	if *sandboxF {
		mode = "sandbox"
	}
	if *msg != "" && *headless {
		return errors.New("thread new: --msg is for headed spawns (use send-headless for a headless first turn)")
	}
	c := daemonClient(cfg)
	resp, err := c.ThreadNew(context.Background(), api.NewThreadRequest{
		Agent: *agent, Name: *name, Cwd: *cwd, Headless: *headless, Parent: resolvedParent,
		ForkFrom: forkID, MessageID: *messageID, Mode: mode, Msg: *msg, Model: *model,
		IntoSession: *intoSession, IntoWindow: *intoWindow, IntoPane: *intoPane,
		Virtual: *virtual,
	})
	if err != nil {
		return err
	}
	// --into-pane is register-then-exec: the daemon registered the thread + marked
	// the pane and returned the exact agent command; with --exec we replace THIS
	// process with the agent so it runs in the calling pane.
	if *doExec {
		if resp.LaunchCommand == "" {
			return errors.New("thread new: --exec: daemon returned no launch command")
		}
		return execAgent(resp.Thread.Cwd, resp.LaunchCommand, resp.LaunchEnv)
	}
	if *asJSON {
		// Normal/placement spawns keep emitting the bare thread (stable `.id`);
		// --into-pane additionally carries launch_command/launch_env.
		if resp.LaunchCommand != "" {
			return emitJSON(resp)
		}
		return emitJSON(resp.Thread)
	}
	fmt.Println(resp.Thread.ID)
	if resp.LaunchCommand != "" {
		// Registered but not exec'd — surface the command the caller must run.
		fmt.Fprintln(os.Stderr, "launch:", resp.LaunchCommand)
	}
	return nil
}

// execAgent replaces the current process with the agent command (run through the
// login shell so PATH/mise shims resolve), chdir'd to cwd, env extended with the
// thread's launch env. Used by `thread new --into-pane --exec` so the agent takes
// over the calling pane. It only returns on failure (success replaces the image).
func execAgent(cwd, command string, extra map[string]string) error {
	if cwd != "" {
		if err := os.Chdir(cwd); err != nil {
			return fmt.Errorf("thread new: --exec: chdir %s: %w", cwd, err)
		}
	}
	env := os.Environ()
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	if err := syscall.Exec(shell, []string{shell, "-lc", command}, env); err != nil {
		return fmt.Errorf("thread new: --exec %s: %w", command, err)
	}
	return nil
}

// nSet counts the true booleans (placement modes are mutually exclusive).
func nSet(bs ...bool) int {
	n := 0
	for _, b := range bs {
		if b {
			n++
		}
	}
	return n
}

func threadList(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	archived := fs.Bool("archived", false, "include archived (parked) threads")
	allMachines := fs.Bool("all-machines", false, "fan out across the mesh (this machine + peers)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c := daemonClient(cfg)
	resp, err := c.ThreadList(context.Background(), *archived, *allMachines)
	if err != nil {
		return err
	}
	for _, m := range resp.Unreachable {
		fmt.Fprintf(os.Stderr, "warning: peer %s unreachable\n", m)
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		for _, t := range resp.Threads {
			if err := enc.Encode(t); err != nil {
				return err
			}
		}
		return nil
	}
	for _, t := range resp.Threads {
		fmt.Printf("%s\t%s\t%s\t%s\t%s\n", t.ID, t.AgentKind, t.Name, t.SessionName, t.Cwd)
	}
	return nil
}

// threadStop ends a thread's runtime (agent + tmux session) but keeps the record,
// which becomes a dead, resumable thread. (The old `kill` was stop + delete;
// compose them in a wrapper if you want that.)
func threadStop(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	id := fs.String("id", "", "thread id (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return errors.New("thread stop: --id is required")
	}
	// resolveIDPrefix, NOT resolveThreadID: stop ends the agent + tmux session
	// (destructive), so it must never INFER the current thread from an empty id — an
	// ambient guess is a footgun (same rule as `thread delete`). An explicit prefix
	// resolves; an unknown one is loud.
	c := daemonClient(cfg)
	rid, err := resolveIDPrefix(c, *id)
	if err != nil {
		return err
	}
	*id = rid
	if err := c.ThreadStop(context.Background(), *id); err != nil {
		return err
	}
	fmt.Println("stopped", *id)
	return nil
}

func threadPane(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("pane", flag.ContinueOnError)
	id := fs.String("id", "", "thread id (required)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rid, err := resolveIDFlag(cfg, fs, id)
	if err != nil {
		return err
	}
	*id = rid
	c := daemonClient(cfg)
	resp, err := c.ThreadPane(context.Background(), *id)
	if err != nil {
		return err
	}
	if *asJSON {
		return emitJSON(resp)
	}
	if !resp.Found {
		return errors.New("no live pane for thread (dead)")
	}
	fmt.Printf("%s:%d %s (pid %d)\n", resp.Pane.Session, resp.Pane.Window, resp.Pane.Pane, resp.Pane.PanePID)
	return nil
}

// threadCapture prints the live text of a thread's tmux pane (the v1 pane-capture
// port). Useful for supervising a child agent that may have stalled on a prompt.
// Routes cross-machine via --machine (the pane is resolved on the owner's daemon).
func threadCapture(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("capture", flag.ContinueOnError)
	id := fs.String("id", "", "thread id/prefix (default: the current thread)")
	lines := fs.Int("lines", 50, "lines to capture (0 = the visible area only)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rid, err := resolveIDFlag(cfg, fs, id)
	if err != nil {
		return err
	}
	c := daemonClient(cfg)
	resp, err := c.ThreadCapture(context.Background(), rid, *lines)
	if err != nil {
		return err
	}
	if *asJSON {
		return emitJSON(resp)
	}
	fmt.Println(resp.Content)
	return nil
}

// threadReparent re-parents a thread (--root makes it a root). The daemon
// guards existence and cycles loudly.
func threadReparent(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("reparent", flag.ContinueOnError)
	id := fs.String("id", "", "thread id/prefix (default: the current thread)")
	parent := fs.String("parent", "", "new parent thread id/prefix")
	root := fs.Bool("root", false, "make the thread a root (no parent)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if (*parent == "") == !*root {
		return errors.New("thread reparent: exactly one of --parent or --root is required")
	}
	rid, err := resolveIDFlag(cfg, fs, id)
	if err != nil {
		return err
	}
	newParent := ""
	if *parent != "" {
		if newParent, err = resolveThreadID(cfg, *parent); err != nil {
			return err
		}
	}
	c := daemonClient(cfg)
	if err := c.ThreadReparent(context.Background(), rid, newParent); err != nil {
		return err
	}
	if newParent == "" {
		fmt.Printf("%s is now a root\n", rid)
	} else {
		fmt.Printf("%s -> child of %s\n", rid, newParent)
	}
	return nil
}

// threadNotify toggles a thread's notification gate (--on | --off).
func threadNotify(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("notify", flag.ContinueOnError)
	id := fs.String("id", "", "thread id/prefix (default: the current thread)")
	on := fs.Bool("on", false, "enable notifications")
	off := fs.Bool("off", false, "disable notifications")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *on == *off {
		return errors.New("thread notify: exactly one of --on or --off is required")
	}
	rid, err := resolveIDFlag(cfg, fs, id)
	if err != nil {
		return err
	}
	c := daemonClient(cfg)
	if err := c.ThreadNotify(context.Background(), rid, *on); err != nil {
		return err
	}
	state := "off"
	if *on {
		state = "on"
	}
	fmt.Printf("notifications %s for %s\n", state, rid)
	return nil
}

// threadReportState delivers an in-agent reporter's turn-lifecycle fact to the
// thread's daemon (schema 43, _dev/STATE_AUTHORITY.md): the maintainer then
// prefers this reported state over the pane content-diff heuristic. Mechanism
// for reporter hooks (the pi extension, the claude hook script) — SILENT on
// success, since hooks run per turn and their stdout can surface in the agent.
func threadReportState(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("report-state", flag.ContinueOnError)
	id := fs.String("id", "", "thread id/prefix (default: the current thread)")
	event := fs.String("event", "", "lifecycle event: turn_started | turn_ended | blocked | unblocked | release")
	source := fs.String("source", "", "reporter identity (e.g. sesh:pi-ext)")
	seq := fs.Int64("seq", 0, "strictly-increasing per-thread sequence (default: current unix nanos)")
	reason := fs.String("reason", "", "optional description for a blocked event (the prompt's message)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *event == "" {
		return errors.New("thread report-state: --event is required")
	}
	if *source == "" {
		return errors.New("thread report-state: --source is required")
	}
	rid, err := resolveIDFlag(cfg, fs, id)
	if err != nil {
		return err
	}
	s := *seq
	if s == 0 {
		// The default seq is the invocation instant in nanos: successive
		// reporter invocations (which serialize agent-side) read increasing
		// clocks, satisfying the daemon's strictly-monotonic requirement.
		s = time.Now().UnixNano()
	}
	return daemonClient(cfg).ThreadReportState(context.Background(), api.ReportStateRequest{
		ThreadID: rid, Source: *source, Event: *event, Seq: s, Reason: *reason,
	})
}

// threadFlag applies one flagged-system action (schema 44): --on flags (and
// re-enables a flag-disabled thread — one rule, no provenance bit), --off
// clears (flags NEVER auto-clear), --disable suppresses auto-flagging (+
// clears any current flag; parent-monitored children), --enable re-allows it.
func threadFlag(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("flag", flag.ContinueOnError)
	id := fs.String("id", "", "thread id/prefix (default: the current thread)")
	on := fs.Bool("on", false, "flag the thread (also re-enables auto-flagging if disabled)")
	off := fs.Bool("off", false, "clear the flag")
	disable := fs.Bool("disable", false, "suppress auto-flagging for this thread (also clears any current flag)")
	enable := fs.Bool("enable", false, "re-allow auto-flagging")
	if err := fs.Parse(args); err != nil {
		return err
	}
	action := ""
	n := 0
	for _, c := range []struct {
		set bool
		act string
	}{{*on, api.FlagOn}, {*off, api.FlagOff}, {*disable, api.FlagDisable}, {*enable, api.FlagEnable}} {
		if c.set {
			action = c.act
			n++
		}
	}
	if n != 1 {
		return errors.New("thread flag: exactly one of --on, --off, --disable, --enable is required")
	}
	rid, err := resolveIDFlag(cfg, fs, id)
	if err != nil {
		return err
	}
	if err := daemonClient(cfg).ThreadFlag(context.Background(), rid, action); err != nil {
		return err
	}
	fmt.Printf("flag %s: %s\n", action, rid)
	return nil
}

// threadHold parks/unparks a thread: sets on_hold_until to an absolute instant so
// the thread is hidden from the default active view until then (`--until` a date or
// `--until-unix` an exact instant), or clears the hold (`--clear`). It auto-expires
// — once the instant passes the thread silently returns to the active view; there
// is no separate "unhold" beyond letting the deadline lapse or `--clear`.
func threadHold(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("hold", flag.ContinueOnError)
	id := fs.String("id", "", "thread id/prefix (default: the current thread)")
	until := fs.String("until", "", "hold until the START of this date (YYYY-MM-DD, local time)")
	untilUnix := fs.Int64("until-unix", 0, "hold until this absolute unix instant (seconds)")
	clear := fs.Bool("clear", false, "clear the hold (return the thread to the active view now)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// Exactly one of --until / --until-unix / --clear.
	set := 0
	if *until != "" {
		set++
	}
	if *untilUnix != 0 {
		set++
	}
	if *clear {
		set++
	}
	if set != 1 {
		return errors.New("thread hold: exactly one of --until, --until-unix, or --clear is required")
	}
	var when int64
	switch {
	case *clear:
		when = 0
	case *untilUnix != 0:
		when = *untilUnix
	default:
		d, err := time.ParseInLocation("2006-01-02", *until, time.Local)
		if err != nil {
			return fmt.Errorf("thread hold: bad --until date %q (want YYYY-MM-DD): %w", *until, err)
		}
		when = d.Unix()
	}
	rid, err := resolveIDFlag(cfg, fs, id)
	if err != nil {
		return err
	}
	c := daemonClient(cfg)
	if err := c.ThreadHold(context.Background(), rid, when); err != nil {
		return err
	}
	if when == 0 {
		fmt.Printf("hold cleared for %s\n", rid)
	} else {
		fmt.Printf("%s on hold until %s\n", rid, time.Unix(when, 0).Format("2006-01-02 15:04"))
	}
	return nil
}

// threadTranscript prints a thread conversation's raw transcript lines (the
// owner-side D0 read; remote threads route with --machine). D1's `sesh tail`
// adds the ergonomic follow form.
func threadTranscript(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("transcript", flag.ContinueOnError)
	id := fs.String("id", "", "thread id/prefix (default: the current thread)")
	tail := fs.Int("tail", -1, "only the last N lines (default: all)")
	asJSON := fs.Bool("json", false, "emit JSON (lines + last_reply + reply_count)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rid, err := resolveIDFlag(cfg, fs, id)
	if err != nil {
		return err
	}
	c := daemonClient(cfg)
	resp, err := c.ThreadTranscript(context.Background(), rid, *tail)
	if err != nil {
		return err
	}
	if *asJSON {
		return emitJSON(resp)
	}
	for _, l := range resp.Lines {
		fmt.Println(l)
	}
	return nil
}

// threadAdopt brings an agent under sesh management. With a pane (default: the
// caller's own) it adopts a live work-server agent. With --agent it is a HEADLESS
// adopt: register an EXISTING, not-running conversation (--session-id) as a
// durable headless thread — no pane is used.
func threadAdopt(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("adopt", flag.ContinueOnError)
	pane := fs.String("pane", "", "tmux pane id on the work server (default: $TMUX_PANE; ignored for headless adopt)")
	name := fs.String("name", "", "thread name (required)")
	sessionID := fs.String("session-id", "", "agent conversation id; supplied explicitly when it can't be auto-detected (e.g. a claude launched with a bare -r); REQUIRED for headless adopt")
	agent := fs.String("agent", "", "agent: claude|codex|pi — selects HEADLESS adopt (register an existing, not-running conversation; no pane used)")
	cwd := fs.String("cwd", "", "headless adopt working directory, relative or ~ ok (default: the current dir '.'; expanded against the invocation dir)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return errors.New("thread adopt: --name is required")
	}

	// --agent selects HEADLESS adopt: no pane is consulted (so the $TMUX_PANE
	// default never hijacks it when run from inside a tmux pane).
	if *agent != "" {
		if *sessionID == "" {
			return errors.New("thread adopt (headless): --agent requires --session-id (the existing conversation to bind to)")
		}
		c, err := absCwd(*cwd)
		if err != nil {
			return err
		}
		if c == "" {
			c, err = absCwd(".")
			if err != nil {
				return err
			}
		}
		resp, err := daemonClient(cfg).ThreadAdopt(context.Background(), "", *name, *sessionID, *agent, c)
		if err != nil {
			return err
		}
		if *asJSON {
			return emitJSON(resp.Thread)
		}
		fmt.Printf("adopted headless %s (%s, session %s) as %s\n", resp.Thread.ID, resp.Thread.AgentKind, resp.Thread.AgentSessionID, resp.Thread.Name)
		return nil
	}

	if *pane == "" {
		*pane = os.Getenv("TMUX_PANE")
	}
	if *pane == "" {
		return errors.New("thread adopt: a pane (--pane or $TMUX_PANE) is required for pane adopt; for headless adopt of an existing conversation pass --agent and --session-id")
	}
	resp, err := daemonClient(cfg).ThreadAdopt(context.Background(), *pane, *name, *sessionID, "", "")
	if err != nil {
		return err
	}
	if *asJSON {
		return emitJSON(resp.Thread)
	}
	fmt.Printf("adopted %s (%s, session %s) as %s\n", *pane, resp.Thread.AgentKind, resp.Thread.AgentSessionID, resp.Thread.ID)
	return nil
}
