package tui

// The tickets view (K): a full-screen takeover of the grid that lists the
// selected thread's tickets, drills into one ticket's fields + actions, and edits
// a text field in $EDITOR (suspend → vim → save). It is the in-TUI twin of the
// myrig shell editor — both are thin front-ends over the same `sesh ticket`
// mechanism (get/set/set-status/delete/send-prompt), which auto-routes to the
// ticket owner. So ALL ticket ops here EXEC the binary (not m.client, which would
// hit the local daemon that may not own the ticket) — exactly as routedVerb does.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/api"
)

// ticketMode is the tickets-view sub-state (ticketNone = the view is closed).
type ticketMode int

const (
	ticketNone ticketMode = iota
	ticketList            // listing the thread's tickets
	ticketDetail          // one ticket's fields + action menu
	ticketStatusPick      // choosing a new status
	ticketThreadPick      // choosing a thread to (re)bind (fzf-style, search by uuid)
	ticketConfirmDel      // y/n delete confirmation
	ticketNewPrompt       // typing a name to create a new ticket (bound to this thread)
	ticketFilterPick      // choosing which status to show (Tab)
)

// detail item indices (the navigable rows of the detail view, in render order).
const (
	tdName = iota
	tdPrompt
	tdStatus
	tdThread
	tdSend
	tdDelete
	tdCount
)

// ticketStatuses is the status picker's options, in lifecycle order.
var ticketStatuses = []string{api.StatusTriage, api.StatusReady, api.StatusActive, api.StatusDone, api.StatusDropped}

// ticketFilterAll shows every status; ticketFilters is the Tab status-filter
// picker's options (lifecycle order + "all"). The view defaults to "active".
const ticketFilterAll = "all"

var ticketFilters = []string{api.StatusTriage, api.StatusReady, api.StatusActive, api.StatusDone, api.StatusDropped, ticketFilterAll}

// filterIndex is the picker cursor position for a filter (the active row if unknown).
func filterIndex(f string) int {
	for i, x := range ticketFilters {
		if x == f {
			return i
		}
	}
	for i, x := range ticketFilters {
		if x == api.StatusActive {
			return i
		}
	}
	return 0
}

// ---- messages ----

type ticketsLoadedMsg struct {
	thread  api.ThreadRow
	tickets []api.Ticket
	err     error
}

// ticketEditDoneMsg is posted when the suspended $EDITOR exits.
type ticketEditDoneMsg struct {
	id    string
	field string // "name" | "prompt"
	file  string // temp file to read back
	err   error
}

// ticketActionMsg is the result of a ticket mutation (set/set-status/delete/send).
// On success reload != "" triggers a re-list so the detail/list refresh.
type ticketActionMsg struct {
	err    error
	reload string // thread id to re-list (empty after a delete that empties the view)
	note   string
}

// ---- open / key handling ----

// WithEditor sets the editor command used to edit a ticket field in the tickets
// view (resolved by the caller as --editor → [tui] editor → $EDITOR; "" = none).
func (m Model) WithEditor(editor string) Model {
	m.editor = editor
	return m
}

// openTicketView enters the tickets view for a thread and loads its tickets. The
// list defaults to showing ACTIVE tickets (Tab opens the status-filter picker).
func (m *Model) openTicketView(row api.ThreadRow) tea.Cmd {
	m.ticketMode = ticketList
	m.ticketThread = row
	m.tickets = nil
	m.ticketCursor, m.ticketDetail = 0, 0
	m.ticketFilter = api.StatusActive
	m.ticketErr = nil
	return m.loadTickets(row)
}

// visibleTickets is m.tickets narrowed to the current status filter ("all" = no
// filter). The list cursor and detail/actions operate over THIS slice.
func (m Model) visibleTickets() []api.Ticket {
	if m.ticketFilter == "" || m.ticketFilter == ticketFilterAll {
		return m.tickets
	}
	out := make([]api.Ticket, 0, len(m.tickets))
	for _, tk := range m.tickets {
		if tk.Status == m.ticketFilter {
			out = append(out, tk)
		}
	}
	return out
}

// selectedTicket is the ticket under the list cursor (within the filtered view).
func (m Model) selectedTicket() (api.Ticket, bool) {
	vis := m.visibleTickets()
	if m.ticketCursor < 0 || m.ticketCursor >= len(vis) {
		return api.Ticket{}, false
	}
	return vis[m.ticketCursor], true
}

