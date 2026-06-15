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

// postActionReconcileDelay schedules ONE fast follow-up reconcile after a
// successful action. The immediate post-action fetch races the maintainer's
// snapshot republish (it ticks every ~300ms and the mesh/self view is read from
// THAT published snapshot, not live from the DB), so a structural change with no
// optimistic patch — notably `reparent` (incl. reparent-to-root on an empty
// prompt) — would otherwise not show until the next 3s poll. 500ms reliably
// outlasts one maintainer tick, so the change appears promptly.
const postActionReconcileDelay = 500 * time.Millisecond

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
	promptNone     promptKind = iota
	promptRename              // rename the selected thread
	promptTag                 // add a tag to the selected thread
	promptReparent            // set the selected thread's parent (paste a uuid; empty = root)
)

// confirmKind says which destructive action a y/n confirmation popup gates.
type confirmKind int

const (
	confirmNone   confirmKind = iota
	confirmDelete             // `d` — drop the record
	confirmArchive            // `a` — archive/unarchive toggle
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

	// masterCursorMachine: when set (the master popup's prefix+s), the TUI resolves
	// the thread the active master window is CURRENTLY showing — ASYNCHRONOUSLY, after
	// the first render, so it never delays startup — then jumps the cursor to it. The
	// resolve runs on that machine's daemon (the work server it owns), routed when
	// remote. masterCursorDone makes it a one-shot.
	masterCursorMachine string
	masterCursorDone    bool

	// uuidPopup: `y` shows the selected thread's FULL uuid in a popup; `c` inside
	// it copies to the system clipboard, any other key closes. note is the last
	// one-line status (e.g. "UUID copied"), shown dim under the title.
	uuidPopup bool
	note      string

	// confirming, when non-zero, means a y/n confirmation popup is open for a
	// destructive action (delete / archive); confirmRow is the row it acts on
	// (captured when the popup opened). `y` runs it, anything else cancels.
	confirming confirmKind
	confirmRow api.ThreadRow

	// actionErr is the last in-app ACTION error (reparent/delete/tag/…). Unlike
	// lastErr (fetch/daemon reachability), it PERSISTS across reconcile fetches so a
	// failed mutation stays on screen — it is cleared only by the next action. (A
	// reconcile fetch silently clearing it was the "no warning on a bad reparent" bug.)
	actionErr error

	// tagPopup: `T` opens a picker over the selected thread's tags; ↑/↓ (or j/k) move,
	// enter removes the highlighted tag (routed to the owner), esc/q closes. tagPopupRow
	// holds the tag list captured at open and is decremented optimistically as tags are
	// removed, so several can be stripped without reopening; the popup closes when the
	// last tag goes.
	tagPopup       bool
	tagPopupRow    api.ThreadRow
	tagPopupCursor int

	// customViews are the compiled [[tui.views]] entries (after the built-ins
	// in the Tab cycle).
	customViews []customView

	// Tree fold state: per-node overrides + the configured default (children
	// start collapsed unless [tui] expand_children / --expand).
	expanded      map[string]bool
	defaultExpand bool

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
	// colColors tints individual columns ([[tui.column_color]] + built-in defaults).
	// Applied to non-selected, non-highlighted cells only (see renderCells).
	colColors map[string]lipgloss.Style

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

	// pending holds confirmed-but-not-yet-reflected optimistic mutations
	// (rename/tag/notify): the daemon applied them, but the TUI's read path
	// (maintainer snapshot + mesh-sync) lags the write by a tick, so without this a
	// reconciling fetch would briefly redisplay the STALE value. Applied to matching
	// rows after each fetch; dropped once the server agrees — or after a few cycles
	// (ttl), so a mutation that silently didn't take SURFACES rather than sticking.
	pending map[string]*rowPatch

	// tickStarted guards the one-time bootstrap of the poll timer (see meshMsg).
	tickStarted bool

	rows      []api.ThreadRow
	machines  []api.MachineView // per-machine freshness, for the staleness footer
	fetchedAt int64             // unix time the current data was fetched (for staleness age)
	cursor    int
	err       error
	lastErr   error
	width     int
	height    int
	// vOffset is the index of the first row in the vertical viewport; hOffset is the
	// number of leading data columns scrolled past (horizontal pan). Both are 0 when
	// nothing is clipped. Only active when width/height are known (a WindowSizeMsg has
	// arrived) — with them unset the whole grid renders unclipped.
	vOffset int
	hOffset int
	// Mouse-wheel sensitivity ([tui] mouse_scroll_v/h, default 1): how many wheel
	// notches move one row / pan one column. wheelAccV/H accumulate notches between
	// steps so a higher divisor dampens fast trackpad scrolling.
	scrollDivV, scrollDivH int
	wheelAccV, wheelAccH   int

	// attachTarget, when set, means the TUI quit in order to ATTACH the terminal to a
	// thread (Enter from a plain shell, outside tmux). The caller reads PendingAttach
	// after Run() and execs the attach. "" = quit normally. attachThread carries the
	// thread id so the attach lands on the window holding its pane.
	attachTarget string
	attachThread string

	// Tickets view (K): a full-screen takeover listing the selected thread's tickets,
	// drilling into one ticket's fields + actions, editing a field in $EDITOR. See
	// tickets.go. ticketMode == ticketNone means the view is closed.
	ticketMode         ticketMode
	ticketThread       api.ThreadRow // the thread whose tickets are shown
	tickets            []api.Ticket
	ticketCursor       int    // selection in the list
	ticketDetail       int    // selection in the detail item list
	ticketErr          error  // loud ticket-view error (load/action)
	ticketStatusCursor int    // selection in the status picker
	ticketPickQuery    []rune // change-thread picker query (search by name/uuid)
	ticketPickCursor   int
	ticketNewInput     []rune // name buffer while creating a new ticket (n in the list)
	editor             string // editor for in-TUI field edits (--editor / [tui] editor / $EDITOR)
}

