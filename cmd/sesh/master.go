package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/lukastk/sesh/internal/config"
	"github.com/lukastk/sesh/internal/peers"
)

// The master tmux server is the cross-machine cockpit: one session with one window
// PER MACHINE (window-name == machine-name), each window an auto-reconnecting attach
// into that machine's WORK server. sesh both BUILDS it (`master up`) and DRIVES it
// (`tmux nav`), so the window-name/work-socket conventions are sesh-internal — there
// is no cross-repo contract. myrig only configures (a tmux conf) and aliases. See
// _dev/MASTER.md.
const masterSession = "master"

func runMaster(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: sesh master <up|window|attach|down>")
	}
	cfg := config.Load()
	switch args[0] {
	case "up":
		return masterUp(cfg, args[1:])
	case "window":
		return masterWindow(cfg, args[1:])
	case "attach":
		return masterAttach(cfg, args[1:])
	case "down":
		return masterDown(cfg, args[1:])
	default:
		return fmt.Errorf("unknown master subcommand %q", args[0])
	}
}

// mtmux runs a tmux command on the master socket, with $TMUX scrubbed so it works
// from inside another tmux.
func mtmux(cfg config.Config, args ...string) (string, error) {
	full := append([]string{"-L", cfg.MasterSocket}, args...)
	cmd := exec.Command("tmux", full...)
	cmd.Env = tmuxCleanEnv()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// tmuxCleanEnv is the current env minus $TMUX (so nested tmux attach/new works).
func tmuxCleanEnv() []string {
	out := make([]string, 0, len(os.Environ()))
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "TMUX=") {
			continue
		}
		out = append(out, e)
	}
	return out
}

