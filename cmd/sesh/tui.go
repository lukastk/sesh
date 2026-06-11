package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/config"
	"github.com/lukastk/sesh/internal/tmux"
	"github.com/lukastk/sesh/internal/tui"
)

// runTUI launches the live thread grid. It is a thin client over the local
// daemon's HTTP+JSON surface (use --all-machines to fan out across the mesh).
func runTUI(args []string) error {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	allMachines := fs.Bool("all-machines", false, "show threads from every machine in the mesh")
	cursor := fs.Bool("cursor", false, "start with the cursor on the current pane's thread ($SESH_TUI_PANE from a popup binding, else $TMUX_PANE)")
	filter := fs.Bool("filter", false, "start in filter mode (type-to-narrow immediately)")
	expand := fs.Bool("expand", false, "start with tree nodes expanded (default from [tui] expand_children)")
	columnsFlag := fs.String("columns", "", "comma-separated visible columns (default from [tui] columns in config.toml; valid: "+strings.Join(tui.ValidColumnNames(), ",")+")")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := config.Load()
	// Column set precedence: --columns flag > [tui] columns config > built-in
	// default. Unknown names are loud at every level.
	tcfg, err := config.LoadTUI(cfg.Home)
	if err != nil {
		return err
	}
	var wantCols []string
	switch {
	case *columnsFlag != "":
		wantCols = strings.Split(*columnsFlag, ",")
	case tcfg != nil && len(tcfg.Columns) > 0:
		wantCols = tcfg.Columns
	}
	cols, err := tui.ResolveColumns(wantCols)
	if err != nil {
		return fmt.Errorf("tui columns: %w", err)
	}
	// [[tui.column]] moves reposition individual columns over the base set.
	if tcfg != nil && len(tcfg.ColumnMoves) > 0 {
		var moves []tui.ColumnMove
		for _, mv := range tcfg.ColumnMoves {
			moves = append(moves, tui.ColumnMove{Name: mv.Name, After: mv.After, Before: mv.Before, Position: mv.Position})
		}
		if cols, err = tui.ApplyColumnMoves(cols, moves); err != nil {
			return fmt.Errorf("tui columns: %w", err)
		}
	}
	var views []tui.ViewSpec
	if tcfg != nil {
		for _, v := range tcfg.Views {
			views = append(views, tui.ViewSpec{Name: v.Name, Filter: v.Filter})
		}
	}
	compiled, err := tui.CompileViews(views)
	if err != nil {
		return err
	}
	// Per-column colours: built-in defaults (NAME blue, CWD green) overlaid by any
	// [[tui.column_color]] entries. Unknown column / bad colour is loud here.
	var colorSpecs []tui.ColumnColorSpec
	if tcfg != nil {
		for _, c := range tcfg.ColumnColors {
			colorSpecs = append(colorSpecs, tui.ColumnColorSpec{Name: c.Name, Color: c.Color})
		}
	}
	colColors, err := tui.ResolveColumnColors(colorSpecs)
	if err != nil {
		return fmt.Errorf("tui colors: %w", err)
	}
	m := tui.New(cfg.SocketPath(), *allMachines).WithLocal(cfg.Machine, cfg.TmuxSocket).WithColumns(cols).WithViews(compiled).WithColumnColors(colColors)
	// CWD column labels: compiled ONCE here, loud on a broken rule; per-cwd
	// label errors (unknown placeholder in a matching rule) are loud at startup
	// too — never a silently-wrong column. Inside the labeler a rule error is
	// impossible by then, so the fallback below cannot mask one.
	labels, err := config.LoadCwdLabels(cfg.Home)
	if err != nil {
		return err
	}
	if labels != nil {
		uh, _ := os.UserHomeDir()
		m = m.WithCwdLabeler(func(cwd string) string {
			out, lerr := labels.LabelFor(cwd, uh)
			if lerr != nil {
				return "!ERR " + lerr.Error()
			}
			return out
		})
	}
	if *cursor {
		// Which pane was the user on? From a popup the only honest carrier is the
		// binding's $SESH_TUI_PANE (the popup's own $TMUX_PANE is the popup); from a
		// plain pane shell $TMUX_PANE is the pane itself. A pane with no thread mark
		// is a legitimate no-preselect; a missing carrier with --cursor is loud.
		pane := os.Getenv("SESH_TUI_PANE")
		if pane == "" {
			pane = os.Getenv("TMUX_PANE")
		}
		if pane == "" {
			return errors.New("tui --cursor: neither $SESH_TUI_PANE nor $TMUX_PANE is set (not inside tmux?)")
		}
		id, err := tmux.ThreadIDOfPane(cfg.TmuxSocket, pane)
		if err != nil {
			return fmt.Errorf("tui --cursor: %w", err)
		}
		if id != "" {
			m = m.WithPreselect(id)
		}
	}
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
	// Master prefix+s: the binding bakes the active window's machine into
	// $SESH_TUI_MASTER_MACHINE (the popup's own pane is the popup, and the thread the
	// user is in lives on that machine's work server). The TUI resolves it
	// ASYNCHRONOUSLY after the first render, so prefix+s startup is never delayed.
	if mm := os.Getenv("SESH_TUI_MASTER_MACHINE"); mm != "" {
		m = m.WithMasterCursor(mm)
	}
	if *filter {
		m = m.WithFilterStart()
	}
	if *expand || (tcfg != nil && tcfg.ExpandChildren) {
		m = m.WithExpand(true)
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