// New builds a model talking to the daemon at socketPath.
func New(socketPath string, allMachines bool) Model {
	bin, err := os.Executable()
	if err != nil {
		bin = "sesh"
	}
	home, _ := os.UserHomeDir()
	return Model{client: client.New(socketPath), allMachines: allMachines, binaryPath: bin,
		tmux: os.Getenv("TMUX"), columns: append([]string(nil), DefaultColumns...), userHome: home,
		scrollDivV: 1, scrollDivH: 1}
}

// WithMouseScroll sets the wheel sensitivity divisors ([tui] mouse_scroll_v/h): how
// many notches move one row / pan one column. Values < 1 clamp to 1.
func (m Model) WithMouseScroll(v, h int) Model {
	if v < 1 {
		v = 1
	}
	if h < 1 {
		h = 1
	}
	m.scrollDivV, m.scrollDivH = v, h
	return m
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
	argv := []string{m.binaryPath, "tmux", "nav", "--to", m.attachTarget, "--attach"}
	if m.attachThread != "" {
		argv = append(argv, "--thread", m.attachThread)
	}
	return argv, true
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

// WithColumnColors sets the per-column colour styles (already validated +
// defaults-merged via ResolveColumnColors). Unset = no per-column tint.
func (m Model) WithColumnColors(c map[string]lipgloss.Style) Model {
	m.colColors = c
	return m
}

// WithPreselect makes the first fetch place the cursor on the given thread id
// (no-op if the thread is not in the current view).
func (m Model) WithPreselect(threadID string) Model {
	m.preselectID = threadID
	return m
}

// WithMasterCursor makes the TUI asynchronously resolve, then jump to, the thread
// the given machine's master window is currently showing (the master prefix+s
// affordance). machine is the active master window's machine. The resolve happens
// AFTER the first render, so startup is never blocked.
func (m Model) WithMasterCursor(machine string) Model {
	m.masterCursorMachine = machine
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

// Init kicks off the first fetch. (The single poll timer is started from the first
// meshMsg — see tickStarted — so Init stays a lone fetch cmd, which the conformance
// harness drives directly.)
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
				row := api.ThreadRow{Thread: t.Thread, Head: t.Head, Busy: t.Busy, Attachment: t.Attachment, TicketsOpen: t.TicketsOpen, TicketName: t.TicketName, TicketNeedsInput: t.TicketNeedsInput, CwdRel: t.CwdRel}
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
		m.ensureCursorVisible() // a resize can shrink the viewport under the cursor
		if m.hOffset > m.maxHOffset() {
			m.hOffset = m.maxHOffset()
		}
		return m, nil
	case tea.MouseMsg:
		// The mouse wheel moves the SELECTION between rows (up/down, like ↑/↓, viewport
		// following — works even when the grid fits the screen, unlike a viewport-only
		// scroll). Horizontal pan (like h/l) comes from a native wheel-left/right OR
		// Shift+vertical-wheel — the reliable cross-terminal path, since many terminals
		// don't emit horizontal wheel events. Sensitivity divisors ([tui] mouse_scroll_*)
		// dampen fast scrolling: a notch acts only every Nth event.
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			if msg.Shift {
				m.wheelPanH(-1)
			} else {
				m.wheelMoveV(-1)
			}
		case tea.MouseButtonWheelDown:
			if msg.Shift {
				m.wheelPanH(1)
			} else {
				m.wheelMoveV(1)
			}
		case tea.MouseButtonWheelLeft:
			m.wheelPanH(-1)
		case tea.MouseButtonWheelRight:
			m.wheelPanH(1)
		}
		return m, nil
	case meshMsg:
		if msg.err != nil {
			m.lastErr = msg.err
		} else {
			m.lastErr = nil
			m.rows = msg.rows
			m.machines = msg.machines
			m.fetchedAt = msg.fetchedAt
			m.applyPending(true) // reconcile: re-apply optimistic patches, GC satisfied/expired ones
			if m.preselectID != "" && m.positionCursorOn(m.preselectID) {
				m.preselectID = "" // landed — release it so the user can move freely
			}
			if vis := len(m.visibleMatches()); m.cursor >= vis {
				m.cursor = max(0, vis-1)
			}
			m.ensureCursorVisible() // the row set changed: keep the viewport over the cursor
		}
		// Bootstrap the SINGLE poll timer exactly once (on the first successful
		// fetch). Subsequent meshMsgs — including those from action/reconcile
		// fetches — do NOT re-arm, so the poll rate can't multiply. On the same first
		// fetch, kick the (async) master-cursor resolve so it never blocks startup.
		if !m.tickStarted {
			m.tickStarted = true
			if m.masterCursorMachine != "" && !m.masterCursorDone {
				m.masterCursorDone = true
				return m, tea.Batch(tick(), m.resolveMasterCursor())
			}
			return m, tick()
		}
		return m, nil
	case preselectMsg:
		// The async master-cursor resolve landed: jump to the thread the master window
		// is showing. Route it through the persistent preselect so that if the row
		// isn't published yet, a later fetch still lands it (expanding a nested child's
		// ancestors). Empty id = nothing to preselect (no master client / plain shell).
		if msg.id != "" {
			if !m.positionCursorOn(msg.id) {
				m.preselectID = msg.id
			}
		}
		return m, nil
	case tickMsg:
		return m, tea.Batch(m.fetch(), tick())
	case reconcileMsg:
		// A fast follow-up reconcile after a successful action (does NOT re-arm the
		// poll timer — it's a one-shot, scheduled by the actionMsg handler).
		return m, m.fetch()
	case actionMsg:
		m.note = ""
		if msg.err != nil {
			// Action errors live in actionErr, NOT lastErr: the reconcile fetch below
			// clears lastErr on success, which would instantly erase this warning.
			m.actionErr = msg.err
		} else {
			m.actionErr = nil
			if msg.patch != nil && msg.id != "" {
				// The mutation is CONFIRMED (no error). Record it as an optimistic patch
				// and apply it now, so the value updates instantly instead of waiting for
				// the snapshot/mesh-sync read path to catch up.
				if m.pending == nil {
					m.pending = map[string]*rowPatch{}
				}
				if cur, ok := m.pending[msg.id]; ok {
					cur.merge(msg.patch)
				} else {
					m.pending[msg.id] = msg.patch
				}
				m.applyPending(false) // apply for instant feedback (no GC — server is still stale)
				// An optimistic hide can drop the row under/below the cursor — keep it in range.
				if vis := len(m.visibleMatches()); m.cursor >= vis {
					m.cursor = max(0, vis-1)
				}
				m.ensureCursorVisible()
			}
		}
		if msg.err == nil && msg.preselect != "" {
			m.preselectID = msg.preselect // land the cursor on the moved node once the refetch lands
		}
		if msg.err == nil && msg.expand != "" {
			if m.expanded == nil {
				m.expanded = map[string]bool{}
			}
			m.expanded[msg.expand] = true // keep the new parent open so the moved child is visible
		}
		if msg.err == nil {
			// Reconcile now AND once more shortly: the immediate fetch can outrun the
			// maintainer's snapshot republish, so a no-optimistic-patch structural
			// change (reparent, incl. reparent-to-root) would otherwise not show until
			// the next 3s poll. The delayed reconcile makes it appear promptly.
			return m, tea.Batch(m.fetch(), reconcileAfter(postActionReconcileDelay))
		}
		return m, m.fetch() // reconcile against the daemon
	case navDoneMsg:
		// The selected thread is now on screen (the client switched under us) —
		// quit so the TUI (and the popup hosting it) gets out of the way. Staying
		// open would leave the TUI covering the very thread the user entered.
		return m, tea.Quit
	case attachMsg:
		// Quit so the terminal is restored, then runTUI execs the attach.
		m.attachTarget, m.attachThread = msg.target, msg.thread
		return m, tea.Quit
	case ticketsLoadedMsg:
		// Ignore a stale load (the user backed out / switched threads).
		if m.ticketMode == ticketNone || msg.thread.ID != m.ticketThread.ID {
			return m, nil
		}
		if msg.err != nil {
			m.ticketErr = msg.err
			return m, nil
		}
		m.ticketErr = nil
		m.tickets = msg.tickets
		if m.ticketCursor >= len(m.tickets) {
			m.ticketCursor = max(0, len(m.tickets)-1)
		}
		return m, nil
	case ticketEditDoneMsg:
		return m, m.applyTicketEdit(msg)
	case ticketActionMsg:
		if msg.err != nil {
			m.ticketErr = msg.err
			return m, nil
		}
		m.ticketErr = nil
		m.note = msg.note
		if msg.reload != "" {
			return m, m.loadTickets(m.ticketThread)
		}
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// actionMsg is the result of an in-app mutation. On success (err == nil) an
// optional patch records the confirmed change for optimistic display (see pending).
type actionMsg struct {
	err       error
	id        string    // the thread the mutation targeted
	patch     *rowPatch // the optimistic change to show until the daemon's read path catches up
	preselect string    // on success, move the cursor here on the next fetch (structural changes like reparent: no patch, the tree refetches and the moved node is re-selected)
	expand    string    // on success, expand this node so a freshly-nested child stays visible even before the snapshot reflects the new parent (avoids the preselect/propagation race)
}

// reconcileMsg fires after postActionReconcileDelay to trigger one extra fetch (see
// reconcileAfter) so a server-side change that lagged the immediate post-action fetch
// shows promptly instead of waiting for the next 3s poll.
type reconcileMsg struct{}

// reconcileAfter schedules a single delayed reconcile fetch (via reconcileMsg).
func reconcileAfter(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return reconcileMsg{} })
}

