package fork

import (
	"encoding/json"
	"reflect"
	"strings"
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

// A mypi fork rewrites pi's format and lands where pi's resume will find it:
// the fork is about the on-disk FORMAT, and mypi shares pi's entirely. The
// forked thread's own record keeps kind mypi, so its later launches still run
// the mypi executable.
func TestMyPiForksThroughThePiFormatAndStore(t *testing.T) {
	oldID, newID := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	lines := []string{
		`{"type":"session","version":3,"id":"` + oldID + `","cwd":"/tmp/proj"}`,
		`{"type":"message","id":"u1","message":{"role":"user","content":[{"type":"text","text":"one"}]}}`,
		`{"type":"message","id":"a1","message":{"role":"assistant","content":[{"type":"text","text":"two"}]}}`,
		`{"type":"message","id":"u2","message":{"role":"user","content":[{"type":"text","text":"three"}]}}`,
	}
	for _, agent := range []string{"pi", "mypi"} {
		out, err := Branch(agent, lines, oldID, newID, 1)
		if err != nil {
			t.Fatalf("%s branch: %v", agent, err)
		}
		if len(out) != 3 {
			t.Fatalf("%s branch kept %d lines, want the prefix through turn 1", agent, len(out))
		}
		if !strings.Contains(out[0], newID) || strings.Contains(out[0], oldID) {
			t.Fatalf("%s session record was not re-keyed: %s", agent, out[0])
		}
		path, err := DestPath(agent, "/claude", "/codex", "/pi-home", "/tmp/proj", newID)
		if err != nil {
			t.Fatalf("%s dest path: %v", agent, err)
		}
		if !strings.HasPrefix(path, "/pi-home/sessions/--tmp-proj--/") || !strings.HasSuffix(path, "_"+newID+".jsonl") {
			t.Fatalf("%s dest path = %q", agent, path)
		}
	}
	if _, err := Branch("gemini", lines, oldID, newID, 1); err == nil {
		t.Fatal("an unknown harness was accepted by fork")
	}
}
