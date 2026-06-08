package store

import "fmt"

// migrations is the ordered list of schema steps. Each is applied once, inside a
// transaction, and the meta.version is bumped to its index. Append-only: never
// edit a shipped migration, add a new one.
var migrations = []string{
	// 1: bookkeeping table.
	`CREATE TABLE IF NOT EXISTS meta (
		id      INTEGER PRIMARY KEY CHECK (id = 1),
		version INTEGER NOT NULL
	);`,
}

// migrate applies any unapplied migrations. The current version lives in
// meta.version; a fresh database starts at 0.
func (s *Store) migrate() error {
	if _, err := s.db.Exec(migrations[0]); err != nil {
		return fmt.Errorf("store: bootstrap meta: %w", err)
	}
	if _, err := s.db.Exec(`INSERT INTO meta (id, version) VALUES (1, 0) ON CONFLICT(id) DO NOTHING`); err != nil {
		return fmt.Errorf("store: seed meta: %w", err)
	}

	var current int
	if err := s.db.QueryRow("SELECT version FROM meta WHERE id = 1").Scan(&current); err != nil {
		return fmt.Errorf("store: read schema version: %w", err)
	}

	for i := current; i < len(migrations); i++ {
		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("store: begin migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: apply migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec("UPDATE meta SET version = ? WHERE id = 1", i+1); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: bump version to %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: commit migration %d: %w", i+1, err)
		}
	}
	return nil
}
