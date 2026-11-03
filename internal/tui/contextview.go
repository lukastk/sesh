package tui

// The thread-CONTEXT viewer and editor (schema 60; api/context.go). A thread's
// context lives on its owner, so both fetch it there (`sesh context show --json`,
// routed with --machine) when opened rather than carrying the text on every row
// — a row only says whether there is one (context_hash, the CONTEXT column).
//
//   - context-view: a full-screen, scrollable read of the text (word-wrapped to
//     the pane), with `e` to edit from there;
//   - context-edit: suspend to the editor ([tui] editor / --editor / $EDITOR,
//     like the ticket editor) on a temp file holding the text; on exit the
//     result is saved with `context set --expect-hash`, so a context someone
//     changed meanwhile is refused instead of overwritten (the edited file is
//     kept and named), and an emptied file removes the context.

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/api"
)

// contextChrome is the viewer's fixed non-text line count: title, the blank line
// under it, the two more-indicators, the blank line above the footer, the footer.
const contextChrome = 6

// contextLoadedMsg carries a fetched context to the viewer (or, edit=true, to
// the editor launch).
type contextLoadedMsg struct {
	row  api.ThreadRow
	resp api.ThreadContextResponse
	err  error
	edit bool
}

// contextEditDoneMsg is posted when the editor exits.
type contextEditDoneMsg struct {
	row  api.ThreadRow
	was  api.ThreadContextResponse
	file string
	err  error
}

// fetchContext loads row's context from its owner.
func (m Model) fetchContext(row api.ThreadRow, edit bool) tea.Cmd {
	args := m.routeArgs([]string{"context", "show", "--id", row.ID, "--json"}, row.Machine)
	return func() tea.Msg {
		var resp api.ThreadContextResponse
		err := m.runJSON(args, &resp)
		return contextLoadedMsg{row: row, resp: resp, err: err, edit: edit}
	}
}

// contextCapableRow reports whether row runs an agent that can receive a context.
func contextCapableRow(row api.ThreadRow) error {
	switch row.AgentKind {
	case api.ShellAgentKind, api.VirtualAgentKind, api.DividerAgentKind:
		return fmt.Errorf("%q is a %s thread: it runs no agent, so it has no context", rowDisplayName(row), row.AgentKind)
	}
	return nil
}

// openContextView opens the viewer for the selection and starts the fetch.
func (m Model) openContextView() (tea.Model, tea.Cmd) {
	row, ok := m.Selected()
	if !ok {
		return m, nil
	}
	if err := contextCapableRow(row); err != nil {
		m.actionErr = err
		return m, nil
	}
	m.contextPopup, m.contextRow, m.contextOffset = true, row, 0
	m.contextResp, m.contextErr, m.contextLoading = api.ThreadContextResponse{}, nil, true
	return m, m.fetchContext(row, false)
}

// openContextEdit fetches the selection's context, then suspends to the editor.
func (m Model) openContextEdit() (tea.Model, tea.Cmd) {
	row, ok := m.Selected()
	if !ok {
		return m, nil
	}
	if err := contextCapableRow(row); err != nil {
		m.actionErr = err
		return m, nil
	}
	m.note = "loading the context of " + rowDisplayName(row) + "…"
	return m, m.fetchContext(row, true)
}

// applyContextLoaded routes a fetch to the viewer or the editor.
func (m Model) applyContextLoaded(msg contextLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.edit {
		m.note = ""
		if msg.err != nil {
			m.actionErr = fmt.Errorf("context of %q: %w", rowDisplayName(msg.row), msg.err)
			return m, nil
		}
		return m, m.editContext(msg.row, msg.resp)
	}
	if !m.contextPopup || m.contextRow.ID != msg.row.ID {
		return m, nil // closed (or moved on) before the fetch landed
	}
	m.contextLoading, m.contextResp, m.contextErr = false, msg.resp, msg.err
	return m, nil
}