// handleTicketKey is the tickets-view sub-state machine (active while
// ticketMode != ticketNone). It fully owns the keyboard until the view closes.
func (m Model) handleTicketKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.ticketMode {
	case ticketStatusPick:
		return m.handleTicketStatusKey(msg)
	case ticketThreadPick:
		return m.handleTicketThreadPickKey(msg)
	case ticketConfirmDel:
		return m.handleTicketConfirmDelKey(msg)
	case ticketNewPrompt:
		return m.handleTicketNewKey(msg)
	case ticketFilterPick:
		return m.handleTicketFilterKey(msg)
	case ticketDetail:
		return m.handleTicketDetailKey(msg)
	default: // ticketList
		return m.handleTicketListKey(msg)
	}
}

// handleTicketFilterKey drives the Tab status-filter picker: ↑/↓ move, enter
// applies the chosen status (or "all"), esc/tab/q cancel without changing it.
func (m Model) handleTicketFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc", "tab":
		m.ticketMode = ticketList
	case "up", "k":
		m.ticketFilterCursor = (m.ticketFilterCursor - 1 + len(ticketFilters)) % len(ticketFilters)
	case "down", "j":
		m.ticketFilterCursor = (m.ticketFilterCursor + 1) % len(ticketFilters)
	case "enter":
		m.ticketFilter = ticketFilters[m.ticketFilterCursor]
		m.ticketMode, m.ticketCursor = ticketList, 0 // new filtered view starts at the top
	}
	return m, nil
}

// handleTicketNewKey drives the new-ticket name prompt: printables build the name,
// Enter creates the ticket (bound active to this thread, so it joins the list),
// Esc cancels. A name is required (ticket create rejects empty).
func (m Model) handleTicketNewKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.ticketMode, m.ticketNewInput = ticketList, nil
		return m, nil
	case "enter":
		name := strings.TrimSpace(string(m.ticketNewInput))
		if name == "" {
			m.ticketErr = fmt.Errorf("a ticket needs a name")
			return m, nil
		}
		m.ticketMode, m.ticketNewInput = ticketList, nil
		return m, m.createTicket(name)
	case "backspace":
		if n := len(m.ticketNewInput); n > 0 {
			m.ticketNewInput = m.ticketNewInput[:n-1]
		}
		return m, nil
	}
	// Text input: append whole runes (so a paste / multi-rune key isn't dropped).
	switch msg.Type {
	case tea.KeyRunes:
		m.ticketNewInput = append(m.ticketNewInput, msg.Runes...)
	case tea.KeySpace:
		m.ticketNewInput = append(m.ticketNewInput, ' ')
	}
	return m, nil
}

func (m Model) handleTicketListKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	n := len(m.visibleTickets())
	switch msg.String() {
	case "q", "esc":
		m.ticketMode = ticketNone
		return m, m.fetch() // refresh the grid (ticket columns may have changed)
	case "up", "k":
		if n > 0 {
			m.ticketCursor = (m.ticketCursor - 1 + n) % n
		}
	case "down", "j":
		if n > 0 {
			m.ticketCursor = (m.ticketCursor + 1) % n
		}
	case "tab":
		// Open the status-filter picker (default view is active; Tab to change).
		m.ticketMode, m.ticketFilterCursor = ticketFilterPick, filterIndex(m.ticketFilter)
	case "enter", "l", "right":
		if _, ok := m.selectedTicket(); ok {
			m.ticketMode, m.ticketDetail = ticketDetail, tdName
		}
	case "n":
		// Create a new ticket bound to this thread.
		m.ticketMode, m.ticketNewInput, m.ticketErr = ticketNewPrompt, nil, nil
	case "R":
		return m, m.loadTickets(m.ticketThread)
	}
	return m, nil
}

func (m Model) handleTicketDetailKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	tk, ok := m.selectedTicket()
	if !ok {
		m.ticketMode = ticketList
		return m, nil
	}
	switch msg.String() {
	case "q", "esc", "h", "left":
		m.ticketMode = ticketList
	case "up", "k":
		m.ticketDetail = (m.ticketDetail - 1 + tdCount) % tdCount
	case "down", "j":
		m.ticketDetail = (m.ticketDetail + 1) % tdCount
	case "enter", "l", "right":
		switch m.ticketDetail {
		case tdName:
			return m, m.editTicketField(tk, "name")
		case tdPrompt:
			return m, m.editTicketField(tk, "prompt")
		case tdStatus:
			m.ticketMode, m.ticketStatusCursor = ticketStatusPick, statusIndex(tk.Status)
		case tdThread:
			m.ticketMode, m.ticketPickQuery, m.ticketPickCursor = ticketThreadPick, nil, 0
		case tdSend:
			return m, m.ticketAction("ticket sent", "send-prompt", "--id", tk.ID)
		case tdDelete:
			m.ticketMode = ticketConfirmDel
		}
	}
	return m, nil
}

