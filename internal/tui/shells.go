package tui

// The shells viewer (`S` / the `shells` command): a full-screen takeover that
// lists every tmux SESSION on every reachable machine's work server — the
// sessions the grid cannot show, because a plain session has no thread record.
//
// It exists because the cockpit IS tmux sessions, and sesh has so far only ever
// shown the ones running an agent. A session started by hand (mmt-enter-box, an
// ad-hoc mt-create-session, anything) is invisible to the TUI even though it is
// sitting right there in the cockpit.
//
// Sessions are enumerated LIVE and never recorded (see _dev/SHELL.md): the
// listing execs `sesh tmux info` per machine, exactly as the myrig fzf pickers
// do, and holds nothing. That is deliberate — recording every session would mint
// a record per throwaway shell, churn the mesh, and force sesh to auto-delete
// records, which it does nowhere else.
//
// Classification lives in the DAEMON (see daemon.classifySession), not here: it
// is mechanism, so every client — this viewer, sesh-ui, a script — gets the same
// answer. The viewer fans `sesh shell sessions --json` out per machine and
// renders what comes back.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/api"
)

// sessionRow is one row of the viewer: a classified live session (from the
// daemon) plus display-only thread NAMES resolved against the grid's rows.
// Nothing here is persisted — sessions are enumerated live every fan-out.
type sessionRow struct {
	api.ShellSession
	// Threads names the agent threads whose panes live in this session, resolved
	// from the grid's rows so it costs no extra fetch (ids fall back to a short
	// form when the grid does not carry that thread).
	Threads []string
}

// promotable reports whether `P` can turn this session into a tracked shell
// thread: anything that is not already one. A STALE marker is promotable —
// re-promoting is the repair for a record that went away without unstamping.
func (r sessionRow) promotable() bool { return r.Class != api.ShellClassShell }

// shellsLoadedMsg carries a completed fan-out. errs are per-machine failures,
// reported alongside whatever DID load — one unreachable machine must not blank
// the whole viewer.
type shellsLoadedMsg struct {
	rows []sessionRow
	errs []string
}

// shellActionMsg is the result of a viewer action (kill). reload re-runs the
// fan-out so the list reflects what actually happened.
type shellActionMsg struct {
	note   string
	err    error
	reload bool
}

// openShells opens the viewer and kicks the fan-out.
func (m Model) openShells() (tea.Model, tea.Cmd) {
	m.shells = true
	m.shellCursor = 0
	m.shellRows = nil
	m.shellErrs = nil
	m.shellNote = ""
	m.shellConfirmKill = false
	m.shellLoading = true
	return m, m.loadShells()
}

// reachableMachines is the set the viewer fans out to: every machine the mesh
// currently reports reachable. An offline machine is SKIPPED rather than probed
// — the same rule the grid's offline gate applies, and the reason the viewer
// cannot hang on an asleep peer.
func (m Model) reachableMachines() []string {
	var out []string
	for _, mv := range m.machines {
		if mv.Reachable {
			out = append(out, mv.Machine)
		}
	}
	// No mesh view at all (a hand-built Model, or the first frame before the
	// first fetch lands): fall back to this machine, which is always reachable
	// from itself. Never a silent empty list.
	if len(out) == 0 && m.machine != "" {
		out = []string{m.machine}
	}
	sort.Strings(out)
	return out
}

