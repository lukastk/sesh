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
	"github.com/lukastk/sesh/internal/tmux"
)

// runTmux implements `sesh tmux <current|info|create-session|create-pane|
// send-text|stage-file>`. All but `current` are served by the local daemon;
// `current` is client-side (it needs the caller's $TMUX).
func runTmux(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: sesh tmux <current|info|create-session|create-pane|send-text|stage-file>")
	}
	cfg := config.Load()
	sub, rest := args[0], args[1:]
	switch sub {
	case "current":
		return tmuxCurrent(cfg, rest)
	case "info":
		return tmuxInfo(cfg, rest)
	case "create-session":
		return tmuxCreateSession(cfg, rest)
	case "create-pane":
		return tmuxCreatePane(cfg, rest)
	case "send-text":
		return tmuxSendText(cfg, rest)
	case "stage-file":
		return tmuxStageFile(cfg, rest)
	default:
		return fmt.Errorf("unknown tmux subcommand %q", sub)
	}
}

func tmuxCurrent(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("current", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cur, err := tmux.CurrentFromEnv(cfg.Machine)
	if err != nil {
		return err
	}
	if *asJSON {
		return emitJSON(cur)
	}
	fmt.Printf("machine:   %s\n", cur.Machine)
	fmt.Printf("socket:    %s\n", cur.Socket)
	fmt.Printf("session:   %s\n", cur.Session)
	fmt.Printf("window:    %d\n", cur.Window)
	fmt.Printf("pane:      %s\n", cur.Pane)
	fmt.Printf("thread_id: %s\n", cur.ThreadID)
	return nil
}

func tmuxInfo(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("info", flag.ContinueOnError)
	session := fs.String("session", "", "filter to one session")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// Cross-machine `tmux info --machine X` is handled by the global router in
	// main (it ssh-routes to X, which runs `tmux info` against its own daemon).
	c := client.New(cfg.SocketPath())
	resp, err := c.TmuxInfo(context.Background(), *session)
	if err != nil {
		return err
	}
	// Contract: JSONL — one session object per line.
	enc := json.NewEncoder(os.Stdout)
	for _, s := range resp.Sessions {
		if err := enc.Encode(s); err != nil {
			return err
		}
	}
	return nil
}

func tmuxCreateSession(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("create-session", flag.ContinueOnError)
	name := fs.String("name", "", "session name (required)")
	dir := fs.String("dir", "", "start directory")
	var env envFlag
	fs.Var(&env, "env", "environment KEY=VALUE (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return errors.New("create-session: --name is required")
	}
	c := client.New(cfg.SocketPath())
	resp, err := c.TmuxCreateSession(context.Background(), api.CreateSessionRequest{Name: *name, Dir: *dir, Env: env.m})
	if err != nil {
		return err
	}
	fmt.Println(resp.Session)
	return nil
}

func tmuxCreatePane(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("create-pane", flag.ContinueOnError)
	target := fs.String("target", "", "tmux target (session/window/pane) to split (required)")
	dir := fs.String("dir", "", "start directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *target == "" {
		return errors.New("create-pane: --target is required")
	}
	c := client.New(cfg.SocketPath())
	resp, err := c.TmuxCreatePane(context.Background(), api.CreatePaneRequest{Target: *target, Dir: *dir})
	if err != nil {
		return err
	}
	fmt.Println(resp.Pane)
	return nil
}

func tmuxSendText(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("send-text", flag.ContinueOnError)
	target := fs.String("target", "", "tmux target pane (required)")
	text := fs.String("text", "", "literal text to send (required)")
	enter := fs.Bool("enter", false, "send a trailing Enter")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *target == "" || *text == "" {
		return errors.New("send-text: --target and --text are required")
	}
	c := client.New(cfg.SocketPath())
	return c.TmuxSendText(context.Background(), api.SendTextRequest{Target: *target, Text: *text, Enter: *enter})
}

func tmuxStageFile(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("stage-file", flag.ContinueOnError)
	to := fs.String("to", "", "machine to stage onto (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *to == "" {
		return errors.New("stage-file: --to <machine> is required")
	}
	if fs.NArg() != 1 {
		return errors.New("stage-file: exactly one local file path required")
	}
	if *to != cfg.Machine {
		return fmt.Errorf("NOT IMPLEMENTED: staging onto remote machine %q lands with the mesh (Phase 4)", *to)
	}
	path := fs.Arg(0)
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	c := client.New(cfg.SocketPath())
	resp, err := c.TmuxStageFile(context.Background(), baseName(path), content)
	if err != nil {
		return err
	}
	fmt.Println(resp.Path)
	return nil
}

// envFlag collects repeated --env KEY=VALUE flags.
type envFlag struct{ m map[string]string }

func (e *envFlag) String() string { return "" }
func (e *envFlag) Set(v string) error {
	k, val, ok := strings.Cut(v, "=")
	if !ok {
		return fmt.Errorf("env must be KEY=VALUE, got %q", v)
	}
	if e.m == nil {
		e.m = map[string]string{}
	}
	e.m[k] = val
	return nil
}

func baseName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}