func (m Model) handleTicketStatusKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	tk, ok := m.selectedTicket()
	if !ok {
		m.ticketMode = ticketList
		return m, nil
	}
	switch msg.String() {
	case "q", "esc", "h", "left":
		m.ticketMode = ticketDetail
	case "up", "k":
		m.ticketStatusCursor = (m.ticketStatusCursor - 1 + len(ticketStatuses)) % len(ticketStatuses)
	case "down", "j":
		m.ticketStatusCursor = (m.ticketStatusCursor + 1) % len(ticketStatuses)
	case "enter":
		status := ticketStatuses[m.ticketStatusCursor]
		m.ticketMode = ticketDetail
		// set-status binds the existing thread (required when entering active; the
		// daemon errors LOUDLY if active with no thread — bind one via the thread item).
		args := []string{"set-status", "--id", tk.ID, "--status", status}
		if tk.ThreadID != "" {
			args = append(args, "--thread", tk.ThreadID)
		}
		return m, m.ticketAction("status → "+status, args...)
	}
	return m, nil
}

func (m Model) handleTicketThreadPickKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	tk, ok := m.selectedTicket()
	if !ok {
		m.ticketMode = ticketList
		return m, nil
	}
	cands := m.ticketThreadCandidates()
	switch msg.String() {
	case "esc":
		m.ticketMode = ticketDetail
	case "up", "ctrl+k":
		if len(cands) > 0 {
			m.ticketPickCursor = (m.ticketPickCursor - 1 + len(cands)) % len(cands)
		}
	case "down", "ctrl+j":
		if len(cands) > 0 {
			m.ticketPickCursor = (m.ticketPickCursor + 1) % len(cands)
		}
	case "backspace":
		if n := len(m.ticketPickQuery); n > 0 {
			m.ticketPickQuery = m.ticketPickQuery[:n-1]
			m.ticketPickCursor = 0
		}
	case "enter":
		if m.ticketPickCursor < len(cands) {
			target := cands[m.ticketPickCursor]
			m.ticketMode = ticketDetail
			// Rebind keeps the current status; the daemon re-validates existence.
			status := tk.Status
			if status == "" {
				status = api.StatusActive
			}
			return m, m.ticketAction("rebound to "+tid8(target.ID), "set-status", "--id", tk.ID, "--status", status, "--thread", target.ID)
		}
		return m, nil
	}
	// Type to filter (by name or uuid) — whole runes, so a paste isn't dropped.
	switch msg.Type {
	case tea.KeyRunes:
		m.ticketPickQuery = append(m.ticketPickQuery, msg.Runes...)
		m.ticketPickCursor = 0
	case tea.KeySpace:
		m.ticketPickQuery = append(m.ticketPickQuery, ' ')
		m.ticketPickCursor = 0
	}
	return m, nil
}

func (m Model) handleTicketConfirmDelKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	tk, ok := m.selectedTicket()
	m.ticketMode = ticketList // back to the list either way (the detail's ticket may be gone)
	if !ok {
		return m, nil
	}
	if s := msg.String(); s == "y" || s == "Y" {
		if m.ticketCursor > 0 {
			m.ticketCursor-- // keep the cursor in range after the row vanishes
		}
		return m, m.ticketAction("deleted "+tid8(tk.ID), "delete", "--id", tk.ID)
	}
	return m, nil
}

// ---- commands (all EXEC the binary so ticket-owner routing applies) ----

// ticketArgs builds `ticket <args…>` and routes to the thread's OWNING machine
// (--machine) when it differs from this client's. A ticket binds to / is validated
// against the thread, and with no SESH_TICKET_OWNER set tickets are local to a
// daemon — so a thread on another machine needs its ticket ops carried there, or
// the bind/list/columns all miss (the "bound thread not found" cross-machine bug).
func (m Model) ticketArgs(args ...string) []string {
	out := append([]string{"ticket"}, args...)
	if mc := m.ticketThread.Machine; mc != "" && mc != m.machine {
		out = append(out, "--machine", mc)
	}
	return out
}

