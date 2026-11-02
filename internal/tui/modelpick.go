package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/api"
)

// The MODEL PICKER: choose a model from the list the thread's harness reports on
// the thread's own machine (`sesh model list`), fuzzy-filtered as you type. One
// picker, three uses:
//
//   - SWITCH (`M`): `thread switch-model` — the registered model AND the live
//     agent. For a live claude pane that RESTARTS the pane (the process ends, the
//     conversation resumes), so it asks y/n first and says so.
//   - REGISTER (`set-model`, or Tab inside the picker): `thread set-model` — the
//     registered model only; a live agent keeps what it runs. Offers "(none)" to
//     clear it.
//   - DEFAULT (`set-default-model`): `model set-default` — the harness's default
//     model on the selected row's machine, for threads created from now on.
//     Offers "(none)" to remove it.
//
// The list is the harness's, which can be shorter than what it accepts (claude's
// handshake list omits older models its --model still takes), so a query that is
// not an exact entry is offered as a typed id too.

type modelPickMode int

const (
	pickSwitch modelPickMode = iota
	pickRegister
	pickDefault
)

// modelPickRowsTop / modelPickChrome mirror modelPickView's fixed layout (title,
// subtitle, query, ▲ line above the list; ▼ line and footer below).
const (
	modelPickRowsTop = 4
	modelPickChrome  = 6
)

// modelListMsg delivers the asynchronously fetched model list (and, for the
// DEFAULT picker, the current defaults) for the row the picker was opened on.
type modelListMsg struct {
	rowID    string
	list     api.ModelListResponse
	defaults map[string]string
	err      error
}

type modelCandidate struct {
	id    string // what --model gets; "" for the clear entry
	label string // the line's descriptive text
	clear bool   // "(none)": clear the registration / remove the default
	typed bool   // the query itself, offered as an id
	pos   []int  // matched positions in the rendered id (highlighting)
	score int
}

func agentRow(row api.ThreadRow) bool {
	switch row.AgentKind {
	case "claude", "codex", "pi", "mypi":
		return true
	}
	return false
}

// openModelPick opens the picker on the selected row and starts fetching the
// list from the row's owner.
func (m Model) openModelPick(mode modelPickMode) (tea.Model, tea.Cmd) {
	row, ok := m.Selected()
	if !ok {
		m.note = "no thread selected"
		return m, nil
	}
	if !agentRow(row) {
		m.actionErr = fmt.Errorf("%q is a %s thread, which runs no agent model", rowDisplayName(row), row.AgentKind)
		return m, nil
	}
	m.modelPick, m.modelPickMode, m.modelPickRow = true, mode, row
	m.modelPickQuery, m.modelPickCursor, m.modelPickOffset = nil, 0, 0
	m.modelPickList, m.modelPickDefaults, m.modelPickErr = nil, nil, nil
	return m, m.fetchModelList(row, mode == pickDefault)
}

func (m *Model) closeModelPick() {
	m.modelPick = false
	m.modelPickQuery, m.modelPickCursor, m.modelPickOffset = nil, 0, 0
	m.modelPickList, m.modelPickDefaults, m.modelPickErr = nil, nil, nil
}

// routeArgs appends --machine for a row another machine owns.
func (m Model) routeArgs(args []string, machine string) []string {
	if m.machine == "" || machine != m.machine {
		args = append(args, "--machine", machine)
	}
	return args
}

