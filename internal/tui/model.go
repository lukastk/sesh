// Package tui is the sesh live grid — a Bubble Tea app over the daemon's
// HTTP+JSON surface. It is a THIN renderer + action dispatcher: it owns no domain
// logic, and its ONLY source of state is the api.http-json client. By rule it
// imports internal/client and internal/api but never internal/store or daemon
// internals (enforced by a test), so "the grid renders real daemon state" and
// "actions really act" are testable claims, not vibes.
package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/client"
)

// refreshInterval is the poll cadence (poll-first, no SSE).
const refreshInterval = 3 * time.Second

// Model is the TUI state. Its rows come only from the daemon grid.
type Model struct {
	client      *client.Client
	allMachines bool
	archived    bool

	// binaryPath + navEnv: how the nav action execs the `sesh tmux nav` primitive
	// (the TUI drives the primitive, it does not re-implement nav). Defaults to the
	// running sesh binary; tests override both.
	binaryPath string
	navEnv     []string

	rows        []api.ThreadRow
	unreachable []string
	cursor      int
	err         error
	lastErr     error
	width       int
	height      int
}

// New builds a model talking to the daemon at socketPath.
func New(socketPath string, allMachines bool) Model {
	bin, err := os.Executable()
	if err != nil {
		bin = "sesh"
	}
	return Model{client: client.New(socketPath), allMachines: allMachines, binaryPath: bin}
}

// WithExec overrides how the nav action execs sesh (binary path + extra env) —
// used by tests so nav drives a sandbox's tmux/mesh config.
func (m Model) WithExec(binaryPath string, env []string) Model {
	m.binaryPath = binaryPath
	m.navEnv = env
	return m
}

// gridMsg carries a freshly-fetched grid.
type gridMsg struct {
	resp api.ThreadGridResponse
	err  error
}

type tickMsg time.Time

// Init kicks off the first fetch.
func (m Model) Init() tea.Cmd { return m.fetch() }

// fetch returns a command that polls the daemon grid (off the UI thread).
func (m Model) fetch() tea.Cmd {
	c, archived, all := m.client, m.archived, m.allMachines
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		resp, err := c.ThreadGrid(ctx, archived, all)
		return gridMsg{resp: resp, err: err}
	}
}

func tick() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// Update handles messages (satisfies tea.Model). Tests type-assert the returned
// tea.Model back to Model to inspect the result.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case gridMsg:
		if msg.err != nil {
			m.lastErr = msg.err
		} else {
			m.lastErr = nil
			m.rows = msg.resp.Rows
			m.unreachable = msg.resp.Unreachable
			if m.cursor >= len(m.rows) {
				m.cursor = max(0, len(m.rows)-1)
			}
		}
		return m, tick()
	case tickMsg:
		return m, m.fetch()
	case actionMsg:
		if msg.err != nil {
			m.lastErr = msg.err
		}
		return m, m.fetch() // re-fetch so the grid reflects the mutation
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// actionMsg is the result of an in-app mutation.
type actionMsg struct{ err error }

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.rows)-1 {
			m.cursor++
		}
	case "r":
		return m, m.fetch()
	case "x":
		return m, m.stopSelected()
	case "d":
		return m, m.deleteSelected()
	case "a":
		return m, m.archiveSelected()
	case "enter":
		return m, m.navSelected()
	}
	return m, nil
}

// navSelected jumps to the selected thread's session by driving the `sesh tmux
// nav` primitive (outer switch + inner switch-client + bare-shell kick). The TUI
// emits a locator and shells out; it does not re-implement nav.
func (m Model) navSelected() tea.Cmd {
	row, ok := m.Selected()
	if !ok {
		return nil
	}
	bin, env := m.binaryPath, m.navEnv
	target := row.Machine + ":" + row.SessionName
	return func() tea.Msg {
		cmd := exec.Command(bin, "tmux", "nav", "--to", target)
		cmd.Env = append(os.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return actionMsg{err: fmt.Errorf("nav %s: %v: %s", target, err, strings.TrimSpace(string(out)))}
		}
		return actionMsg{}
	}
}

