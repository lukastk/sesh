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

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
)

// absCwd expands a --cwd value to an absolute path relative to the process's
// working directory — i.e. WHERE the command was invoked — so a relative path
// (or a leading ~) just works (the daemon requires absolute). Empty stays empty
// (the caller's own required-check handles that). Note: for a cross-machine
// (--machine) spawn the expansion is still against the LOCAL cwd, so pass an
// absolute path when the target dir only exists on the remote.
func absCwd(cwd string) (string, error) {
	if cwd == "" {
		return "", nil
	}
	if cwd == "~" || strings.HasPrefix(cwd, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		cwd = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(cwd, "~"), "/"))
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
	name := fs.String("name", "", "new name (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return errors.New("thread rename: --name is required")
	}
	rid, err := resolveThreadID(cfg, *id)
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
	rid, err := resolveThreadID(cfg, *id)
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
	rid, err := resolveThreadID(cfg, *id)
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
	// Accept a positional id too (`sesh resume <id>`).
	if *id == "" && fs.NArg() == 1 {
		*id = fs.Arg(0)
	}
	rid, err := resolveThreadID(cfg, *id)
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
	if *id == "" && fs.NArg() == 1 {
		*id = fs.Arg(0)
	}
	rid, err := resolveThreadID(cfg, *id)
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *text == "" {
		return errors.New("thread send-headless: --text is required")
	}
	rid, err := resolveThreadID(cfg, *id)
	if err != nil {
		return err
	}
	*id = rid
	c := daemonClient(cfg)
	if err := c.ThreadSendHeadless(context.Background(), *id, *text); err != nil {
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
	rid, err := resolveThreadID(cfg, *id)
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *text == "" {
		return errors.New("thread send: --text is required")
	}
	rid, err := resolveThreadID(cfg, *id)
	if err != nil {
		return err
	}
	*id = rid
	c := daemonClient(cfg)
	if err := c.ThreadSend(context.Background(), *id, *text); err != nil {
		return err
	}
	fmt.Println("sent", *id)
	return nil
}

func threadStatus(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	id := fs.String("id", "", "thread id (required)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rid, err := resolveThreadID(cfg, *id)
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
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
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
	// --name is OPTIONAL (empty = a nameless thread). --agent is required.
	if *agent == "" {
		return errors.New("thread new: --agent is required")
	}
	// --cwd defaults to the current dir ('.'); --into-pane inherits the pane's cwd
	// (leave it empty so the daemon takes the pane's path); a fork already defaulted
	// it to the source's cwd above.
	if *cwd == "" && *intoPane == "" {
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
		ForkFrom: forkID, MessageID: *messageID, Mode: mode, Msg: *msg,
		IntoSession: *intoSession, IntoWindow: *intoWindow, IntoPane: *intoPane,
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
	rid, err := resolveThreadID(cfg, *id)
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
	rid, err := resolveThreadID(cfg, *id)
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
	rid, err := resolveThreadID(cfg, *id)
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
	rid, err := resolveThreadID(cfg, *id)
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
	rid, err := resolveThreadID(cfg, *id)
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

// threadAdopt brings a manually-launched agent under sesh management
// (work-server panes only; the pane defaults to the caller's own).
func threadAdopt(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("adopt", flag.ContinueOnError)
	pane := fs.String("pane", "", "tmux pane id on the work server (default: $TMUX_PANE)")
	name := fs.String("name", "", "thread name (required)")
	sessionID := fs.String("session-id", "", "agent conversation id, supplied explicitly when it can't be auto-detected (e.g. a claude launched with a bare -r)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *pane == "" {
		*pane = os.Getenv("TMUX_PANE")
	}
	if *pane == "" || *name == "" {
		return errors.New("thread adopt: --name and a pane (--pane or $TMUX_PANE) are required")
	}
	resp, err := daemonClient(cfg).ThreadAdopt(context.Background(), *pane, *name, *sessionID)
	if err != nil {
		return err
	}
	if *asJSON {
		return emitJSON(resp.Thread)
	}
	fmt.Printf("adopted %s (%s, session %s) as %s\n", *pane, resp.Thread.AgentKind, resp.Thread.AgentSessionID, resp.Thread.ID)
	return nil
}
