package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
	"github.com/lukastk/sesh/internal/peers"
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
	case "nav":
		return tmuxNav(cfg, rest)
	default:
		return fmt.Errorf("unknown tmux subcommand %q", sub)
	}
}

// tmuxNav implements `sesh tmux nav --to <machine>:<session>` — the nav
// primitive. From the mymastertmux client it (1) switches the OUTER client to
// machine M's window, then (2) drives M's INNER mytmux to switch-client to the
// target session (over ssh for a remote M), with the detached "bare-shell kick".
func tmuxNav(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("nav", flag.ContinueOnError)
	to := fs.String("to", "", "target as <machine>:<session> (required)")
	inClient := fs.Bool("in-client", false, "switch the CURRENT tmux client to the target (local target on the current work socket only; no master)")
	attach := fs.Bool("attach", false, "ATTACH this terminal to the target thread (for use from a plain shell outside tmux); REPLACES the process")
	if err := fs.Parse(args); err != nil {
		return err
	}
	machine, session, ok := strings.Cut(*to, ":")
	if !ok || machine == "" || session == "" {
		return errors.New("nav: --to must be <machine>:<session>")
	}

	// Attach: from a plain shell (no tmux client to switch), make this terminal BECOME
	// the thread — exec `tmux attach` locally, or `ssh -t … tmux attach` for a peer. This
	// replaces the process, so on detach the user returns to the shell they launched from.
	if *attach {
		if machine == cfg.Machine {
			tb, err := exec.LookPath("tmux")
			if err != nil {
				return err
			}
			// attach -t does NOT honor the "=" exact-match prefix, so use the bare name.
			return syscall.Exec(tb, []string{"tmux", "-L", cfg.TmuxSocket, "attach", "-t", session}, tmuxCleanEnv())
		}
		reg, err := peers.Load(cfg.PeersPath())
		if err != nil {
			return err
		}
		peer, ok := reg.Get(machine)
		if !ok {
			return fmt.Errorf("nav --attach: unknown machine %q (no peer registered)", machine)
		}
		if peer.TmuxSocket == "" {
			return fmt.Errorf("nav --attach: peer %q has no tmux socket (see `sesh peer add --tmux-socket`)", machine)
		}
		sb, err := exec.LookPath("ssh")
		if err != nil {
			return err
		}
		remote := "env -u TMUX tmux -L " + shellQuote(peer.TmuxSocket) + " attach -t " + shellQuote(session)
		sshArgs := append([]string{"ssh", "-t", "-o", "StrictHostKeyChecking=no"}, peer.SSHArgs()...)
		sshArgs = append(sshArgs, peer.SSH, remote)
		return syscall.Exec(sb, sshArgs, os.Environ())
	}

	// In-client nav: skip the master entirely and just switch THIS tmux client to the
	// target session — for a LOCAL target on the work socket we're already attached to.
	// Loud (never a silent no-op): errors if the target isn't local, or if we aren't in
	// a tmux client on that work socket.
	if *inClient {
		if machine != cfg.Machine {
			return fmt.Errorf("nav --in-client: target %q is not this machine (%q) — --in-client is local-only", machine, cfg.Machine)
		}
		t := os.Getenv("TMUX")
		if t == "" {
			return errors.New("nav --in-client: not inside a tmux client")
		}
		if sock := filepath.Base(strings.SplitN(t, ",", 2)[0]); sock != cfg.TmuxSocket {
			return fmt.Errorf("nav --in-client: current client is on tmux socket %q, not the work socket %q", sock, cfg.TmuxSocket)
		}
		script := tmux.InnerSwitchInClientScript(cfg.TmuxSocket, session)
		if out, err := exec.Command("sh", "-c", script).CombinedOutput(); err != nil {
			return fmt.Errorf("nav --in-client: switch-client: %v: %s", err, out)
		}
		return nil
	}

	// (1) Outer: switch the mymastertmux client to machine M's window.
	master := tmux.NewServer(cfg.MasterSocket)
	if err := master.SelectWindow(machine); err != nil {
		return fmt.Errorf("nav outer select (window %q on %s): %w", machine, cfg.MasterSocket, err)
	}

	// (2) Inner: switch M's mytmux client to the target session — specifically the
	// client this master's window recorded in its marker (see MasterClientMarker),
	// so a direct attach or another master's window is never the one moved.
	if machine == cfg.Machine {
		script := tmux.InnerSwitchScript(cfg.TmuxSocket, session, tmux.MasterClientMarker(cfg.Home, cfg.Machine))
		out, err := exec.Command("sh", "-c", script).CombinedOutput()
		if err != nil {
			return fmt.Errorf("nav inner switch (local): %v: %s", err, out)
		}
		return nil
	}
	reg, err := peers.Load(cfg.PeersPath())
	if err != nil {
		return err
	}
	peer, ok := reg.Get(machine)
	if !ok {
		return fmt.Errorf("nav: unknown machine %q (no peer registered)", machine)
	}
	if peer.TmuxSocket == "" {
		return fmt.Errorf("nav: peer %q has no tmux socket registered (see `sesh peer add --tmux-socket`)", machine)
	}
	if peer.Home == "" {
		return fmt.Errorf("nav: peer %q has no home registered (see `sesh peer add --home`)", machine)
	}
	script := tmux.InnerSwitchScript(peer.TmuxSocket, session, tmux.MasterClientMarker(peer.Home, cfg.Machine))
	navArgs := append([]string{"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no"}, peer.SSHArgs()...)
	navArgs = append(navArgs, peer.SSH, script)
	out, err := exec.Command("ssh", navArgs...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("nav inner switch on %s: %v: %s", machine, err, out)
	}
	return nil
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
	c := daemonClient(cfg)
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
	c := daemonClient(cfg)
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
	c := daemonClient(cfg)
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
	c := daemonClient(cfg)
	return c.TmuxSendText(context.Background(), api.SendTextRequest{Target: *target, Text: *text, Enter: *enter})
}

func tmuxStageFile(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("stage-file", flag.ContinueOnError)
	to := fs.String("to", "", "machine to stage onto (required)")
	stdin := fs.Bool("stdin", false, "read the file bytes from stdin (used internally for remote staging)")
	name := fs.String("name", "", "staged file name (required with --stdin)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *to == "" {
		return errors.New("stage-file: --to <machine> is required")
	}

	// Resolve the bytes + the staged name, from stdin or a local file path.
	var content []byte
	var staged string
	if *stdin {
		if *name == "" {
			return errors.New("stage-file --stdin: --name is required")
		}
		var err error
		if content, err = io.ReadAll(os.Stdin); err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
		staged = *name
	} else {
		if fs.NArg() != 1 {
			return errors.New("stage-file: exactly one local file path required")
		}
		path := fs.Arg(0)
		var err error
		if content, err = os.ReadFile(path); err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		staged = baseName(path)
	}

	// Remote target: the file is on THIS machine, so route the BYTES (not the
	// path) — pipe them over ssh to the peer running stage-file --stdin.
	if *to != cfg.Machine {
		return stageFileRemote(cfg, *to, staged, content)
	}

	c := daemonClient(cfg)
	resp, err := c.TmuxStageFile(context.Background(), staged, content)
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
