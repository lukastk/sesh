package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/lukastk/sesh/internal/config"
	"github.com/lukastk/sesh/internal/peers"
)

// routableSubcommand reports whether `--machine` routing applies to this
// subcommand. Local-only meta commands are excluded.
func routableSubcommand(sub string) bool {
	switch sub {
	case "peer", "matrix", "help", "-h", "--help":
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

// routeToMachine runs the command on machine via a real ssh hop into that
// machine's daemon (the honest remote path). The forwarded command carries NO
// --machine flag, so it runs against the peer's OWN local daemon. stdout/stderr/
// stdin are wired straight through.
func routeToMachine(cfg config.Config, machine string, rest []string) error {
	reg, err := peers.Load(cfg.PeersPath())
	if err != nil {
		return err
	}
	peer, ok := reg.Get(machine)
	if !ok {
		return fmt.Errorf("unknown machine %q: no peer registered (see `sesh peer add`)", machine)
	}

	remote := []string{"env", "SESH_HOME=" + shellQuote(peer.Home), shellQuote(peer.Binary)}
	for _, a := range rest {
		remote = append(remote, shellQuote(a))
	}
	sshArgs := []string{
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=no",
		peer.SSH,
		strings.Join(remote, " "),
	}
	cmd := exec.Command("ssh", sshArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
