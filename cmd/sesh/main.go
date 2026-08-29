// Command sesh is the single sesh v2 binary. It is mechanism, not UX: explicit
// flags or explicit config policy, no hidden magic defaults, machine-readable
// output. Ergonomics live in myrig wrappers, never here.
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

	// Help is intercepted BEFORE routing/dispatch so `sesh <cmd> [sub] --help`,
	// `sesh -h`, and `sesh help <cmd> [sub]` all print the registry's help (stdout,
	// exit 0) regardless of any other flags (e.g. a stray --machine).
	if path, ok := resolveHelpRequest(os.Args[1:]); ok {
		printCommandHelp(path)
		return
	}

	// `--allow-unverified` is a pseudo-global too (see extractAllowUnverifiedFlag):
	// strip it and record it before anything reads the subcommand, so it works
	// whether it precedes or follows the verb.
	if allow, rest := extractAllowUnverifiedFlag(os.Args[1:]); allow {
		allowUnverifiedCurrent = true
		os.Args = append([]string{os.Args[0]}, rest...)
		if len(os.Args) < 2 {
			usage()
			os.Exit(2)
		}
	}

	// `--machine X` is a pseudo-global routing flag handled before dispatch: if X is
	// a remote peer, forward the command there over the peer's EXPLICIT transport —
	// ssh (a real ssh hop) or http (the peer's TCP API). Local-only meta commands
	// (`peer`, `matrix`, help) are excluded — their own args may legitimately contain
	// `--machine` (e.g. `peer add --machine Q`).
	//
	// postRouteNudge, when set, fires after a ROUTED command completes successfully
	// (error paths os.Exit before reaching it): it asks the LOCAL daemon to re-sync
	// its cached view of the routed-to peer now, so a routed mutation shows up in
	// local reads in ~an RTT (see nudgeLocalMesh). Set for the http-routed
	// continue-local-dispatch path; the ssh-handled path nudges inline below.
	var postRouteNudge func()
	if routableSubcommand(os.Args[1]) {
		machine, rest := extractMachineFlag(os.Args[1:])
		if machine != "" {
			cfg := config.Load() // the LOCAL config — captured before routing can set SESH_REMOTE
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
					nudgeLocalMesh(cfg, machine)
					return
				}
				postRouteNudge = func() { nudgeLocalMesh(cfg, machine) }
			}
			// machine == self, or http-routed: drop the flag and run locally.
			os.Args = append([]string{os.Args[0]}, rest...)
		}
	}

	switch os.Args[1] {
	case "help-tree":
		fmt.Print(renderHelpTree())
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
	case "shell":
		if err := runShell(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "sesh shell:", err)
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
	case "blob":
		if err := runBlob(config.Load(), os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "sesh blob:", err)
			os.Exit(1)
		}
	case "fs":
		if err := runFs(config.Load(), os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "sesh fs:", err)
			os.Exit(1)
		}
	case "plugins":
		if err := runPlugins(config.Load(), os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "sesh plugins:", err)
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
	case "meta":
		if err := runMeta(config.Load(), os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "sesh meta:", err)
			os.Exit(1)
		}
	case "backup":
		if err := runBackup(config.Load(), os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "sesh backup:", err)
			os.Exit(1)
		}
	case "restore":
		if err := runRestore(config.Load(), os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "sesh restore:", err)
			os.Exit(1)
		}
	case "copy":
		if err := runCopy(config.Load(), os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "sesh copy:", err)
			os.Exit(1)
		}
	case "tail":
		if err := runTail(config.Load(), os.Args[2:], false); err != nil {
			fmt.Fprintln(os.Stderr, "sesh tail:", err)
			os.Exit(1)
		}
	case "transcript":
		if err := runTail(config.Load(), os.Args[2:], true); err != nil {
			fmt.Fprintln(os.Stderr, "sesh transcript:", err)
			os.Exit(1)
		}
	case "subscribe":
		if err := runSubscribe(config.Load(), os.Args[2:], false); err != nil {
			fmt.Fprintln(os.Stderr, "sesh subscribe:", err)
			os.Exit(1)
		}
	case "unsubscribe":
		if err := runSubscribe(config.Load(), os.Args[2:], true); err != nil {
			fmt.Fprintln(os.Stderr, "sesh unsubscribe:", err)
			os.Exit(1)
		}
	case "subscriptions":
		if err := runSubscriptions(config.Load(), os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "sesh subscriptions:", err)
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
	case "import":
		if err := runImport(config.Load(), os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "sesh import:", err)
			os.Exit(1)
		}
	case "doctor":
		if err := runDoctor(config.Load(), os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "sesh doctor:", err)
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
	default:
		fmt.Fprintf(os.Stderr, "sesh: unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	// Reached only when an http-routed command dispatched above and SUCCEEDED
	// (every failure path exits). See postRouteNudge above.
	if postRouteNudge != nil {
		postRouteNudge()
	}
}

// usage is the BRIEF error-path message (stderr). Rich help (stdout, exit 0) is the
// help registry, reached via `sesh help` / `sesh <cmd> --help` (see help.go).
func usage() {
	fmt.Fprintln(os.Stderr, "sesh — multi-machine coding-agent session management")
	fmt.Fprintln(os.Stderr, "\nusage: sesh <command> [args]")
	fmt.Fprintln(os.Stderr, "run `sesh help` for the command list, or `sesh <command> --help` for one command.")
}
