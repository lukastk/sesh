package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lukastk/sesh/internal/api"
)

// ErrThreadNotFound is returned when a thread id has no record.
var ErrThreadNotFound = errors.New("thread not found")

// InsertThread persists a new thread. The UNIQUE(session_name) constraint makes
// a duplicate session a loud error rather than a silent clobber.
func (s *Store) InsertThread(t api.Thread) error {
	tags, err := json.Marshal(t.Tags)
	if err != nil {
		return err
	}
	// The headless COLUMN is deprecated (the unified model infers headless/headful
	// from runtime); kept in the schema for compatibility, always written 0, never read.
	headless := 0
	started := 0
	if t.HeadlessStarted {
		started = 1
	}
	_, err = s.db.Exec(
		`INSERT INTO threads (id, machine, session_name, cwd, agent_kind, name, tags, headless, created_at, agent_session_id, headless_started, parent, notify, meta, model, on_hold_until, hold_release_until, archived_at, pin_order, flagged, flag_reason, flag_disabled)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Machine, t.SessionName, t.Cwd, t.AgentKind, t.Name, string(tags), headless, t.CreatedAtUnix, t.AgentSessionID, started, t.Parent, boolInt(t.Notify), metaJSON(t.Meta), t.Model, t.OnHoldUntilUnix, t.HoldReleaseUntilUnix, t.ArchivedAtUnix, t.PinOrder, boolInt(t.Flagged), t.FlagReason, boolInt(t.FlagDisabled),
	)
	if err != nil {
		return fmt.Errorf("store: insert thread: %w", err)
	}
	return nil
}

// GetThread returns a thread by id, or ErrThreadNotFound.
func (s *Store) GetThread(id string) (api.Thread, error) {
	row := s.db.QueryRow(
		`SELECT id, machine, session_name, cwd, agent_kind, name, tags, headless, created_at, agent_session_id, headless_started, archived, parent, notify, meta, model, on_hold_until, hold_release_until, archived_at, pin_order, flagged, flag_reason, flag_disabled
		 FROM threads WHERE id = ?`, id)
	t, err := scanThread(row)
	if errors.Is(err, sql.ErrNoRows) {
		return api.Thread{}, ErrThreadNotFound
	}
	return t, err
}

// ListThreads returns this machine's threads, newest first. Archived threads are
// excluded unless includeArchived is set (the active list hides them).
func (s *Store) ListThreads(includeArchived bool) ([]api.Thread, error) {
	q := `SELECT id, machine, session_name, cwd, agent_kind, name, tags, headless, created_at, agent_session_id, headless_started, archived, parent, notify, meta, model, on_hold_until, hold_release_until, archived_at, pin_order, flagged, flag_reason, flag_disabled
		 FROM threads`
	if !includeArchived {
		q += ` WHERE archived = 0`
	}
	q += ` ORDER BY created_at DESC, id`
	rows, err := s.db.Query(q)
	if err != nil {
		return nil, fmt.Errorf("store: list threads: %w", err)
	}
	defer rows.Close()
	var out []api.Thread
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteThread removes a thread record. Returns ErrThreadNotFound if absent.
// Its children are PROMOTED to the deleted thread's own parent (the grandparent;
// ” = root) in the same transaction — a parent id must never dangle (a dangling
// id silently renders children as roots and can never be cleaned up later, since
// the grandparent is unknowable once the row is gone).
func (s *Store) DeleteThread(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: delete thread: begin: %w", err)
	}
	defer tx.Rollback()
	var parent string
	if err := tx.QueryRow(`SELECT parent FROM threads WHERE id = ?`, id).Scan(&parent); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrThreadNotFound
		}
		return fmt.Errorf("store: delete thread: read parent: %w", err)
	}
	if _, err := tx.Exec(`UPDATE threads SET parent = ? WHERE parent = ?`, parent, id); err != nil {
		return fmt.Errorf("store: delete thread: promote children: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM threads WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: delete thread: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: delete thread: commit: %w", err)
	}
	return nil
}

// RealizeThread converts a VIRTUAL thread into a real one in place: agent kind,
// pre-minted agent session id (pi/claude; ” for codex, which mints its own on
// the first turn), a definite cwd, the headless-style session name (so a later
// headful revival mints a real session name, exactly like a fresh headless
// thread), and an optional pinned model. The WHERE clause requires the row to
// still be virtual, so two concurrent realizes cannot double-convert (the loser
// reads back the row and reports what it actually is). Returns ErrThreadNotFound
// when the row is missing OR no longer virtual — the caller distinguishes by
// re-reading the record.
func (s *Store) RealizeThread(id, agentKind, agentSessionID, cwd, sessionName, model string) error {
	res, err := s.db.Exec(
		`UPDATE threads SET agent_kind = ?, agent_session_id = ?, cwd = ?, session_name = ?, model = ?
		 WHERE id = ? AND agent_kind = ?`,
		agentKind, agentSessionID, cwd, sessionName, model, id, api.VirtualAgentKind,
	)
	if err != nil {
		return fmt.Errorf("store: realize thread: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrThreadNotFound
	}
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

// SetHeadlessSession records the agent session id and marks the headless thread
// as started (after its first turn). codex discovers its id on the first turn;
// pi/claude keep the pre-assigned one.
func (s *Store) SetHeadlessSession(id, agentSessionID string) error {
	res, err := s.db.Exec(
		`UPDATE threads SET agent_session_id = ?, headless_started = 1 WHERE id = ?`,
		agentSessionID, id)
	if err != nil {
		return fmt.Errorf("store: set headless session: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrThreadNotFound
	}
	return nil
}

// RenameThread updates a thread's display name.
func (s *Store) RenameThread(id, name string) error {
	return s.updateThread(`UPDATE threads SET name = ? WHERE id = ?`, name, id)
}

// SetThreadTags replaces a thread's tag set.
func (s *Store) SetThreadTags(id string, tags []string) error {
	if tags == nil {
		tags = []string{}
	}
	b, err := json.Marshal(tags)
	if err != nil {
		return err
	}
	return s.updateThread(`UPDATE threads SET tags = ? WHERE id = ?`, string(b), id)
}

// SetThreadArchived parks/unparks a thread (record kept). archived_at tracks when
// the thread was most recently archived: the caller passes the current unix time as
// `now`, and the CASE stamps it on the archive transition, PRESERVES an existing
// non-zero value across an idempotent re-archive, and clears it to 0 on un-archive
// (so the next archive re-stamps a fresh time). The store never calls time.Now — the
// daemon owns the clock, mirroring closed_at on tickets.
func (s *Store) SetThreadArchived(id string, archived bool, now int64) error {
	v := boolInt(archived)
	// Archiving also clears any manual ordering (pin_order → NULL): a thread loses its
	// pinned position when parked. Un-archiving leaves it cleared (not restored).
	res, err := s.db.Exec(
		`UPDATE threads SET archived = ?, archived_at = CASE
			WHEN ? = 1 THEN (CASE WHEN archived_at = 0 THEN ? ELSE archived_at END)
			ELSE 0 END,
		 pin_order = CASE WHEN ? = 1 THEN NULL ELSE pin_order END
		 WHERE id = ?`,
		v, v, now, v, id)
	if err != nil {
		return fmt.Errorf("store: set thread archived: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrThreadNotFound
	}
	return nil
}

// SetThreadHold writes a thread's hold state. BOTH columns are written in one
// statement, so the three states are structurally exclusive and no caller can
// leave a thread both held and released: hold = (until, 0), release = (0,
// until), clear = (0, 0). Deadlines are absolute instants — "on hold right now"
// / "released right now" are derived live against the owning daemon's clock, so
// a past instant stores fine and simply reads as lapsed.
func (s *Store) SetThreadHold(id string, onHoldUntilUnix, holdReleaseUntilUnix int64) error {
	return s.updateThread(`UPDATE threads SET on_hold_until = ?, hold_release_until = ? WHERE id = ?`,
		onHoldUntilUnix, holdReleaseUntilUnix, id)
}

// SetThreadAgentSession records a headed thread's captured agent session id (used
// by resume; for codex it is discovered after the first turn).
func (s *Store) SetThreadAgentSession(id, agentSessionID string) error {
	return s.updateThread(`UPDATE threads SET agent_session_id = ? WHERE id = ?`, agentSessionID, id)
}

// SetThreadSessionName records the tmux session name a thread's runtime actually
// landed on. Used by shell threads, whose session name is DESCRIPTIVE (identity
// is the @sesh-shell-id marker), so a name collision is resolved by suffixing and
// the record must be corrected to the name that was really used.
func (s *Store) SetThreadSessionName(id, sessionName string) error {
	return s.updateThread(`UPDATE threads SET session_name = ? WHERE id = ?`, sessionName, id)
}

// SetThreadHeaded flips a headless thread to headed and gives it a real tmux session
// name (used by `thread headful` promotion).
func (s *Store) SetThreadHeaded(id, sessionName string) error {
	res, err := s.db.Exec(`UPDATE threads SET headless = 0, session_name = ? WHERE id = ?`, sessionName, id)
	if err != nil {
		return fmt.Errorf("store: set thread headed: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrThreadNotFound
	}
	return nil
}

// updateThread runs a single-row UPDATE and turns "matched nothing" into
// ErrThreadNotFound. Args are the query's placeholders in order, so the thread id
// is simply the LAST one (variadic because a write may set more than one column —
// SetThreadHold writes hold and release together to keep them exclusive).
func (s *Store) updateThread(query string, args ...any) error {
	res, err := s.db.Exec(query, args...)
	if err != nil {
		return fmt.Errorf("store: update thread: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrThreadNotFound
	}
	return nil
}

func scanThread(r scanner) (api.Thread, error) {
	var t api.Thread
	var tags string
	var headless, started, archived, notify, flagged, flagDisabled int
	var meta string
	var pinOrder sql.NullFloat64
	if err := r.Scan(&t.ID, &t.Machine, &t.SessionName, &t.Cwd, &t.AgentKind, &t.Name, &tags, &headless, &t.CreatedAtUnix, &t.AgentSessionID, &started, &archived, &t.Parent, &notify, &meta, &t.Model, &t.OnHoldUntilUnix, &t.HoldReleaseUntilUnix, &t.ArchivedAtUnix, &pinOrder, &flagged, &t.FlagReason, &flagDisabled); err != nil {
		return t, err
	}
	t.Archived = archived == 1
	t.Flagged = flagged == 1
	t.FlagDisabled = flagDisabled == 1
	if pinOrder.Valid {
		v := pinOrder.Float64
		t.PinOrder = &v
	}
	if err := json.Unmarshal([]byte(tags), &t.Tags); err != nil {
		return t, fmt.Errorf("store: decode thread tags: %w", err)
	}
	_ = headless // deprecated column (see InsertThread); headless-ness is runtime-inferred
	t.HeadlessStarted = started == 1
	t.Notify = notify == 1
	if meta != "" {
		if err := json.Unmarshal([]byte(meta), &t.Meta); err != nil {
			return t, fmt.Errorf("store: decode thread meta: %w", err)
		}
	}
	return t, nil
}

// SetThreadParent re-parents a thread (” = make it a root). Existence and
// cycle guards live at the daemon layer. Reparenting UNDER another thread (a
// non-empty parent) also clears any manual ordering (pin_order → NULL): only
// top-level threads can be pinned, so a thread that becomes a child loses its
// pinned position. Reparent-to-root leaves pin_order untouched (already NULL).
func (s *Store) SetThreadParent(id, parent string) error {
	res, err := s.db.Exec(
		`UPDATE threads SET parent = ?, pin_order = CASE WHEN ? != '' THEN NULL ELSE pin_order END WHERE id = ?`,
		parent, parent, id)
	if err != nil {
		return fmt.Errorf("store: set parent: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrThreadNotFound
	}
	return nil
}

// SetThreadPin sets (order non-nil) or clears (order nil) a thread's manual-ordering
// key. The daemon supplies the absolute float; the fractional math is client-side.
func (s *Store) SetThreadPin(id string, order *float64) error {
	res, err := s.db.Exec(`UPDATE threads SET pin_order = ? WHERE id = ?`, order, id)
	if err != nil {
		return fmt.Errorf("store: set pin: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrThreadNotFound
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// SetThreadNotify persists a thread's notification toggle.
func (s *Store) SetThreadNotify(id string, on bool) error {
	res, err := s.db.Exec(`UPDATE threads SET notify = ? WHERE id = ?`, boolInt(on), id)
	if err != nil {
		return fmt.Errorf("store: set notify: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrThreadNotFound
	}
	return nil
}

func metaJSON(m map[string]string) string {
	if len(m) == 0 {
		return "{}"
	}
	b, _ := json.Marshal(m) //nolint:errcheck — map[string]string cannot fail
	return string(b)
}

// SetThreadMetaKey sets (or, with value "", deletes) one meta key.
func (s *Store) SetThreadMetaKey(id, key, value string) error {
	th, err := s.GetThread(id)
	if err != nil {
		return err
	}
	if th.Meta == nil {
		th.Meta = map[string]string{}
	}
	if value == "" {
		delete(th.Meta, key)
	} else {
		th.Meta[key] = value
	}
	res, err := s.db.Exec(`UPDATE threads SET meta = ? WHERE id = ?`, metaJSON(th.Meta), id)
	if err != nil {
		return fmt.Errorf("store: set meta: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrThreadNotFound
	}
	return nil
}

// SetThreadFlagAction applies one manual flag action atomically (see
// api.FlagThreadRequest): "on" flags AND re-enables a flag-disabled thread
// (one rule, no auto-vs-manual provenance); "off" clears flag + reason (flags
// never auto-clear); "disable" suppresses auto-flagging and clears any
// current flag; "enable" re-allows auto-flagging. reason is stored only for
// "on" (an optional note; auto-flags carry their trigger's reason instead).
func (s *Store) SetThreadFlagAction(id, action, reason string) error {
	var q string
	args := []any{id}
	switch action {
	case "on":
		q = `UPDATE threads SET flagged = 1, flag_reason = ?, flag_disabled = 0 WHERE id = ?`
		args = []any{reason, id}
	case "off":
		q = `UPDATE threads SET flagged = 0, flag_reason = '' WHERE id = ?`
	case "disable":
		q = `UPDATE threads SET flag_disabled = 1, flagged = 0, flag_reason = '' WHERE id = ?`
	case "enable":
		q = `UPDATE threads SET flag_disabled = 0 WHERE id = ?`
	default:
		return fmt.Errorf("store: unknown flag action %q", action)
	}
	res, err := s.db.Exec(q, args...)
	if err != nil {
		return fmt.Errorf("store: flag %s: %w", action, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrThreadNotFound
	}
	return nil
}

// AutoFlag sets the flag from an automatic trigger (turn end / question
// stall). The flag-disabled and already-flagged guards live IN the SQL so
// check-and-set is atomic; returns whether the flag was newly set (false for
// disabled/already-flagged — the caller then emits nothing). An unknown id
// also returns false: the thread may have been deleted mid-tick, which is a
// benign race, not an error.
func (s *Store) AutoFlag(id, reason string) (bool, error) {
	res, err := s.db.Exec(
		`UPDATE threads SET flagged = 1, flag_reason = ? WHERE id = ? AND flag_disabled = 0 AND flagged = 0`,
		reason, id)
	if err != nil {
		return false, fmt.Errorf("store: auto-flag: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
