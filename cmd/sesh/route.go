package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/lukastk/sesh/internal/config"
	"github.com/lukastk/sesh/internal/peers"
)

// stageFileRemote stages content onto a remote machine by piping the BYTES over
// ssh to the peer running `tmux stage-file --stdin` (the file is on this machine,
// so we cannot just route the path). The peer's printed staged path is relayed.
func stageFileRemote(cfg config.Config, machine, name string, content []byte) error {
	reg, err := peers.Load(cfg.PeersPath())
	if err != nil {
		return err
	}
	peer, ok := reg.Get(machine)
	if !ok {
		return fmt.Errorf("unknown machine %q: no peer registered (see `sesh peer add`)", machine)
	}
	remote := strings.Join([]string{
		"env",
		"SESH_HOME=" + shellQuote(peer.Home),
		"SESH_MACHINE=" + shellQuote(peer.Machine),
		shellQuote(peer.Binary),
		"tmux", "stage-file", "--to", shellQuote(peer.Machine), "--stdin", "--name", shellQuote(name),
	}, " ")
	sshArgs := append([]string{"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no"}, peer.SSHArgs()...)
	sshArgs = append(sshArgs, peer.SSH, remote)
	cmd := exec.Command("ssh", sshArgs...)
	cmd.Stdin = bytes.NewReader(content)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("remote stage-file: %w", err)
	}
	fmt.Print(string(out))
	return nil
}

// routableSubcommand reports whether `--machine` routing applies to this
// subcommand. Local-only meta commands are excluded.
func routableSubcommand(sub string) bool {
	switch sub {
	case "peer", "matrix", "master", "help", "-h", "--help":
		return false
	default:
		return true
	}
}

// extractMachineFlag pulls a pseudo-global `--machine X` / `--machine=X` out of
// args (from anywhere), returning the value and the args with it removed.
// `--machine` is handled here, before subcommand dispatch, so a single chokepoint
// routes ANY command to a remote machine — and the subcommands never need to know
// about routing.
func extractMachineFlag(args []string) (machine string, rest []string) {
	rest = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--machine" || a == "-machine":
			if i+1 < len(args) {
				machine = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--machine="):
			machine = strings.TrimPrefix(a, "--machine=")
		case strings.HasPrefix(a, "-machine="):
			machine = strings.TrimPrefix(a, "-machine=")
		default:
			rest = append(rest, a)
		}
	}
	return machine, rest
}

// httpRoutable reports whether `--machine` routing for this command MAY go over the
// peer's HTTP API (when the peer has one). The carve-outs need remote EXECUTION and
// stay on ssh regardless of the peer's transport: daemon lifecycle (you cannot start
// a not-yet-running daemon via its own API), tmux nav (outer + inner switch-client),
// tmux stage-file (pipes local bytes). Everything else is a client-only call the
// peer's daemon serves identically over its TCP API.
func httpRoutable(rest []string) bool {
	if len(rest) == 0 {
		return false
	}
	switch rest[0] {
	case "daemon":
		return false
	case "tmux":
		if len(rest) >= 2 && (rest[1] == "nav" || rest[1] == "stage-file") {
			return false
		}
		return true
	default:
		return true
	}
}

// routeMachine routes a `--machine` command to a remote peer over the peer's EXPLICIT
// transport (peers.Peer.Transport) — never an automatic fallback. It returns:
//   - handled=true: it ran the command remotely itself (ssh transport); the caller
//     is done.
//   - handled=false: it has pointed THIS process at the peer's TCP API by setting
//     SESH_REMOTE/SESH_API_TOKEN (http transport); the caller should drop --machine
//     and continue LOCAL dispatch, where daemonClient reaches the peer's API.
//
// An http peer with an unresolvable token is a LOUD error, not a silent ssh attempt.
func routeMachine(cfg config.Config, machine string, rest []string) (handled bool, err error) {
	reg, err := peers.Load(cfg.PeersPath())
	if err != nil {
		return false, err
	}
	peer, ok := reg.Get(machine)
	if !ok {
		return false, fmt.Errorf("unknown machine %q: no peer registered (see `sesh peer add`)", machine)
	}
	// HTTP transport for a client-only command: aim this process at the peer's API.
	if peer.Transport() == "http" && httpRoutable(rest) {
		token, err := peer.ResolveAPIToken()
		if err != nil {
			return false, err
		}
		os.Setenv("SESH_REMOTE", peer.ApiAddr)
		os.Setenv("SESH_API_TOKEN", token)
		return false, nil
	}
	// SSH transport (the default, and the only path for the carve-outs).
	return true, routeToMachineSSH(peer, rest)
}

// routeToMachineSSH runs the command on the peer via a real ssh hop into that
// machine's daemon (the honest remote path). The forwarded command carries NO
// --machine flag, so it runs against the peer's OWN local daemon. stdout/stderr/
// stdin are wired straight through.
func routeToMachineSSH(peer peers.Peer, rest []string) error {
	// Forward SESH_HOME and the peer's own SESH_MACHINE so the remote command
	// runs against the peer's daemon AND knows its own identity (so it won't
	// recursively re-route, e.g. ticket-owner auto-routing).
	remote := []string{
		"env",
		"SESH_HOME=" + shellQuote(peer.Home),
		"SESH_MACHINE=" + shellQuote(peer.Machine),
		shellQuote(peer.Binary),
	}
	for _, a := range rest {
		remote = append(remote, shellQuote(a))
	}
	sshArgs := append([]string{"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no"}, peer.SSHArgs()...)
	sshArgs = append(sshArgs, peer.SSH, strings.Join(remote, " "))
	cmd := exec.Command("ssh", sshArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
