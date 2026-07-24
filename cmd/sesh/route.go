package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/lukastk/sesh/internal/config"
	"github.com/lukastk/sesh/internal/peers"
)

// peerKnownOffline reports whether the LOCAL daemon's liveness cache currently has
// `machine` marked unreachable (its background probe flips a peer to reachable=0 within
// ~8s of it going offline). It is authoritative-but-conservative: a local daemon that
// is down, a machine absent from the cache (never synced), a machine still cached
// reachable, or any read error ALL return false — so routing only fast-fails a peer we
// DEFINITIVELY know is offline and otherwise dials live (no behavior change). The read
// is a local unix-socket call (bounded to 2s), which resolves in ms or fails fast when
// the daemon is down.
func peerKnownOffline(cfg config.Config, machine string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	mesh, err := daemonClient(cfg).Mesh(ctx)
	if err != nil {
		return false
	}
	for _, mv := range mesh.Machines {
		if mv.Machine == machine {
			return !mv.Self && !mv.Reachable
		}
	}
	return false
}

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
	sshArgs := append(peers.SSHMultiplexArgs(), peer.SSHArgs()...)
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
	case "peer", "matrix", "master", "help", "help-tree", "-h", "--help":
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
	// Fast-fail a peer the local daemon's liveness cache DEFINITIVELY knows is offline,
	// instead of stalling on a dial (15s HTTP client timeout, or an ssh TCP-connect
	// wait). Same failure the dial would eventually produce — just instant and clearer.
	// Conservative: an unknown/never-synced peer, a reachable peer, or a down local
	// daemon all fall through to the live dial (no behavior change).
	if peerKnownOffline(cfg, machine) {
		return false, fmt.Errorf("machine %q is offline per the local mesh cache; not routing (check `sesh mesh`, retry when it is back)", machine)
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

// nudgeLocalMesh tells the LOCAL daemon to re-sync its cached snapshot of
// `machine` NOW (POST /v1/mesh/nudge, schema 45) — called after a successfully
// ROUTED command, so a mutation on the peer becomes visible in local reads
// (TUI/mesh) in ~an RTT instead of at the next sync-cadence tick. cfg must be
// the LOCAL config captured BEFORE routing (http routing points config.Load at
// the peer via SESH_REMOTE — the nudge must reach the local daemon).
//
// BEST-EFFORT BY DESIGN, errors dropped: the routed command already succeeded,
// and this is a pure freshness hint to a component that may legitimately be
// absent — the local daemon down (routing never needed it) or pre-45 (no such
// endpoint during a rolling deploy). Failing the user's successful command over
// it would be wrong. Not the silent-fallback anti-pattern: nothing is masked —
// without the nudge the cache simply catches up at the normal cadence (the
// pre-45 behavior), observable in `sesh mesh` synced_at.
func nudgeLocalMesh(cfg config.Config, machine string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	daemonClient(cfg).MeshNudge(ctx, machine) //nolint:errcheck — best-effort, see above
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
	}
	if peer.CodexHome != "" {
		// Client-side transcript reads (backup/copy) resolve codex's home from
		// the routed process's env, not the daemon's.
		remote = append(remote, "SESH_CODEX_HOME="+shellQuote(peer.CodexHome))
	}
	remote = append(remote, shellQuote(peer.Binary))
	for _, a := range rest {
		remote = append(remote, shellQuote(a))
	}
	sshArgs := append(peers.SSHMultiplexArgs(), peer.SSHArgs()...)
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
