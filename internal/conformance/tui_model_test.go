package conformance

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
	"github.com/lukastk/sesh/internal/tui"
)

func init() {
	registerTUIClaim("model-column", claimModelColumn)
	registerTUIClaim("model-picker", claimModelPicker)
}

// claimModelColumn: the MODEL column is in the DEFAULT set and renders a REAL
// thread's model from the owner: the registered model in parentheses before any
// turn, then — after a real headless turn run on a one-turn --model override —
// the model the transcript says it ran, marked `*` because it disagrees with
// the registered one. A thread with neither shows a blank cell.
func claimModelColumn(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	out, stderr, err := sb.Runner.Run(t, "thread", "new", "--agent", "codex", "--name", "pinned-thread", "--cwd", "/tmp", "--headless", "--model", "gpt-6-luna", "--json")
	if err != nil {
		t.Fatalf("thread new: %v\n%s", err, stderr)
	}
	var th api.Thread
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &th); err != nil {
		t.Fatal(err)
	}
	sb.newHeadlessThread(t, "codex", "bare-thread")

	cols, err := tui.ResolveColumns(nil)
	if err != nil {
		t.Fatal(err)
	}
	m := tui.New(sb.Home+"/daemon.sock", false).WithColumns(cols)
	m, view := renderUntilRowView(t, m, "bare-thread")
	if !strings.Contains(view, "MODEL") {
		t.Fatalf("MODEL is not in the default column set:\n%s", view)
	}
	if l := lineWith(view, "pinned-thread"); !strings.Contains(l, "(gpt-6-luna)") {
		t.Errorf("a registered, not-yet-run thread should show (gpt-6-luna): %q\n%s", l, view)
	}
	if l := lineWith(view, "bare-thread"); strings.Contains(l, "gpt-") || strings.Contains(l, "(") {
		t.Errorf("a thread with no model shows one: %q", l)
	}

	sb.headlessTurnArgs(t, th.ID, "Reply with exactly the word OK and nothing else.", "--model", "gpt-6-astra")
	if !waitUntil(30*time.Second, func() bool {
		m, view = renderUntilRowView(t, m, "bare-thread")
		return strings.Contains(lineWith(view, "pinned-thread"), "gpt-6-astra*")
	}) {
		t.Fatalf("after a turn on gpt-6-astra the row never showed gpt-6-astra*:\n%s", view)
	}
}

func pickerHas(m tui.Model, id string) bool {
	for _, c := range m.ModelPickCandidates() {
		if c == id {
			return true
		}
	}
	return false
}

