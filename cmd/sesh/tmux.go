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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/client"
	"github.com/lukastk/sesh/internal/config"
	"github.com/lukastk/sesh/internal/peers"
	"github.com/lukastk/sesh/internal/tmux"
)

// runTmux implements `sesh tmux <current|info|create-session|create-pane|
// send-text|stage-file>`. All but `current` are served by the local daemon;
// `current` is client-side (it needs the caller's $TMUX).
func runTmux(args []string) error {
	if len(args) == 0 {
		return printGroupHelp("tmux")
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
	case "master-current":
		return tmuxMasterCurrent(cfg, rest)
	default:
		return fmt.Errorf("unknown tmux subcommand %q", sub)
	}
}

// tmuxMasterCurrent prints the thread id the origin master's window is currently
// showing on this daemon's work server (empty line = none). Routed with --machine to
// query a specific machine's work server. Used by the TUI's async master-cursor resolve.
func tmuxMasterCurrent(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("master-current", flag.ContinueOnError)
	origin := fs.String("origin", "", "the master's machine (whose marker to read) (required)")
	asSession := fs.Bool("session", false, "print the current SESSION name instead of the thread id (for the prefix+a picker)")
	asJSON := fs.Bool("json", false, "emit the full {thread_id, session, window} (the prefix+L 'last window' recorder)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *origin == "" {
		return errors.New("master-current: --origin is required")
	}
	session, tid, window, err := daemonClient(cfg).TmuxMasterCurrent(context.Background(), *origin)
	if err != nil {
		return err
	}
	switch {
	case *asJSON:
		return emitJSON(api.MasterCurrentResponse{Schema: api.SchemaVersion, ThreadID: tid, Session: session, Window: window})
	case *asSession:
		fmt.Println(session)
	default:
		fmt.Println(tid)
	}
	return nil
}

