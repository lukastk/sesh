package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lukastk/sesh/internal/api"
)

// ErrScheduleNotFound is returned for an unknown schedule id.
var ErrScheduleNotFound = errors.New("schedule not found")

const scheduleCols = `id, name, action, spec, tz, catchup, not_before, not_after, max_fires,
	thread_id, text, agent, cwd, prompt, model, spawn_mode, headless, into_session, parent_id, name_template, rules,
	next_fire, last_fire, last_outcome, fire_count, skip_count, fail_streak, missed, last_run_thread, created_at,
	state, state_reason, state_at`

// scheduleWriteCols adds the two SUPERSEDED columns every write still fills:
// `enabled` mirrors state == active and `disabled_reason` mirrors state_reason,
// so a daemon rolled back to a pre-54 binary still reads a coherent store
// instead of treating every schedule as enabled. Nothing READS them any more —
// state is the single authority — and they are absent from scheduleCols for
// exactly that reason.
const scheduleWriteCols = scheduleCols + `, enabled, disabled_reason`

// InsertSchedule persists a new schedule (state fields included — the caller
// computes next_fire; the store never reads the clock).
func (s *Store) InsertSchedule(sc api.Schedule) error {
	rules, err := json.Marshal(sc.Rules)
	if err != nil {
		return fmt.Errorf("store: insert schedule: rules: %w", err)
	}
	_, err = s.db.Exec(`INSERT INTO schedules (`+scheduleWriteCols+`)
		VALUES (?,?,?,?,?,?,?,?,?, ?,?,?,?,?,?,?,?,?,?,?,?, ?,?,?,?,?,?,?,?,?, ?,?,?, ?,?)`,
		sc.ID, sc.Name, sc.Action, sc.Spec, sc.TZ, sc.Catchup, sc.NotBeforeUnix, sc.NotAfterUnix, sc.MaxFires,
		sc.ThreadID, sc.Text, sc.Agent, sc.Cwd, sc.Prompt, sc.Model, sc.SpawnMode, sc.Headless, sc.IntoSession, sc.ParentID, sc.NameTemplate, string(rules),
		sc.NextFireUnix, sc.LastFireUnix, sc.LastOutcome, sc.FireCount, sc.SkipCount, sc.FailStreak, sc.Missed, sc.LastRunThreadID, sc.CreatedAtUnix,
		sc.State, sc.StateReason, sc.StateAtUnix,
		boolInt(sc.State == api.ScheduleActive), sc.StateReason)
	if err != nil {
		return fmt.Errorf("store: insert schedule: %w", err)
	}
	return nil
}