// stopSelected ends the selected thread's runtime (agent + session) but keeps
// the record (it becomes a dead, resumable thread).
func (m Model) stopSelected() tea.Cmd {
	row, ok := m.Selected()
	if !ok {
		return nil
	}
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return actionMsg{err: c.ThreadStop(ctx, row.ID)}
	}
}

// deleteSelected drops the selected thread's record. The daemon refuses a live
// thread (orphan guard); stop it first. Surfaced as an error in lastErr.
func (m Model) deleteSelected() tea.Cmd {
	row, ok := m.Selected()
	if !ok {
		return nil
	}
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return actionMsg{err: c.ThreadDelete(ctx, row.ID, false)}
	}
}

// archiveSelected parks the selected thread (record kept, hidden from the list).
func (m Model) archiveSelected() tea.Cmd {
	row, ok := m.Selected()
	if !ok {
		return nil
	}
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return actionMsg{err: c.ThreadArchive(ctx, row.ID, true)}
	}
}

// Selected returns the row under the cursor, if any.
func (m Model) Selected() (api.ThreadRow, bool) {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return api.ThreadRow{}, false
	}
	return m.rows[m.cursor], true
}

// Rows exposes the current rows (for tests).
func (m Model) Rows() []api.ThreadRow { return m.rows }

// LastErr exposes the most recent fetch/action error (for tests).
func (m Model) LastErr() error { return m.lastErr }

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ---- rendering ----

var (
	styleHeader   = lipgloss.NewStyle().Bold(true)
	styleSelected = lipgloss.NewStyle().Reverse(true)
	styleDim      = lipgloss.NewStyle().Faint(true)
)

// Glyph maps a row's live state to a status glyph. These are part of the contract
// the TUI conformance asserts against REAL state.
//
//	◐ working   ● waiting (idle / needs input)   ✗ dead
func Glyph(row api.ThreadRow) string {
	switch row.Activity {
	case api.ActivityWorking:
		return "◐"
	case api.ActivityWaiting:
		return "●"
	case api.ActivityDead:
		return "✗"
	default:
		return "?"
	}
}

// View renders the grid. Kept pure (Model -> string) so a snapshot test can assert
// the rendered output reflects the model's REAL rows.
func (m Model) View() string {
	var b strings.Builder
	b.WriteString(styleHeader.Render("sesh — live threads") + "\n")
	if m.lastErr != nil {
		b.WriteString(styleDim.Render("(daemon unreachable: "+m.lastErr.Error()+")") + "\n")
	}
	b.WriteString(styleHeader.Render(fmt.Sprintf("  %-2s %-12s %-7s %-7s %-20s %s", "", "MACHINE", "AGENT", "STATE", "NAME", "TAGS")) + "\n")

	if len(m.rows) == 0 {
		b.WriteString(styleDim.Render("  (no threads)") + "\n")
	}
	for i, row := range m.rows {
		tags := ""
		if len(row.Tags) > 0 {
			tags = "[" + strings.Join(row.Tags, ",") + "]"
		}
		att := " "
		if row.Attachment == api.Attached {
			att = "*" // a client is attached
		}
		line := fmt.Sprintf("%s %s %-12s %-7s %-7s %-20s %s",
			Glyph(row), att, trunc(row.Machine, 12), trunc(row.AgentKind, 7), string(row.Activity), trunc(row.Name, 20), tags)
		if i == m.cursor {
			b.WriteString(styleSelected.Render("> "+line) + "\n")
		} else {
			b.WriteString("  " + line + "\n")
		}
	}
	for _, u := range m.unreachable {
		b.WriteString(styleDim.Render("  ! peer "+u+" unreachable") + "\n")
	}
	b.WriteString(styleDim.Render("\n  ↑/↓ move · enter nav · x stop · d delete · a archive · r refresh · q quit") + "\n")
	return b.String()
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