func (m Model) loadTickets(thread api.ThreadRow) tea.Cmd {
	bin, env := m.binaryPath, m.navEnv
	return func() tea.Msg {
		cmd := exec.Command(bin, m.ticketArgs("list", "--thread", thread.ID, "--json")...)
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.Output()
		if err != nil {
			return ticketsLoadedMsg{thread: thread, err: fmt.Errorf("list tickets: %v: %s", err, execStderr(err))}
		}
		var tickets []api.Ticket
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var tk api.Ticket
			if err := json.Unmarshal([]byte(line), &tk); err != nil {
				return ticketsLoadedMsg{thread: thread, err: fmt.Errorf("decode ticket: %w", err)}
			}
			tickets = append(tickets, tk)
		}
		return ticketsLoadedMsg{thread: thread, tickets: tickets}
	}
}

// ticketAction execs `sesh ticket <args...>` (routes to the owner) and, on
// success, re-lists so the view refreshes. note is the success status line.
func (m Model) ticketAction(note string, args ...string) tea.Cmd {
	bin, env, threadID, fullArgs := m.binaryPath, m.navEnv, m.ticketThread.ID, m.ticketArgs(args...)
	return func() tea.Msg {
		cmd := exec.Command(bin, fullArgs...)
		cmd.Env = append(os.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return ticketActionMsg{err: fmt.Errorf("ticket %s: %v: %s", args[0], err, strings.TrimSpace(string(out)))}
		}
		return ticketActionMsg{reload: threadID, note: note}
	}
}

// createTicket creates a ticket and binds it ACTIVE to this thread (so it joins
// the thread's list), then re-lists. The bind is best-effort over the same
// owner-routed CLI; a created-but-unbound ticket would just not show here.
func (m Model) createTicket(name string) tea.Cmd {
	bin, env, threadID := m.binaryPath, m.navEnv, m.ticketThread.ID
	createArgs := m.ticketArgs("create", "--name", name, "--json")
	bindArgs := func(id string) []string {
		return m.ticketArgs("set-status", "--id", id, "--status", api.StatusActive, "--thread", threadID)
	}
	return func() tea.Msg {
		out, err := runSesh(bin, env, createArgs...)
		if err != nil {
			return ticketActionMsg{err: fmt.Errorf("ticket create: %v: %s", err, out)}
		}
		var tk api.Ticket
		if e := json.Unmarshal([]byte(strings.TrimSpace(out)), &tk); e != nil {
			return ticketActionMsg{err: fmt.Errorf("decode created ticket: %w", e)}
		}
		if bout, berr := runSesh(bin, env, bindArgs(tk.ID)...); berr != nil {
			return ticketActionMsg{err: fmt.Errorf("bind new ticket: %v: %s", berr, bout)}
		}
		return ticketActionMsg{reload: threadID, note: "created " + tid8(tk.ID)}
	}
}

// runSesh execs `sesh <args...>` with the model's exec env, returning combined output.
func runSesh(bin string, env []string, args ...string) (string, error) {
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// editTicketField writes the current value to a temp file, suspends to the editor,
// and (on exit) posts ticketEditDoneMsg so the edited value is saved.
func (m Model) editTicketField(tk api.Ticket, field string) tea.Cmd {
	editor := strings.TrimSpace(m.editor)
	if editor == "" {
		return func() tea.Msg {
			return ticketActionMsg{err: fmt.Errorf("no editor configured: set `sesh tui --editor`, [tui] editor, or $EDITOR")}
		}
	}
	var cur string
	switch field {
	case "name":
		cur = tk.Name
	case "prompt":
		cur = tk.Prompt
	}
	f, err := os.CreateTemp("", "sesh-ticket-*.txt")
	if err != nil {
		return func() tea.Msg { return ticketActionMsg{err: fmt.Errorf("editor temp file: %w", err)} }
	}
	_, _ = f.WriteString(cur)
	_ = f.Close()
	parts := strings.Fields(editor)
	c := exec.Command(parts[0], append(parts[1:], f.Name())...)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return ticketEditDoneMsg{id: tk.ID, field: field, file: f.Name(), err: err}
	})
}