// UpdateSchedule rewrites a schedule's definition AND state (the whole row but
// id/created_at). Used by edit/pause/resume and by the engine's state writes.
func (s *Store) UpdateSchedule(sc api.Schedule) error {
	rules, err := json.Marshal(sc.Rules)
	if err != nil {
		return fmt.Errorf("store: update schedule: rules: %w", err)
	}
	res, err := s.db.Exec(`UPDATE schedules SET
		name=?, action=?, spec=?, tz=?, catchup=?, not_before=?, not_after=?, max_fires=?,
		thread_id=?, text=?, agent=?, cwd=?, prompt=?, model=?, spawn_mode=?, headless=?, into_session=?, parent_id=?, name_template=?, rules=?,
		next_fire=?, last_fire=?, last_outcome=?, fire_count=?, skip_count=?, fail_streak=?, missed=?, last_run_thread=?,
		state=?, state_reason=?, state_at=?, enabled=?, disabled_reason=?
		WHERE id = ?`,
		sc.Name, sc.Action, sc.Spec, sc.TZ, sc.Catchup, sc.NotBeforeUnix, sc.NotAfterUnix, sc.MaxFires,
		sc.ThreadID, sc.Text, sc.Agent, sc.Cwd, sc.Prompt, sc.Model, sc.SpawnMode, sc.Headless, sc.IntoSession, sc.ParentID, sc.NameTemplate, string(rules),
		sc.NextFireUnix, sc.LastFireUnix, sc.LastOutcome, sc.FireCount, sc.SkipCount, sc.FailStreak, sc.Missed, sc.LastRunThreadID,
		sc.State, sc.StateReason, sc.StateAtUnix, boolInt(sc.State == api.ScheduleActive), sc.StateReason,
		sc.ID)
	if err != nil {
		return fmt.Errorf("store: update schedule: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrScheduleNotFound
	}
	return nil
}

// GetSchedule returns one schedule, or ErrScheduleNotFound.
func (s *Store) GetSchedule(id string) (api.Schedule, error) {
	row := s.db.QueryRow(`SELECT `+scheduleCols+` FROM schedules WHERE id = ?`, id)
	sc, err := scanSchedule(row)
	if errors.Is(err, sql.ErrNoRows) {
		return api.Schedule{}, ErrScheduleNotFound
	}
	return sc, err
}

// ListSchedules returns every schedule, oldest first (creation order is the
// stable listing order; ids break ties).
func (s *Store) ListSchedules() ([]api.Schedule, error) {
	rows, err := s.db.Query(`SELECT ` + scheduleCols + ` FROM schedules ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("store: list schedules: %w", err)
	}
	defer rows.Close()
	return scanSchedules(rows)
}

// ListSchedulesActive returns only the schedules that can fire — the
// scheduler's reload query. Filtering in SQL rather than loading every row and
// discarding in Go is what makes an accumulated history of spent records cost
// the firing loop nothing (and the rev triggers reload on every fire).
func (s *Store) ListSchedulesActive() ([]api.Schedule, error) {
	rows, err := s.db.Query(`SELECT `+scheduleCols+` FROM schedules WHERE state = ? ORDER BY created_at, id`, api.ScheduleActive)
	if err != nil {
		return nil, fmt.Errorf("store: list active schedules: %w", err)
	}
	defer rows.Close()
	return scanSchedules(rows)
}

// CountSchedulesByState is the per-state census behind `schedule list`'s
// footer: a default listing hides the inert states, and saying how many it hid
// is what keeps the hiding honest.
func (s *Store) CountSchedulesByState() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT state, count(*) FROM schedules GROUP BY state`)
	if err != nil {
		return nil, fmt.Errorf("store: count schedules by state: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			return nil, err
		}
		out[state] = n
	}
	return out, rows.Err()
}

// SetSchedulesState moves a set of schedules into one state in a single
// transaction — the thread archive/unarchive cascade (§9.4). next_fire is the
// caller's business: it passes nextFire per id (nil = leave each alone), since
// only the daemon can parse a spec.
func (s *Store) SetSchedulesState(ids []string, state, reason string, now int64, nextFire map[string]int64) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: set schedules state: begin: %w", err)
	}
	defer tx.Rollback()
	enabled := boolInt(state == api.ScheduleActive)
	for _, id := range ids {
		if nf, ok := nextFire[id]; ok {
			_, err = tx.Exec(`UPDATE schedules SET state=?, state_reason=?, state_at=?, enabled=?, disabled_reason=?, next_fire=? WHERE id=?`,
				state, reason, now, enabled, reason, nf, id)
		} else {
			_, err = tx.Exec(`UPDATE schedules SET state=?, state_reason=?, state_at=?, enabled=?, disabled_reason=? WHERE id=?`,
				state, reason, now, enabled, reason, id)
		}
		if err != nil {
			return fmt.Errorf("store: set schedule %s state: %w", id, err)
		}
	}
	return tx.Commit()
}

// ListSchedulesByThread returns the message schedules targeting a thread.
func (s *Store) ListSchedulesByThread(threadID string) ([]api.Schedule, error) {
	rows, err := s.db.Query(`SELECT `+scheduleCols+` FROM schedules WHERE thread_id = ? ORDER BY created_at, id`, threadID)
	if err != nil {
		return nil, fmt.Errorf("store: list schedules by thread: %w", err)
	}
	defer rows.Close()
	return scanSchedules(rows)
}

