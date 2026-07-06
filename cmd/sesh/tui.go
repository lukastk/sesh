package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

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
	showOffline := fs.Bool("show-offline", false, "show the last-known threads of OFFLINE mesh machines (default: hidden; toggle in-TUI with `o`)")
	cursor := fs.Bool("cursor", false, "start with the cursor on the current pane's thread ($SESH_TUI_PANE from a popup binding, else $TMUX_PANE)")
	filter := fs.Bool("filter", false, "start in filter mode (type-to-narrow immediately)")
	expand := fs.Bool("expand", false, "start with tree nodes expanded (default from [tui] expand_children)")
	columnsFlag := fs.String("columns", "", "comma-separated visible columns (default from [tui] columns in config.toml; valid: "+strings.Join(tui.ValidColumnNames(), ",")+")")
	editorFlag := fs.String("editor", "", "editor for in-TUI ticket field edits (default: [tui] editor, then $EDITOR)")
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
	// Per-column max-width overrides ([[tui.column_width]]) + the cap on/off default
	// ([tui] max_column_widths, unset = on). Unknown column / bad max is loud here.
	var widthSpecs []tui.ColumnWidthSpec
	if tcfg != nil {
		for _, cw := range tcfg.ColumnWidths {
			widthSpecs = append(widthSpecs, tui.ColumnWidthSpec{Name: cw.Name, Max: cw.Max})
		}
	}
	colWidths, err := tui.ResolveColumnWidths(widthSpecs)
	if err != nil {
		return fmt.Errorf("tui column widths: %w", err)
	}
	// Ticket-editor precedence: --editor flag > [tui] editor config > $EDITOR. Empty
	// is allowed (a loud error only fires if you try to edit a ticket field with none).
	editor := *editorFlag
	if editor == "" && tcfg != nil {
		editor = tcfg.Editor
	}
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	// Learn IDENTITY from the daemon (its machine + work/master sockets + home) so the
	// TUI resolves the right machine/socket for nav even when launched WITHOUT the SESH_*
	// env — i.e. a fast `zsh -fc` popup with no shell wrapper. The env-derived cfg
	// (SESH_MACHINE/SESH_TMUX_SOCKET, hostname/"mytmux" when unset) is the fallback for a
	// pre-schema-20 daemon or an unreachable one. The daemon's identity is also injected
	// into nav subprocesses (navEnv) so `sesh tmux nav` / `thread resume` target the right
	// servers regardless of the TUI's own env.
	localMachine, localSocket := cfg.Machine, cfg.TmuxSocket
	var navEnv []string
	{
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		st, serr := daemonClient(cfg).Status(ctx)
		cancel()
		if serr == nil {
			if st.Machine != "" {
				localMachine = st.Machine
			}
			if st.TmuxSocket != "" {
				localSocket = st.TmuxSocket
			}
			for _, kv := range [][2]string{
				{"SESH_HOME", st.Home}, {"SESH_MACHINE", st.Machine},
				{"SESH_TMUX_SOCKET", st.TmuxSocket}, {"SESH_MASTER_SOCKET", st.MasterSocket},
			} {
				if kv[1] != "" {
					navEnv = append(navEnv, kv[0]+"="+kv[1])
				}
			}
		}
	}
	// --all-machines from the flag OR the [tui] all_machines config default (the latter
	// lets the popup bindings drop the wrapper that used to add the flag).
	useAllMachines := *allMachines || (tcfg != nil && tcfg.AllMachines)
	// Offline machines' stale threads are hidden by default; --show-offline or
	// [tui] show_offline reveals them (the `o` key toggles it per-session either way).
	showOfflineDefault := *showOffline || (tcfg != nil && tcfg.ShowOffline)
	m := tui.New(cfg.SocketPath(), useAllMachines).WithShowOffline(showOfflineDefault).WithLocal(localMachine, localSocket).WithNavEnv(navEnv).WithColumns(cols).WithViews(compiled).WithColumnColors(colColors).WithEditor(editor).
		WithMaxColumnWidths(tcfg.MaxColumnWidthsDefault()).WithColumnWidths(colWidths)
	// Mouse-wheel sensitivity ([tui] mouse_scroll_v/h; default 1 = move every notch).
	if tcfg != nil {
		m = m.WithMouseScroll(tcfg.ScrollV(), tcfg.ScrollH())
	}
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
		// Use the daemon's work socket (localSocket), not the env default — under a
		// wrapper-less `zsh -fc` launch cfg.TmuxSocket is the bare "mytmux" default.
		id, err := tmux.ThreadIDOfPane(localSocket, pane)
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
	// WithMouseCellMotion enables mouse reporting so the grid responds to the wheel
	// (vertical scroll + horizontal pan — see Model.Update's tea.MouseMsg case). Trade-off:
	// the app captures the mouse, so plain-drag text selection needs Shift (terminal-native).
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
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