// tmuxNav implements `sesh tmux nav --to <machine>:<session>` — the nav
// primitive. From the mymastertmux client it (1) switches the OUTER client to
// machine M's window, then (2) drives M's INNER mytmux to switch-client to the
// target session (over ssh for a remote M), with the detached "bare-shell kick".
func tmuxNav(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("nav", flag.ContinueOnError)
	to := fs.String("to", "", "target as <machine>:<session> (required)")
	inClient := fs.Bool("in-client", false, "switch the CURRENT tmux client to the target (local target on the current work socket only; no master)")
	clientFlag := fs.String("client", "", "with --in-client: the exact tmux client to switch (e.g. the TUI resolves its own client before grabbing the terminal); required when stdin is not a tty")
	attach := fs.Bool("attach", false, "ATTACH this terminal to the target thread (for use from a plain shell outside tmux); REPLACES the process")
	thread := fs.String("thread", "", "the thread id whose pane to land on: nav targets the WINDOW holding its @sesh-thread-id pane, not just the session's last-active window (omit for a plain session nav)")
	last := fs.Bool("last", false, "switch to the PREVIOUS sesh-nav location — the cross-machine 'last window' toggle (ignores --to)")
	windowFlag := fs.Int("window", -1, "explicit window index to land on (internal: prefix+L replays a recorded location)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	windowTarget := *windowFlag
	// prefix+L: target the previously-recorded location instead of --to. Read it BEFORE
	// recording the current location below (which overwrites it) so the two swap = toggle.
	if *last {
		m, s, w, ok := readNavPrev(cfg.Home)
		if !ok {
			return errors.New("nav --last: no previous sesh-nav location yet (navigate somewhere first)")
		}
		*to = m + ":" + s
		windowTarget = w
		*thread = "" // an explicit recorded window overrides thread-window resolution
	}
	machine, session, ok := strings.Cut(*to, ":")
	if !ok || machine == "" || session == "" {
		return errors.New("nav: --to must be <machine>:<session>")
	}
	// Record where the cockpit is NOW (the "from"), so the NEXT prefix+L returns here.
	// Master-path only (the cockpit carries SESH_NAV_CLIENT); best-effort — a failure to
	// resolve just skips the history update and never blocks the nav.
	if !*attach && !*inClient {
		if cm, cs, cw, ok := resolveMasterLocation(cfg); ok {
			writeNavPrev(cfg.Home, cm, cs, cw)
		}
	}
	// windowStr is the explicit window index (recorded location), "" when unset — it
	// overrides the thread-window resolution in the switch below.
	windowStr := ""
	if windowTarget >= 0 {
		windowStr = strconv.Itoa(windowTarget)
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
			// Land on the thread's WINDOW: attach shows the session's active window, so
			// set it to the thread's pane's window first (best-effort; a plain session
			// nav with no --thread leaves the active window as-is).
			if tgt := threadWindowTarget(cfg.TmuxSocket, session, *thread, windowStr); tgt != "="+session {
				exec.Command("tmux", "-L", cfg.TmuxSocket, "select-window", "-t", tgt).Run() //nolint:errcheck — best-effort
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
		sock := shellQuote(peer.TmuxSocket)
		remote := "env -u TMUX tmux -L " + sock + " attach -t " + shellQuote(session)
		if *thread != "" {
			// Land on the thread's window: resolve it on the peer (in-shell, no extra
			// round-trip) and select it before attaching. S holds the session literal so
			// `=$S:$w` is safe for session names with spaces/slashes.
			filter := shellQuote("#{==:#{" + tmux.ThreadIDOption + "}," + *thread + "}")
			remote = "S=" + shellQuote(session) + "; " +
				"w=$(tmux -L " + sock + " list-panes -s -t \"=$S\" -f " + filter + " -F '#{window_index}' 2>/dev/null | head -n1); " +
				"[ -n \"$w\" ] && tmux -L " + sock + " select-window -t \"=$S:$w\" 2>/dev/null; " +
				"env -u TMUX tmux -L " + sock + " attach -t \"$S\""
		}
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
		// WHICH client — this must be EXACT, never ambient: tmux's "current client"
		// for a command client is an arbitrary pick (it cannot identify a piped
		// subprocess, a popup's pty, or even a pane's pty as any attached client —
		// observed live moving a master window's attach instead of the invoker).
		// Deterministic carriers, in order:
		//   1. --client          (the caller resolved it, e.g. the TUI)
		//   2. $SESH_NAV_CLIENT  (the keybinding expanded #{client_name} via run-shell
		//                         and baked it into the popup env — see the myrig confs)
		//   3. $TMUX_PANE's session has EXACTLY ONE client (a directly-run command in
		//      an unshared pane — unambiguous without any carrier)
		// Anything else is ambiguous => loud error, never a wrong-client switch.
		client := *clientFlag
		if client == "" {
			client = os.Getenv("SESH_NAV_CLIENT")
		}
		if client == "" {
			pane := os.Getenv("TMUX_PANE")
			if pane == "" {
				return errors.New("nav --in-client: cannot identify the invoking client ($TMUX_PANE unset) — pass --client")
			}
			sessOut, err := exec.Command("tmux", "-L", cfg.TmuxSocket, "display-message", "-p", "-t", pane, "#{session_name}").Output()
			if err != nil {
				return fmt.Errorf("nav --in-client: resolve %s's session: %v", pane, err)
			}
			sess := strings.TrimSpace(string(sessOut))
			clOut, err := exec.Command("tmux", "-L", cfg.TmuxSocket, "list-clients", "-t", "="+sess, "-F", "#{client_name}").Output()
			if err != nil {
				return fmt.Errorf("nav --in-client: list clients of %q: %v", sess, err)
			}
			var cls []string
			for _, l := range strings.Split(strings.TrimSpace(string(clOut)), "\n") {
				if l = strings.TrimSpace(l); l != "" {
					cls = append(cls, l)
				}
			}
			switch len(cls) {
			case 1:
				client = cls[0]
			case 0:
				return fmt.Errorf("nav --in-client: no client attached to session %q", sess)
			default:
				return fmt.Errorf("nav --in-client: %d clients on session %q — ambiguous; use the sesh keybindings (which carry the client) or pass --client", len(cls), sess)
			}
		}
		// Target the thread's WINDOW (=session:window) so a multi-window session lands
		// on the thread's pane, not the session's last-active window. Falls back to the
		// bare session when no thread id is given or its pane can't be resolved.
		target := threadWindowTarget(cfg.TmuxSocket, session, *thread, windowStr)
		if out, err := exec.Command("tmux", "-L", cfg.TmuxSocket, "switch-client", "-c", client, "-t", target).CombinedOutput(); err != nil {
			return fmt.Errorf("nav --in-client: switch-client %s: %v: %s", client, err, out)
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
		script := tmux.InnerSwitchScript(cfg.TmuxSocket, session, *thread, windowStr, tmux.MasterClientMarker(cfg.Home, cfg.Machine))
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

	// The inner switch FOLLOWS the peer's transport (same rule as every other
	// cross-machine command). For an http peer the remote daemon runs the
	// switch-client in-process — no ssh hop on this interactive hot path.
	if peer.Transport() == "http" {
		token, err := peer.ResolveAPIToken()
		if err != nil {
			return err
		}
		c := client.NewRemote(peer.ApiAddr, token)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var wptr *int
		if windowTarget >= 0 {
			wptr = &windowTarget
		}
		if err := c.TmuxNav(ctx, api.NavRequest{Session: session, Origin: cfg.Machine, ThreadID: *thread, Window: wptr}); err != nil {
			return fmt.Errorf("nav inner switch on %s (http): %w", machine, err)
		}
		return nil
	}

	// ssh transport: run the inner script over a MULTIPLEXED ssh — it rides the
	// warm ControlMaster the daemon's mesh-sync keeps open, so there's no cold
	// handshake on this hot path.
	if peer.TmuxSocket == "" {
		return fmt.Errorf("nav: peer %q has no tmux socket registered (see `sesh peer add --tmux-socket`)", machine)
	}
	if peer.Home == "" {
		return fmt.Errorf("nav: peer %q has no home registered (see `sesh peer add --home`)", machine)
	}
	script := tmux.InnerSwitchScript(peer.TmuxSocket, session, *thread, windowStr, tmux.MasterClientMarker(peer.Home, cfg.Machine))
	navArgs := append(peers.SSHMultiplexArgs(), peer.SSHArgs()...)
	navArgs = append(navArgs, peer.SSH, script)
	out, err := exec.Command("ssh", navArgs...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("nav inner switch on %s: %v: %s", machine, err, out)
	}
	return nil
}

// threadWindowTarget returns a switch-client / select-window target for a session:
// "=session:window". An EXPLICIT windowIndex (prefix+L replaying a recorded location)
// wins; otherwise it resolves the window holding the thread's @sesh-thread-id pane (so
// nav lands on the thread's OWN window in a multi-window session); else the bare
// "=session".
func threadWindowTarget(socket, session, threadID, windowIndex string) string {
	base := "=" + session
	if windowIndex != "" {
		return base + ":" + windowIndex
	}
	if threadID == "" {
		return base
	}
	out, err := exec.Command("tmux", "-L", socket, "list-panes", "-s", "-t", base,
		"-f", "#{==:#{"+tmux.ThreadIDOption+"},"+threadID+"}", "-F", "#{window_index}").Output()
	if err != nil {
		return base
	}
	w := strings.TrimSpace(string(out))
	if i := strings.IndexByte(w, '\n'); i >= 0 {
		w = w[:i] // first match wins (a thread has exactly one marked pane anyway)
	}
	if w == "" {
		return base // no marked pane (dead/just-revived single-window session) — session-level
	}
	return base + ":" + w
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
