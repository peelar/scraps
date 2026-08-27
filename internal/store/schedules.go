package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Schedule is a durable, execution-agnostic clock definition.
type Schedule struct {
	ID                string
	Name              string
	CronExpression    string
	Timezone          string
	Enabled           bool
	Payload           []byte
	ConcurrencyPolicy string
	NextRunAt         *time.Time
	LastRunAt         *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Occurrence is one durable firing of a schedule.
type Occurrence struct {
	ID          string
	ScheduleID  string
	ScheduledAt time.Time
	State       string
	LeaseToken  string
	LeaseUntil  *time.Time
	Attempts    int
	Error       string
	Payload     []byte
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (s *Store) CreateSchedule(ctx context.Context, schedule Schedule) error {
	now := time.Now().UTC()
	if schedule.CreatedAt.IsZero() {
		schedule.CreatedAt = now
	}
	if schedule.UpdatedAt.IsZero() {
		schedule.UpdatedAt = schedule.CreatedAt
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO schedules
		(id, name, cron_expression, timezone, enabled, payload, concurrency_policy, next_run_at, last_run_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, schedule.ID, schedule.Name, schedule.CronExpression,
		schedule.Timezone, schedule.Enabled, string(schedule.Payload), schedule.ConcurrencyPolicy,
		nullTime(schedule.NextRunAt), nullTime(schedule.LastRunAt), schedule.CreatedAt.Unix(), schedule.UpdatedAt.Unix())
	if err != nil {
		return fmt.Errorf("insert schedule: %w", err)
	}
	return nil
}

func (s *Store) GetSchedule(ctx context.Context, id string) (Schedule, error) {
	return scanSchedule(s.db.QueryRowContext(ctx, `SELECT id, name, cron_expression, timezone, enabled, payload,
		concurrency_policy, next_run_at, last_run_at, created_at, updated_at FROM schedules WHERE id = ?`, id))
}

func (s *Store) ListSchedules(ctx context.Context) ([]Schedule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, cron_expression, timezone, enabled, payload,
		concurrency_policy, next_run_at, last_run_at, created_at, updated_at FROM schedules ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list schedules: %w", err)
	}
	defer rows.Close()
	var out []Schedule
	for rows.Next() {
		item, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) UpdateSchedule(ctx context.Context, schedule Schedule) error {
	result, err := s.db.ExecContext(ctx, `UPDATE schedules SET name = ?, cron_expression = ?, timezone = ?, enabled = ?,
		payload = ?, concurrency_policy = ?, next_run_at = ?, updated_at = ? WHERE id = ?`, schedule.Name,
		schedule.CronExpression, schedule.Timezone, schedule.Enabled, string(schedule.Payload), schedule.ConcurrencyPolicy,
		nullTime(schedule.NextRunAt), time.Now().UTC().Unix(), schedule.ID)
	if err != nil {
		return fmt.Errorf("update schedule: %w", err)
	}
	return requireAffected(result)
}

func (s *Store) DeleteSchedule(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM schedule_occurrences WHERE schedule_id = ?`, id); err != nil {
		return fmt.Errorf("delete schedule occurrences: %w", err)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM schedules WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete schedule: %w", err)
	}
	if err := requireAffected(result); err != nil {
		return err
	}
	return tx.Commit()
}

// DueSchedules returns enabled schedules whose next firing is due.
func (s *Store) DueSchedules(ctx context.Context, now time.Time, limit int) ([]Schedule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, cron_expression, timezone, enabled, payload,
		concurrency_policy, next_run_at, last_run_at, created_at, updated_at FROM schedules
		WHERE enabled = 1 AND next_run_at IS NOT NULL AND next_run_at <= ? ORDER BY next_run_at, id LIMIT ?`, now.Unix(), limit)
	if err != nil {
		return nil, fmt.Errorf("list due schedules: %w", err)
	}
	defer rows.Close()
	var out []Schedule
	for rows.Next() {
		item, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// FireSchedule atomically advances a due schedule and records its occurrence.
func (s *Store) FireSchedule(ctx context.Context, schedule Schedule, occurrenceID string, next time.Time) error {
	if schedule.NextRunAt == nil {
		return ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE schedules SET next_run_at = ?, last_run_at = ?, updated_at = ?
		WHERE id = ? AND enabled = 1 AND next_run_at = ?`, next.Unix(), schedule.NextRunAt.Unix(), time.Now().UTC().Unix(),
		schedule.ID, schedule.NextRunAt.Unix())
	if err != nil {
		return fmt.Errorf("advance schedule: %w", err)
	}
	if err := requireAffected(result); err != nil {
		return ErrConflict
	}
	state := "pending"
	if schedule.ConcurrencyPolicy == "skip" {
		var active int
		err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM schedule_occurrences WHERE schedule_id = ?
			AND state IN ('pending', 'leased')`, schedule.ID).Scan(&active)
		if err != nil {
			return fmt.Errorf("check active occurrence: %w", err)
		}
		if active > 0 {
			state = "skipped"
		}
	}
	now := time.Now().UTC().Unix()
	_, err = tx.ExecContext(ctx, `INSERT INTO schedule_occurrences
		(id, schedule_id, scheduled_at, state, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		occurrenceID, schedule.ID, schedule.NextRunAt.Unix(), state, now, now)
	if err != nil {
		return fmt.Errorf("insert schedule occurrence: %w", err)
	}
	return tx.Commit()
}

// ClaimOccurrence leases the oldest pending or expired occurrence.
func (s *Store) ClaimOccurrence(ctx context.Context, now time.Time, leaseUntil time.Time, leaseToken string) (Occurrence, error) {
	row := s.db.QueryRowContext(ctx, `UPDATE schedule_occurrences SET state = 'leased', lease_token = ?, lease_until = ?,
		attempts = attempts + 1, updated_at = ? WHERE id = (
			SELECT id FROM schedule_occurrences WHERE state = 'pending' OR (state = 'leased' AND lease_until <= ?)
			ORDER BY scheduled_at, id LIMIT 1
		) RETURNING id, schedule_id, scheduled_at, state, lease_token, lease_until, attempts, error,
		(SELECT payload FROM schedules WHERE schedules.id = schedule_occurrences.schedule_id), created_at, updated_at`,
		leaseToken, leaseUntil.Unix(), now.Unix(), now.Unix())
	return scanOccurrence(row)
}

func (s *Store) RenewOccurrence(ctx context.Context, id, leaseToken string, leaseUntil time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE schedule_occurrences SET lease_until = ?, updated_at = ?
		WHERE id = ? AND state = 'leased' AND lease_token = ?`, leaseUntil.Unix(), time.Now().UTC().Unix(), id, leaseToken)
	if err != nil {
		return fmt.Errorf("renew occurrence: %w", err)
	}
	if err := requireAffected(result); err != nil {
		exists, getErr := s.occurrenceExists(ctx, id)
		if getErr != nil {
			return getErr
		}
		if exists {
			return ErrConflict
		}
		return ErrNotFound
	}
	return nil
}

func (s *Store) CompleteOccurrence(ctx context.Context, id, leaseToken, state, message string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE schedule_occurrences SET state = ?, error = ?, lease_token = '',
		lease_until = NULL, updated_at = ? WHERE id = ? AND state = 'leased' AND lease_token = ?`,
		state, message, time.Now().UTC().Unix(), id, leaseToken)
	if err != nil {
		return fmt.Errorf("complete occurrence: %w", err)
	}
	if err := requireAffected(result); err != nil {
		exists, getErr := s.occurrenceExists(ctx, id)
		if getErr != nil {
			return getErr
		}
		if exists {
			return ErrConflict
		}
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListOccurrences(ctx context.Context, scheduleID string, limit int) ([]Occurrence, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `SELECT o.id, o.schedule_id, o.scheduled_at, o.state, o.lease_token, o.lease_until, o.attempts, o.error,
		s.payload, o.created_at, o.updated_at FROM schedule_occurrences o JOIN schedules s ON s.id = o.schedule_id`
	args := []any{}
	if scheduleID != "" {
		query += ` WHERE o.schedule_id = ?`
		args = append(args, scheduleID)
	}
	query += ` ORDER BY o.scheduled_at DESC, o.id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list occurrences: %w", err)
	}
	defer rows.Close()
	var out []Occurrence
	for rows.Next() {
		item, err := scanOccurrence(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) occurrenceExists(ctx context.Context, id string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM schedule_occurrences WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func scanSchedule(row rowScanner) (Schedule, error) {
	var item Schedule
	var enabled int
	var payload string
	var next, last sql.NullInt64
	var created, updated int64
	if err := row.Scan(&item.ID, &item.Name, &item.CronExpression, &item.Timezone, &enabled, &payload,
		&item.ConcurrencyPolicy, &next, &last, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Schedule{}, ErrNotFound
		}
		return Schedule{}, fmt.Errorf("scan schedule: %w", err)
	}
	item.Enabled = enabled != 0
	item.Payload = []byte(payload)
	item.NextRunAt = scanNullTime(next)
	item.LastRunAt = scanNullTime(last)
	item.CreatedAt = time.Unix(created, 0).UTC()
	item.UpdatedAt = time.Unix(updated, 0).UTC()
	return item, nil
}

func scanOccurrence(row rowScanner) (Occurrence, error) {
	var item Occurrence
	var lease sql.NullInt64
	var payload string
	var scheduled, created, updated int64
	if err := row.Scan(&item.ID, &item.ScheduleID, &scheduled, &item.State, &item.LeaseToken, &lease,
		&item.Attempts, &item.Error, &payload, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Occurrence{}, ErrNotFound
		}
		return Occurrence{}, fmt.Errorf("scan occurrence: %w", err)
	}
	item.ScheduledAt = time.Unix(scheduled, 0).UTC()
	item.LeaseUntil = scanNullTime(lease)
	item.Payload = []byte(payload)
	item.CreatedAt = time.Unix(created, 0).UTC()
	item.UpdatedAt = time.Unix(updated, 0).UTC()
	return item, nil
}

func nullTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Unix()
}

func scanNullTime(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	parsed := time.Unix(value.Int64, 0).UTC()
	return &parsed
}

func requireAffected(result sql.Result) error {
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}