// preselectMsg carries the result of the async master-cursor resolve (the thread the
// active master window is showing). id == "" means nothing to preselect (no master
// client, or its pane is a plain shell) — a legitimate no-op, never an error.
type preselectMsg struct{ id string }

// resolveMasterCursor execs `tmux master-current` against the active master window's
// machine (routed when remote), off the main loop, returning a preselectMsg. Failures
// resolve to an empty preselect — a missing thread must never disrupt the TUI.
func (m Model) resolveMasterCursor() tea.Cmd {
	bin, env, origin, machine := m.binaryPath, m.navEnv, m.machine, m.masterCursorMachine
	return func() tea.Msg {
		args := []string{"tmux", "master-current", "--origin", origin}
		if machine != "" && machine != origin {
			args = append(args, "--machine", machine)
		}
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.Output()
		if err != nil {
			return preselectMsg{} // resolve failed → no preselect (non-fatal)
		}
		return preselectMsg{id: strings.TrimSpace(string(out))}
	}
}

// positionCursorOn moves the cursor to the row for id, expanding a nested child's
// ancestors first so it's actually visible in the tree. Returns true if the row was
// found and the cursor placed; false if id isn't in the current rows yet (the caller
// keeps the pending preselect so a later fetch — once the maintainer has published it
// — lands it).
func (m *Model) positionCursorOn(id string) bool {
	if id == "" {
		return false
	}
	m.expandAncestors(id)
	for i, rm := range m.visibleMatches() {
		if rm.row.ID == id {
			m.cursor = i
			return true
		}
	}
	return false
}

