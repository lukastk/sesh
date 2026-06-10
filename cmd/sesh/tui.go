package main

import (
	"flag"
	"os"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/config"
	"github.com/lukastk/sesh/internal/tui"
)

// runTUI launches the live thread grid. It is a thin client over the local
// daemon's HTTP+JSON surface (use --all-machines to fan out across the mesh).
func runTUI(args []string) error {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	allMachines := fs.Bool("all-machines", false, "show threads from every machine in the mesh")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := config.Load()
	m := tui.New(cfg.SocketPath(), *allMachines).WithLocal(cfg.Machine, cfg.TmuxSocket)
	// WHICH tmux client this TUI renders on, for in-client nav's --client. The only
	// trustworthy carrier is the keybinding: the myrig confs run the TUI popup via
	// run-shell, which expands #{client_name} for the PRESSING client and bakes it
	// into $SESH_NAV_CLIENT. (In-process resolution is ambient/arbitrary — tmux can't
	// map a popup's or pane's pty back to a client; observed live switching a master
	// window's attach instead of the invoker.) Without it, nav falls back to the
	// unambiguous single-client-pane case or fails loudly — never a wrong client.
	if name := os.Getenv("SESH_NAV_CLIENT"); name != "" {
		m = m.WithClient(name)
	}
	p := tea.NewProgram(m, tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		return err
	}
	// Enter from a plain shell (outside tmux) quits the TUI to ATTACH the terminal to the
	// thread: now that the TUI has exited and restored the terminal, exec the attach so
	// this process BECOMES the thread (on detach the user returns to their shell).
	if fm, ok := final.(tui.Model); ok {
		if argv, attach := fm.PendingAttach(); attach {
			return syscall.Exec(argv[0], argv, os.Environ())
		}
	}
	return nil
}
