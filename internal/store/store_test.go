package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "scrapd.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestOpenSerializesSQLiteAccess(t *testing.T) {
	store := openTestStore(t)
	if got := store.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("max open connections = %d, want 1", got)
	}
}

func TestMigratesRunsWithoutSessionSnapshots(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old-runs.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE workspaces (id TEXT PRIMARY KEY, project TEXT NOT NULL DEFAULT '', repo_url TEXT NOT NULL DEFAULT '', state TEXT NOT NULL DEFAULT 'running', provider TEXT NOT NULL DEFAULT 'directory', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
		CREATE TABLE runs (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), session_key TEXT NOT NULL, prompt TEXT NOT NULL, state TEXT NOT NULL DEFAULT 'queued', error TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, started_at INTEGER, finished_at INTEGER, updated_at INTEGER NOT NULL);
		INSERT INTO workspaces (id, state, created_at, updated_at) VALUES ('ws', 'running', 1, 1);
		INSERT INTO runs (id, workspace_id, session_key, prompt, state, created_at, updated_at) VALUES ('run', 'ws', 'session', 'prompt', 'failed', 1, 1);
	`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	run, err := st.GetRun(context.Background(), "run")
	if err != nil {
		t.Fatal(err)
	}
	if string(run.SessionSnapshot) != "[]" {
		t.Fatalf("migrated session snapshot = %q, want []", run.SessionSnapshot)
	}
}

func TestWorkspaceRoundTrip(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	created := Workspace{ID: "quiet-river", Project: "owner/repo", RepoURL: "https://example.com/repo.git", State: "running"}
	if err := store.CreateWorkspace(ctx, created); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := store.GetWorkspace(ctx, "quiet-river")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != created.ID || got.Project != created.Project || got.RepoURL != created.RepoURL || got.State != created.State {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	if time.Since(got.CreatedAt) > time.Minute {
		t.Fatalf("created at = %v, want recent", got.CreatedAt)
	}
}

func TestMigratesPreProviderWorkspacesToDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE workspaces (id TEXT PRIMARY KEY, project TEXT NOT NULL DEFAULT '', repo_url TEXT NOT NULL DEFAULT '', state TEXT NOT NULL DEFAULT 'running', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL); INSERT INTO workspaces VALUES ('old', '', '', 'running', 1, 1)`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	got, err := st.GetWorkspace(context.Background(), "old")
	if err != nil || got.Provider != "directory" {
		t.Fatalf("migrated = %+v, %v", got, err)
	}
}

func TestGetMissing(t *testing.T) {
	store := openTestStore(t)
	if _, err := store.GetWorkspace(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestListOrdersByCreation(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	for _, id := range []string{"alpha", "beta", "gamma"} {
		if err := store.CreateWorkspace(ctx, Workspace{ID: id}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	// Same-second timestamps fall back to the id tiebreaker.

	got, err := store.ListWorkspaces(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 3 || got[0].ID != "alpha" || got[2].ID != "gamma" {
		t.Fatalf("list = %+v, want alpha..gamma", got)
	}
}

func TestDelete(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if err := store.CreateWorkspace(ctx, Workspace{ID: "doomed"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.DeleteWorkspace(ctx, "doomed"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := store.DeleteWorkspace(ctx, "doomed"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete err = %v, want ErrNotFound", err)
	}
}

func TestExists(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if err := store.CreateWorkspace(ctx, Workspace{ID: "present"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if exists, _ := store.WorkspaceExists(ctx, "present"); !exists {
		t.Fatal("present workspace reported missing")
	}
	if exists, _ := store.WorkspaceExists(ctx, "absent"); exists {
		t.Fatal("absent workspace reported present")
	}
}