// loadShells fans out `sesh tmux info` across the reachable machines CONCURRENTLY
// and classifies the result. It execs the binary (rather than m.client) for the
// same reason every routed verb does: the local daemon does not own a peer's tmux
// server, so the call has to be routed with --machine.
func (m Model) loadShells() tea.Cmd {
	bin, env, self := m.binaryPath, m.navEnv, m.machine
	machines := m.reachableMachines()
	// Thread names by id, for the "what agent threads live in here" column.
	names := map[string]string{}
	for _, r := range m.rows {
		names[r.ID] = r.Name
	}
	return func() tea.Msg {
		type result struct {
			rows []sessionRow
			err  string
		}
		results := make([]result, len(machines))
		var wg sync.WaitGroup
		for i, machine := range machines {
			wg.Add(1)
			go func(i int, machine string) {
				defer wg.Done()
				args := []string{"shell", "sessions", "--json"}
				if self == "" || machine != self {
					args = append(args, "--machine", machine)
				}
				cmd := exec.Command(bin, args...)
				cmd.Env = append(os.Environ(), env...)
				out, err := cmd.Output()
				if err != nil {
					results[i] = result{err: fmt.Sprintf("%s: %v: %s", machine, err, execStderr(err))}
					return
				}
				rows, perr := parseSessionRows(machine, out, names)
				if perr != nil {
					results[i] = result{err: fmt.Sprintf("%s: %v", machine, perr)}
					return
				}
				results[i] = result{rows: rows}
			}(i, machine)
		}
		wg.Wait()
		msg := shellsLoadedMsg{}
		for _, r := range results {
			msg.rows = append(msg.rows, r.rows...)
			if r.err != "" {
				msg.errs = append(msg.errs, r.err)
			}
		}
		sort.SliceStable(msg.rows, func(i, j int) bool {
			if msg.rows[i].Machine != msg.rows[j].Machine {
				return msg.rows[i].Machine < msg.rows[j].Machine
			}
			return msg.rows[i].Name < msg.rows[j].Name
		})
		return msg
	}
}

// parseSessionRows turns `sesh shell sessions --json` JSONL into viewer rows,
// resolving agent-thread ids to names against the grid.
func parseSessionRows(machine string, out []byte, names map[string]string) ([]sessionRow, error) {
	var rows []sessionRow
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var ss api.ShellSession
		if err := json.Unmarshal([]byte(line), &ss); err != nil {
			return nil, fmt.Errorf("decode session: %w", err)
		}
		if ss.Machine == "" {
			ss.Machine = machine // a peer that did not stamp it
		}
		row := sessionRow{ShellSession: ss}
		for _, id := range ss.AgentThreads {
			if n := names[id]; n != "" {
				row.Threads = append(row.Threads, n)
			} else {
				row.Threads = append(row.Threads, shortID(id))
			}
		}
		sort.Strings(row.Threads)
		rows = append(rows, row)
	}
	return rows, nil
}

// shortID renders an unknown thread id compactly (the grid's tid8 convention).
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// selectedSession is the row under the viewer's cursor.
func (m Model) selectedSession() (sessionRow, bool) {
	if m.shellCursor < 0 || m.shellCursor >= len(m.shellRows) {
		return sessionRow{}, false
	}
	return m.shellRows[m.shellCursor], true
}

// navSession jumps the cockpit to a plain tmux session. Unlike navRow there is no
// thread to resolve a WINDOW from, so this is a session-level switch — the
// session's last-active window, which is what `tmux nav --to machine:session`
// already does.
func (m Model) navSession(row sessionRow) tea.Cmd {
	bin, env := m.binaryPath, m.navEnv
	sidebar := m.sidebar
	local := m.machine != "" && row.Machine == m.machine
	useInClient := local && onWorkSocket(m.tmux, m.tmuxSocket)
	tmuxEnv, clientName := m.tmux, m.clientName
	return func() tea.Msg {
		target := row.Machine + ":" + row.Name
		// Outside tmux entirely: nothing to switch — attach this terminal, the
		// same handoff navRow performs.
		if tmuxEnv == "" {
			return attachMsg{target: target}
		}
		args := []string{"tmux", "nav", "--to", target}
		if useInClient {
			args = append(args, "--in-client")
			if clientName != "" {
				args = append(args, "--client", clientName)
			}
		}
		// Same intent protocol as an ENTER from the grid (H72): clear any stale
		// preview intent, then declare "enter" so the traveling sidebar's swap
		// hook focuses the attach pane rather than keeping focus in the sidebar.
		if sidebar && !useInClient {
			clearSidebarIntent()
			declareSidebarIntent("enter")
		}
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		debugLog("SHELL NAV args=%v -> err=%v out=%q", args, err, strings.TrimSpace(string(out)))
		if err != nil {
			if sidebar && !useInClient {
				clearSidebarIntent()
			}
			return shellActionMsg{err: fmt.Errorf("nav %s: %v: %s", target, err, strings.TrimSpace(string(out)))}
		}
		return navDoneMsg{}
	}
}

