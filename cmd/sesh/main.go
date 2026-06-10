// Command sesh is the single sesh v2 binary. It is mechanism, not UX: explicit
// flags, no magic defaults, machine-readable output. Ergonomics live in myrig
// wrappers, never here.
//
// Phase 0 ships only the `matrix` subcommand, which reports the feature-matrix
// state. Subsequent phases add daemon/tmux/thread/ticket subcommands.
package main

import (
	"fmt"
	"os"

	"github.com/lukastk/sesh/internal/config"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	// `--machine X` is a pseudo-global routing flag handled before dispatch: if X is
	// a remote peer, forward the command there over the peer's EXPLICIT transport —
	// ssh (a real ssh hop) or http (the peer's TCP API). Local-only meta commands
	// (`peer`, `matrix`, help) are excluded — their own args may legitimately contain
	// `--machine` (e.g. `peer add --machine Q`).
	if routableSubcommand(os.Args[1]) {
		machine, rest := extractMachineFlag(os.Args[1:])
		if machine != "" {
			cfg := config.Load()
			if machine != cfg.Machine {
				// handled=true: ran remotely over ssh (done). handled=false: pointed
				// this process at the peer's TCP API (SESH_REMOTE now set) — continue
				// local dispatch, where daemonClient reaches the peer.
				handled, err := routeMachine(cfg, machine, rest)
				if err != nil {
					fmt.Fprintln(os.Stderr, "sesh:", err)
					os.Exit(1)
				}
				if handled {
					return
				}
			}
			// machine == self, or http-routed: drop the flag and run locally.
			os.Args = append([]string{os.Args[0]}, rest...)
		}
	}

	switch os.Args[1] {
	case "matrix":
		if err := runMatrix(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "sesh matrix:", err)
			os.Exit(1)
		}
	case "daemon":
		if err := runDaemon(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "sesh daemon:", err)
			os.Exit(1)
		}
	case "tmux":
		if err := runTmux(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "sesh tmux:", err)
			os.Exit(1)
		}
	case "thread":
		if err := runThread(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "sesh thread:", err)
			os.Exit(1)
		}
	case "resume": // top-level alias for `sesh thread resume`
		if err := runThread(append([]string{"resume"}, os.Args[2:]...)); err != nil {
			fmt.Fprintln(os.Stderr, "sesh resume:", err)
			os.Exit(1)
		}
	case "ticket":
		if err := runTicket(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "sesh ticket:", err)
			os.Exit(1)
		}
	case "tui":
		if err := runTUI(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "sesh tui:", err)
			os.Exit(1)
		}
	case "info":
		if err := runInfo(config.Load(), os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "sesh info:", err)
			os.Exit(1)
		}
	case "delegate":
		if err := runDelegate(config.Load(), os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "sesh delegate:", err)
			os.Exit(1)
		}
	case "await":
		if err := runAwait(config.Load(), os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "sesh await:", err)
			os.Exit(1)
		}
	case "hooks":
		if err := runHooks(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "sesh hooks:", err)
			os.Exit(1)
		}
	case "cwd-label":
		if err := runCwdLabel(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "sesh cwd-label:", err)
			os.Exit(1)
		}
	case "mesh":
		if err := runMesh(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "sesh mesh:", err)
			os.Exit(1)
		}
	case "master":
		if err := runMaster(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "sesh master:", err)
			os.Exit(1)
		}
	case "peer":
		if err := runPeer(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "sesh peer:", err)
			os.Exit(1)
		}
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "sesh: unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `sesh — multi-machine coding-agent session management

usage: sesh <command> [args]

commands:
  daemon    per-machine daemon (run | start | stop | status)
  tmux      tmux layer (current|info|create-session|create-pane|send-text|stage-file|nav)
  thread    thread layer (new|list|stop|delete|resume|rename|tag|archive|pane|status|send|send-headless|headless-reply)
  ticket    ticket layer (create | list | set-status | needs-input | send-prompt)
  tui       cross-machine TUI (--all-machines)
  mesh      print the merged cross-machine view
  master    master-tmux cockpit (up | window | attach | down)
  peer      mesh registry (add | list)
  matrix    report the feature-matrix state (grid | skips)

global:
  --machine X   route the command to remote machine X via a real ssh hop

(more commands land as the development plan progresses)`)
}
