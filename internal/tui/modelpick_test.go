package tui

import (
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/api"
)

func pickerModel(mode modelPickMode, row api.ThreadRow) Model {
	return Model{modelPick: true, modelPickMode: mode, modelPickRow: row, modelPickList: &api.ModelListResponse{
		Agent: row.AgentKind,
		Models: []api.ModelOption{
			{ID: "sonnet", Label: "Sonnet", Resolved: "claude-sonnet-5-5"},
			{ID: "opus", Label: "Opus", Resolved: "claude-opus-5-5"},
			{ID: "haiku", Label: "Haiku", Resolved: "claude-haiku-5-5"},
		},
	}}
}

func TestModelCandidatesPerMode(t *testing.T) {
	row := api.ThreadRow{Thread: api.Thread{ID: "t1", AgentKind: "claude", Model: "opus"}, ObservedModel: "claude-opus-5-5"}
	if got := pickerModel(pickSwitch, row).ModelPickCandidates(); !reflect.DeepEqual(got, []string{"sonnet", "opus", "haiku"}) {
		t.Errorf("switch: %v (no clear entry, the harness's order)", got)
	}
	if got := pickerModel(pickRegister, row).ModelPickCandidates(); !reflect.DeepEqual(got, []string{"", "sonnet", "opus", "haiku"}) {
		t.Errorf("register: %v (the (none) entry first)", got)
	}
	m := pickerModel(pickSwitch, row)
	m.modelPickQuery = []rune("hai")
	if got := m.ModelPickCandidates(); len(got) != 2 || got[0] != "haiku" || got[1] != "hai" {
		t.Errorf("filtered: %v (best match, then the query as typed)", got)
	}
	m.modelPickQuery = []rune("opus")
	if got := m.ModelPickCandidates(); !reflect.DeepEqual(got, []string{"opus"}) {
		t.Errorf("an exact id is not offered again as typed: %v", got)
	}
	// Loading: nothing to pick yet.
	m.modelPickList = nil
	if got := m.ModelPickCandidates(); got != nil {
		t.Errorf("loading picker offered %v", got)
	}
}

func TestModelMarks(t *testing.T) {
	row := api.ThreadRow{Thread: api.Thread{AgentKind: "claude", Model: "opus"}, ObservedModel: "claude-opus-5-5"}
	m := pickerModel(pickSwitch, row)
	if got := m.modelMarks(modelCandidate{id: "opus"}); got != "registered, running" {
		t.Errorf("opus marks = %q", got)
	}
	if got := m.modelMarks(modelCandidate{id: "sonnet"}); got != "" {
		t.Errorf("sonnet marks = %q", got)
	}
	d := pickerModel(pickDefault, row)
	d.modelPickDefaults = map[string]string{"claude": "sonnet"}
	if got := d.modelMarks(modelCandidate{id: "sonnet"}); got != "default" {
		t.Errorf("default marks = %q", got)
	}
}

func TestModelPickTabAndClaudeConfirm(t *testing.T) {
	row := api.ThreadRow{Thread: api.Thread{ID: "t1", AgentKind: "claude", Model: "opus"}, Head: api.Headful}
	m := pickerModel(pickSwitch, row)
	nm, _ := m.handleModelPickKey(tea.KeyMsg{Type: tea.KeyTab})
	if nm.(Model).modelPickMode != pickRegister {
		t.Fatalf("tab did not flip switch -> register")
	}
	nm, _ = nm.(Model).handleModelPickKey(tea.KeyMsg{Type: tea.KeyTab})
	if nm.(Model).modelPickMode != pickSwitch {
		t.Fatalf("tab did not flip register -> switch")
	}
	// Enter on a LIVE claude row asks first (no command runs yet).
	nm, cmd := nm.(Model).handleModelPickKey(tea.KeyMsg{Type: tea.KeyEnter})
	got := nm.(Model)
	if cmd != nil || got.confirming != confirmSwitchModel || got.confirmModel != "sonnet" || got.modelPick {
		t.Fatalf("live claude switch: cmd=%v confirming=%v model=%q pickerOpen=%v", cmd != nil, got.confirming, got.confirmModel, got.modelPick)
	}
	// A headless claude (nothing to restart) switches without asking.
	row.Head = api.Headless
	nm, cmd = pickerModel(pickSwitch, row).handleModelPickKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || nm.(Model).confirming != confirmNone {
		t.Fatalf("headless claude switch should run directly (cmd=%v confirming=%v)", cmd != nil, nm.(Model).confirming)
	}
}

func TestDefaultPickerStartsOnTheHarnessStage(t *testing.T) {
	// No selection at all: the default picker does not need a thread.
	nm, cmd := Model{machine: "mymain"}.openModelPick(pickDefault)
	m := nm.(Model)
	if !m.modelPick || !m.modelPickHarness || cmd == nil {
		t.Fatalf("open: pick=%v harness=%v cmd=%v", m.modelPick, m.modelPickHarness, cmd != nil)
	}
	if m.ModelPickLoaded() || m.ModelPickCandidates() != nil {
		t.Fatalf("the harness stage should load first")
	}
	m = m.applyModelDefaults(modelDefaultsMsg{defaults: map[string]string{"claude": "opus", "codex": "", "pi": "", "mypi": ""}})
	if got := m.ModelPickCandidates(); !reflect.DeepEqual(got, []string{"claude", "codex", "mypi", "pi"}) {
		t.Fatalf("harnesses = %v", got)
	}
	if v := m.modelPickView(); !strings.Contains(v, "default: opus") || !strings.Contains(v, "on mymain") {
		t.Errorf("harness stage view:\n%s", v)
	}
	m.modelPickQuery = []rune("cod")
	if got := m.ModelPickCandidates(); !reflect.DeepEqual(got, []string{"codex"}) {
		t.Fatalf("filtered harnesses = %v", got)
	}
	// Choosing one moves to its model list (fetched), on the same machine.
	nm, cmd = m.handleModelPickKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(Model)
	if m.modelPickHarness || m.modelPickRow.AgentKind != "codex" || m.modelPickRow.Machine != "mymain" || cmd == nil || len(m.modelPickQuery) != 0 {
		t.Fatalf("after choosing: harness=%v row=%+v cmd=%v query=%q", m.modelPickHarness, m.modelPickRow.Thread, cmd != nil, string(m.modelPickQuery))
	}
	// A late list for another harness is dropped; this one's is taken.
	if got := m.applyModelList(modelListMsg{rowID: "default:claude", list: api.ModelListResponse{Models: []api.ModelOption{{ID: "x"}}}}); got.modelPickList != nil {
		t.Errorf("a stale list was applied")
	}
	m = m.applyModelList(modelListMsg{rowID: "default:codex", list: api.ModelListResponse{Models: []api.ModelOption{{ID: "gpt-6-luna"}}}})
	if got := m.ModelPickCandidates(); !reflect.DeepEqual(got, []string{"", "gpt-6-luna"}) {
		t.Fatalf("model stage = %v ((none) first)", got)
	}
}

func TestRouteArgsLocalMachine(t *testing.T) {
	m := Model{machine: "mymain"}
	if got := m.routeArgs([]string{"x"}, "mymain"); len(got) != 1 {
		t.Errorf("own machine routed: %v", got)
	}
	if got := m.routeArgs([]string{"x"}, ""); len(got) != 1 {
		t.Errorf("empty machine routed: %v", got)
	}
	if got := m.routeArgs([]string{"x"}, "macbook"); !reflect.DeepEqual(got, []string{"x", "--machine", "macbook"}) {
		t.Errorf("peer: %v", got)
	}
}