// masterUp builds the master server: one session, one window per machine (named after
// the machine), each running the per-window supervisor (`master window <machine>`).
func masterUp(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	machinesFlag := fs.String("machines", "", "comma-separated machines (default: self + all peers; 'self' allowed)")
	tmuxConf := fs.String("tmux-conf", "", "tmux config file to apply to the master server (myrig's look/keybindings)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	machines, err := masterMachines(cfg, *machinesFlag)
	if err != nil {
		return err
	}
	if len(machines) == 0 {
		return errors.New("master up: no machines (self + peers is empty)")
	}
	// Idempotency guard: refuse if already up (loud, no silent rebuild).
	if _, err := mtmux(cfg, "has-session", "-t", "="+masterSession); err == nil {
		return fmt.Errorf("master up: already up on socket %q (run `sesh master down` first)", cfg.MasterSocket)
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}

	for i, m := range machines {
		winCmd := fmt.Sprintf("%s master window %s", shellQuote(self), shellQuote(m))
		if i == 0 {
			if out, err := mtmux(cfg, "new-session", "-d", "-s", masterSession, "-n", m, winCmd); err != nil {
				return fmt.Errorf("master up: new-session: %v: %s", err, out)
			}
			// Lock window names so nav's select-window-by-name stays valid.
			mtmux(cfg, "set-option", "-g", "automatic-rename", "off") //nolint:errcheck
			mtmux(cfg, "set-option", "-g", "allow-rename", "off")     //nolint:errcheck
		} else {
			if out, err := mtmux(cfg, "new-window", "-t", masterSession+":", "-n", m, winCmd); err != nil {
				return fmt.Errorf("master up: new-window %s: %v: %s", m, err, out)
			}
		}
	}
	if *tmuxConf != "" {
		if out, err := mtmux(cfg, "source-file", *tmuxConf); err != nil {
			return fmt.Errorf("master up: source-file %s: %v: %s", *tmuxConf, err, out)
		}
	}
	fmt.Printf("master up on %q: %s\n", cfg.MasterSocket, strings.Join(machines, ", "))
	return nil
}

// masterMachines resolves the machine set: self first, then peers (sorted). An empty
// filter means all of them; otherwise it intersects with the requested list ('self'
// is an alias for cfg.Machine), erroring loudly on an unknown machine.
func masterMachines(cfg config.Config, filter string) ([]string, error) {
	reg, err := peers.Load(cfg.PeersPath())
	if err != nil {
		return nil, err
	}
	all := []string{cfg.Machine}
	known := map[string]bool{cfg.Machine: true}
	for _, p := range reg.List() {
		if !known[p.Machine] {
			all = append(all, p.Machine)
			known[p.Machine] = true
		}
	}
	if filter == "" {
		return all, nil
	}
	want := map[string]bool{}
	for _, m := range strings.Split(filter, ",") {
		m = strings.TrimSpace(m)
		if m == "self" {
			m = cfg.Machine
		}
		if m == "" {
			continue
		}
		if !known[m] {
			return nil, fmt.Errorf("master up: machine %q is not self or a registered peer", m)
		}
		want[m] = true
	}
	var out []string
	for _, m := range all {
		if want[m] {
			out = append(out, m)
		}
	}
	return out, nil
}

// masterWindow is the per-window supervisor each master window runs: it attaches into
// the machine's WORK server and re-establishes (with backoff) whenever the attach
// exits — laptop sleep, ssh blip, remote tmux restart, or simply no session to attach
// to yet. It never returns; the window thus self-heals.
func masterWindow(cfg config.Config, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: sesh master window <machine>")
	}
	makeAttach, err := masterAttachCommand(cfg, args[0])
	if err != nil {
		return err
	}
	const minBackoff, maxBackoff = 500 * time.Millisecond, 5 * time.Second
	backoff := minBackoff
	for {
		cmd := makeAttach()
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		start := time.Now()
		cmd.Run() //nolint:errcheck — a drop is expected; we re-establish below
		if time.Since(start) > 10*time.Second {
			backoff = minBackoff // it was a real, long-lived attach; reset
		} else {
			if backoff *= 2; backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
		time.Sleep(backoff)
	}
}

// masterAttachCommand returns a factory that builds the attach process for a machine:
// a local `tmux attach` for self, or `ssh -t … tmux -L <work> attach` for a peer
// (work socket from the peer registry).
func masterAttachCommand(cfg config.Config, machine string) (func() *exec.Cmd, error) {
	if machine == cfg.Machine {
		return func() *exec.Cmd {
			c := exec.Command("tmux", "-L", cfg.TmuxSocket, "attach")
			c.Env = tmuxCleanEnv()
			return c
		}, nil
	}
	reg, err := peers.Load(cfg.PeersPath())
	if err != nil {
		return nil, err
	}
	peer, ok := reg.Get(machine)
	if !ok {
		return nil, fmt.Errorf("master window: unknown machine %q (no peer registered)", machine)
	}
	if peer.TmuxSocket == "" {
		return nil, fmt.Errorf("master window: peer %q has no tmux socket (see `sesh peer add --tmux-socket`)", machine)
	}
	remote := "env -u TMUX tmux -L " + shellQuote(peer.TmuxSocket) + " attach"
	sshArgs := append([]string{"-tt", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no"}, peer.SSHArgs()...)
	sshArgs = append(sshArgs, peer.SSH, remote)
	return func() *exec.Cmd {
		c := exec.Command("ssh", sshArgs...)
		c.Env = tmuxCleanEnv()
		return c
	}, nil
}

// masterAttach replaces this process with a tmux attach to the master server.
func masterAttach(cfg config.Config, args []string) error {
	bin, err := exec.LookPath("tmux")
	if err != nil {
		return err
	}
	argv := []string{"tmux", "-L", cfg.MasterSocket, "attach", "-t", masterSession}
	return syscall.Exec(bin, argv, tmuxCleanEnv())
}

// masterDown tears the master server down (idempotent: already-down is not an error).
func masterDown(cfg config.Config, args []string) error {
	if out, err := mtmux(cfg, "kill-server"); err != nil {
		if strings.Contains(out, "no server running") {
			return nil
		}
		return fmt.Errorf("master down: %v: %s", err, out)
	}
	return nil
}