// claimModelPicker drives the model picker against a real daemon and real
// harnesses: `switch-model` lists the models the REAL codex reports, typing
// filters them, and Enter switches the LIVE codex (codex confirms, the record
// follows); Tab flips to register-only, whose "(none)" entry clears the record;
// a typed id the list lacks is offered and registered as typed;
// `set-default-model` starts on a harness stage (no selection needed) and
// writes a default for pi, which has no thread at all;
// and on a LIVE claude pane the switch stops at a y/n that says the pane
// restarts — declining changes nothing.
func claimModelPicker(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	live := newThreadOnModel(t, sb, "codex", "live-codex", "gpt-6-luna", false)
	sb.waitThreadReady(t, live.ID, "codex")
	sb.newHeadlessThread(t, "codex", "idle-codex")
	cl := newThreadOnModel(t, sb, "claude", "live-claude", "haiku", false)
	sb.waitThreadReady(t, cl.ID, "claude")

	bin := seshBin(t)
	env := []string{"SESH_HOME=" + sb.Home, "SESH_MACHINE=" + sb.Machine}
	m := tui.New(sb.Home+"/daemon.sock", false).WithExec(bin, env).WithLocal(sb.Machine, sb.TmuxSocket)
	if !waitUntil(20*time.Second, func() bool { m, _ = render(t, m); return len(m.Rows()) == 3 }) {
		t.Fatalf("3 rows never appeared")
	}

	// SWITCH a live codex from the real list, filtered.
	m = selectRowByName(t, m, "live-codex")
	m = runCommand(t, m, "switch-model")
	if !m.ModelPickOpen() || !m.ModelPickLoaded() {
		t.Fatalf("switch-model did not open a loaded picker (open=%v loaded=%v)", m.ModelPickOpen(), m.ModelPickLoaded())
	}
	if !pickerHas(m, "gpt-6-luna") || !pickerHas(m, "gpt-6-astra") {
		t.Fatalf("the picker does not list the real codex models: %v", m.ModelPickCandidates())
	}
	if v := m.View(); !strings.Contains(v, "switch the model of") || !strings.Contains(v, "[registered, ") && !strings.Contains(v, "[registered]") {
		t.Errorf("picker view lacks its title or the registered mark:\n%s", v)
	}
	m = typeText(t, m, "astra")
	if got := m.ModelPickCandidates(); len(got) == 0 || got[0] != "gpt-6-astra" {
		t.Fatalf("filtering by \"astra\" put %v first", got)
	}
	m = runSpecial(t, m, tea.KeyEnter)
	if m.ActionErr() != nil {
		t.Fatalf("switch errored: %v", m.ActionErr())
	}
	if !strings.Contains(m.Note(), "[switched]") || !strings.Contains(m.Note(), "Model changed to gpt-6-astra") {
		t.Errorf("switch note = %q, want codex's confirmation", m.Note())
	}
	if got := threadByName(t, sb, "live-codex").Model; got != "gpt-6-astra" {
		t.Errorf("record after the switch = %q", got)
	}

	// REGISTER (Tab) a typed id the list lacks, then clear it with (none).
	m = selectRowByName(t, m, "idle-codex")
	m = runCommand(t, m, "switch-model")
	m = runSpecial(t, m, tea.KeyTab)
	if v := m.View(); !strings.Contains(v, "register a model for") {
		t.Fatalf("Tab did not flip to register mode:\n%s", v)
	}
	m = typeText(t, m, "gpt-9-imaginary")
	if got := m.ModelPickCandidates(); len(got) != 1 || got[0] != "gpt-9-imaginary" {
		t.Fatalf("a typed id should be the only candidate: %v", got)
	}
	m = runSpecial(t, m, tea.KeyEnter)
	if m.ActionErr() != nil {
		t.Fatalf("register errored: %v", m.ActionErr())
	}
	if got := threadByName(t, sb, "idle-codex").Model; got != "gpt-9-imaginary" {
		t.Fatalf("typed registration = %q", got)
	}
	m = runCommand(t, m, "set-model")
	if got := m.ModelPickCandidates(); len(got) == 0 || got[0] != "" {
		t.Fatalf("register mode should offer (none) first: %v", got)
	}
	m = runSpecial(t, m, tea.KeyEnter)
	if got := threadByName(t, sb, "idle-codex").Model; got != "" {
		t.Errorf("(none) left the registration %q", got)
	}

	// A harness DEFAULT, independent of the selection: the picker starts on the
	// harness stage, and pi — which has NO thread here — gets a default.
	m = runCommand(t, m, "set-default-model")
	if !m.ModelPickLoaded() || !pickerHas(m, "pi") || !pickerHas(m, "claude") || !strings.Contains(m.View(), "set a harness's default model") {
		t.Fatalf("set-default-model did not open on the harness stage: %v\n%s", m.ModelPickCandidates(), m.View())
	}
	m = typeText(t, m, "pi")
	if got := m.ModelPickCandidates(); len(got) == 0 || got[0] != "pi" {
		t.Fatalf("filtering harnesses by \"pi\" put %v first", got)
	}
	m = runSpecial(t, m, tea.KeyEnter) // choose pi -> its REAL model list loads
	if !m.ModelPickLoaded() || !pickerHas(m, "openai-codex/gpt-6-luna") {
		t.Fatalf("pi's model stage did not load its real list: %v", m.ModelPickCandidates())
	}
	m = typeText(t, m, "openai-codex/gpt-6-luna")
	m = runSpecial(t, m, tea.KeyEnter)
	if m.ActionErr() != nil {
		t.Fatalf("set-default errored: %v", m.ActionErr())
	}
	out, _, err := sb.Runner.Run(t, "model", "defaults", "--json")
	if err != nil || !strings.Contains(out, `"pi":"openai-codex/gpt-6-luna"`) {
		t.Errorf("the pi default was not written: %v %s", err, out)
	}

	// A live claude pane: the switch asks first and says the pane restarts.
	_, pidBefore := liveAgentPID(t, sb, cl.ID)
	m = selectRowByName(t, m, "live-claude")
	m = runCommand(t, m, "switch-model")
	m = typeText(t, m, "sonnet")
	m = runSpecial(t, m, tea.KeyEnter)
	if !m.Confirming() || !strings.Contains(m.View(), "restart") {
		t.Fatalf("switching a live claude pane did not ask about the restart:\n%s", m.View())
	}
	m = runKey(t, m, "n")
	if m.Confirming() {
		t.Fatalf("declining left the confirmation open")
	}
	if got := threadByName(t, sb, "live-claude").Model; got != "haiku" {
		t.Errorf("a declined switch changed the record to %q", got)
	}
	if _, pid := liveAgentPID(t, sb, cl.ID); pid != pidBefore {
		t.Errorf("a declined switch restarted claude (pid %d -> %d)", pidBefore, pid)
	}
}