// expandAncestors opens every ancestor of id so a collapsed child becomes visible.
func (m *Model) expandAncestors(id string) {
	byID := make(map[string]api.ThreadRow, len(m.rows))
	for _, r := range m.rows {
		byID[r.ID] = r
	}
	if m.expanded == nil {
		m.expanded = map[string]bool{}
	}
	cur, ok := byID[id]
	seen := map[string]bool{} // cycle guard
	for ok && cur.Parent != "" && !seen[cur.Parent] {
		seen[cur.Parent] = true
		m.expanded[cur.Parent] = true
		cur, ok = byID[cur.Parent]
	}
}

// optimisticTTL bounds how many reconcile fetches an unsatisfied optimistic patch
// survives before it's dropped and the server's truth shows. A mutation normally
// reflects within one fetch; surviving several cycles means it silently didn't take
// — which should surface, not be masked forever.
const optimisticTTL = 4

// rowPatch is a set of optimistic field overrides for one row (nil/empty = untouched).
type rowPatch struct {
	name       *string
	notify     *bool
	addTags    []string
	removeTags []string
	// hide drops the row from the CURRENT view instantly (archive/unarchive/delete
	// remove it from this view; the mesh read path lags the write by up to one
	// maintainer tick). Unlike the field overlays, it removes the row rather than
	// editing it. A pure hide patch carries no field overrides.
	hide bool
	ttl  int
}

func (p *rowPatch) merge(o *rowPatch) {
	if o.name != nil {
		p.name = o.name
	}
	if o.notify != nil {
		p.notify = o.notify
	}
	if o.hide {
		p.hide = true
	}
	p.addTags = append(p.addTags, o.addTags...)
	p.removeTags = append(p.removeTags, o.removeTags...)
	if o.ttl > p.ttl {
		p.ttl = o.ttl
	}
}

func (p *rowPatch) apply(r *api.ThreadRow) {
	if p.name != nil {
		r.Name = *p.name
	}
	if p.notify != nil {
		r.Notify = *p.notify
	}
	for _, t := range p.addTags {
		if !containsStr(r.Tags, t) {
			r.Tags = append(append([]string(nil), r.Tags...), t)
		}
	}
	for _, t := range p.removeTags {
		if containsStr(r.Tags, t) {
			r.Tags = removeStr(r.Tags, t)
		}
	}
}

// satisfied reports whether the server row already reflects every override.
func (p *rowPatch) satisfied(r api.ThreadRow) bool {
	if p.hide {
		// satisfied() is only consulted for a row STILL present in the fetch — i.e.
		// the read path still shows it in this view, so the hide hasn't landed.
		// (When it has landed, the row is absent and the patch is GC'd in applyPending.)
		return false
	}
	if p.name != nil && r.Name != *p.name {
		return false
	}
	if p.notify != nil && r.Notify != *p.notify {
		return false
	}
	for _, t := range p.addTags {
		if !containsStr(r.Tags, t) {
			return false
		}
	}
	for _, t := range p.removeTags {
		if containsStr(r.Tags, t) {
			return false
		}
	}
	return true
}

