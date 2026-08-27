package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Run is one durable Pi turn executed against a workspace.
type Run struct {
	ID              string
	WorkspaceID     string
	SessionKey      string
	Prompt          string
	SessionSnapshot []byte
	State           string
	Error           string
	CreatedAt       time.Time
	StartedAt       *time.Time
	FinishedAt      *time.Time
	UpdatedAt       time.Time
}

// RunEvent is an immutable JSON event emitted by a remote Pi process.
type RunEvent struct {
	Sequence  int64
	Data      []byte
	CreatedAt time.Time
}

// ReconcileInterruptedRuns marks executions orphaned by a prior daemon process.
// Pi children cannot be reattached safely after the control plane restarts.
func (s *Store) ReconcileInterruptedRuns(ctx context.Context) (int64, error) {
	now := time.Now().UTC().UnixMilli()
	result, err := s.db.ExecContext(ctx, `UPDATE runs
		SET state = 'failed', error = 'scrapd restarted while the run was active', finished_at = ?, updated_at = ?
		WHERE state IN ('queued', 'running')`, now, now)
	if err != nil {
		return 0, fmt.Errorf("reconcile interrupted runs: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count reconciled runs: %w", err)
	}
	return count, nil
}

func (s *Store) CreateRun(ctx context.Context, run Run) error {
	now := time.Now().UTC()
	if run.CreatedAt.IsZero() {
		run.CreatedAt = now
	}
	if run.UpdatedAt.IsZero() {
		run.UpdatedAt = run.CreatedAt
	}
	if run.State == "" {
		run.State = "queued"
	}
	if len(run.SessionSnapshot) == 0 {
		run.SessionSnapshot = []byte("[]")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO runs
		(id, workspace_id, session_key, prompt, session_snapshot, state, error, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, '', ?, ?)`, run.ID, run.WorkspaceID, run.SessionKey,
		run.Prompt, run.SessionSnapshot, run.State, run.CreatedAt.UnixMilli(), run.UpdatedAt.UnixMilli())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: runs.workspace_id") {
			return fmt.Errorf("active workspace run: %w", ErrConflict)
		}
		return fmt.Errorf("insert run: %w", err)
	}
	return nil
}

func (s *Store) GetRun(ctx context.Context, id string) (Run, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, session_key, prompt, session_snapshot,
		state, error, created_at, started_at, finished_at, updated_at FROM runs WHERE id = ?`, id)
	return scanRun(row)
}

func scanRun(row rowScanner) (Run, error) {
	var run Run
	var created, updated int64
	var started, finished sql.NullInt64
	if err := row.Scan(&run.ID, &run.WorkspaceID, &run.SessionKey, &run.Prompt, &run.SessionSnapshot,
		&run.State, &run.Error, &created, &started, &finished, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Run{}, ErrNotFound
		}
		return Run{}, fmt.Errorf("scan run: %w", err)
	}
	run.CreatedAt = time.UnixMilli(created).UTC()
	run.UpdatedAt = time.UnixMilli(updated).UTC()
	if started.Valid {
		value := time.UnixMilli(started.Int64).UTC()
		run.StartedAt = &value
	}
	if finished.Valid {
		value := time.UnixMilli(finished.Int64).UTC()
		run.FinishedAt = &value
	}
	return run, nil
}

func (s *Store) StartRun(ctx context.Context, id string) error {
	now := time.Now().UTC().UnixMilli()
	result, err := s.db.ExecContext(ctx, `UPDATE runs SET state = 'running', started_at = ?, updated_at = ?
		WHERE id = ? AND state = 'queued'`, now, now, id)
	if err != nil {
		return fmt.Errorf("start run: %w", err)
	}
	return requireChanged(result)
}

func (s *Store) FinishRun(ctx context.Context, id, state, message string) error {
	if state != "succeeded" && state != "failed" && state != "cancelled" {
		return fmt.Errorf("invalid terminal run state %q", state)
	}
	now := time.Now().UTC().UnixMilli()
	result, err := s.db.ExecContext(ctx, `UPDATE runs SET state = ?, error = ?, finished_at = ?, updated_at = ?
		WHERE id = ? AND state IN ('queued', 'running')`, state, message, now, now, id)
	if err != nil {
		return fmt.Errorf("finish run: %w", err)
	}
	return requireChanged(result)
}

func requireChanged(result sql.Result) error {
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return ErrConflict
	}
	return nil
}

func (s *Store) AppendRunEvent(ctx context.Context, id string, data []byte) (RunEvent, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RunEvent{}, err
	}
	defer tx.Rollback()
	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0) + 1 FROM run_events WHERE run_id = ?`, id).Scan(&sequence); err != nil {
		return RunEvent{}, fmt.Errorf("next run event: %w", err)
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_events (run_id, sequence, data, created_at) VALUES (?, ?, ?, ?)`,
		id, sequence, data, now.UnixMilli()); err != nil {
		return RunEvent{}, fmt.Errorf("insert run event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return RunEvent{}, err
	}
	return RunEvent{Sequence: sequence, Data: append([]byte(nil), data...), CreatedAt: now}, nil
}

func (s *Store) ListRunEvents(ctx context.Context, id string, after int64) ([]RunEvent, error) {
	if _, err := s.GetRun(ctx, id); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT sequence, data, created_at FROM run_events
		WHERE run_id = ? AND sequence > ? ORDER BY sequence`, id, after)
	if err != nil {
		return nil, fmt.Errorf("query run events: %w", err)
	}
	defer rows.Close()
	var events []RunEvent
	for rows.Next() {
		var event RunEvent
		var created int64
		if err := rows.Scan(&event.Sequence, &event.Data, &created); err != nil {
			return nil, err
		}
		event.CreatedAt = time.UnixMilli(created).UTC()
		events = append(events, event)
	}
	return events, rows.Err()
}