// editContext writes the text to a temp file and suspends to the editor.
func (m Model) editContext(row api.ThreadRow, was api.ThreadContextResponse) tea.Cmd {
	editor := strings.TrimSpace(m.editor)
	if editor == "" {
		return func() tea.Msg {
			return actionMsg{err: fmt.Errorf("no editor configured: set `sesh tui --editor`, [tui] editor, or $EDITOR")}
		}
	}
	f, err := os.CreateTemp("", "sesh-context-*.md")
	if err != nil {
		return func() tea.Msg { return actionMsg{err: fmt.Errorf("editor temp file: %w", err)} }
	}
	_, werr := f.WriteString(was.Text)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(f.Name())
		return func() tea.Msg { return actionMsg{err: fmt.Errorf("editor temp file: %w", werr)} }
	}
	parts := strings.Fields(editor)
	c := exec.Command(parts[0], append(parts[1:], f.Name())...)
	name := f.Name()
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return contextEditDoneMsg{row: row, was: was, file: name, err: err}
	})
}

// applyContextEdit saves the edited file on the owner — conditional on the
// version that was opened.
func (m Model) applyContextEdit(msg contextEditDoneMsg) tea.Cmd {
	return func() tea.Msg {
		name := rowDisplayName(msg.row)
		if msg.err != nil {
			return actionMsg{err: fmt.Errorf("editor: %w (your text is in %s)", msg.err, msg.file)}
		}
		raw, err := os.ReadFile(msg.file)
		if err != nil {
			return actionMsg{err: fmt.Errorf("read the edited context: %w", err)}
		}
		text := strings.TrimRight(string(raw), "\n")
		if text == strings.TrimRight(msg.was.Text, "\n") {
			os.Remove(msg.file)
			return actionMsg{id: msg.row.ID, note: "context of " + name + " unchanged"}
		}
		expect := msg.was.Hash
		if expect == "" {
			expect = "none"
		}
		// The trimmed text, exactly as `sesh context edit` saves it (editors add a
		// trailing newline the user did not write).
		args := []string{"context", "set", "--id", msg.row.ID, "--text", text, "--expect-hash", expect, "--json"}
		if strings.TrimSpace(text) == "" {
			args = []string{"context", "clear", "--id", msg.row.ID, "--expect-hash", expect, "--json"}
		}
		var resp api.SetThreadContextResponse
		if err := m.runJSON(m.routeArgs(args, msg.row.Machine), &resp); err != nil {
			return actionMsg{err: fmt.Errorf("save the context of %q: %w (your text is in %s)", name, err, msg.file)}
		}
		os.Remove(msg.file)
		return actionMsg{id: msg.row.ID, note: "context of " + name + " " + resp.Detail}
	}
}

// contextLines is the viewer's text, word-wrapped to the pane width — ONE source
// for the renderer and the scroll budget.
func (m Model) contextLines() []string {
	switch {
	case m.contextLoading:
		return []string{"loading…"}
	case m.contextErr != nil:
		return wrapText("could not load the context: "+m.contextErr.Error(), m.contextWrapWidth())
	case m.contextResp.Text == "":
		return []string{"(this thread has no context — `context-edit` adds one)"}
	}
	return wrapText(m.contextResp.Text, m.contextWrapWidth())
}

func (m Model) contextWrapWidth() int {
	if m.width <= 4 {
		return 76
	}
	return m.width - 4
}

func (m Model) contextVisibleRows() int {
	return listVisibleRows(len(m.contextLines()), m.height, contextChrome)
}

func (m Model) contextMaxOffset() int {
	return max(0, len(m.contextLines())-m.contextVisibleRows())
}

