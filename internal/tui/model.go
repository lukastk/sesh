// Package tui is the sesh live grid — a Bubble Tea app over the daemon's
// HTTP+JSON surface. It is a THIN renderer + action dispatcher: it owns no domain
// logic, and its ONLY source of state is the api.http-json client. By rule it
// imports internal/client and internal/api but never internal/store or daemon
// internals (enforced by a test), so "the grid renders real daemon state" and
// "actions really act" are testable claims, not vibes.
package tui

import (
	"bytes"
	"context"
	"encoding/json"
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
// View indexes into the model's view list: the three built-ins followed by
// the config-defined custom views ([[tui.views]]). Tab cycles; the current
// view's name is rendered in the title.
type View int

const (
	ViewActive   View = iota // non-archived (the default)
	ViewArchived             // archived only
	ViewAll                  // everything
	viewBuiltins             // = number of built-ins; custom views follow
)

// customView is one compiled [[tui.views]] entry.
type customView struct {
	name string
	pred Predicate
}

func (m Model) viewName() string {
	switch m.view {
	case ViewActive:
		return "active"
	case ViewArchived:
		return "archived"
	case ViewAll:
		return "all"
	}
	if i := int(m.view - viewBuiltins); i >= 0 && i < len(m.customViews) {
		return m.customViews[i].name
	}
	return "?"
}

func (m Model) viewCount() int { return int(viewBuiltins) + len(m.customViews) }

// WithViews installs the compiled custom views (see CompileViews).
func (m Model) WithViews(views []customView) Model {
	m.customViews = views
	return m
}

// ViewSpec is one [[tui.views]] entry as the caller hands it over.
type ViewSpec struct{ Name, Filter string }

// CompileViews compiles [[tui.views]] specs (loud on a broken name/filter).
func CompileViews(specs []ViewSpec) ([]customView, error) {
	out := make([]customView, 0, len(specs))
	for i, sp := range specs {
		if sp.Name == "" || sp.Filter == "" {
			return nil, fmt.Errorf("[tui.views] entry %d: name and filter are both required", i+1)
		}
		pred, err := CompilePredicate(sp.Filter)
		if err != nil {
			return nil, fmt.Errorf("[tui.views] %q: %w", sp.Name, err)
		}
		out = append(out, customView{name: sp.Name, pred: pred})
	}
	return out, nil
}

// promptKind says what the line-prompt input will do on submit.
type promptKind int

const (
	promptNone   promptKind = iota
	promptRename            // rename the selected thread
	promptTag               // add a tag to the selected thread
)

type Model struct {
	client      *client.Client
	allMachines bool
	view        View
	showID      bool // `i` toggles an ID column (tid8) — the only id surface in the TUI

	// Line-prompt input mode (rename / tag-add). While prompting, ordinary keys
	// edit the input; Enter submits, Esc cancels.
	prompting   promptKind
	promptInput []rune
	promptRow   api.ThreadRow // the row the prompt acts on (captured at open)

	// preselectID: when set, the first successful fetch moves the cursor to this
	// thread (the `--cursor` start-on-current-thread affordance), then clears it.
	preselectID string

	// uuidPopup: `y` shows the selected thread's FULL uuid in a popup; `c` inside
	// it copies to the system clipboard, any other key closes. note is the last
	// one-line status (e.g. "UUID copied"), shown dim under the title.
	uuidPopup bool
	note      string

	// customViews are the compiled [[tui.views]] entries (after the built-ins
	// in the Tab cycle).
	customViews []customView

	// Filter state (see filter.go): filtering = the prompt is being edited;
	// filter = the ACTIVE query (persists when applied via Esc); filterCaret =
	// rune index of the text caret; target = what the query matches.
	filtering   bool
	filter      string
	filterCaret int
	target      filterTarget

	// columns is the visible column set (validated names; see columns.go).
	// userHome powers the CWD column's ~-relative display; cwdLabeler, when set,
	// transforms it further ([[cwd_label]] rules — see WithCwdLabeler).
	columns    []string
	userHome   string
	cwdLabeler func(cwd string) string

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
	// clientName is the tmux client this TUI is rendered on (resolved by the caller
	// BEFORE the TUI grabs the terminal, while stdin is still the tty — see runTUI).
	// In-client nav passes it as `--client` so the switch targets exactly this client:
	// a ttyless nav subprocess cannot resolve it itself (tmux's ambient "current
	// client" fallback picks an arbitrary client — observed live moving a master
	// window's attach instead of the invoker).
	clientName string

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
	home, _ := os.UserHomeDir()
	return Model{client: client.New(socketPath), allMachines: allMachines, binaryPath: bin,
		tmux: os.Getenv("TMUX"), columns: append([]string(nil), DefaultColumns...), userHome: home}
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

// WithClient sets the tmux client this TUI renders on (for in-client nav's --client).
func (m Model) WithClient(name string) Model {
	m.clientName = name
	return m
}

// WithLocal sets this client's own machine + work socket, enabling in-client nav for
// a local thread when the TUI is inside that work socket's tmux.
func (m Model) WithLocal(machine, tmuxSocket string) Model {
	m.machine = machine
	m.tmuxSocket = tmuxSocket
	return m
}

// WithCwdLabeler sets the CWD-column display transform (the compiled
// [[cwd_label]] rules; errors are resolved by the CALLER — a labeler given here
// is total). Unset = ~-relative paths.
func (m Model) WithCwdLabeler(f func(string) string) Model {
	m.cwdLabeler = f
	return m
}

// WithPreselect makes the first fetch place the cursor on the given thread id
// (no-op if the thread is not in the current view).
func (m Model) WithPreselect(threadID string) Model {
	m.preselectID = threadID
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
	c, view, all := m.client, m.view, m.allMachines
	var pred *Predicate
	if i := int(view - viewBuiltins); i >= 0 && i < len(m.customViews) {
		p := m.customViews[i].pred
		pred = &p
	}
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
				row := api.ThreadRow{Thread: t.Thread, Head: t.Head, Busy: t.Busy, Attachment: t.Attachment, TicketsOpen: t.TicketsOpen}
				if pred != nil {
					// A custom view sees EVERYTHING its predicate admits
					// (archived included — `not archived` is the user's call).
					if !pred.Eval(row) {
						continue
					}
				} else if (view == ViewActive && t.Archived) || (view == ViewArchived && !t.Archived) {
					continue
				}
				rows = append(rows, row)
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
			if m.preselectID != "" {
				for i, rm := range m.visibleMatches() {
					if rm.row.ID == m.preselectID {
						m.cursor = i
						break
					}
				}
				m.preselectID = "" // one-shot: only the first fetch positions the cursor
			}
			if vis := len(m.visibleMatches()); m.cursor >= vis {
				m.cursor = max(0, vis-1)
			}
		}
		return m, tick()
	case tickMsg:
		return m, m.fetch()
	case actionMsg:
		m.note = ""
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
	if m.uuidPopup {
		return m.handleUUIDKey(msg)
	}
	if m.prompting != promptNone {
		return m.handlePromptKey(msg)
	}
	if m.filtering {
		return m.handleFilterKey(msg)
	}
	switch msg.String() {
	// Esc quits from normal mode. (When a filter mode lands, Esc-in-filter will
	// apply/leave the filter first, v1-style — quitting stays a normal-mode-only Esc.)
	case "q", "esc", "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		m.moveCursor(-1) // wraps (fzf --cycle feel), over the VISIBLE (filtered) rows
	case "down", "j":
		m.moveCursor(1)
	case "/":
		m.filtering = true
		m.filterCaret = len([]rune(m.filter))
	case "tab":
		m.view = (m.view + 1) % View(m.viewCount())
		m.cursor = 0
		return m, m.fetch()
	case "i":
		m.showID = !m.showID
	case "y":
		if _, ok := m.Selected(); ok {
			m.uuidPopup = true
		}
	case "R":
		return m, m.fetch()
	case "r":
		if row, ok := m.Selected(); ok {
			m.prompting, m.promptRow, m.promptInput = promptRename, row, []rune(row.Name)
		}
	case "t":
		if row, ok := m.Selected(); ok {
			m.prompting, m.promptRow, m.promptInput = promptTag, row, nil
		}
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

// handleUUIDKey: inside the uuid popup, `c` copies the full uuid to the system
// clipboard (failure is a LOUD error in the error line); any key closes the popup.
func (m Model) handleUUIDKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.uuidPopup = false
	if msg.String() == "c" {
		if row, ok := m.Selected(); ok {
			if err := copyToClipboard(row.ID); err != nil {
				m.lastErr = fmt.Errorf("copy uuid: %w", err)
			} else {
				m.note = "UUID copied to clipboard"
			}
		}
	}
	return m, nil
}

// handlePromptKey edits the line-prompt: Enter submits, Esc cancels, Backspace
// deletes, printable runes append.
func (m Model) handlePromptKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.prompting, m.promptInput = promptNone, nil
		return m, nil
	case "enter":
		kind, row, input := m.prompting, m.promptRow, strings.TrimSpace(string(m.promptInput))
		m.prompting, m.promptInput = promptNone, nil
		if input == "" {
			return m, nil
		}
		switch kind {
		case promptRename:
			return m, m.renameRow(row, input)
		case promptTag:
			return m, m.tagRow(row, input)
		}
		return m, nil
	case "backspace":
		if len(m.promptInput) > 0 {
			m.promptInput = m.promptInput[:len(m.promptInput)-1]
		}
		return m, nil
	}
	switch msg.Type {
	case tea.KeyRunes:
		m.promptInput = append(m.promptInput, msg.Runes...)
	case tea.KeySpace:
		m.promptInput = append(m.promptInput, ' ')
	}
	return m, nil
}

