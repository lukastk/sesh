package tui

// The thread-ENV manager (schema 62; api/env.go): a full-screen list of a thread's
// variables, fetched from its owner when opened (`sesh env show --reveal --json`,
// routed with --machine) because a row only carries the count (env_count, the ENV
// column). Values are masked until `v`. `a` adds, `e` edits the selected value, `d`
// deletes and `r` restarts the agent, the last two on a second press, because
// neither can be undone. Every change is one `sesh env` call on the owner, and the
// line it answers with says how the change reaches the running agent.

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/api"
)

// envChrome is the manager's fixed non-list line count: title, blank, the two
// more-indicators, note, input, blank, footer.
const envChrome = 8

// envMask stands for a value until the user reveals it.
const envMask = "••••••"

const (
	envInputNone = iota
	envInputAdd  // "KEY=value"
	envInputEdit // the selected key's value
)

// envLoadedMsg carries a fetched set of variables.
type envLoadedMsg struct {
	row  api.ThreadRow
	resp api.ThreadEnvResponse
	err  error
}

// envChangedMsg is the owner's answer to a change; a refetch follows.
type envChangedMsg struct {
	row  api.ThreadRow
	note string
	err  error
}

func (m Model) fetchEnv(row api.ThreadRow) tea.Cmd {
	args := m.routeArgs([]string{"env", "show", "--id", row.ID, "--reveal", "--json"}, row.Machine)
	return func() tea.Msg {
		var resp api.ThreadEnvResponse
		err := m.runJSON(args, &resp)
		return envLoadedMsg{row: row, resp: resp, err: err}
	}
}

// runEnvChange runs one `sesh env` verb on row's owner.
func (m Model) runEnvChange(row api.ThreadRow, args ...string) tea.Cmd {
	full := m.routeArgs(args, row.Machine)
	return func() tea.Msg {
		var resp api.SetThreadEnvResponse
		if err := m.runJSON(full, &resp); err != nil {
			return envChangedMsg{row: row, err: err}
		}
		return envChangedMsg{row: row, note: fmt.Sprintf("[%s] %s", resp.Outcome, resp.Detail)}
	}
}

func (m Model) runAgentRestart(row api.ThreadRow) tea.Cmd {
	args := m.routeArgs([]string{"thread", "restart", "--id", row.ID, "--json"}, row.Machine)
	return func() tea.Msg {
		var resp api.RestartThreadResponse
		if err := m.runJSON(args, &resp); err != nil {
			return envChangedMsg{row: row, err: err}
		}
		return envChangedMsg{row: row, note: resp.Detail}
	}
}

// openEnvEdit opens the manager on the selection and starts the fetch.
func (m Model) openEnvEdit() (tea.Model, tea.Cmd) {
	row, ok := m.Selected()
	if !ok {
		return m, nil
	}
	switch row.AgentKind {
	case api.ShellAgentKind, api.VirtualAgentKind, api.DividerAgentKind:
		m.actionErr = fmt.Errorf("%q is a %s thread: it runs no agent, so it has no environment", rowDisplayName(row), row.AgentKind)
		return m, nil
	}
	m.envPopup, m.envRow, m.envCursor, m.envOffset = true, row, 0, 0
	m.envResp, m.envErr, m.envNote, m.envLoading = api.ThreadEnvResponse{}, nil, "", true
	m.envReveal, m.envInputMode, m.envConfirm = false, envInputNone, ""
	return m, m.fetchEnv(row)
}

func (m Model) applyEnvLoaded(msg envLoadedMsg) (tea.Model, tea.Cmd) {
	if !m.envPopup || m.envRow.ID != msg.row.ID {
		return m, nil // closed (or moved on) before the fetch landed
	}
	m.envLoading = false
	if msg.err != nil {
		m.envErr = msg.err
		return m, nil
	}
	// A refetch follows every change, so it must not clear a change's error.
	m.envResp = msg.resp
	if keys := m.envKeys(); m.envCursor >= len(keys) {
		m.envCursor = max(0, len(keys)-1)
	}
	return m, nil
}

func (m Model) applyEnvChanged(msg envChangedMsg) (tea.Model, tea.Cmd) {
	if !m.envPopup || m.envRow.ID != msg.row.ID {
		return m, m.fetch()
	}
	m.envErr, m.envNote = msg.err, msg.note
	return m, tea.Batch(m.fetchEnv(msg.row), m.fetch())
}

