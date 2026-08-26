// Package store persists scrapd state in SQLite.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// Workspace is the persisted workspace record.
type Workspace struct {
	ID        string
	Project   string
	RepoURL   string
	Provider  string
	State     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

// Open creates or opens the database at path and applies migrations.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// modernc.org/sqlite dislikes concurrent writers; serialize access.
	if _, err := db.Exec(`PRAGMA journal_mode = WAL; PRAGMA busy_timeout = 5000;`); err != nil {
		db.Close()
		return nil, fmt.Errorf("configure database: %w", err)
	}

	store := &Store{db: db}
	if err := store.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS workspaces (
	id         TEXT PRIMARY KEY,
	project    TEXT NOT NULL DEFAULT '',
	repo_url   TEXT NOT NULL DEFAULT '',
	state      TEXT NOT NULL DEFAULT 'running',
	provider   TEXT NOT NULL DEFAULT 'directory',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);`)
	if err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	// Databases created before providers were selectable contain directory
	// workspaces. SQLite has no ADD COLUMN IF NOT EXISTS, so inspect first.
	rows, err := s.db.Query(`PRAGMA table_info(workspaces)`)
	if err != nil {
		return fmt.Errorf("inspect workspace schema: %w", err)
	}
	hasProvider := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == "provider" {
			hasProvider = true
		}
	}
	rows.Close()
	if !hasProvider {
		if _, err := s.db.Exec(`ALTER TABLE workspaces ADD COLUMN provider TEXT NOT NULL DEFAULT 'directory'`); err != nil {
			return fmt.Errorf("add workspace provider: %w", err)
		}
	}
	return nil
}

// CreateWorkspace inserts a workspace row. Timestamps are set when zero.
func (s *Store) CreateWorkspace(ctx context.Context, w Workspace) error {
	now := time.Now().UTC()
	if w.CreatedAt.IsZero() {
		w.CreatedAt = now
	}
	if w.UpdatedAt.IsZero() {
		w.UpdatedAt = w.CreatedAt
	}
	if w.Provider == "" {
		w.Provider = "directory"
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO workspaces (id, project, repo_url, state, provider, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?);`,
		w.ID, w.Project, w.RepoURL, w.State, w.Provider, w.CreatedAt.Unix(), w.UpdatedAt.Unix())
	if err != nil {
		return fmt.Errorf("insert workspace: %w", err)
	}
	return nil
}

// GetWorkspace loads a workspace by ID.
func (s *Store) GetWorkspace(ctx context.Context, id string) (Workspace, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, project, repo_url, state, provider, created_at, updated_at
		 FROM workspaces WHERE id = ?;`, id)
	return scanWorkspace(row)
}

// ListWorkspaces returns all workspaces ordered by creation time.
func (s *Store) ListWorkspaces(ctx context.Context) ([]Workspace, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, project, repo_url, state, provider, created_at, updated_at
		 FROM workspaces ORDER BY created_at, id;`)
	if err != nil {
		return nil, fmt.Errorf("query workspaces: %w", err)
	}
	defer rows.Close()

	var workspaces []Workspace
	for rows.Next() {
		w, err := scanWorkspace(rows)
		if err != nil {
			return nil, err
		}
		workspaces = append(workspaces, w)
	}
	return workspaces, rows.Err()
}

// AssignWorkspace atomically turns a preheated runtime into a user workspace.
func (s *Store) AssignWorkspace(ctx context.Context, id, project, repoURL string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE workspaces SET project = ?, repo_url = ?, state = 'running', updated_at = ? WHERE id = ? AND state = 'preheated'`, project, repoURL, time.Now().UTC().Unix(), id)
	if err != nil {
		return fmt.Errorf("assign workspace: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateWorkspaceState changes the persisted lifecycle state.
func (s *Store) UpdateWorkspaceState(ctx context.Context, id, state string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE workspaces SET state = ?, updated_at = ? WHERE id = ?`, state, time.Now().UTC().Unix(), id)
	if err != nil {
		return fmt.Errorf("update workspace state: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteWorkspace removes a workspace row.
func (s *Store) DeleteWorkspace(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM workspaces WHERE id = ?;`, id)
	if err != nil {
		return fmt.Errorf("delete workspace: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete workspace: %w", err)
	}
	if deleted == 0 {
		return ErrNotFound
	}
	return nil
}

// WorkspaceExists reports whether an ID is taken.
func (s *Store) WorkspaceExists(ctx context.Context, id string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM workspaces WHERE id = ?;`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("query workspace: %w", err)
	}
	return true, nil
}

type rowScanner interface{ Scan(dest ...any) error }

func scanWorkspace(row rowScanner) (Workspace, error) {
	var w Workspace
	var created, updated int64
	if err := row.Scan(&w.ID, &w.Project, &w.RepoURL, &w.State, &w.Provider, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Workspace{}, ErrNotFound
		}
		return Workspace{}, fmt.Errorf("scan workspace: %w", err)
	}
	w.CreatedAt = time.Unix(created, 0).UTC()
	w.UpdatedAt = time.Unix(updated, 0).UTC()
	return w, nil
}
