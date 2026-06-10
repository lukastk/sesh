package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
)

// runThread implements `sesh thread <new|list|kill|pane>`. Mutations route to
// the local daemon (this machine is the thread's owner).
func runThread(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: sesh thread <new|list|stop|delete|pane>")
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
	if err := c.ThreadDelete(context.Background(), *id, *force); err != nil {
		return err
	}
	fmt.Println("deleted", *id)
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
	name := fs.String("name", "", "thread name (required)")
	cwd := fs.String("cwd", "", "absolute start directory (required)")
	headless := fs.Bool("headless", false, "spawn headless (no window)")
	parent := fs.String("parent", "", "parent thread id/prefix (default: the CURRENT thread when run inside one)")
	noParent := fs.Bool("no-parent", false, "force a root thread (suppress parent inference)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *agent == "" || *name == "" || *cwd == "" {
		return errors.New("thread new: --agent, --name and --cwd are required")
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
	c := daemonClient(cfg)
	resp, err := c.ThreadNew(context.Background(), api.NewThreadRequest{
		Agent: *agent, Name: *name, Cwd: *cwd, Headless: *headless, Parent: resolvedParent,
	})
	if err != nil {
		return err
	}
	if *asJSON {
		return emitJSON(resp.Thread)
	}
	fmt.Println(resp.Thread.ID)
	return nil
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
	rid, err := resolveThreadID(cfg, *id)
	if err != nil {
		return err
	}
	*id = rid
	c := daemonClient(cfg)
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
