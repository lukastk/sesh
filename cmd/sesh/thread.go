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
	"github.com/lukastk/sesh/internal/client"
	"github.com/lukastk/sesh/internal/config"
)

// runThread implements `sesh thread <new|list|kill|pane>`. Mutations route to
// the local daemon (this machine is the thread's owner).
func runThread(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: sesh thread <new|list|kill|pane>")
	}
	cfg := config.Load()
	sub, rest := args[0], args[1:]
	switch sub {
	case "new":
		return threadNew(cfg, rest)
	case "list":
		return threadList(cfg, rest)
	case "kill":
		return threadKill(cfg, rest)
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
	case "tag":
		return threadTag(cfg, rest)
	case "archive":
		return threadArchive(cfg, rest)
	case "delete":
		return threadDelete(cfg, rest)
	case "resume":
		return threadResume(cfg, rest)
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
	if *id == "" || *name == "" {
		return errors.New("thread rename: --id and --name are required")
	}
	c := client.New(cfg.SocketPath())
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
	if *id == "" || (len(add) == 0 && len(remove) == 0) {
		return errors.New("thread tag: --id and at least one --add/--remove required")
	}
	c := client.New(cfg.SocketPath())
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
	if *id == "" {
		return errors.New("thread archive: --id is required")
	}
	c := client.New(cfg.SocketPath())
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return errors.New("thread delete: --id is required")
	}
	c := client.New(cfg.SocketPath())
	if err := c.ThreadDelete(context.Background(), *id); err != nil {
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
	if *id == "" {
		return errors.New("resume: a thread id is required (--id or positional)")
	}
	c := client.New(cfg.SocketPath())
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
	if *id == "" || *text == "" {
		return errors.New("thread send-headless: --id and --text are required")
	}
	c := client.New(cfg.SocketPath())
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
	if *id == "" {
		return errors.New("thread headless-reply: --id is required")
	}
	c := client.New(cfg.SocketPath())
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
	if *id == "" || *text == "" {
		return errors.New("thread send: --id and --text are required")
	}
	c := client.New(cfg.SocketPath())
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
	if *id == "" {
		return errors.New("thread status: --id is required")
	}
	c := client.New(cfg.SocketPath())
	resp, err := c.ThreadStatus(context.Background(), *id)
	if err != nil {
		return err
	}
	if *asJSON {
		return emitJSON(resp)
	}
	fmt.Printf("activity:      %s\n", resp.Activity)
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
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *agent == "" || *name == "" || *cwd == "" {
		return errors.New("thread new: --agent, --name and --cwd are required")
	}
	c := client.New(cfg.SocketPath())
	resp, err := c.ThreadNew(context.Background(), api.NewThreadRequest{
		Agent: *agent, Name: *name, Cwd: *cwd, Headless: *headless,
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	c := client.New(cfg.SocketPath())
	resp, err := c.ThreadList(context.Background(), *archived)
	if err != nil {
		return err
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

func threadKill(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("kill", flag.ContinueOnError)
	id := fs.String("id", "", "thread id (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return errors.New("thread kill: --id is required")
	}
	c := client.New(cfg.SocketPath())
	if err := c.ThreadKill(context.Background(), *id); err != nil {
		return err
	}
	fmt.Println("killed", *id)
	return nil
}

func threadPane(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("pane", flag.ContinueOnError)
	id := fs.String("id", "", "thread id (required)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return errors.New("thread pane: --id is required")
	}
	c := client.New(cfg.SocketPath())
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