// renameRow renames a thread via the CLI verb (uniform local/remote: the CLI's
// --machine routing reaches the owning daemon; the TUI does not re-implement it).
func (m Model) renameRow(row api.ThreadRow, name string) tea.Cmd {
	bin, env, machine := m.binaryPath, m.navEnv, m.machine
	return func() tea.Msg {
		args := []string{"thread", "rename", "--id", row.ID, "--name", name}
		if machine == "" || row.Machine != machine {
			args = append(args, "--machine", row.Machine)
		}
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return actionMsg{err: fmt.Errorf("rename %q: %v: %s", row.Name, err, strings.TrimSpace(string(out)))}
		}
		return actionMsg{}
	}
}

// tagRow adds a tag to a thread via the CLI verb (routing as renameRow).
func (m Model) tagRow(row api.ThreadRow, tag string) tea.Cmd {
	bin, env, machine := m.binaryPath, m.navEnv, m.machine
	return func() tea.Msg {
		args := []string{"thread", "tag", "--id", row.ID, "--add", tag}
		if machine == "" || row.Machine != machine {
			args = append(args, "--machine", row.Machine)
		}
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return actionMsg{err: fmt.Errorf("tag %q: %v: %s", row.Name, err, strings.TrimSpace(string(out)))}
		}
		return actionMsg{}
	}
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
		// headless·busy: a turn is mid-flight — there is no pane to enter and a
		// revival would fork the conversation (the daemon would 409 anyway): loud.
		if row.Head == api.Headless && row.Busy == api.BusyBusy {
			return actionMsg{err: fmt.Errorf("%q: a headless turn is in flight — wait for it to finish (◌▶ → ◌·), then enter", row.Name)}
		}
		// headless·idle => no runtime to enter: REVIVE it first (a resumable
		// conversation, whether it last ran headed or headless), on the local
		// daemon directly or ROUTED to the owning machine (`--machine`, the same
		// mesh routing the CLI uses) — then enter.
		if row.Head == api.Headless {
			if local {
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel()
				resp, err := m.client.ThreadResume(ctx, row.ID)
				if err != nil {
					return actionMsg{err: fmt.Errorf("revive %q: %w", row.Name, err)}
				}
				sessionName = resp.Thread.SessionName
			} else {
				cmd := exec.Command(bin, "thread", "resume", "--id", row.ID, "--machine", row.Machine)
				cmd.Env = append(os.Environ(), env...)
				if out, err := cmd.CombinedOutput(); err != nil {
					return actionMsg{err: fmt.Errorf("revive %q on %s: %v: %s", row.Name, row.Machine, err, strings.TrimSpace(string(out)))}
				}
				// Revival can mint the session name — re-resolve it on the owner.
				lout, err := routedSessionName(bin, env, row.Machine, row.ID)
				if err != nil {
					return actionMsg{err: fmt.Errorf("revive %q: re-resolve session: %w", row.Name, err)}
				}
				sessionName = lout
			}
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
			if m.clientName != "" {
				args = append(args, "--client", m.clientName)
			}
		}
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return actionMsg{err: fmt.Errorf("nav %s: %v: %s", target, err, strings.TrimSpace(string(out)))}
		}
		return navDoneMsg{}
	}
}

