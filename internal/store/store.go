// Package store is the daemon's single-writer SQLite persistence layer. The
// daemon process is the only writer; clients reach it only through the daemon's
// HTTP API, never by opening the DB directly. WAL mode keeps reads concurrent
// with the single writer.
package store

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite" // pure-Go driver (no cgo) — portable across the mesh
)

// Store wraps the daemon's database handle.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path with WAL mode and
// foreign keys, then applies migrations. The DSN pragmas are applied on every
// connection in the pool via the connection string.
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	// Single writer: serialize writes through one connection. WAL still allows
	// concurrent readers. This is the daemon's single-writer guarantee made
	// concrete rather than assumed.
	db.SetMaxOpenConns(1)

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// DB exposes the underlying handle for later layers (threads, tickets).
func (s *Store) DB() *sql.DB { return s.db }

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Ping verifies the connection and that WAL is actually in effect.
func (s *Store) Ping() error {
	var mode string
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		return fmt.Errorf("store: ping: %w", err)
	}
	if mode != "wal" {
		return fmt.Errorf("store: expected WAL journal mode, got %q", mode)
	}
	return nil
}

// SchemaVersion returns the applied schema version.
func (s *Store) SchemaVersion() (int, error) {
	var v int
	err := s.db.QueryRow("SELECT version FROM meta WHERE id = 1").Scan(&v)
	return v, err
}