// applyTicketEdit reads the edited temp file and persists it via `sesh ticket set`.
func (m Model) applyTicketEdit(msg ticketEditDoneMsg) tea.Cmd {
	bin, env, threadID := m.binaryPath, m.navEnv, m.ticketThread.ID
	setArgs := func(val string) []string { return m.ticketArgs("set", "--id", msg.id, "--"+msg.field, val) }
	return func() tea.Msg {
		defer os.Remove(msg.file)
		if msg.err != nil {
			return ticketActionMsg{err: fmt.Errorf("editor: %w", msg.err)}
		}
		raw, err := os.ReadFile(msg.file)
		if err != nil {
			return ticketActionMsg{err: fmt.Errorf("read edited %s: %w", msg.field, err)}
		}
		val := strings.TrimRight(string(raw), "\n") // editors add a trailing newline
		cmd := exec.Command(bin, setArgs(val)...)
		cmd.Env = append(os.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return ticketActionMsg{err: fmt.Errorf("save %s: %v: %s", msg.field, err, strings.TrimSpace(string(out)))}
		}
		return ticketActionMsg{reload: threadID, note: msg.field + " saved"}
	}
}

// ticketThreadCandidates filters the grid's threads by the picker query (name or
// uuid substring, case-insensitive).
func (m Model) ticketThreadCandidates() []api.ThreadRow {
	q := strings.ToLower(string(m.ticketPickQuery))
	out := make([]api.ThreadRow, 0, len(m.rows))
	for _, r := range m.rows {
		if q == "" || strings.Contains(strings.ToLower(r.Name), q) || strings.Contains(strings.ToLower(r.ID), q) {
			out = append(out, r)
		}
	}
	return out
}

func statusIndex(s string) int {
	for i, st := range ticketStatuses {
		if st == s {
			return i
		}
	}
	return 0
}

// execStderr extracts an *exec.ExitError's stderr, if any (for loud errors).
func execStderr(err error) string {
	if ee, ok := err.(*exec.ExitError); ok {
		return strings.TrimSpace(string(ee.Stderr))
	}
	return ""
}

// ---- rendering ----

// ticketView renders the tickets-view takeover (returned from View when active).
func (m Model) ticketView() string {
	var b strings.Builder
	title := fmt.Sprintf("sesh — tickets · %s [%s]", m.ticketThread.Name, tid8(m.ticketThread.ID))
	b.WriteString(styleHeader.Render(title) + "\n")
	if m.ticketErr != nil {
		b.WriteString(styleErr.Render("✗ "+m.ticketErr.Error()) + "\n")
	}
	if m.note != "" {
		b.WriteString(styleDim.Render(m.note) + "\n")
	}
	b.WriteString("\n")

	switch m.ticketMode {
	case ticketDetail, ticketStatusPick, ticketThreadPick, ticketConfirmDel:
		b.WriteString(m.ticketDetailView())
	default:
		b.WriteString(m.ticketListView())
	}
	if m.ticketMode == ticketNewPrompt {
		b.WriteString("\n" + styleHeader.Render(fmt.Sprintf("new ticket name> %s█", string(m.ticketNewInput))) + "\n")
		b.WriteString(styleDim.Render("  enter create (bound to this thread) · esc cancel") + "\n")
	}
	if m.ticketMode == ticketFilterPick {
		b.WriteString("\n" + m.ticketFilterView())
	}
	return b.String()
}

func (m Model) ticketListView() string {
	var b strings.Builder
	filter := m.ticketFilter
	if filter == "" {
		filter = ticketFilterAll
	}
	b.WriteString(styleDim.Render(fmt.Sprintf("showing: %s  (tab to change)", filter)) + "\n")
	vis := m.visibleTickets()
	if len(vis) == 0 {
		b.WriteString(styleDim.Render(fmt.Sprintf("  (no %s tickets)", filter)) + "\n")
	}
	for i, tk := range vis {
		line := fmt.Sprintf("%-8s  %-7s  %s", tid8(tk.ID), tk.Status, tk.Name)
		if i == m.ticketCursor {
			b.WriteString(styleSelected.Render("> "+line) + "\n")
		} else {
			b.WriteString("  " + line + "\n")
		}
	}
	b.WriteString("\n" + styleDim.Render("↑/↓ move · enter open · n new · tab filter · R reload · q/esc back to grid") + "\n")
	return b.String()
}