// routedSessionName fetches a thread's session name from its owning machine via the
// routed CLI (`thread list --json --machine M`) — used after a routed promote/resume,
// which can mint the session name on the owner.
func routedSessionName(bin string, env []string, machine, id string) (string, error) {
	cmd := exec.Command(bin, "thread", "list", "--json", "--archived", "--machine", machine)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var th api.Thread
		if err := dec.Decode(&th); err != nil {
			return "", err
		}
		if th.ID == id {
			if th.SessionName == "" {
				return "", fmt.Errorf("thread %s has no session name on %s", id, machine)
			}
			return th.SessionName, nil
		}
	}
	return "", fmt.Errorf("thread %s not found on %s", id, machine)
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

// archiveSelected TOGGLES the selected thread's archived state (archive in the
// active view, unarchive in the archived/all views — the row knows which).
func (m Model) archiveSelected() tea.Cmd {
	row, ok := m.Selected()
	if !ok {
		return nil
	}
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return actionMsg{err: c.ThreadArchive(ctx, row.ID, !row.Archived)}
	}
}

// Selected returns the VISIBLE row under the cursor, if any (the filter
// narrows what the cursor moves over).
func (m Model) Selected() (api.ThreadRow, bool) {
	vis := m.visibleMatches()
	if m.cursor < 0 || m.cursor >= len(vis) {
		return api.ThreadRow{}, false
	}
	return vis[m.cursor].row, true
}

