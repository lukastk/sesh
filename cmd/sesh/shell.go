package main

// `sesh shell` — the CLI for SHELL THREADS: a tracked tmux session as a
// first-class thread. See _dev/SHELL.md.
//
// Only the shell-SPECIFIC verbs live here. Everything a shell thread shares with
// an agent thread is the ordinary `sesh thread` surface, unchanged: list, rename,
// tag, pin, hold, archive, delete, reparent, meta, notify, flag — and resume
// (which creates the session), stop (which kills it) and send.

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
	"github.com/lukastk/sesh/internal/tmux"
)

// printShellThread renders a created/promoted shell thread: the record as JSON,
// or the one line that says what happened and where.
func printShellThread(th api.Thread, asJSON bool) error {
	if asJSON {
		return emitJSON(th)
	}
	fmt.Printf("%s  %s  %s  (session %s)\n", th.ID, th.Name, th.Cwd, th.SessionName)
	return nil
}

func runShell(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: sesh shell <new|enter|here|promote|sessions|info|panes> [flags]")
	}
	cfg := config.Load()
	sub, rest := args[0], args[1:]
	switch sub {
	case "new":
		return shellNew(cfg, rest, false)
	case "enter":
		return shellNew(cfg, rest, true)
	case "here":
		return shellHere(cfg, rest)
	case "promote":
		return shellPromote(cfg, rest)
	case "sessions":
		return shellSessions(cfg, rest)
	case "info":
		return shellInfo(cfg, rest, false)
	case "panes":
		return shellInfo(cfg, rest, true)
	default:
		return fmt.Errorf("unknown shell subcommand %q", sub)
	}
}

func shellNew(cfg config.Config, args []string, idempotent bool) error {
	verb := "new"
	if idempotent {
		verb = "enter"
	}
	fs := flag.NewFlagSet("shell "+verb, flag.ContinueOnError)
	cwd := fs.String("cwd", "", "absolute working directory for the session (required)")
	name := fs.String("name", "", "thread name (default: derived from the cwd)")
	sessionName := fs.String("session-name", "", "tmux session name (default: derived from the name)")
	parent := fs.String("parent", "", "parent thread id ('' = root)")
	noStart := fs.Bool("no-start", false, "record the place WITHOUT starting a session (born headless)")
	asJSON := fs.Bool("json", false, "emit the thread record as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cwd == "" {
		return errors.New("shell " + verb + ": --cwd is required (a shell thread's working directory IS its durable content)")
	}
	// absCwd keeps a ~-relative path AS IS so it stays portable across machines
	// (the owning daemon expands it against ITS home) and expands a bare relative
	// path locally, where it is the only place it means anything.
	dir, err := absCwd(*cwd)
	if err != nil {
		return err
	}
	if *noStart && idempotent {
		return errors.New("shell enter: --no-start makes no sense here (enter always leaves you a live session); use `shell new --no-start`")
	}
	c := daemonClient(cfg)
	resp, err := c.ShellNew(context.Background(), api.NewShellRequest{
		Cwd: dir, Name: *name, SessionName: *sessionName, Parent: *parent,
		NoStart: *noStart, Idempotent: idempotent,
	})
	if err != nil {
		return err
	}
	return printShellThread(resp.Thread, *asJSON)
}