// runJSON runs one sesh CLI verb and decodes its --json output.
func (m Model) runJSON(args []string, out any) error {
	cmd := exec.Command(m.binaryPath, args...)
	cmd.Env = append(os.Environ(), m.navEnv...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return json.Unmarshal(stdout, out)
}

func (m Model) fetchModelList(row api.ThreadRow, withDefaults bool) tea.Cmd {
	return func() tea.Msg {
		msg := modelListMsg{rowID: row.ID}
		if err := m.runJSON(m.routeArgs([]string{"model", "list", "--agent", row.AgentKind, "--json"}, row.Machine), &msg.list); err != nil {
			msg.err = fmt.Errorf("list %s models on %s: %w", row.AgentKind, row.Machine, err)
			return msg
		}
		if withDefaults {
			var d api.ModelDefaultsResponse
			if err := m.runJSON(m.routeArgs([]string{"model", "defaults", "--json"}, row.Machine), &d); err != nil {
				msg.err = fmt.Errorf("read the default models on %s: %w", row.Machine, err)
				return msg
			}
			msg.defaults = d.Defaults
		}
		return msg
	}
}

// applyModelList stores a fetched list if it is for the row the picker is still
// open on (a late answer for a closed or re-opened picker is dropped).
func (m Model) applyModelList(msg modelListMsg) Model {
	if !m.modelPick || msg.rowID != m.modelPickRow.ID {
		return m
	}
	if msg.err != nil {
		m.modelPickErr = msg.err
		return m
	}
	list := msg.list
	m.modelPickList, m.modelPickDefaults = &list, msg.defaults
	return m
}

// modelCandidates lists the picker's entries, filtered by the query and ranked
// best-first; an empty query keeps the harness's own order.
func (m Model) modelCandidates() []modelCandidate {
	if m.modelPickList == nil {
		return nil
	}
	q := strings.TrimSpace(string(m.modelPickQuery))
	var out []modelCandidate
	if m.modelPickMode != pickSwitch {
		what := "clear the registered model (the harness's own default from then on)"
		if m.modelPickMode == pickDefault {
			what = "remove the default (new threads get the harness's own default)"
		}
		c := modelCandidate{label: "(none) — " + what, clear: true}
		if q == "" {
			out = append(out, c)
		} else if r := fuzzyScore(q, "(none)"); r.ok {
			c.score = r.score
			out = append(out, c)
		}
	}
	exact := false
	for _, mo := range m.modelPickList.Models {
		c := modelCandidate{id: mo.ID, label: strings.Join(nonEmptyStrings(mo.Label, mo.Description), " · ")}
		if mo.Resolved != "" {
			c.label = strings.TrimSpace(c.label + " (= " + mo.Resolved + ")")
		}
		if mo.ID == q {
			exact = true
		}
		if q == "" {
			out = append(out, c)
			continue
		}
		best, matched := c, false
		if r := fuzzyScore(q, c.id); r.ok {
			best.pos, best.score, matched = r.pos, r.score, true
		}
		if r := fuzzyScore(q, c.label); r.ok && (!matched || r.score > best.score) {
			best.pos, best.score, matched = nil, r.score, true
		}
		if matched {
			out = append(out, best)
		}
	}
	if q != "" {
		sort.SliceStable(out, func(i, j int) bool { return out[i].score > out[j].score })
		if !exact && !strings.ContainsAny(q, " \t") {
			out = append(out, modelCandidate{id: q, typed: true, label: "use as typed (not in the harness's list)"})
		}
	}
	return out
}

func nonEmptyStrings(ss ...string) []string {
	var out []string
	for _, s := range ss {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// modelMarks annotates a candidate with how it relates to the thread / harness.
func (m Model) modelMarks(c modelCandidate) string {
	if c.clear {
		return ""
	}
	row := m.modelPickRow
	var marks []string
	if m.modelPickMode == pickDefault {
		if d := m.modelPickDefaults[row.AgentKind]; d != "" && d == c.id {
			marks = append(marks, "default")
		}
		return strings.Join(marks, ", ")
	}
	if row.Model != "" && row.Model == c.id {
		marks = append(marks, "registered")
	}
	if row.ObservedModel != "" && modelsAgree(row.AgentKind, c.id, row.ObservedModel) {
		marks = append(marks, "running")
	}
	return strings.Join(marks, ", ")
}

// pickModel acts on the chosen candidate.
func (m Model) pickModel(c modelCandidate) (tea.Model, tea.Cmd) {
	row, mode := m.modelPickRow, m.modelPickMode
	m.closeModelPick()
	switch mode {
	case pickSwitch:
		// A live claude pane is RESTARTED by a switch — say so and ask first.
		if row.AgentKind == "claude" && row.Head == api.Headful {
			m.confirming, m.confirmRow, m.confirmModel = confirmSwitchModel, row, c.id
			return m, nil
		}
		return m, m.switchModel(row, c.id)
	case pickRegister:
		args := []string{"thread", "set-model", "--id", row.ID, "--json"}
		if c.clear {
			args = append(args, "--clear")
		} else {
			args = append(args, "--model", c.id)
		}
		return m, m.modelAction(row, args, "set-model")
	default:
		args := []string{"model", "set-default", "--agent", row.AgentKind, "--model", c.id, "--json"}
		if c.clear {
			args = []string{"model", "unset-default", "--agent", row.AgentKind, "--json"}
		}
		return m, m.defaultModelAction(row, args, c)
	}
}

func (m Model) switchModel(row api.ThreadRow, model string) tea.Cmd {
	return m.modelAction(row, []string{"thread", "switch-model", "--id", row.ID, "--model", model, "--json"}, "switch-model")
}

// modelAction runs set-model / switch-model on the row's owner and reports the
// daemon's own account of what happened.
func (m Model) modelAction(row api.ThreadRow, args []string, verb string) tea.Cmd {
	return func() tea.Msg {
		var resp api.SetThreadModelResponse
		if err := m.runJSON(m.routeArgs(args, row.Machine), &resp); err != nil {
			return actionMsg{err: fmt.Errorf("%s %q: %w", verb, rowDisplayName(row), err)}
		}
		model := resp.Thread.Model
		if model == "" {
			model = "(none)"
		}
		return actionMsg{id: row.ID, note: fmt.Sprintf("%s → %s [%s]: %s", rowDisplayName(row), model, resp.Outcome, resp.Detail)}
	}
}

func (m Model) defaultModelAction(row api.ThreadRow, args []string, c modelCandidate) tea.Cmd {
	return func() tea.Msg {
		var resp api.ModelDefaultsResponse
		if err := m.runJSON(m.routeArgs(args, row.Machine), &resp); err != nil {
			return actionMsg{err: fmt.Errorf("default %s model on %s: %w", row.AgentKind, row.Machine, err)}
		}
		if c.clear {
			return actionMsg{id: row.ID, note: fmt.Sprintf("removed the %s default model on %s (%s)", row.AgentKind, row.Machine, resp.WrotePath)}
		}
		return actionMsg{id: row.ID, note: fmt.Sprintf("%s default model on %s = %s (%s)", row.AgentKind, row.Machine, c.id, resp.WrotePath)}
	}
}

// handleModelPickKey: type to filter, ↑/↓ (^k/^j) move, Enter picks, Tab flips
// between switch and register, Esc cancels.
func (m Model) handleModelPickKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	cands := m.modelCandidates()
	switch msg.String() {
	case "esc", hardQuitKey:
		m.closeModelPick()
		return m, nil
	case "up", "ctrl+k":
		if len(cands) > 0 {
			m.modelPickCursor = (m.modelPickCursor - 1 + len(cands)) % len(cands)
			m.ensureModelPickVisible(len(cands))
		}
		return m, nil
	case "down", "ctrl+j":
		if len(cands) > 0 {
			m.modelPickCursor = (m.modelPickCursor + 1) % len(cands)
			m.ensureModelPickVisible(len(cands))
		}
		return m, nil
	case "tab":
		switch m.modelPickMode {
		case pickSwitch:
			m.modelPickMode = pickRegister
		case pickRegister:
			m.modelPickMode = pickSwitch
		}
		m.modelPickCursor, m.modelPickOffset = 0, 0
		return m, nil
	case "backspace":
		if n := len(m.modelPickQuery); n > 0 {
			m.modelPickQuery = m.modelPickQuery[:n-1]
			m.modelPickCursor, m.modelPickOffset = 0, 0
		}
		return m, nil
	case "enter":
		if m.modelPickCursor < 0 || m.modelPickCursor >= len(cands) {
			return m, nil // nothing (yet) to pick: keep the picker open
		}
		return m.pickModel(cands[m.modelPickCursor])
	}
	switch msg.Type {
	case tea.KeyRunes:
		m.modelPickQuery = append(m.modelPickQuery, msg.Runes...)
		m.modelPickCursor, m.modelPickOffset = 0, 0
	case tea.KeySpace:
		m.modelPickQuery = append(m.modelPickQuery, ' ')
		m.modelPickCursor, m.modelPickOffset = 0, 0
	}
	return m, nil
}

// handleModelPickMouse: the wheel moves the selection, a click picks.
func (m Model) handleModelPickMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	cands := m.modelCandidates()
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if len(cands) > 0 {
			m.modelPickCursor = (m.modelPickCursor - 1 + len(cands)) % len(cands)
			m.ensureModelPickVisible(len(cands))
		}
	case tea.MouseButtonWheelDown:
		if len(cands) > 0 {
			m.modelPickCursor = (m.modelPickCursor + 1) % len(cands)
			m.ensureModelPickVisible(len(cands))
		}
	case tea.MouseButtonLeft:
		if msg.Action == tea.MouseActionPress {
			if i, ok := m.modelPickRowAtY(msg.Y); ok {
				return m.pickModel(cands[i])
			}
		}
	}
	return m, nil
}