// DeleteSchedule removes a schedule and its run history. Unknown id = loud.
func (s *Store) DeleteSchedule(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: delete schedule: begin: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM schedules WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete schedule: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrScheduleNotFound
	}
	if _, err := tx.Exec(`DELETE FROM schedule_runs WHERE schedule_id = ?`, id); err != nil {
		return fmt.Errorf("store: delete schedule runs: %w", err)
	}
	return tx.Commit()
}

// SchedulesRev is the trigger-bumped revision of the schedules table — the
// scheduler's cache gate (one integer read per tick when nothing changed).
func (s *Store) SchedulesRev() (int64, error) {
	var rev int64
	if err := s.db.QueryRow(`SELECT rev FROM revs WHERE name = 'schedules'`).Scan(&rev); err != nil {
		return 0, fmt.Errorf("store: schedules rev: %w", err)
	}
	return rev, nil
}

// --- runs ---

// InsertScheduleRun records one fire. (schedule_id, fired_at) is the key, so a
// caller firing twice inside one second must pass distinct instants.
func (s *Store) InsertScheduleRun(r api.ScheduleRun) error {
	_, err := s.db.Exec(`INSERT INTO schedule_runs (schedule_id, fired_at, thread_id, outcome, detail, ended_at) VALUES (?,?,?,?,?,?)`,
		r.ScheduleID, r.FiredAtUnix, r.ThreadID, r.Outcome, r.Detail, r.EndedAtUnix)
	if err != nil {
		return fmt.Errorf("store: insert schedule run: %w", err)
	}
	return nil
}