func shellHere(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("shell here", flag.ContinueOnError)
	name := fs.String("name", "", "thread name (default: the session's own name)")
	parent := fs.String("parent", "", "parent thread id ('' = root)")
	asJSON := fs.Bool("json", false, "emit the thread record as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// Resolve the CALLER's session from $TMUX/$TMUX_PANE — the same client-side
	// resolver `tmux current` uses. This is why `here` is not --machine-routable:
	// the session it means is the one the caller is sitting in.
	cur, err := tmux.CurrentFromEnv(cfg.Machine)
	if err != nil {
		return fmt.Errorf("shell here: %w", err)
	}
	if cur.Session == "" {
		return errors.New("shell here: not inside a tmux session on sesh's work server (promotion is work-server-only: every shell feature assumes that socket)")
	}
	c := daemonClient(cfg)
	resp, err := c.ShellPromote(context.Background(), api.PromoteShellRequest{
		Session: cur.Session, Name: *name, Parent: *parent,
	})
	if err != nil {
		return err
	}
	return printShellThread(resp.Thread, *asJSON)
}

func shellPromote(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("shell promote", flag.ContinueOnError)
	session := fs.String("session", "", "tmux session name to promote (required)")
	name := fs.String("name", "", "thread name (default: the session's own name)")
	parent := fs.String("parent", "", "parent thread id ('' = root)")
	asJSON := fs.Bool("json", false, "emit the thread record as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *session == "" {
		return errors.New("shell promote: --session is required")
	}
	c := daemonClient(cfg)
	resp, err := c.ShellPromote(context.Background(), api.PromoteShellRequest{
		Session: *session, Name: *name, Parent: *parent,
	})
	if err != nil {
		return err
	}
	return printShellThread(resp.Thread, *asJSON)
}

func shellSessions(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("shell sessions", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit one session object per line (JSONL)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c := daemonClient(cfg)
	resp, err := c.ShellSessions(context.Background())
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		for _, s := range resp.Sessions {
			if err := enc.Encode(s); err != nil {
				return err
			}
		}
		return nil
	}
	for _, s := range resp.Sessions {
		att := " "
		if s.Attached {
			att = "*"
		}
		line := fmt.Sprintf("%s%-24s %-6s %d/%d %s", att, s.Name, s.Class, s.Windows, s.Panes, s.Path)
		if len(s.AgentThreads) > 0 {
			line += "  [" + strings.Join(s.AgentThreads, " ") + "]"
		}
		fmt.Println(line)
	}
	return nil
}

func shellInfo(cfg config.Config, args []string, panesOnly bool) error {
	verb := "info"
	if panesOnly {
		verb = "panes"
	}
	fs := flag.NewFlagSet("shell "+verb, flag.ContinueOnError)
	id := fs.String("id", "", "shell thread id (required)")
	asJSON := fs.Bool("json", false, "emit the full locator + window/pane tree as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return errors.New("shell " + verb + ": --id is required")
	}
	resolved, err := resolveThreadID(cfg, *id)
	if err != nil {
		return err
	}
	c := daemonClient(cfg)
	resp, err := c.ShellInfo(context.Background(), resolved)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(resp)
	}
	if panesOnly {
		if !resp.Live {
			fmt.Println("(headless — no live session)")
			return nil
		}
		for _, w := range resp.Windows {
			for _, p := range w.Panes {
				active := " "
				if p.Active {
					active = "*"
				}
				owner := ""
				if p.ThreadID != "" {
					owner = "  thread=" + p.ThreadID
				}
				fmt.Printf("%s%s:%d.%d  %-12s pid=%d  %s%s\n",
					active, resp.Session, w.Index, p.Index, p.Command, p.PID, p.Path, owner)
			}
		}
		return nil
	}
	fmt.Println("id:         ", resp.ID)
	fmt.Println("machine:    ", resp.Machine)
	fmt.Println("name:       ", resp.Name)
	fmt.Println("cwd:        ", resp.Cwd)
	if resp.Live {
		att := "detached"
		if resp.Attached {
			att = "attached"
		}
		fmt.Println("session:    ", resp.Session, "("+att+")")
	} else {
		fmt.Println("session:     (headless — no live session; `sesh thread resume --id " + resp.ID + "` recreates it)")
	}
	fmt.Println("socket:     ", resp.Socket)
	fmt.Println("socket path:", resp.SocketPath)
	// The raw-tmux escape hatch: sesh does not reimplement tmux, so anything
	// advanced is done straight against the server with this prefix.
	fmt.Println("raw tmux:   ", resp.TmuxPrefix)
	return nil
}