// handleContextKey drives the viewer: scroll, `e` to edit, esc/q to close.
func (m Model) handleContextKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	page := max(1, m.contextVisibleRows()-1)
	switch msg.String() {
	case "esc", "q", "ctrl+c", "enter":
		m.contextPopup, m.contextOffset = false, 0
	case "up", "k":
		m.contextOffset = max(0, m.contextOffset-1)
	case "down", "j":
		m.contextOffset = min(m.contextMaxOffset(), m.contextOffset+1)
	case "ctrl+k", "pgup", "b":
		m.contextOffset = max(0, m.contextOffset-page)
	case "ctrl+j", "pgdown", " ", "f":
		m.contextOffset = min(m.contextMaxOffset(), m.contextOffset+page)
	case "g", "home":
		m.contextOffset = 0
	case "G", "end":
		m.contextOffset = m.contextMaxOffset()
	case "e":
		if m.contextLoading || m.contextErr != nil {
			return m, nil
		}
		row, was := m.contextRow, m.contextResp
		m.contextPopup, m.contextOffset = false, 0
		return m, m.editContext(row, was)
	}
	return m, nil
}

// contextView renders the viewer. Like the details popup, the two indicator lines
// are always present so the layout does not jump while scrolling.
func (m Model) contextView() string {
	lines := m.contextLines()
	avail := m.contextVisibleRows()
	off := min(max(0, m.contextOffset), m.contextMaxOffset())
	var b strings.Builder
	title := rowDisplayName(m.contextRow)
	meta := ""
	if m.contextResp.Hash != "" {
		meta = fmt.Sprintf(" · %d bytes · version %s", len(m.contextResp.Text), m.contextResp.Hash)
	}
	b.WriteString(styleHeader.Render("thread context · "+title) + styleDim.Render(meta) + "\n")
	if off > 0 {
		b.WriteString(styleDim.Render(fmt.Sprintf("  ▲ %d more", off)) + "\n")
	} else {
		b.WriteString("\n")
	}
	for _, l := range lines[off:min(len(lines), off+avail)] {
		b.WriteString("  " + l + "\n")
	}
	if rest := len(lines) - off - avail; rest > 0 {
		b.WriteString(styleDim.Render(fmt.Sprintf("  ▼ %d more", rest)) + "\n")
	} else {
		b.WriteString("\n")
	}
	b.WriteString("\n" + styleDim.Render("  ↑/↓ scroll · pgup/pgdn page · g/G top/bottom · e edit · esc/q close"))
	return b.String()
}

// ContextOpen reports whether the context viewer is up (tests).
func (m Model) ContextOpen() bool { return m.contextPopup }

// wrapText word-wraps s to width columns (runes), hard-breaking words longer than
// a line. Control characters are made visible so stored text can never drive the
// terminal; tabs become spaces.
func wrapText(s string, width int) []string {
	if width < 8 {
		width = 8
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		para = sanitizeLine(para)
		if strings.TrimSpace(para) == "" {
			out = append(out, "")
			continue
		}
		// Keep the line's indentation (markdown lists, quoted blocks) on every
		// wrapped piece of it; give up on it only when it leaves no room.
		indent := []rune(para[:len(para)-len(strings.TrimLeft(para, " "))])
		if len(indent) > width/2 {
			indent = indent[:0]
		}
		room := width - len(indent)
		emit := func(r []rune) { out = append(out, string(indent)+string(r)) }
		line := []rune{}
		for _, word := range strings.Fields(para) {
			w := []rune(word)
			for len(w) > room {
				if len(line) > 0 {
					emit(line)
					line = line[:0]
				}
				emit(w[:room])
				w = w[room:]
			}
			switch {
			case len(line) == 0:
				line = append(line, w...)
			case len(line)+1+len(w) <= room:
				line = append(append(line, ' '), w...)
			default:
				emit(line)
				line = append([]rune{}, w...)
			}
		}
		if len(line) > 0 {
			emit(line)
		}
	}
	return out
}

func sanitizeLine(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return ' '
		case unicode.IsControl(r):
			return '�'
		}
		return r
	}, s)
}