// Rows exposes the current rows (for tests).
func (m Model) Rows() []api.ThreadRow { return m.rows }

// CurrentView exposes the active view (for tests).
func (m Model) CurrentView() View { return m.view }

// Cursor exposes the cursor index (for tests).
func (m Model) Cursor() int { return m.cursor }

// Prompting reports whether the line-prompt is open (for tests).
func (m Model) Prompting() bool { return m.prompting != promptNone }

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
	styleMatch    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
)

// Glyph maps a row's live state to a status glyph. These are part of the contract
// the TUI conformance asserts against REAL state.
//
// HeadGlyph renders the runtime-form axis:
//
//	● headful (a live pane — enterable)   ◌ headless (no pane)   ? unknown
func HeadGlyph(row api.ThreadRow) string {
	switch row.Head {
	case api.Headful:
		return "●"
	case api.Headless:
		return "◌"
	default:
		return "?"
	}
}

// BusyGlyph renders the execution axis:
//
//	▶ busy (a turn is executing)   · idle (quiet)   ? unknown
func BusyGlyph(row api.ThreadRow) string {
	switch row.Busy {
	case api.BusyBusy:
		return "▶"
	case api.BusyIdle:
		return "·"
	default:
		return "?"
	}
}

// View renders the grid. Kept pure (Model -> string) so a snapshot test can assert
// the rendered output reflects the model's REAL rows.
func (m Model) View() string {
	var b strings.Builder
	b.WriteString(styleHeader.Render("sesh — live threads · ["+m.viewName()+"]") + "\n")
	if m.lastErr != nil {
		b.WriteString(styleDim.Render("(daemon unreachable: "+m.lastErr.Error()+")") + "\n")
	}
	if m.note != "" {
		b.WriteString(styleDim.Render(m.note) + "\n")
	}
	if m.uuidPopup {
		if row, ok := m.Selected(); ok {
			b.WriteString(styleHeader.Render("┃ "+row.ID+" ┃") + "\n")
			b.WriteString(styleDim.Render("  c to copy · any other key to close") + "\n")
		}
	}
	if m.prompting != promptNone {
		label := "rename"
		if m.prompting == promptTag {
			label = "tag"
		}
		b.WriteString(styleHeader.Render(fmt.Sprintf("%s %q> %s█", label, m.promptRow.Name, string(m.promptInput))) + "\n")
	}
	cols := m.activeColumns()
	widths := m.colWidths(cols)
	b.WriteString(styleHeader.Render("  HB  "+m.renderHeader(cols, widths)) + "\n")

	vis := m.visibleMatches()
	if len(vis) == 0 {
		b.WriteString(styleDim.Render("  (no threads)") + "\n")
	}
	for i, rm := range vis {
		row := rm.row
		att := " "
		if row.Attachment == api.Attached {
			att = "*" // a client is attached
		}
		if i == m.cursor {
			// The selected row uses reverse video; matched-rune styling inside it
			// would reset the reverse — selection is the dominant cue.
			line := HeadGlyph(row) + BusyGlyph(row) + att + " " + m.renderCells(cols, widths, row, nil)
			b.WriteString(styleSelected.Render("> "+line) + "\n")
		} else {
			line := HeadGlyph(row) + BusyGlyph(row) + att + " " + m.renderCells(cols, widths, row, rm.pos)
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
	switch {
	case m.filtering:
		b.WriteString("\n" + m.renderFilterPrompt(len(vis), len(m.rows)) + "\n")
	case m.filter != "":
		b.WriteString(styleDim.Render(fmt.Sprintf("\n  filter: %s (%d/%d) · / to edit", m.filter, len(vis), len(m.rows))) + "\n")
		b.WriteString(styleDim.Render("  ↑/↓ move · enter nav · / filter · tab view · r rename · t tag · i ids · y uuid · x stop · d delete · a archive · R refresh · q/esc quit") + "\n")
	default:
		b.WriteString(styleDim.Render("\n  ↑/↓ move · enter nav · / filter · tab view · r rename · t tag · i ids · y uuid · x stop · d delete · a archive · R refresh · q/esc quit") + "\n")
	}
	return b.String()
}

// trunc shortens to n COLUMNS' worth of runes (byte slicing would split
// multi-byte runes — session/thread names contain non-ASCII).
func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