func (m Model) modelPickVisibleRows(n int) int {
	return listVisibleRows(n, m.height, modelPickChrome)
}

func (m *Model) ensureModelPickVisible(n int) {
	avail := m.modelPickVisibleRows(n)
	m.modelPickOffset = listClampOffset(listEnsureVisible(m.modelPickOffset, m.modelPickCursor, avail), n, avail)
}

func (m Model) modelPickRowAtY(y int) (int, bool) {
	cands := m.modelCandidates()
	avail := m.modelPickVisibleRows(len(cands))
	i := y - modelPickRowsTop + m.modelPickOffset
	if i < m.modelPickOffset || i >= m.modelPickOffset+avail || i >= len(cands) {
		return 0, false
	}
	return i, true
}

func (m Model) modelPickTitle() (string, string) {
	row := m.modelPickRow
	name := rowDisplayName(row)
	current := row.Model
	if current == "" {
		current = "none"
	}
	switch m.modelPickMode {
	case pickRegister:
		return fmt.Sprintf("register a model for %q (%s on %s)", name, row.AgentKind, row.Machine),
			"registered: " + current + " · the live agent keeps its model; the next spawn/resume/turn uses this"
	case pickDefault:
		d := m.modelPickDefaults[row.AgentKind]
		if d == "" {
			d = "none"
		}
		return fmt.Sprintf("default %s model on %s", row.AgentKind, row.Machine),
			"current default: " + d + " · applies to threads created from now on"
	}
	sub := "registered: " + current + " · switches the live agent for this session"
	if row.AgentKind == "claude" && row.Head == api.Headful {
		sub = "registered: " + current + " · a live claude pane RESTARTS on the new model (the conversation resumes)"
	}
	return fmt.Sprintf("switch the model of %q (%s on %s)", name, row.AgentKind, row.Machine), sub
}

