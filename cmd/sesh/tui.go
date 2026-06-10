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