// applyPending overlays the pending optimistic patches onto m.rows. When gc is true
// (a reconcile after a fresh fetch), a patch the server has caught up to is dropped,
// a patch whose row is gone is dropped, and an unsatisfied patch's ttl is decremented
// (dropped at zero). When gc is false (right after recording a patch), it only
// overlays — the freshly-read rows are still stale, so nothing should be GC'd yet.
func (m *Model) applyPending(gc bool) {
	if len(m.pending) == 0 {
		return
	}
	idx := map[string]int{}
	for i := range m.rows {
		idx[m.rows[i].ID] = i
	}
	hidden := map[string]bool{}
	for id, p := range m.pending {
		i, ok := idx[id]
		if !ok {
			if gc {
				delete(m.pending, id) // the row vanished (deleted/archived-out) — give up
			}
			continue
		}
		if gc {
			if p.satisfied(m.rows[i]) {
				delete(m.pending, id)
				continue
			}
			p.ttl--
			if p.ttl <= 0 {
				delete(m.pending, id) // server never caught up — surface its truth
				continue
			}
		}
		if p.hide {
			hidden[id] = true // drop it from the view until the read path catches up (or TTL resurrects it)
		} else {
			p.apply(&m.rows[i])
		}
	}
	if len(hidden) > 0 {
		kept := m.rows[:0]
		for _, r := range m.rows {
			if !hidden[r.ID] {
				kept = append(kept, r)
			}
		}
		m.rows = kept
	}
}

