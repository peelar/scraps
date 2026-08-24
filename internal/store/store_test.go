package store

import (
	"context"
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