func (m Model) envKeys() []string {
	keys := make([]string, 0, len(m.envResp.Vars))
	for k := range m.envResp.Vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (m Model) envVisibleRows() int { return listVisibleRows(len(m.envKeys()), m.height, envChrome) }

// handleEnvKey drives the manager; the input line takes every key while it is open.
func (m Model) handleEnvKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.envInputMode != envInputNone {
		return m.handleEnvInputKey(msg)
	}
	keys := m.envKeys()
	selected := ""
	if m.envCursor < len(keys) {
		selected = keys[m.envCursor]
	}
	// A pending confirmation stands only for the very next key.
	confirm := m.envConfirm
	m.envConfirm = ""
	switch msg.String() {
	case "esc", "q", "ctrl+c":
		m.envPopup = false
		return m, nil
	case "up", "k":
		m.envCursor = max(0, m.envCursor-1)
	case "down", "j":
		m.envCursor = min(max(0, len(keys)-1), m.envCursor+1)
	case "v":
		m.envReveal = !m.envReveal
	case "a":
		m.envInputMode, m.envInput, m.envNote, m.envErr = envInputAdd, nil, "", nil
	case "e", "enter":
		if selected != "" {
			m.envInputMode, m.envInput, m.envNote, m.envErr = envInputEdit, []rune(m.envResp.Vars[selected]), "", nil
		}
	case "d":
		if selected == "" {
			break
		}
		if confirm != "d" {
			m.envConfirm, m.envNote, m.envErr = "d", "press d again to delete "+selected, nil
			break
		}
		m.envNote = ""
		return m, m.runEnvChange(m.envRow, "env", "unset", "--id", m.envRow.ID, "--json", selected)
	case "r":
		if confirm != "r" {
			m.envConfirm, m.envNote, m.envErr = "r", "press r again to restart the agent: its process is replaced, the conversation resumes", nil
			break
		}
		m.envNote = "restarting…"
		return m, m.runAgentRestart(m.envRow)
	}
	return m, nil
}

func (m Model) handleEnvInputKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		m.envInputMode = envInputNone
	case tea.KeyBackspace:
		if n := len(m.envInput); n > 0 {
			m.envInput = m.envInput[:n-1]
		}
	case tea.KeyCtrlU:
		m.envInput = nil
	case tea.KeySpace:
		m.envInput = append(m.envInput, ' ')
	case tea.KeyRunes:
		m.envInput = append(m.envInput, msg.Runes...)
	case tea.KeyEnter:
		pair := string(m.envInput)
		if m.envInputMode == envInputEdit {
			pair = m.envKeys()[m.envCursor] + "=" + pair
		} else if !strings.Contains(pair, "=") {
			m.envErr = fmt.Errorf("expected KEY=value")
			return m, nil
		}
		m.envInputMode = envInputNone
		m.envInput = nil
		return m, m.runEnvChange(m.envRow, "env", "set", "--id", m.envRow.ID, "--json", pair)
	}
	return m, nil
}

// envValueCell is how a value shows: masked, or one line cut to the pane.
func (m Model) envValueCell(value string, width int) string {
	if !m.envReveal {
		return envMask
	}
	return trunc(sanitizeLine(value), max(8, width))
}

// envView renders the manager. Like the context viewer, the indicator, note and
// input lines are always present so the layout does not jump.
func (m Model) envView() string {
	var b strings.Builder
	keys := m.envKeys()
	b.WriteString(styleHeader.Render("thread env · "+rowDisplayName(m.envRow)) + styleDim.Render(fmt.Sprintf(" · %d variable(s)", len(keys))) + "\n\n")
	avail := m.envVisibleRows()
	off := min(max(0, m.envOffset), max(0, len(keys)-avail))
	if m.envCursor < off {
		off = m.envCursor
	} else if m.envCursor >= off+avail {
		off = m.envCursor - avail + 1
	}
	if off > 0 {
		b.WriteString(styleDim.Render(fmt.Sprintf("  ▲ %d more", off)) + "\n")
	} else {
		b.WriteString("\n")
	}
	switch {
	case m.envLoading:
		b.WriteString("  loading…\n")
	case len(keys) == 0:
		b.WriteString("  (this thread has no variables — `a` adds one)\n")
	}
	keyW := 0
	for _, k := range keys {
		keyW = max(keyW, len(k))
	}
	for i := off; i < min(len(keys), off+avail); i++ {
		k := keys[i]
		cursor := "  "
		if i == m.envCursor {
			cursor = "▸ "
		}
		b.WriteString(fmt.Sprintf("%s%-*s = %s\n", cursor, keyW, k, m.envValueCell(m.envResp.Vars[k], m.width-keyW-7)))
	}
	if rest := len(keys) - off - avail; rest > 0 {
		b.WriteString(styleDim.Render(fmt.Sprintf("  ▼ %d more", rest)) + "\n")
	} else {
		b.WriteString("\n")
	}
	switch {
	case m.envErr != nil:
		b.WriteString(styleErr.Render("  ✗ "+m.envErr.Error()) + "\n")
	case m.envNote != "":
		b.WriteString(styleDim.Render("  "+m.envNote) + "\n")
	default:
		b.WriteString("\n")
	}
	switch m.envInputMode {
	case envInputAdd:
		b.WriteString("  add KEY=value: " + string(m.envInput) + "█\n")
	case envInputEdit:
		b.WriteString("  " + keys[m.envCursor] + "=" + string(m.envInput) + "█\n")
	default:
		b.WriteString("\n")
	}
	footer := "  a add · e edit · d delete · v reveal · r restart agent · ↑/↓ move · esc/q close"
	if m.envInputMode != envInputNone {
		footer = "  enter save · esc cancel · ctrl+u clear"
	}
	b.WriteString("\n" + styleDim.Render(footer))
	return b.String()
}

// EnvOpen reports whether the env manager is up (tests).
func (m Model) EnvOpen() bool { return m.envPopup }