func containsStr(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// removeStr returns a copy of s with every occurrence of v dropped.
func removeStr(s []string, v string) []string {
	out := make([]string, 0, len(s))
	for _, x := range s {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

func sptr(s string) *string { return &s }
func bptr(b bool) *bool     { return &b }

// attachMsg asks the TUI to quit and have the caller attach the terminal to target
// (<machine>:<session>) — used when Enter is pressed outside tmux. thread carries the
// thread id so the attach lands on the WINDOW holding its pane.
type attachMsg struct{ target, thread string }

// navDoneMsg reports a successful nav: the user is where they asked to be, so the
// TUI quits. (A FAILED nav stays an actionMsg with the error, keeping the TUI open.)
type navDoneMsg struct{}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.ticketMode != ticketNone {
		return m.handleTicketKey(msg)
	}
	if m.confirming != confirmNone {
		return m.handleConfirmKey(msg)
	}
	if m.uuidPopup {
		return m.handleUUIDKey(msg)
	}
	if m.tagPopup {
		return m.handleTagPopupKey(msg)
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
		m.ensureCursorVisible()
	case "down", "j":
		m.moveCursor(1)
		m.ensureCursorVisible()
	case "ctrl+k":
		m.scrollRows(-m.halfPage()) // scroll the viewport up a half-page (cursor follows into view)
	case "ctrl+j":
		m.scrollRows(m.halfPage())
	case "/":
		m.filtering = true
		m.filterCaret = len([]rune(m.filter))
	// Fold/unfold the tree is on the ARROW keys; h/l pan the columns horizontally so
	// clipped columns can be brought into view (Lukas, 2026-06-11).
	case "right":
		m.foldSelected(true)
	case "left":
		m.foldSelected(false)
	case "l":
		if m.hOffset < m.maxHOffset() {
			m.hOffset++
		}
	case "h":
		if m.hOffset > 0 {
			m.hOffset--
		}
	case "tab":
		m.view = (m.view + 1) % View(m.viewCount())
		m.cursor = 0
		return m, m.fetch()
	case "i":
		m.showID = !m.showID
	case "n":
		return m, m.notifySelected()
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
	case "T":
		if row, ok := m.Selected(); ok {
			if len(row.Tags) == 0 {
				m.note = "no tags to remove"
			} else {
				m.tagPopup, m.tagPopupRow, m.tagPopupCursor = true, row, 0
			}
		}
	case "P":
		if row, ok := m.Selected(); ok {
			m.prompting, m.promptRow, m.promptInput = promptReparent, row, nil
		}
	case "x":
		return m, m.stopSelected()
	case "d":
		// Destructive: confirm before dropping the record (y/n popup).
		if row, ok := m.Selected(); ok {
			m.confirming, m.confirmRow = confirmDelete, row
		}
	case "a":
		// Archive/unarchive toggle: confirm before parking the thread.
		if row, ok := m.Selected(); ok {
			m.confirming, m.confirmRow = confirmArchive, row
		}
	case "K":
		// Tickets view: a full-screen takeover of the selected thread's tickets.
		if row, ok := m.Selected(); ok {
			return m, m.openTicketView(row)
		}
	case "enter":
		return m, m.navSelected()
	}
	return m, nil
}

// handleConfirmKey drives the y/n confirmation popup for a destructive action:
// `y` (or `Y`) runs it on the captured row, ANY other key cancels (so a stray
// keystroke never deletes/archives). Routed to the owner like the direct verbs.
func (m Model) handleConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	kind, row := m.confirming, m.confirmRow
	m.confirming = confirmNone
	if s := msg.String(); s == "y" || s == "Y" {
		switch kind {
		case confirmDelete:
			return m, m.deleteRow(row)
		case confirmArchive:
			return m, m.archiveRow(row)
		}
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

// handleTagPopupKey drives the remove-tag picker: ↑/↓ (or j/k) move, enter removes
// the highlighted tag from the thread (routed to the owner) and drops it from the
// list, esc/q closes. The popup closes when the last tag is removed.
func (m Model) handleTagPopupKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	tags := m.tagPopupRow.Tags
	switch msg.String() {
	case "esc", "ctrl+c", "q":
		m.tagPopup = false
		return m, nil
	case "up", "k":
		if m.tagPopupCursor > 0 {
			m.tagPopupCursor--
		}
		return m, nil
	case "down", "j":
		if m.tagPopupCursor < len(tags)-1 {
			m.tagPopupCursor++
		}
		return m, nil
	case "enter":
		if m.tagPopupCursor < 0 || m.tagPopupCursor >= len(tags) {
			m.tagPopup = false
			return m, nil
		}
		tag := tags[m.tagPopupCursor]
		cmd := m.removeTagRow(m.tagPopupRow, tag)
		// Drop the tag from the popup's own list optimistically so several can be
		// stripped in one sitting; close when none remain, clamp the cursor otherwise.
		remaining := removeStr(tags, tag)
		m.tagPopupRow.Tags = remaining
		if len(remaining) == 0 {
			m.tagPopup = false
		} else if m.tagPopupCursor >= len(remaining) {
			m.tagPopupCursor = len(remaining) - 1
		}
		return m, cmd
	}
	return m, nil
}

// removeTagRow removes a tag from a thread via the `thread tag --remove` verb,
// routed to the owning machine, with an optimistic patch so the TAGS column drops
// it instantly.
func (m Model) removeTagRow(row api.ThreadRow, tag string) tea.Cmd {
	return m.routedVerb(row, &rowPatch{removeTags: []string{tag}, ttl: optimisticTTL}, "tag", "--remove", tag)
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
		switch kind {
		case promptRename:
			if input == "" {
				return m, nil
			}
			return m, m.renameRow(row, input)
		case promptTag:
			if input == "" {
				return m, nil
			}
			return m, m.tagRow(row, input)
		case promptReparent:
			// Empty input is meaningful here: make the thread a root.
			return m, m.reparentRow(row, input)
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
		return actionMsg{id: row.ID, patch: &rowPatch{name: sptr(name), ttl: optimisticTTL}}
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
		return actionMsg{id: row.ID, patch: &rowPatch{addTags: []string{tag}, ttl: optimisticTTL}}
	}
}

// reparentRow sets a thread's parent via the `thread reparent` verb (empty newParent
// = --root), routed to the owner. The tree reshapes, so there's NO optimistic patch
// (structural optimism is a code-smell risk) — on success the reconcile fetch refreshes
// the tree and the moved node is re-selected with its new ancestors expanded (preselect).
// A prefix is accepted: the CLI verb resolves --parent the same way it resolves --id.
func (m Model) reparentRow(row api.ThreadRow, newParent string) tea.Cmd {
	bin, env, machine := m.binaryPath, m.navEnv, m.machine
	return func() tea.Msg {
		args := []string{"thread", "reparent", "--id", row.ID}
		if newParent == "" {
			args = append(args, "--root")
		} else {
			args = append(args, "--parent", newParent)
		}
		if machine == "" || row.Machine != machine {
			args = append(args, "--machine", row.Machine)
		}
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return actionMsg{err: fmt.Errorf("reparent %q: %v: %s", row.Name, err, strings.TrimSpace(string(out)))}
		}
		return actionMsg{id: row.ID, preselect: row.ID, expand: newParent}
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
			return attachMsg{target: target, thread: row.ID}
		}
		// --thread makes nav land on the WINDOW holding this thread's pane (not the
		// session's last-active window) — resolved on the owner's work server.
		args := []string{"tmux", "nav", "--to", target, "--thread", row.ID}
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

// routedVerb execs `thread <verb> --id <row>` against the row's OWNING machine,
// adding --machine when the row isn't local so the mutation ROUTES over the mesh
// (http/ssh) — exactly like rename/tag. This is why these work on remote threads:
// the local daemon doesn't own them, so a direct client call would silently miss.
// On success the (optional) optimistic patch is returned for instant display.
func (m Model) routedVerb(row api.ThreadRow, patch *rowPatch, verb string, extra ...string) tea.Cmd {
	bin, env, machine := m.binaryPath, m.navEnv, m.machine
	return func() tea.Msg {
		args := append([]string{"thread", verb, "--id", row.ID}, extra...)
		if machine == "" || row.Machine != machine {
			args = append(args, "--machine", row.Machine)
		}
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return actionMsg{err: fmt.Errorf("%s %q: %v: %s", verb, row.Name, err, strings.TrimSpace(string(out)))}
		}
		return actionMsg{id: row.ID, patch: patch}
	}
}

// stopSelected ends the selected thread's runtime (agent + session) but keeps
// the record (it becomes a dead, resumable thread). Routed to the owner.
func (m Model) stopSelected() tea.Cmd {
	row, ok := m.Selected()
	if !ok {
		return nil
	}
	return m.routedVerb(row, nil, "stop")
}

// deleteSelected drops the selected thread's record. The daemon refuses a live
// thread (orphan guard); stop it first. Surfaced as an error in actionErr.
func (m Model) deleteSelected() tea.Cmd {
	row, ok := m.Selected()
	if !ok {
		return nil
	}
	return m.deleteRow(row)
}

// deleteRow drops a specific thread's record (routed to the owner). Delete removes
// the record from EVERY view, so hide it instantly (the mesh read path lags the
// write by up to one maintainer tick).
func (m Model) deleteRow(row api.ThreadRow) tea.Cmd {
	return m.routedVerb(row, &rowPatch{hide: true, ttl: optimisticTTL}, "delete")
}

// leavesCurrentView reports whether setting row.Archived = newArchived removes the
// row from the CURRENTLY displayed view — i.e. whether an archive/unarchive should
// optimistically hide it.
func (m Model) leavesCurrentView(row api.ThreadRow, newArchived bool) bool {
	switch m.view {
	case ViewActive:
		return newArchived // active hides archived rows
	case ViewArchived:
		return !newArchived // archived view hides un-archived rows
	case ViewAll:
		return false // all shows both
	}
	if i := int(m.view - viewBuiltins); i >= 0 && i < len(m.customViews) {
		r := row
		r.Archived = newArchived
		return !m.customViews[i].pred.Eval(r) // the predicate no longer admits it
	}
	return false
}

// notifySelected TOGGLES the selected thread's notification gate (routed to the
// owner, with an optimistic flip so the NTF column updates instantly).
func (m Model) notifySelected() tea.Cmd {
	row, ok := m.Selected()
	if !ok {
		return nil
	}
	want := !row.Notify
	flag := "--off"
	if want {
		flag = "--on"
	}
	return m.routedVerb(row, &rowPatch{notify: bptr(want), ttl: optimisticTTL}, "notify", flag)
}

// archiveSelected TOGGLES the selected thread's archived state (archive in the
// active view, unarchive in the archived/all views — the row knows which). Routed.
func (m Model) archiveSelected() tea.Cmd {
	row, ok := m.Selected()
	if !ok {
		return nil
	}
	return m.archiveRow(row)
}

// archiveRow toggles a specific thread's archived state (routed to the owner). When
// the toggle removes the row from the current view it is hidden optimistically, so
// it disappears at once instead of after the next mesh read.
func (m Model) archiveRow(row api.ThreadRow) tea.Cmd {
	newArchived := !row.Archived
	var patch *rowPatch
	if m.leavesCurrentView(row, newArchived) {
		patch = &rowPatch{hide: true, ttl: optimisticTTL}
	}
	if row.Archived {
		return m.routedVerb(row, patch, "archive", "--unarchive")
	}
	return m.routedVerb(row, patch, "archive")
}

// Selected returns the VISIBLE row under the cursor, if any (the filter and
// the tree's fold state decide what the cursor moves over).
func (m Model) Selected() (api.ThreadRow, bool) {
	tr, ok := m.selectedTree()
	return tr.row, ok
}

// Rows exposes the current rows (for tests).
func (m Model) Rows() []api.ThreadRow { return m.rows }

// CurrentView exposes the active view (for tests).
func (m Model) CurrentView() View { return m.view }

// Cursor exposes the cursor index (for tests).
func (m Model) Cursor() int { return m.cursor }

// VOffset / HOffset expose the scroll offsets (for tests).
func (m Model) VOffset() int { return m.vOffset }
func (m Model) HOffset() int { return m.hOffset }

// Prompting reports whether the line-prompt is open (for tests).
func (m Model) Prompting() bool { return m.prompting != promptNone }

// LastErr exposes the most recent fetch/daemon-reachability error (for tests).
func (m Model) LastErr() error { return m.lastErr }

// ActionErr exposes the most recent in-app ACTION error (reparent/delete/…),
// which persists across reconcile fetches (for tests).
func (m Model) ActionErr() error { return m.actionErr }

// Confirming reports whether a y/n destructive-action popup is open (for tests).
func (m Model) Confirming() bool { return m.confirming != confirmNone }

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
	styleErr      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196"))
)

