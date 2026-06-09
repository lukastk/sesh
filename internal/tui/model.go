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
	return Model{client: client.New(socketPath), allMachines: allMachines}
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
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

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
	}
	return m, nil
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
	b.WriteString(styleDim.Render("\n  ↑/↓ move · r refresh · q quit") + "\n")
	return b.String()
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