// killSession kills a whole tmux session on its owning machine. This is a WIDER
// blast radius than stopping a thread (which kills one pane): every window and
// pane goes, including any agent panes inside — which is why the viewer confirms
// first and the confirmation names the threads that would drop to headless.
func (m Model) killSession(row sessionRow) tea.Cmd {
	bin, env, self := m.binaryPath, m.navEnv, m.machine
	return func() tea.Msg {
		args := []string{"tmux", "kill-session", "--target", row.Name}
		if self == "" || row.Machine != self {
			args = append(args, "--machine", row.Machine)
		}
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return shellActionMsg{err: fmt.Errorf("kill %s:%s: %v: %s", row.Machine, row.Name, err, strings.TrimSpace(string(out)))}
		}
		return shellActionMsg{note: fmt.Sprintf("killed %s:%s", row.Machine, row.Name), reload: true}
	}
}

// promoteSession turns an untracked session into a tracked shell thread on its
// owning machine. This is the whole point of recognising ghosts: you work by
// hand, then keep the ones worth keeping.
func (m Model) promoteSession(row sessionRow) tea.Cmd {
	bin, env, self := m.binaryPath, m.navEnv, m.machine
	return func() tea.Msg {
		args := []string{"shell", "promote", "--session", row.Name}
		if self == "" || row.Machine != self {
			args = append(args, "--machine", row.Machine)
		}
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return shellActionMsg{err: fmt.Errorf("promote %s:%s: %v: %s", row.Machine, row.Name, err, strings.TrimSpace(string(out)))}
		}
		return shellActionMsg{note: fmt.Sprintf("promoted %s:%s to a shell thread", row.Machine, row.Name), reload: true}
	}
}

// handleShellKey is the viewer's sub-state machine (active while m.shells).
func (m Model) handleShellKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// The kill confirmation owns every key while it is up.
	if m.shellConfirmKill {
		switch key {
		case "y", "Y":
			m.shellConfirmKill = false
			row, ok := m.selectedSession()
			if !ok {
				return m, nil
			}
			return m, m.killSession(row)
		default:
			m.shellConfirmKill = false
			m.shellNote = "kill cancelled"
			return m, nil
		}
	}

	switch key {
	case "up", "k":
		if m.shellCursor > 0 {
			m.shellCursor--
		}
	case "down", "j":
		if m.shellCursor < len(m.shellRows)-1 {
			m.shellCursor++
		}
	case "enter":
		row, ok := m.selectedSession()
		if !ok {
			return m, nil
		}
		// Entering closes the viewer: the point of the jump is to land in the
		// session, and leaving a full-screen takeover over it would hide it.
		m.shells = false
		return m, m.navSession(row)
	case "P":
		// P, not p — p is the global command palette (H88). A modal sub-view has
		// its own keyspace, but shadowing the palette key reads badly.
		row, ok := m.selectedSession()
		if !ok {
			return m, nil
		}
		if !row.promotable() {
			m.shellNote = fmt.Sprintf("%s:%s is already a shell thread", row.Machine, row.Name)
			return m, nil
		}
		return m, m.promoteSession(row)
	case "x":
		if _, ok := m.selectedSession(); !ok {
			return m, nil
		}
		m.shellConfirmKill = true
	case "R":
		m.shellLoading = true
		return m, m.loadShells()
	case "esc", "q", "S":
		m.shells = false
	}
	return m, nil
}

