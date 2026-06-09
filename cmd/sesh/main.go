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

	// `--machine X` is a pseudo-global routing flag handled before dispatch: if X
	// is a remote peer, forward the whole command there via a real ssh hop. Local-
	// only meta commands (`peer`, `matrix`, help) are excluded — their own args may
	// legitimately contain `--machine` (e.g. `peer add --machine Q`).
	if routableSubcommand(os.Args[1]) {
		machine, rest := extractMachineFlag(os.Args[1:])
		if machine != "" {
			cfg := config.Load()
			if machine != cfg.Machine {
				if err := routeToMachine(cfg, machine, rest); err != nil {
					fmt.Fprintln(os.Stderr, "sesh:", err)
					os.Exit(1)
				}
				return
			}
			// machine == self: drop the flag and run locally.
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
	case "ticket":
		if err := runTicket(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "sesh ticket:", err)
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
  thread    thread layer (new|list|kill|pane|status|send|send-headless|headless-reply)
  ticket    ticket layer (create | list | set-status | needs-input | send-prompt)
  peer      mesh registry (add | list)
  matrix    report the feature-matrix state (grid | skips)

global:
  --machine X   route the command to remote machine X via a real ssh hop

(more commands land as the development plan progresses)`)
}
