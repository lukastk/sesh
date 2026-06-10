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
	"path/filepath"
	"sort"
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

	// machine + tmuxSocket: this client's own identity + work socket, so Enter can use
	// in-client nav (no master) for a LOCAL thread when the TUI is running inside that
	// work socket's tmux. Empty (the test/default) => always use the master nav path.
	machine    string
	tmuxSocket string
	// tmux is this process's $TMUX (captured at construction so behavior is deterministic
	// and testable). "" => not in tmux (Enter attaches); basename == tmuxSocket => on the
	// work socket (Enter switches in place).
	tmux string

	rows      []api.ThreadRow
	machines  []api.MachineView // per-machine freshness, for the staleness footer
	fetchedAt int64             // unix time the current data was fetched (for staleness age)
	cursor    int
	err       error
	lastErr   error
	width     int
	height    int

	// attachTarget, when set, means the TUI quit in order to ATTACH the terminal to a
	// thread (Enter from a plain shell, outside tmux). The caller reads PendingAttach
	// after Run() and execs the attach. "" = quit normally.
	attachTarget string
}

// New builds a model talking to the daemon at socketPath.
func New(socketPath string, allMachines bool) Model {
	bin, err := os.Executable()
	if err != nil {
		bin = "sesh"
	}
	return Model{client: client.New(socketPath), allMachines: allMachines, binaryPath: bin, tmux: os.Getenv("TMUX")}
}

// WithTmux overrides the captured $TMUX value (tests set it to drive the Enter path
// deterministically: "" => attach, a work-socket value => in-client, else master path).
func (m Model) WithTmux(tmux string) Model {
	m.tmux = tmux
	return m
}

// WithExec overrides how the nav action execs sesh (binary path + extra env) —
// used by tests so nav drives a sandbox's tmux/mesh config.
func (m Model) WithExec(binaryPath string, env []string) Model {
	m.binaryPath = binaryPath
	m.navEnv = env
	return m
}

// PendingAttach reports whether the TUI quit to attach the terminal to a thread, and
// if so returns the argv to exec (`sesh tmux nav --to <target> --attach`). The caller
// (runTUI) execs it AFTER the TUI has exited and restored the terminal.
func (m Model) PendingAttach() ([]string, bool) {
	if m.attachTarget == "" {
		return nil, false
	}
	return []string{m.binaryPath, "tmux", "nav", "--to", m.attachTarget, "--attach"}, true
}

// WithLocal sets this client's own machine + work socket, enabling in-client nav for
// a local thread when the TUI is inside that work socket's tmux.
func (m Model) WithLocal(machine, tmuxSocket string) Model {
	m.machine = machine
	m.tmuxSocket = tmuxSocket
	return m
}

// meshMsg carries a freshly-fetched mesh view, already flattened to rows.
type meshMsg struct {
	rows      []api.ThreadRow
	machines  []api.MachineView
	fetchedAt int64
	err       error
}

type tickMsg time.Time

// Init kicks off the first fetch.
func (m Model) Init() tea.Cmd { return m.fetch() }

