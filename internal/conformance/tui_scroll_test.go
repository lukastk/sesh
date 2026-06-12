package conformance

// G5 scroll claims: a vertical viewport (ctrl+j/k + cursor-follow; the mouse wheel
// moves the SELECTION with the viewport following) and horizontal column pan (h/l +
// mouse wheel left/right), over REAL threads in a fixed-size window. The TUI clips to
// the window and the offsets/indicators move honestly — proven by Cursor/VOffset/HOffset
// and the ▲/▼/‹/› markers, not internal mocks.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/matrix"
	"github.com/lukastk/sesh/internal/tui"
)

func init() {
	registerTUIClaim("scroll-vertical", claimScrollVertical)
	registerTUIClaim("scroll-horizontal", claimScrollHorizontal)
}

func sendMsg(t *testing.T, m tui.Model, msg tea.Msg) tui.Model {
	t.Helper()
	nm, _ := m.Update(msg)
	return nm.(tui.Model)
}

// claimScrollVertical: with more threads than fit the window, the grid clips to a
// viewport. ctrl+j/k scroll it (▲/▼ indicators track the clipped rows) and moving
// the cursor past the edge pulls the viewport along so the selection stays visible.
func claimScrollVertical(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	const n = 9
	for i := 0; i < n; i++ {
		sb.newHeadlessThread(t, "pi", fmt.Sprintf("srow-%d", i))
	}
	m := tui.New(sb.Home+"/daemon.sock", false).WithLocal(sb.Machine, sb.TmuxSocket)
	if !waitUntil(25*time.Second, func() bool { m, _ = render(t, m); return len(m.Rows()) == n }) {
		t.Fatalf("want %d rows, got %d", n, len(m.Rows()))
	}
	// A short window: fewer rows fit than exist, so content clips. Cursor at the top.
	m = sendMsg(t, m, tea.WindowSizeMsg{Width: 120, Height: 14})
	view := m.View()
	if m.VOffset() != 0 {
		t.Errorf("vOffset should start at 0, got %d", m.VOffset())
	}
	if !strings.Contains(view, "▼") {
		t.Fatalf("no down-scroll indicator with clipped content:\n%s", view)
	}
	if strings.Contains(view, "▲") {
		t.Errorf("up indicator shown at the very top:\n%s", view)
	}

	// Cursor-follow (cursor is guaranteed at the top here): drive it to the LAST row;
	// the viewport must follow so the selected row is rendered, top scrolled off.
	top, _ := m.Selected()
	for i := 0; i < n-1; i++ {
		m = runSpecial(t, m, tea.KeyDown)
	}
	last, ok := m.Selected()
	if !ok || last.Name == top.Name {
		t.Fatalf("cursor did not move off the first row")
	}
	if m.VOffset() == 0 {
		t.Errorf("viewport did not follow the cursor to the bottom (vOffset still 0)")
	}
	if !strings.Contains(m.View(), last.Name) {
		t.Errorf("the selected last row is not visible (cursor-follow failed):\n%s", m.View())
	}
	if strings.Contains(m.View(), top.Name) {
		t.Errorf("the top row %q should have scrolled off:\n%s", top.Name, m.View())
	}

	// ctrl+k scrolls the viewport back to the top.
	for i := 0; i < n && m.VOffset() > 0; i++ {
		m = runSpecial(t, m, tea.KeyCtrlK)
	}
	if m.VOffset() != 0 {
		t.Errorf("ctrl+k did not return to the top: vOffset=%d", m.VOffset())
	}
	if !strings.Contains(m.View(), "▼") || strings.Contains(m.View(), "▲") {
		t.Errorf("at the top, expect a ▼ indicator and no ▲:\n%s", m.View())
	}

	// ctrl+j scrolls the viewport down; the up indicator now appears.
	m = runSpecial(t, m, tea.KeyCtrlJ)
	if m.VOffset() == 0 {
		t.Errorf("ctrl+j did not scroll the viewport (vOffset still 0)")
	}
	if !strings.Contains(m.View(), "▲") {
		t.Errorf("up indicator missing after scrolling down:\n%s", m.View())
	}

	// The MOUSE WHEEL moves the SELECTION between rows (like ↑/↓), with the viewport
	// following — so it works even when the grid fits the screen. Wheel up to the first
	// row, then wheel down advances the cursor one row.
	for i := 0; i < 3*n && m.Cursor() > 0; i++ {
		m = sendMsg(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	}
	if m.Cursor() != 0 {
		t.Errorf("mouse wheel-up did not move the selection to the first row: cursor=%d", m.Cursor())
	}
	if m.VOffset() != 0 {
		t.Errorf("viewport did not follow the cursor back to the top: vOffset=%d", m.VOffset())
	}
	m = sendMsg(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	if m.Cursor() != 1 {
		t.Errorf("mouse wheel-down did not move the selection down one row: cursor=%d", m.Cursor())
	}
}

// claimScrollHorizontal: when the row is wider than the window, h/l pan the columns.
// ‹/› in the header flag clipped columns, and a column clipped off the right is
// brought into view by panning right (then panning back restores the left columns).
func claimScrollHorizontal(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	sb.newHeadlessThread(t, "pi", "wide")

	cols := []string{tui.ColMachine, tui.ColAgent, tui.ColName, tui.ColCwd, tui.ColTags, tui.ColCreated}
	m := tui.New(sb.Home+"/daemon.sock", false).WithLocal(sb.Machine, sb.TmuxSocket).WithColumns(cols)
	if !waitUntil(20*time.Second, func() bool { m, _ = render(t, m); return len(m.Rows()) == 1 }) {
		t.Fatalf("row never appeared")
	}
	// A narrow window: the full column set cannot fit.
	m = sendMsg(t, m, tea.WindowSizeMsg{Width: 40, Height: 20})
	view := m.View()
	if m.HOffset() != 0 {
		t.Errorf("hOffset should start at 0, got %d", m.HOffset())
	}
	if !strings.Contains(view, "MACHINE") {
		t.Fatalf("leftmost column not visible at offset 0:\n%s", view)
	}
	if !strings.Contains(view, "›") {
		t.Fatalf("no right-clip indicator with a too-wide row:\n%s", view)
	}
	if strings.Contains(view, "CREATED") {
		t.Fatalf("rightmost column visible despite clipping:\n%s", view)
	}

	// Pan right until the rightmost column (CREATED) comes into view.
	for i := 0; i < len(cols)+2 && !strings.Contains(m.View(), "CREATED"); i++ {
		m = runKey(t, m, "l")
	}
	if !strings.Contains(m.View(), "CREATED") {
		t.Fatalf("panning right never revealed the clipped column:\n%s", m.View())
	}
	if m.HOffset() == 0 {
		t.Errorf("hOffset did not advance while panning right")
	}
	if !strings.Contains(m.View(), "‹") {
		t.Errorf("left-clip indicator missing after panning right:\n%s", m.View())
	}
	if strings.Contains(m.View(), "MACHINE") {
		t.Errorf("leftmost column should have scrolled off:\n%s", m.View())
	}

	// Pan all the way back: the left columns return, hOffset is 0.
	for i := 0; i < len(cols)+2 && m.HOffset() > 0; i++ {
		m = runKey(t, m, "h")
	}
	if m.HOffset() != 0 {
		t.Errorf("panning left did not return to offset 0: hOffset=%d", m.HOffset())
	}
	if !strings.Contains(m.View(), "MACHINE") {
		t.Errorf("leftmost column not restored after panning back:\n%s", m.View())
	}

	// The MOUSE WHEEL pans columns too: native wheel-right advances hOffset, wheel-left
	// restores. (Many terminals don't emit horizontal wheel events — the Shift path below
	// is the reliable one.)
	m = sendMsg(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelRight})
	if m.HOffset() == 0 {
		t.Errorf("mouse wheel-right did not pan the columns")
	}
	for i := 0; i < len(cols)+2 && m.HOffset() > 0; i++ {
		m = sendMsg(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelLeft})
	}
	if m.HOffset() != 0 {
		t.Errorf("mouse wheel-left did not return to offset 0: hOffset=%d", m.HOffset())
	}

	// Shift+vertical-wheel ALSO pans (the cross-terminal-reliable horizontal path):
	// shift+wheel-down advances hOffset, shift+wheel-up restores it.
	m = sendMsg(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown, Shift: true})
	if m.HOffset() == 0 {
		t.Errorf("Shift+wheel-down did not pan the columns")
	}
	for i := 0; i < len(cols)+2 && m.HOffset() > 0; i++ {
		m = sendMsg(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp, Shift: true})
	}
	if m.HOffset() != 0 {
		t.Errorf("Shift+wheel-up did not return to offset 0: hOffset=%d", m.HOffset())
	}
}