func (m Model) ticketFilterView() string {
	var b strings.Builder
	b.WriteString(styleHeader.Render("┃ show which tickets? ┃") + "\n")
	for i, f := range ticketFilters {
		if i == m.ticketFilterCursor {
			b.WriteString(styleSelected.Render("  > "+f) + "\n")
		} else {
			b.WriteString("    " + f + "\n")
		}
	}
	b.WriteString(styleDim.Render("  ↑/↓ move · enter apply · esc/tab cancel") + "\n")
	return b.String()
}

func (m Model) ticketDetailView() string {
	var b strings.Builder
	tk, ok := m.selectedTicket()
	if !ok {
		return styleDim.Render("  (ticket no longer in view)") + "\n"
	}
	thread := "(unbound)"
	if tk.ThreadID != "" {
		thread = tid8(tk.ThreadID)
	}
	items := []struct{ label, val string }{
		{"name", tk.Name},
		{"prompt", firstLine(tk.Prompt)},
		{"status", tk.Status},
		{"thread", thread},
		{"», send prompt to thread", ""},
		{"», delete ticket", ""},
	}
	for i, it := range items {
		cursor := "  "
		if i == m.ticketDetail {
			cursor = "> "
		}
		var line string
		if it.val == "" {
			line = it.label
		} else {
			line = fmt.Sprintf("%-8s %s", it.label+":", it.val)
		}
		if i == m.ticketDetail {
			b.WriteString(styleSelected.Render(cursor+line) + "\n")
		} else {
			b.WriteString(cursor + line + "\n")
		}
	}
	b.WriteString("\n")
	switch m.ticketMode {
	case ticketStatusPick:
		b.WriteString(m.ticketStatusView())
	case ticketThreadPick:
		b.WriteString(m.ticketThreadPickView())
	case ticketConfirmDel:
		b.WriteString(styleHeader.Render(fmt.Sprintf("┃ delete ticket %q? ┃", tk.Name)) + "\n")
		b.WriteString(styleDim.Render("  y to confirm · any other key to cancel") + "\n")
	default:
		b.WriteString(styleDim.Render("↑/↓ move · enter edit/act · h/esc back · q grid") + "\n")
	}
	return b.String()
}

func (m Model) ticketStatusView() string {
	var b strings.Builder
	b.WriteString(styleHeader.Render("┃ set status ┃") + "\n")
	for i, st := range ticketStatuses {
		if i == m.ticketStatusCursor {
			b.WriteString(styleSelected.Render("  > "+st) + "\n")
		} else {
			b.WriteString("    " + st + "\n")
		}
	}
	b.WriteString(styleDim.Render("  ↑/↓ move · enter set · esc cancel") + "\n")
	return b.String()
}

func (m Model) ticketThreadPickView() string {
	var b strings.Builder
	b.WriteString(styleHeader.Render("┃ bind thread (type to filter by name/uuid) ┃") + "\n")
	b.WriteString(styleDim.Render("  query: "+string(m.ticketPickQuery)+"█") + "\n")
	cands := m.ticketThreadCandidates()
	const shown = 8
	for i, r := range cands {
		if i >= shown {
			b.WriteString(styleDim.Render(fmt.Sprintf("  … %d more", len(cands)-shown)) + "\n")
			break
		}
		line := fmt.Sprintf("%-8s %-12s %s", tid8(r.ID), r.Machine, r.Name)
		if i == m.ticketPickCursor {
			b.WriteString(styleSelected.Render("  > "+line) + "\n")
		} else {
			b.WriteString("    " + line + "\n")
		}
	}
	b.WriteString(styleDim.Render("  ↑/↓ (^k/^j) move · enter bind · esc cancel") + "\n")
	return b.String()
}

// ---- test accessors ----

// TicketViewOpen reports whether the tickets view is active (for tests).
func (m Model) TicketViewOpen() bool { return m.ticketMode != ticketNone }

// Tickets exposes the full loaded ticket list (unfiltered, for tests).
func (m Model) Tickets() []api.Ticket { return m.tickets }

// TicketFilter exposes the current status filter (for tests; "" before open).
func (m Model) TicketFilter() string { return m.ticketFilter }

// VisibleTickets exposes the filtered list the cursor moves over (for tests).
func (m Model) VisibleTickets() []api.Ticket { return m.visibleTickets() }

// TicketErr exposes the last tickets-view error (for tests).
func (m Model) TicketErr() error { return m.ticketErr }

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	return s
}