// shellsView renders the viewer (a full-screen takeover, returned from View).
func (m Model) shellsView() string {
	var b strings.Builder
	b.WriteString(styleHeader.Render("shells — live tmux sessions") + "\n")

	if m.shellLoading && len(m.shellRows) == 0 {
		b.WriteString(styleDim.Render("loading…") + "\n")
	}
	for _, e := range m.shellErrs {
		b.WriteString(styleErr.Render("! "+e) + "\n")
	}
	if m.shellNote != "" {
		b.WriteString(styleDim.Render(m.shellNote) + "\n")
	}

	if !m.shellLoading && len(m.shellRows) == 0 && len(m.shellErrs) == 0 {
		b.WriteString(styleDim.Render("no tmux sessions on any reachable machine") + "\n")
	}

	// Column widths from the data (machine and session names vary a lot).
	mw, nw := len("MACHINE"), len("SESSION")
	for _, r := range m.shellRows {
		mw = max(mw, len(r.Machine))
		nw = max(nw, len(r.Name))
	}
	if len(m.shellRows) > 0 {
		b.WriteString(styleDim.Render(fmt.Sprintf("  %-*s  %-*s  %-5s  %-3s  %s",
			mw, "MACHINE", nw, "SESSION", "CLASS", "W/P", "PATH")) + "\n")
	}
	for i, r := range m.shellRows {
		cursor := "  "
		if i == m.shellCursor {
			cursor = "> "
		}
		att := " "
		if r.Attached {
			att = "*"
		}
		line := fmt.Sprintf("%s%-*s %s%-*s  %-5s  %d/%-2d %s",
			cursor, mw, r.Machine, att, nw, r.Name, r.Class, r.Windows, r.Panes, r.Path)
		if len(r.Threads) > 0 {
			line += "  [" + strings.Join(r.Threads, ", ") + "]"
		}
		if m.width > 0 {
			line = trunc(line, m.width)
		}
		if i == m.shellCursor {
			line = styleSelected.Render(line)
		}
		b.WriteString(line + "\n")
	}

	if m.shellConfirmKill {
		if row, ok := m.selectedSession(); ok {
			warn := fmt.Sprintf("kill %s:%s — the WHOLE session (%d windows, %d panes)?",
				row.Machine, row.Name, row.Windows, row.Panes)
			if len(row.Threads) > 0 {
				warn += fmt.Sprintf(" This also kills the agent panes of [%s], dropping them to headless.",
					strings.Join(row.Threads, ", "))
			}
			b.WriteString("\n" + styleErr.Render(warn+"  y/n") + "\n")
		}
	}

	b.WriteString("\n" + styleDim.Render("enter jump · P promote · x kill · R refresh · esc close") + "\n")
	return b.String()
}

// --- exported accessors (conformance claims read the model, not the render) ---

// ShellsViewOpen reports whether the shells viewer is up.
func (m Model) ShellsViewOpen() bool { return m.shells }

// ShellSessionCount is how many live sessions the last fan-out found.
func (m Model) ShellSessionCount() int { return len(m.shellRows) }

// ShellSessionClass returns the class the viewer assigned to (machine, session),
// and whether that session was listed at all.
func (m Model) ShellSessionClass(machine, name string) (string, bool) {
	for _, r := range m.shellRows {
		if r.Machine == machine && r.Name == name {
			return r.Class, true
		}
	}
	return "", false
}

// ShellSelect moves the viewer's cursor onto (machine, session); false if it is
// not listed. Lets a claim act on a SPECIFIC session rather than assuming an
// ordering.
func (m *Model) ShellSelect(machine, name string) bool {
	for i, r := range m.shellRows {
		if r.Machine == machine && r.Name == name {
			m.shellCursor = i
			return true
		}
	}
	return false
}

// ShellErrs exposes the per-machine fan-out failures (for tests).
func (m Model) ShellErrs() []string { return m.shellErrs }