// legendText is the one-line keymap help. It OVERFLOWS (wraps) to the terminal
// width rather than clipping — see renderLegend.
const legendText = "↑/↓ move · ^j/^k scroll · ←/→ fold · h/l cols · enter nav · / filter · tab view · r rename · t tag · T untag · P parent · K tickets · i ids · y uuid · n notif · x stop · d delete · a archive · R refresh · q/esc quit"

// renderLegend renders the keymap legend, WRAPPED to the terminal width (lipgloss
// soft-wraps on spaces) so every binding stays visible instead of being clipped at
// the right edge. Width unknown (no WindowSizeMsg yet — tests) renders one line.
func (m Model) renderLegend() string {
	if m.width > 1 {
		return styleDim.Width(m.width).Render(legendText)
	}
	return styleDim.Render(legendText)
}

// legendLines is the wrapped legend's height in lines (for the scroll budget —
// chromeLines). 1 when the width is unknown (unwrapped).
func (m *Model) legendLines() int {
	return strings.Count(m.renderLegend(), "\n") + 1
}

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
	if m.ticketMode != ticketNone {
		return m.ticketView() // full-screen takeover
	}
	var b strings.Builder
	b.WriteString(styleHeader.Render("sesh — live threads · ["+m.viewName()+"]") + "\n")
	if m.lastErr != nil {
		b.WriteString(styleDim.Render("(daemon unreachable: "+m.lastErr.Error()+")") + "\n")
	}
	// Action errors are LOUD and persist until the next action (unlike the dim
	// daemon-reachability note) — a failed reparent/delete must not vanish silently.
	if m.actionErr != nil {
		b.WriteString(styleErr.Render("✗ "+m.actionErr.Error()) + "\n")
	}
	if m.note != "" {
		b.WriteString(styleDim.Render(m.note) + "\n")
	}
	if m.confirming != confirmNone {
		verb := "delete"
		if m.confirming == confirmArchive {
			verb = "archive"
			if m.confirmRow.Archived {
				verb = "unarchive"
			}
		}
		b.WriteString(styleHeader.Render(fmt.Sprintf("┃ %s %q? ┃", verb, m.confirmRow.Name)) + "\n")
		b.WriteString(styleDim.Render("  y to confirm · any other key to cancel") + "\n")
	}
	if m.uuidPopup {
		if row, ok := m.Selected(); ok {
			b.WriteString(styleHeader.Render("┃ "+row.ID+" ┃") + "\n")
			b.WriteString(styleDim.Render("  c to copy · any other key to close") + "\n")
		}
	}
	if m.tagPopup {
		b.WriteString(styleHeader.Render("┃ remove tag · "+m.tagPopupRow.Name+" ┃") + "\n")
		for i, tag := range m.tagPopupRow.Tags {
			if i == m.tagPopupCursor {
				b.WriteString(styleSelected.Render("  > "+tag) + "\n")
			} else {
				b.WriteString("    " + tag + "\n")
			}
		}
		b.WriteString(styleDim.Render("  ↑/↓ move · enter remove · esc close") + "\n")
	}
	if m.prompting != promptNone {
		label := "rename"
		switch m.prompting {
		case promptTag:
			label = "tag"
		case promptReparent:
			label = "parent uuid (empty=root)"
		}
		b.WriteString(styleHeader.Render(fmt.Sprintf("%s %q> %s█", label, m.promptRow.Name, string(m.promptInput))) + "\n")
	}
	cols := m.activeColumns()
	vis := m.visibleMatches()
	widths := m.colWidths(cols, vis)

	// Horizontal column window: pan with h/l. Unknown width (no WindowSizeMsg) shows
	// every column. ‹/› in the header flag columns clipped off the left/right.
	hStart, hEnd := 0, len(cols)
	if m.width > 0 {
		hStart, hEnd = horizontalWindow(cols, widths, m.hOffset, m.width-gutterWidth)
	}
	vcols, vwidths := cols[hStart:hEnd], widths[hStart:hEnd]
	hdr := "  HB  " + m.renderHeader(vcols, vwidths)
	if hStart > 0 {
		hdr = "‹" + hdr[1:] // a column is clipped to the left
	}
	if hEnd < len(cols) {
		hdr += " ›"
	}
	b.WriteString(styleHeader.Render(hdr) + "\n")

	if len(vis) == 0 {
		b.WriteString(styleDim.Render("  (no threads)") + "\n")
	}
	// Vertical row window: the viewport [start:end). Unknown height shows every row.
	start, end := 0, len(vis)
	if m.height > 0 {
		start = m.vOffset
		if start > len(vis) {
			start = len(vis)
		}
		end = start + m.bodyHeight()
		if end > len(vis) {
			end = len(vis)
		}
	}
	if start > 0 {
		b.WriteString(styleDim.Render(fmt.Sprintf("  ▲ %d more", start)) + "\n")
	}
	for i := start; i < end; i++ {
		tr := vis[i]
		row := tr.row
		att := " "
		if row.Attachment == api.Attached {
			att = "*" // a client is attached
		}
		if i == m.cursor {
			// The selected row uses reverse video; matched-rune styling AND per-column
			// colour inside it would reset the reverse — selection is the dominant cue.
			line := HeadGlyph(row) + BusyGlyph(row) + att + " " + m.renderCells(vcols, vwidths, tr, nil, false)
			b.WriteString(styleSelected.Render("> "+line) + "\n")
		} else {
			line := HeadGlyph(row) + BusyGlyph(row) + att + " " + m.renderCells(vcols, vwidths, tr, tr.pos, true)
			b.WriteString("  " + line + "\n")
		}
	}
	if end < len(vis) {
		b.WriteString(styleDim.Render(fmt.Sprintf("  ▼ %d more", len(vis)-end)) + "\n")
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
		b.WriteString(m.renderLegend() + "\n")
	default:
		b.WriteString("\n" + m.renderLegend() + "\n")
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