// modelPickView renders the picker. Like the parent picker, both indicator lines
// are always present so modelPickRowAtY's geometry holds while scrolling.
func (m Model) modelPickView() string {
	title, sub := m.modelPickTitle()
	var b strings.Builder
	b.WriteString(styleHeader.Render(title) + "\n")
	b.WriteString(styleDim.Render("  "+sub) + "\n")
	b.WriteString(styleDim.Render("  > "+string(m.modelPickQuery)) + "█\n")
	switch {
	case m.modelPickErr != nil:
		b.WriteString("\n" + styleErr.Render("  ✗ "+m.modelPickErr.Error()) + "\n\n")
		b.WriteString(styleDim.Render("esc close") + "\n")
		return b.String()
	case m.modelPickList == nil:
		b.WriteString("\n" + styleDim.Render(fmt.Sprintf("  asking %s on %s which models it can use…", m.modelPickRow.AgentKind, m.modelPickRow.Machine)) + "\n\n")
		b.WriteString(styleDim.Render("esc cancel") + "\n")
		return b.String()
	}
	cands := m.modelCandidates()
	avail := m.modelPickVisibleRows(len(cands))
	off := m.modelPickOffset
	if max := len(cands) - avail; off > max {
		off = max
	}
	if off < 0 {
		off = 0
	}
	if off > 0 {
		b.WriteString(styleDim.Render(fmt.Sprintf("  ▲ %d more", off)) + "\n")
	} else {
		b.WriteString("\n")
	}
	w := 0
	for _, c := range cands {
		if n := len([]rune(c.id)); n > w {
			w = n
		}
	}
	for i := off; i < off+avail; i++ {
		c := cands[i]
		detail := c.label
		if marks := m.modelMarks(c); marks != "" {
			detail += "  [" + marks + "]"
		}
		pad := strings.Repeat(" ", w-len([]rune(c.id)))
		if i == m.modelPickCursor {
			b.WriteString(styleSelected.Render(fmt.Sprintf("  %s%s  %s", c.id, pad, detail)) + "\n")
		} else {
			b.WriteString("  " + highlight(c.id, c.pos) + pad + "  " + styleDim.Render(detail) + "\n")
		}
	}
	if rest := len(cands) - off - avail; rest > 0 {
		b.WriteString(styleDim.Render(fmt.Sprintf("  ▼ %d more", rest)) + "\n")
	} else {
		b.WriteString("\n")
	}
	footer := "type to filter · ↑/↓ move · enter pick · esc cancel"
	switch m.modelPickMode {
	case pickSwitch:
		footer = "type to filter · ↑/↓ move · enter SWITCH · tab: register only · esc cancel"
	case pickRegister:
		footer = "type to filter · ↑/↓ move · enter REGISTER · tab: switch live · esc cancel"
	}
	b.WriteString(styleDim.Render(footer) + "\n")
	return b.String()
}

// ModelPickOpen / ModelPickCandidates / ModelPickLoaded expose the picker (tests).
func (m Model) ModelPickOpen() bool { return m.modelPick }
func (m Model) ModelPickCandidates() []string {
	var out []string
	for _, c := range m.modelCandidates() {
		out = append(out, c.id)
	}
	return out
}
func (m Model) ModelPickLoaded() bool { return m.modelPickList != nil || m.modelPickErr != nil }
