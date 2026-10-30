package agents

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReadTranscriptWindowReportsLineAndByteCoverage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte("1111\n2222\n3333\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	bounded, err := readTranscriptWindow(path, 3, 8)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(bounded.Lines, []string{"2222", "3333"}) {
		t.Fatalf("byte-bounded lines = %#v", bounded.Lines)
	}
	if bounded.StartLine != 1 || bounded.EndLine != 3 || bounded.TotalLines != 3 || !bounded.ByteLimited {
		t.Fatalf("byte-bounded coverage = %#v", bounded)
	}

	lineBounded, err := readTranscriptWindow(path, 2, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(lineBounded.Lines, []string{"2222", "3333"}) {
		t.Fatalf("line-bounded lines = %#v", lineBounded.Lines)
	}
	if lineBounded.StartLine != 1 || lineBounded.EndLine != 3 || lineBounded.TotalLines != 3 || lineBounded.ByteLimited {
		t.Fatalf("line-bounded coverage = %#v", lineBounded)
	}

	empty, err := readTranscriptWindow(path, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Lines == nil || len(empty.Lines) != 0 || empty.StartLine != 3 || empty.EndLine != 3 || empty.TotalLines != 3 {
		t.Fatalf("zero-tail coverage = %#v", empty)
	}
}
