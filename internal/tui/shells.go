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

	"github.com/charmbracelet/lipgloss"

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

// --- the list: filtering, the scroll window, and the cursor ------------------
//
// The viewer is a LIST SURFACE, so it behaves like the grid does: the query
// narrows it, the cursor wraps, and the viewport FOLLOWS the cursor. It used to
// render every row unconditionally with no window at all, so on a machine with
// forty sessions the cursor simply walked off the bottom of the pane and the
// selected row was no longer on screen (bubbletea keeps the LAST `height` lines
// of an over-tall frame, so the title and the top rows are what get dropped).
// The window arithmetic is the shared one in listwindow.go.

// visibleSessions is the filtered, ranked row list — the cursor indexes THIS,
// not m.shellRows. With no query it is the fan-out's own order (machine, then
// session name); with one it is fuzzy-ranked best-first, the same rule the
// grid's filter follows.
func (m Model) visibleSessions() []sessionRow {
	q := strings.TrimSpace(string(m.shellQuery))
	if q == "" {
		return m.shellRows
	}
	type scored struct {
		row   sessionRow
		score int
	}
	var hits []scored
	for _, r := range m.shellRows {
		// Match against everything that identifies the session on screen: a path
		// fragment ("dev/box") and a machine name are as natural to type as the
		// session name itself.
		best := fuzzyScore(q, r.Name)
		for _, text := range []string{r.Machine, r.Path, strings.Join(r.Threads, " ")} {
			if res := fuzzyScore(q, text); res.ok && (!best.ok || res.score > best.score) {
				best = res
			}
		}
		if best.ok {
			hits = append(hits, scored{r, best.score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	out := make([]sessionRow, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.row)
	}
	return out
}

// selectedSession is the row under the viewer's cursor.
func (m Model) selectedSession() (sessionRow, bool) {
	rows := m.visibleSessions()
	if m.shellCursor < 0 || m.shellCursor >= len(rows) {
		return sessionRow{}, false
	}
	return rows[m.shellCursor], true
}

// sessionKey identifies a session across a re-fan-out (the fan-out returns fresh
// values every time, so an index is not identity).
func sessionKey(r sessionRow) string { return r.Machine + ":" + r.Name }

// moveShellCursor moves the selection by delta and scrolls the window to keep it
// visible. It WRAPS, like the grid's cursor (the fzf --cycle feel).
func (m *Model) moveShellCursor(delta int) {
	n := len(m.visibleSessions())
	if n == 0 {
		m.shellCursor, m.shellOffset = 0, 0
		return
	}
	m.shellCursor = ((m.shellCursor+delta)%n + n) % n
	m.ensureShellCursorVisible()
}

// ensureShellCursorVisible is the fix for the reported bug: after ANY cursor
// move the window is scrolled the minimum distance that keeps the selection on
// screen.
func (m *Model) ensureShellCursorVisible() {
	n := len(m.visibleSessions())
	avail := m.shellVisibleRows(n)
	m.shellOffset = listClampOffset(listEnsureVisible(m.shellOffset, m.shellCursor, avail), n, avail)
}

// scrollShellRows moves the WINDOW by delta rows (ctrl+j/ctrl+k), pulling the
// cursor into the new window — the grid's scrollRows, over this list.
func (m *Model) scrollShellRows(delta int) {
	n := len(m.visibleSessions())
	avail := m.shellVisibleRows(n)
	m.shellOffset = listClampOffset(m.shellOffset+delta, n, avail)
	if m.shellCursor < m.shellOffset {
		m.shellCursor = m.shellOffset
	}
	if m.shellCursor >= m.shellOffset+avail {
		m.shellCursor = m.shellOffset + avail - 1
	}
	if m.shellCursor >= n {
		m.shellCursor = n - 1
	}
	if m.shellCursor < 0 {
		m.shellCursor = 0
	}
}

func (m *Model) shellHalfPage() int {
	if h := m.shellVisibleRows(len(m.visibleSessions())) / 2; h >= 1 {
		return h
	}
	return 1
}

// reanchorShellCursor puts the cursor back on the session it was on before a
// re-fan-out, wherever that session now sorts — the H42 rule, applied here: a
// promote/kill reloads the list, and a positional cursor would silently land on
// a DIFFERENT session, which is the one thing a viewer with a `kill` key must
// never do. A session that is gone (killed) leaves the cursor in its slot, i.e.
// on the neighbour.
func (m *Model) reanchorShellCursor(key string) {
	rows := m.visibleSessions()
	if key != "" {
		for i, r := range rows {
			if sessionKey(r) == key {
				m.shellCursor = i
				m.ensureShellCursorVisible()
				return
			}
		}
	}
	if m.shellCursor >= len(rows) {
		m.shellCursor = max(0, len(rows)-1)
	}
	m.ensureShellCursorVisible()
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
// The movement/scroll/filter keys deliberately MIRROR the grid's — this is the
// same kind of surface, and a list that scrolls differently from the one next to
// it reads as broken.
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

	// FILTER MODE owns the keyboard next: while typing a query, j/k/x/P are query
	// text, not commands (the grid's rule).
	if m.shellFiltering {
		return m.handleShellFilterKey(msg)
	}

	switch key {
	case "up", "k":
		m.moveShellCursor(-1)
	case "down", "j":
		m.moveShellCursor(1)
	case "ctrl+k":
		m.scrollShellRows(-m.shellHalfPage())
	case "ctrl+j":
		m.scrollShellRows(m.shellHalfPage())
	case "/":
		m.shellFiltering = true
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
		// A query is state the user can see; esc clears THAT first and only closes
		// the viewer once there is nothing left to clear (the grid's filter rule).
		if len(m.shellQuery) > 0 {
			m.clearShellFilter()
			return m, nil
		}
		m.shells = false
	}
	return m, nil
}

// handleShellFilterKey drives the `/` query: type to narrow, backspace to edit,
// enter APPLIES it (leaves the query in force and returns to the command keys),
// esc clears it entirely.
func (m Model) handleShellFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.clearShellFilter()
		return m, nil
	case "enter":
		m.shellFiltering = false
		return m, nil
	case "backspace":
		if n := len(m.shellQuery); n > 0 {
			m.shellQuery = m.shellQuery[:n-1]
			m.shellCursor, m.shellOffset = 0, 0
		}
		return m, nil
	}
	switch msg.Type {
	case tea.KeyRunes:
		m.shellQuery = append(m.shellQuery, msg.Runes...)
		m.shellCursor, m.shellOffset = 0, 0
	case tea.KeySpace:
		m.shellQuery = append(m.shellQuery, ' ')
		m.shellCursor, m.shellOffset = 0, 0
	}
	return m, nil
}

func (m *Model) clearShellFilter() {
	m.shellFiltering = false
	m.shellQuery = nil
	m.shellCursor, m.shellOffset = 0, 0
}

// handleShellClick maps a left PRESS in the viewer: a click selects the session
// under the pointer, a double click on the same session enters it — the grid's
// mouse contract (mouse.go), over this list.
func (m Model) handleShellClick(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.shellConfirmKill {
		return m, nil // the confirmation owns the screen; a stray click must not answer it
	}
	i, ok := m.shellRowAtY(msg.Y)
	if !ok {
		return m, nil
	}
	rows := m.visibleSessions()
	m.shellCursor = i
	m.ensureShellCursorVisible()

	key := sessionKey(rows[i])
	now := m.nowTime()
	if m.lastClickID == key && now.Sub(m.lastClickAt) <= doubleClickWindow {
		m.resetClickTracking()
		row := rows[i]
		m.shells = false
		return m, m.navSession(row)
	}
	m.lastClickID, m.lastClickAt = key, now
	return m, nil
}

// --- layout ------------------------------------------------------------------
//
// The viewer's geometry is computed ONCE, here, and BOTH the renderer and the
// click mapping read it. The grid learned this the hard way: a hand-mirrored
// chrome count (H41's rowAtY vs View) drifts the moment a line is added, and the
// symptom is clicks landing on the wrong row.

// shellLayout is the resolved frame: the chrome above the rows, the row window,
// and the chrome below.
type shellLayout struct {
	head    string // everything above the first session row (ends with "\n")
	foot    string // everything below the last session row
	rowsTop int    // terminal Y of the first session row
	rows    []sessionRow
	off     int // index of the first rendered row
	avail   int // how many rows are rendered
}

// wrapToWidth renders text wrapped to the pane width, so a long error or warning
// occupies a KNOWN number of lines instead of silently wrapping past the frame
// (which would push the whole frame over the pane height — the H70 class).
func (m Model) wrapToWidth(st lipgloss.Style, text string) string {
	if m.width > 1 {
		return st.Width(m.width).Render(text)
	}
	return st.Render(text)
}

func countLines(s string) int { return strings.Count(s, "\n") + 1 }

// shellChrome builds the head/foot strings and returns them with their line
// counts — the single source of the viewer's geometry.
func (m Model) shellChrome(n int) (head, foot string, headLines, footLines int) {
	var h strings.Builder
	h.WriteString(styleHeader.Render("shells — live tmux sessions") + "\n")
	headLines = 1
	for _, e := range m.shellErrs {
		line := m.wrapToWidth(styleErr, "! "+e)
		h.WriteString(line + "\n")
		headLines += countLines(line)
	}
	if m.shellNote != "" {
		line := m.wrapToWidth(styleDim, m.shellNote)
		h.WriteString(line + "\n")
		headLines += countLines(line)
	}
	if m.shellConfirmKill {
		// ABOVE the rows, where the grid puts its own confirm: an armed y/n prompt
		// must be the thing you cannot miss, and on a short pane anything rendered
		// below a full list is the first thing to be cut.
		if row, ok := m.selectedSession(); ok {
			warn := fmt.Sprintf("kill %s:%s — the WHOLE session (%d windows, %d panes)?",
				row.Machine, row.Name, row.Windows, row.Panes)
			if len(row.Threads) > 0 {
				warn += fmt.Sprintf(" This also kills the agent panes of [%s], dropping them to headless.",
					strings.Join(row.Threads, ", "))
			}
			line := m.wrapToWidth(styleErr, warn+"  y/n")
			h.WriteString(line + "\n")
			headLines += countLines(line)
		}
	}
	if s := m.shellFilterLine(n); s != "" {
		h.WriteString(s + "\n")
		headLines++
	}
	if n > 0 {
		h.WriteString(m.shellHeaderLine() + "\n")
		headLines++
		headLines++ // the ▲ indicator line, written by shellsView once off is known
	}

	var f strings.Builder
	if n == 0 {
		switch {
		case m.shellLoading:
			f.WriteString(styleDim.Render("loading…") + "\n")
		case len(m.shellQuery) > 0:
			f.WriteString(styleDim.Render("no session matches "+string(m.shellQuery)) + "\n")
		case len(m.shellErrs) == 0:
			f.WriteString(styleDim.Render("no tmux sessions on any reachable machine") + "\n")
		default:
			f.WriteString("\n")
		}
		footLines++
	} else {
		footLines++ // the ▼ indicator line
	}
	legend := m.wrapToWidth(styleDim, m.shellLegend())
	f.WriteString("\n" + legend + "\n")
	footLines += 1 + countLines(legend)
	return h.String(), f.String(), headLines, footLines
}

func (m Model) shellLegend() string {
	if m.shellFiltering {
		return "type to filter · enter apply · esc clear"
	}
	return "enter jump · P promote · x kill · / filter · R refresh · esc close"
}

// shellFilterLine is the query line: the live prompt while typing, a status line
// once applied, nothing when there is no query (so the layout costs nothing in
// the common case).
func (m Model) shellFilterLine(n int) string {
	total := len(m.shellRows)
	switch {
	case m.shellFiltering:
		return styleDim.Render(fmt.Sprintf("  /%s█  %d/%d", string(m.shellQuery), n, total))
	case len(m.shellQuery) > 0:
		return styleDim.Render(fmt.Sprintf("  filter: %s  %d/%d · / edit · esc clear", string(m.shellQuery), n, total))
	}
	return ""
}

// shellColWidths sizes the two variable columns from the CURRENTLY VISIBLE rows.
func (m Model) shellColWidths(rows []sessionRow) (int, int) {
	mw, nw := len("MACHINE"), len("SESSION")
	for _, r := range rows {
		mw = max(mw, len(r.Machine))
		nw = max(nw, len(r.Name))
	}
	return mw, nw
}

func (m Model) shellHeaderLine() string {
	mw, nw := m.shellColWidths(m.visibleSessions())
	return styleDim.Render(trunc(fmt.Sprintf("  %-*s  %-*s  %-5s  %-3s  %s",
		mw, "MACHINE", nw, "SESSION", "CLASS", "W/P", "PATH"), m.paneWidth()))
}

// paneWidth is the width to clip to; an unknown width (tests) clips nothing.
func (m Model) paneWidth() int {
	if m.width > 0 {
		return m.width
	}
	return 1 << 20
}

// shellVisibleRows is how many session rows fit.
func (m Model) shellVisibleRows(n int) int {
	_, _, headLines, footLines := m.shellChrome(n)
	return listVisibleRows(n, m.height, headLines+footLines)
}

// shellResolveLayout resolves the frame.
func (m Model) shellResolveLayout() shellLayout {
	rows := m.visibleSessions()
	head, foot, headLines, footLines := m.shellChrome(len(rows))
	avail := listVisibleRows(len(rows), m.height, headLines+footLines)
	off := listClampOffset(m.shellOffset, len(rows), avail)
	return shellLayout{head: head, foot: foot, rowsTop: headLines, rows: rows, off: off, avail: avail}
}

// shellRowAtY maps a click's terminal row to an index into visibleSessions().
func (m Model) shellRowAtY(y int) (int, bool) {
	l := m.shellResolveLayout()
	i := y - l.rowsTop + l.off
	if i < l.off || i >= l.off+l.avail || i >= len(l.rows) {
		return 0, false
	}
	return i, true
}

// shellsView renders the viewer (a full-screen takeover, returned from View).
func (m Model) shellsView() string {
	l := m.shellResolveLayout()
	var b strings.Builder
	b.WriteString(l.head)
	if len(l.rows) > 0 {
		// The ▲/▼ indicator lines are ALWAYS present (blank when unneeded) so the
		// row geometry does not shift as the list scrolls — the rule every other
		// popup follows, and what shellRowAtY depends on.
		if l.off > 0 {
			b.WriteString(styleDim.Render(fmt.Sprintf("  ▲ %d more", l.off)) + "\n")
		} else {
			b.WriteString("\n")
		}
	}
	mw, nw := m.shellColWidths(l.rows)
	for i := l.off; i < l.off+l.avail; i++ {
		r := l.rows[i]
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
		line = trunc(line, m.paneWidth())
		if i == m.shellCursor {
			line = styleSelected.Render(line)
		}
		b.WriteString(line + "\n")
	}
	if len(l.rows) > 0 {
		if rest := len(l.rows) - l.off - l.avail; rest > 0 {
			b.WriteString(styleDim.Render(fmt.Sprintf("  ▼ %d more", rest)) + "\n")
		} else {
			b.WriteString("\n")
		}
	}
	b.WriteString(l.foot)

	// A pane too short for even the viewer's own chrome (a stubby split, a tiny
	// sidebar) cannot show everything. Keep the TOP of the frame — the title, any
	// fan-out error, the armed y/n confirmation — and drop the tail. Handing
	// bubbletea an over-tall frame instead makes IT drop lines, from the TOP, and
	// what is left is an unlabelled screen of rows (H70).
	return clampFrameHeight(b.String(), m.height)
}

// clampFrameHeight trims a rendered frame to at most h lines, keeping the first
// ones. h <= 0 (size unknown) trims nothing.
func clampFrameHeight(frame string, h int) string {
	if h <= 0 {
		return frame
	}
	lines := strings.Split(strings.TrimSuffix(frame, "\n"), "\n")
	if len(lines) <= h {
		return frame
	}
	return strings.Join(lines[:h], "\n") + "\n"
}

// --- exported accessors (conformance claims read the model, not the render) ---

// ShellsViewOpen reports whether the shells viewer is up.
func (m Model) ShellsViewOpen() bool { return m.shells }

// ShellSessionCount is how many live sessions the last fan-out found (the whole
// list, before any filter).
func (m Model) ShellSessionCount() int { return len(m.shellRows) }

// ShellVisibleCount is how many survive the active `/` filter — what the viewer
// is actually showing.
func (m Model) ShellVisibleCount() int { return len(m.visibleSessions()) }

// ShellSelectedName is the session under the viewer's cursor (tests/claims: the
// observable "which row am I about to act on").
func (m Model) ShellSelectedName() (string, bool) {
	row, ok := m.selectedSession()
	return row.Name, ok
}

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
	for i, r := range m.visibleSessions() {
		if r.Machine == machine && r.Name == name {
			m.shellCursor = i
			m.ensureShellCursorVisible()
			return true
		}
	}
	return false
}

// ShellErrs exposes the per-machine fan-out failures (for tests).
func (m Model) ShellErrs() []string { return m.shellErrs }
