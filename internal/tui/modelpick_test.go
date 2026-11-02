package tui

import (
	"reflect"
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
