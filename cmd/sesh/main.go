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
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
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
  tmux      tmux layer (current | info | create-session | create-pane | send-text | stage-file)
  matrix    report the feature-matrix state (grid | skips)

(more commands land as the development plan progresses)`)
}
