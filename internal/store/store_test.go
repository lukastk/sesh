package store

import (
	"path/filepath"
	"testing"
)

func TestOpenAppliesWALAndMigrations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sesh.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if err := s.Ping(); err != nil {
		t.Fatalf("Ping (expects WAL): %v", err)
	}
	v, err := s.SchemaVersion()
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if v != len(migrations) {
		t.Fatalf("schema version = %d, want %d", v, len(migrations))
	}
}

func TestOpenIsIdempotentAcrossReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sesh.db")
	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	v1, _ := s1.SchemaVersion()
	s1.Close()

	// Reopening an existing DB must not re-run migrations or change the version.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	v2, _ := s2.SchemaVersion()
	if v1 != v2 {
		t.Fatalf("schema version changed across reopen: %d -> %d", v1, v2)
	}
}
