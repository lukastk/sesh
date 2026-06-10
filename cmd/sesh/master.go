package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
	"github.com/lukastk/sesh/internal/peers"
	"github.com/lukastk/sesh/internal/tmux"
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
		return errors.New("usage: sesh master <up|window|attach|down|ensure|watchers>")
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
	case "ensure":
		return masterEnsure(cfg, args[1:])
	case "watchers":
		return masterWatchers(cfg, args[1:])
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
	tmuxConf := fs.String("tmux-conf", "", "tmux config file for the master server, loaded via -f INSTEAD of ~/.tmux.conf (myrig's self-contained look/keybindings)")
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

	// Start the master server with `-f` so it does NOT inherit the user's base
	// ~/.tmux.conf — base's app-specific settings (e.g. `status 2`) would otherwise
	// leak in as stray status lines. The look is entirely the (myrig-owned, self-
	// contained) --tmux-conf's job; with none, /dev/null gives a clean bare master.
	// `-f` only takes effect on the FIRST command (which starts the server).
	conf := "/dev/null"
	if *tmuxConf != "" {
		conf = *tmuxConf
	}

	for i, m := range machines {
		winCmd := fmt.Sprintf("%s master window %s", shellQuote(self), shellQuote(m))
		if i == 0 {
			if out, err := mtmux(cfg, "-f", conf, "new-session", "-d", "-s", masterSession, "-n", m, winCmd); err != nil {
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
	fmt.Printf("master up on %q: %s\n", cfg.MasterSocket, strings.Join(machines, ", "))
	return nil
}

// masterEnsure converges the master to "one window per machine": builds the whole
// master when it is down (== `master up`), and when it is up, (re)creates ONLY the
// missing machine windows — the recovery for a window lost to prefix+K/kill-window,
// which `master up`'s loud non-idempotency refuses to handle. Existing windows are
// never touched, so it is safe to run on a live, attached cockpit (the v1 prefix+R
// muscle memory).
func masterEnsure(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("ensure", flag.ContinueOnError)
	machinesFlag := fs.String("machines", "", "comma-separated machines (default: self + all peers; 'self' allowed)")
	tmuxConf := fs.String("tmux-conf", "", "tmux config file, used only when the master server must be CREATED")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if _, err := mtmux(cfg, "has-session", "-t", "="+masterSession); err != nil {
		// Not up: ensure == up.
		upArgs := []string{}
		if *machinesFlag != "" {
			upArgs = append(upArgs, "--machines", *machinesFlag)
		}
		if *tmuxConf != "" {
			upArgs = append(upArgs, "--tmux-conf", *tmuxConf)
		}
		return masterUp(cfg, upArgs)
	}
	machines, err := masterMachines(cfg, *machinesFlag)
	if err != nil {
		return err
	}
	out, err := mtmux(cfg, "list-windows", "-t", masterSession, "-F", "#{window_name}")
	if err != nil {
		return fmt.Errorf("master ensure: list-windows: %v: %s", err, out)
	}
	existing := map[string]bool{}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			existing[l] = true
		}
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	var created []string
	for _, m := range machines {
		if existing[m] {
			continue
		}
		winCmd := fmt.Sprintf("%s master window %s", shellQuote(self), shellQuote(m))
		if out, err := mtmux(cfg, "new-window", "-d", "-t", masterSession+":", "-n", m, winCmd); err != nil {
			return fmt.Errorf("master ensure: new-window %s: %v: %s", m, err, out)
		}
		created = append(created, m)
	}
	if len(created) == 0 {
		fmt.Printf("master ensure: all windows present (%s)\n", strings.Join(machines, ", "))
	} else {
		fmt.Printf("master ensure: created %s\n", strings.Join(created, ", "))
	}
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

// workAttach builds a shell command that attaches into the work server on `socket`,
// first creating a holding "scratch" session (a $HOME shell) if the server has none.
// So a master window for a machine with NO live threads shows a usable shell instead
// of looping on "no sessions" — and it stays a client of the work server, so nav can
// switch it into a thread the instant one appears. `attach` (no -t) still prefers the
// most-recently-used real thread over the placeholder.
// conf (when non-empty) is the work server's `-f` config, applied if THIS attach is
// what starts the server (so an empty machine's scratch shell already carries sesh's
// tmux UI). The daemon applies the same conf when it starts the server for a thread.
// marker is the MasterClientMarker path on the WORK machine: before attaching, the
// script records "<tty> <pid>" — which, after the exec, are exactly the tmux client's
// #{client_name} and #{client_pid} — so nav's inner switch can target THIS window's
// client among many (other masters' windows, direct attaches). Rewritten on every
// reconnect, so it tracks the supervisor's current attach.
func workAttach(socket, conf, marker string) string {
	newSess := fmt.Sprintf(`tmux -L %s new-session -d -s scratch -c "$HOME"`, socket)
	if conf != "" {
		newSess = fmt.Sprintf(`tmux -f "%s" -L %s new-session -d -s scratch -c "$HOME"`, conf, socket)
	}
	return fmt.Sprintf(
		`tmux -L %[1]s list-sessions >/dev/null 2>&1 || %[2]s; printf '%%s %%s\n' "$(tty)" "$$" > "%[3]s"; exec tmux -L %[1]s attach`,
		socket, newSess, marker)
}

// masterAttachCommand returns a factory that builds the attach process for a machine:
// locally (self) or over `ssh -t` (a peer; work socket from the peer registry). Both
// go through workAttach so an empty work server falls back to a holding shell.
func masterAttachCommand(cfg config.Config, machine string) (func() *exec.Cmd, error) {
	if machine == cfg.Machine {
		marker := tmux.MasterClientMarker(cfg.Home, cfg.Machine)
		return func() *exec.Cmd {
			c := exec.Command("sh", "-c", workAttach(cfg.TmuxSocket, cfg.TmuxConf, marker))
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
	if peer.Home == "" {
		return nil, fmt.Errorf("master window: peer %q has no home (see `sesh peer add --home`)", machine)
	}
	marker := tmux.MasterClientMarker(peer.Home, cfg.Machine)
	remote := "env -u TMUX sh -c " + shellQuote(workAttach(peer.TmuxSocket, peer.TmuxConf, marker))
	sshArgs := append([]string{"-tt", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no"}, peer.SSHArgs()...)
	sshArgs = append(sshArgs, peer.SSH, remote)
	return func() *exec.Cmd {
		c := exec.Command("ssh", sshArgs...)
		c.Env = tmuxCleanEnv()
		return c
	}, nil
}

// masterWatchers lists the origin machines whose master cockpit currently has a LIVE
// window-attach into THIS machine's work server — "who is watching me". Each master
// window's attach records "<client_name> <client_pid>" in MasterClientMarker(home,
// origin); an origin counts only if that exact pair is a current client of the work
// server, so a stale marker from a torn-down master never counts. Consumers (e.g.
// myrig's mt-copy-to-master) use this to route "send to my master" without guessing.
func masterWatchers(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("watchers", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	watchers, err := liveWatchers(cfg)
	if err != nil {
		return err
	}
	if *asJSON {
		return emitJSON(struct {
			Schema   int      `json:"schema"`
			Watchers []string `json:"watchers"`
		}{api.SchemaVersion, watchers})
	}
	for _, w := range watchers {
		fmt.Println(w)
	}
	return nil
}

func liveWatchers(cfg config.Config) ([]string, error) {
	markers, err := filepath.Glob(filepath.Join(cfg.Home, "master-client.*"))
	if err != nil {
		return nil, err
	}
	// No work server / no clients => no watchers (a legitimate state, not an error).
	live := map[string]bool{}
	if out, err := exec.Command("tmux", "-L", cfg.TmuxSocket, "list-clients", "-F", "#{client_name} #{client_pid}").Output(); err == nil {
		for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				live[l] = true
			}
		}
	}
	watchers := []string{}
	for _, m := range markers {
		origin := strings.TrimPrefix(filepath.Base(m), "master-client.")
		if origin == "" {
			continue
		}
		b, err := os.ReadFile(m)
		if err != nil {
			continue // marker vanished mid-scan (supervisor reconnecting) — not live now
		}
		if live[strings.TrimSpace(string(b))] {
			watchers = append(watchers, origin)
		}
	}
	sort.Strings(watchers)
	return watchers, nil
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