// UpdateScheduleRun rewrites a run's outcome/detail/ended_at (the spawn reaper's
// completion write). Unknown key = loud.
func (s *Store) UpdateScheduleRun(r api.ScheduleRun) error {
	res, err := s.db.Exec(`UPDATE schedule_runs SET thread_id=?, outcome=?, detail=?, ended_at=? WHERE schedule_id=? AND fired_at=?`,
		r.ThreadID, r.Outcome, r.Detail, r.EndedAtUnix, r.ScheduleID, r.FiredAtUnix)
	if err != nil {
		return fmt.Errorf("store: update schedule run: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("store: schedule run %s@%d not found", r.ScheduleID, r.FiredAtUnix)
	}
	return nil
}

// SetScheduleRunOutcome stamps a run's outcome (and thread id) leaving its
// detail/ended_at alone — the reaper owns those. Returns false when no such
// run row exists yet.
func (s *Store) SetScheduleRunOutcome(scheduleID string, firedAt int64, outcome, threadID string) (bool, error) {
	res, err := s.db.Exec(`UPDATE schedule_runs SET outcome = ?, thread_id = CASE WHEN ? = '' THEN thread_id ELSE ? END WHERE schedule_id = ? AND fired_at = ?`,
		outcome, threadID, threadID, scheduleID, firedAt)
	if err != nil {
		return false, fmt.Errorf("store: set schedule run outcome: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ListScheduleRuns returns a schedule's runs, newest first, at most limit
// (0 = all).
func (s *Store) ListScheduleRuns(scheduleID string, limit int) ([]api.ScheduleRun, error) {
	q := `SELECT schedule_id, fired_at, thread_id, outcome, detail, ended_at FROM schedule_runs WHERE schedule_id = ? ORDER BY fired_at DESC`
	args := []any{scheduleID}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list schedule runs: %w", err)
	}
	defer rows.Close()
	return scanRuns(rows)
}

// OpenScheduleRuns returns every spawn run whose thread has not been reaped
// yet (ended_at = 0 with a thread) — the reaper's seed at daemon start.
func (s *Store) OpenScheduleRuns() ([]api.ScheduleRun, error) {
	rows, err := s.db.Query(`SELECT schedule_id, fired_at, thread_id, outcome, detail, ended_at FROM schedule_runs WHERE ended_at = 0 AND thread_id != '' ORDER BY fired_at`)
	if err != nil {
		return nil, fmt.Errorf("store: open schedule runs: %w", err)
	}
	defer rows.Close()
	return scanRuns(rows)
}

// PruneScheduleRuns keeps the newest `keep` runs of a schedule.
func (s *Store) PruneScheduleRuns(scheduleID string, keep int) error {
	_, err := s.db.Exec(`DELETE FROM schedule_runs WHERE schedule_id = ? AND fired_at NOT IN
		(SELECT fired_at FROM schedule_runs WHERE schedule_id = ? ORDER BY fired_at DESC LIMIT ?)`, scheduleID, scheduleID, keep)
	if err != nil {
		return fmt.Errorf("store: prune schedule runs: %w", err)
	}
	return nil
}

// --- scanning ---

type scheduleScanner interface {
	Scan(dest ...any) error
}

func scanSchedule(r scheduleScanner) (api.Schedule, error) {
	var sc api.Schedule
	var rules string
	var headless int
	err := r.Scan(&sc.ID, &sc.Name, &sc.Action, &sc.Spec, &sc.TZ, &sc.Catchup, &sc.NotBeforeUnix, &sc.NotAfterUnix, &sc.MaxFires,
		&sc.ThreadID, &sc.Text, &sc.Agent, &sc.Cwd, &sc.Prompt, &sc.Model, &sc.SpawnMode, &headless, &sc.IntoSession, &sc.ParentID, &sc.NameTemplate, &rules,
		&sc.NextFireUnix, &sc.LastFireUnix, &sc.LastOutcome, &sc.FireCount, &sc.SkipCount, &sc.FailStreak, &sc.Missed, &sc.LastRunThreadID, &sc.CreatedAtUnix,
		&sc.State, &sc.StateReason, &sc.StateAtUnix)
	if err != nil {
		return api.Schedule{}, err
	}
	sc.Headless = headless != 0
	if !api.ScheduleStateKnown(sc.State) {
		// Unreachable post-migration: every row is backfilled and every write
		// goes through a validated state. An unknown value means the column was
		// written by something that is not this code — loud, not coerced.
		return api.Schedule{}, fmt.Errorf("store: schedule %s: unknown state %q", sc.ID, sc.State)
	}
	if err := json.Unmarshal([]byte(rules), &sc.Rules); err != nil {
		return api.Schedule{}, fmt.Errorf("store: schedule %s: rules column is not valid JSON: %w", sc.ID, err)
	}
	return sc, nil
}

func scanSchedules(rows *sql.Rows) ([]api.Schedule, error) {
	out := []api.Schedule{}
	for rows.Next() {
		sc, err := scanSchedule(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan schedule: %w", err)
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

func scanRuns(rows *sql.Rows) ([]api.ScheduleRun, error) {
	out := []api.ScheduleRun{}
	for rows.Next() {
		var r api.ScheduleRun
		if err := rows.Scan(&r.ScheduleID, &r.FiredAtUnix, &r.ThreadID, &r.Outcome, &r.Detail, &r.EndedAtUnix); err != nil {
			return nil, fmt.Errorf("store: scan schedule run: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ScheduleDigest is what a thread's snapshot carries about the message
// schedules targeting it: how many are enabled and the earliest next fire.
type ScheduleDigest struct {
	Count        int
	NextFireUnix int64
}

// ScheduleDigests returns the per-thread digest of ACTIVE message schedules —
// what the TUI's SCHED column counts, so a parked or finished schedule never
// shows as pending work against a thread.
func (s *Store) ScheduleDigests() (map[string]ScheduleDigest, error) {
	rows, err := s.db.Query(`SELECT thread_id, next_fire FROM schedules WHERE state = ? AND thread_id != ''`, api.ScheduleActive)
	if err != nil {
		return nil, fmt.Errorf("store: schedule digests: %w", err)
	}
	defer rows.Close()
	out := map[string]ScheduleDigest{}
	for rows.Next() {
		var id string
		var next int64
		if err := rows.Scan(&id, &next); err != nil {
			return nil, err
		}
		d := out[id]
		d.Count++
		if next > 0 && (d.NextFireUnix == 0 || next < d.NextFireUnix) {
			d.NextFireUnix = next
		}
		out[id] = d
	}
	return out, rows.Err()
}