// fetch polls the daemon's merged mesh view (a LOCAL read of the cache — instant,
// offline-capable) and flattens it to sorted rows. Self-only unless --all-machines.
func (m Model) fetch() tea.Cmd {
	c, archived, all := m.client, m.archived, m.allMachines
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		mesh, err := c.Mesh(ctx)
		if err != nil {
			return meshMsg{err: err}
		}
		var rows []api.ThreadRow
		for _, mv := range mesh.Machines {
			if !all && !mv.Self {
				continue
			}
			for _, t := range mv.Threads {
				if t.Archived && !archived {
					continue
				}
				rows = append(rows, api.ThreadRow{Thread: t.Thread, Activity: t.Activity, Attachment: t.Attachment})
			}
		}
		// Stable order (machine, then name, then id) so the cursor never jumps —
		// the maintainer's snapshot map iteration is unordered.
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].Machine != rows[j].Machine {
				return rows[i].Machine < rows[j].Machine
			}
			if rows[i].Name != rows[j].Name {
				return rows[i].Name < rows[j].Name
			}
			return rows[i].ID < rows[j].ID
		})
		return meshMsg{rows: rows, machines: mesh.Machines, fetchedAt: time.Now().Unix()}
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
	case meshMsg:
		if msg.err != nil {
			m.lastErr = msg.err
		} else {
			m.lastErr = nil
			m.rows = msg.rows
			m.machines = msg.machines
			m.fetchedAt = msg.fetchedAt
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
	case navDoneMsg:
		// The selected thread is now on screen (the client switched under us) —
		// quit so the TUI (and the popup hosting it) gets out of the way. Staying
		// open would leave the TUI covering the very thread the user entered.
		return m, tea.Quit
	case attachMsg:
		// Quit so the terminal is restored, then runTUI execs the attach.
		m.attachTarget = msg.target
		return m, tea.Quit
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// actionMsg is the result of an in-app mutation.
type actionMsg struct{ err error }

// attachMsg asks the TUI to quit and have the caller attach the terminal to target
// (<machine>:<session>) — used when Enter is pressed outside tmux.
type attachMsg struct{ target string }

// navDoneMsg reports a successful nav: the user is where they asked to be, so the
// TUI quits. (A FAILED nav stays an actionMsg with the error, keeping the TUI open.)
type navDoneMsg struct{}

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

// navSelected enters the selected thread. A headless thread has no pane and a dead
// thread's pane is gone, so neither can be navved to directly — entering one first
// PROMOTES it (headless -> headed) or RESUMES it (dead -> live), then jumps to the
// resulting session. That compose only works on the owning machine (promote/resume
// route to the local daemon), so cross-machine it fails LOUDLY rather than navving to
// a non-existent session (which would silently no-op). An alive headed thread skips
// straight to nav. The jump itself drives the `sesh tmux nav` primitive (outer switch +
// inner switch-client + bare-shell kick); the TUI shells out, it does not re-implement nav.
func (m Model) navSelected() tea.Cmd {
	row, ok := m.Selected()
	if !ok {
		return nil
	}
	bin, env := m.binaryPath, m.navEnv
	local := m.machine != "" && row.Machine == m.machine
	// A LOCAL thread, when we're inside its work socket's tmux, switches the current
	// client in place (no master). Otherwise: the full master nav path.
	useInClient := local && onWorkSocket(m.tmux, m.tmuxSocket)
	return func() tea.Msg {
		sessionName := row.SessionName
		// Headless or dead => no live session to enter. Promote/resume first (local only).
		if row.Headless || row.Activity == api.ActivityDead {
			verb := "resume"
			if row.Headless {
				verb = "promote"
			}
			if !local {
				return actionMsg{err: fmt.Errorf("%q is on %s — %s it there first (cross-machine %s isn't supported from here)", row.Name, row.Machine, verb, verb)}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			var resp api.ThreadResponse
			var err error
			if row.Headless {
				resp, err = m.client.ThreadHeadful(ctx, row.ID)
			} else {
				resp, err = m.client.ThreadResume(ctx, row.ID)
			}
			if err != nil {
				return actionMsg{err: fmt.Errorf("%s %q: %w", verb, row.Name, err)}
			}
			sessionName = resp.Thread.SessionName
		}
		if sessionName == "" {
			return actionMsg{err: fmt.Errorf("enter %q: thread has no session", row.Name)}
		}
		target := row.Machine + ":" + sessionName
		// Outside tmux entirely (a plain shell): there's no client to switch — ATTACH
		// this terminal to the thread instead. The TUI quits and the caller execs
		// `tmux nav --attach` (which, unlike the TUI, can reach the peer registry for a
		// remote attach). Inside tmux: switch in place / via the master.
		if m.tmux == "" {
			return attachMsg{target: target}
		}
		args := []string{"tmux", "nav", "--to", target}
		if useInClient {
			args = append(args, "--in-client")
		}
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return actionMsg{err: fmt.Errorf("nav %s: %v: %s", target, err, strings.TrimSpace(string(out)))}
		}
		return navDoneMsg{}
	}
}

// onWorkSocket reports whether the $TMUX value `tmux` shows we're a client on tmux
// socket `name` (its basename matches).
func onWorkSocket(tmux, name string) bool {
	if tmux == "" || name == "" {
		return false
	}
	return filepath.Base(strings.SplitN(tmux, ",", 2)[0]) == name
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
	// Per-machine freshness (offline browsing): show every peer's staleness, and
	// flag any that are offline (their last-known threads are still listed above).
	for _, mv := range m.machines {
		if mv.Self {
			continue
		}
		age := m.fetchedAt - mv.SyncedAtUnix
		if mv.Reachable {
			b.WriteString(styleDim.Render(fmt.Sprintf("  %s · synced %ds ago", mv.Machine, age)) + "\n")
		} else {
			b.WriteString(styleDim.Render(fmt.Sprintf("  ! %s OFFLINE · last seen %ds ago", mv.Machine, age)) + "\n")
		}
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
