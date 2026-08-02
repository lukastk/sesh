package fork

import (
	"encoding/json"
	"reflect"
	"testing"
)

func decodeObject(t *testing.T, line string) map[string]any {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal([]byte(line), &obj); err != nil {
		t.Fatal(err)
	}
	return obj
}

func TestClaudeBranchRekeysConversationGraph(t *testing.T) {
	source := []string{
		`{"type":"user","sessionId":"old-session","uuid":"root-message","parentUuid":null,"message":{"role":"user","content":"keep old-session in user content"}}`,
		`{"type":"assistant","sessionId":"old-session","uuid":"answer-message","parentUuid":"root-message","message":{"role":"assistant","content":[{"type":"text","text":"OBSIDIAN"}]}}`,
		`{"type":"system","subtype":"compact_boundary","sessionId":"old-session","uuid":"boundary-message","parentUuid":null,"logicalParentUuid":"answer-message"}`,
	}
	original := append([]string(nil), source...)

	branch, err := Branch("claude", source, "old-session", "new-session", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(source, original) {
		t.Fatal("Branch mutated the source lines")
	}
	if len(branch) != len(source) {
		t.Fatalf("got %d branch lines, want %d", len(branch), len(source))
	}

	root := decodeObject(t, branch[0])
	answer := decodeObject(t, branch[1])
	boundary := decodeObject(t, branch[2])
	if got := root["sessionId"]; got != "new-session" {
		t.Fatalf("root sessionId = %v, want new-session", got)
	}
	if got := root["uuid"]; got == "root-message" || got == "" {
		t.Fatalf("root uuid was not re-keyed: %v", got)
	}
	if answer["parentUuid"] != root["uuid"] {
		t.Fatalf("answer parentUuid = %v, want re-keyed root %v", answer["parentUuid"], root["uuid"])
	}
	if boundary["logicalParentUuid"] != answer["uuid"] {
		t.Fatalf("boundary logicalParentUuid = %v, want re-keyed answer %v", boundary["logicalParentUuid"], answer["uuid"])
	}
	message := root["message"].(map[string]any)
	if got := message["content"]; got != "keep old-session in user content" {
		t.Fatalf("message content changed: %v", got)
	}

	other, err := Branch("claude", source, "old-session", "another-session", 0)
	if err != nil {
		t.Fatal(err)
	}
	if decodeObject(t, other[0])["uuid"] == root["uuid"] {
		t.Fatal("two intentional branches share a conversation root")
	}
}
