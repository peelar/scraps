package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/peelar/scraps/internal/store"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	dataDir := t.TempDir()
	testStore, err := store.Open(filepath.Join(dataDir, "scrapd.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { testStore.Close() })
	manager, err := NewManager(testStore, dataDir)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	return manager
}

func TestGenerateName(t *testing.T) {
	name, err := GenerateName()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	parts := strings.Split(name, "-")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		t.Fatalf("name = %q, want adjective-noun", name)
	}
}

func TestCreateWithoutRepo(t *testing.T) {
	manager := newTestManager(t)

	created, err := manager.Create(context.Background(), CreateOptions{Project: "demo"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	hostRoot, err := manager.ResolvePath(created.ID, ".")
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	info, err := os.Stat(hostRoot)
	if err != nil {
		t.Fatalf("stat root: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("workspace root is not a directory")
	}

	got, err := manager.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Project != "demo" || got.State != "running" {
		t.Fatalf("workspace = %+v", got)
	}
}

func TestNormalizeRepoURL(t *testing.T) {
	for input, want := range map[string]string{
		"git@github.com:peelar/scraps.git":       "https://github.com/peelar/scraps.git",
		"ssh://git@github.com/peelar/scraps.git": "https://github.com/peelar/scraps.git",
		"https://github.com/peelar/scraps.git":   "https://github.com/peelar/scraps.git",
		"  https://example.com/owner/repo.git  ": "https://example.com/owner/repo.git",
	} {
		got, err := NormalizeRepoURL(input)
		if err != nil || got != want {
			t.Errorf("NormalizeRepoURL(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"file:///tmp/repo", "https://token@example.com/repo", "https://example.com/repo?token=x"} {
		if _, err := NormalizeRepoURL(input); !errors.Is(err, ErrInvalidRepoURL) {
			t.Errorf("NormalizeRepoURL(%q) error = %v, want ErrInvalidRepoURL", input, err)
		}
	}
}

func TestCreateRejectsNonHTTPRepo(t *testing.T) {
	manager := newTestManager(t)
	for _, repo := range []string{"ssh:///repo.git", "file:///repo", "example.com/repo"} {
		if _, err := manager.Create(context.Background(), CreateOptions{RepoURL: repo}); err == nil {
			t.Fatalf("repo %q accepted", repo)
		}
	}
}

func TestCreateCloneFailureCleansUp(t *testing.T) {
	manager := newTestManager(t)

	_, err := manager.Create(context.Background(), CreateOptions{
		RepoURL: "http://127.0.0.1:1/repo.git", // nothing listening
	})
	if err == nil {
		t.Fatal("clone succeeded against dead server")
	}
	workspaces, err := manager.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(workspaces) != 0 {
		t.Fatalf("workspaces = %+v, want empty after failed clone", workspaces)
	}
	entries, err := os.ReadDir(manager.Root())
	if err != nil {
		t.Fatalf("read root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("leftover directories: %v", entries)
	}
}

func TestDelete(t *testing.T) {
	manager := newTestManager(t)
	ctx := context.Background()

	created, err := manager.Create(ctx, CreateOptions{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := manager.Delete(ctx, created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := manager.Get(ctx, created.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("get after delete err = %v, want ErrNotFound", err)
	}
	if _, err := os.Stat(filepath.Join(manager.Root(), created.ID)); !os.IsNotExist(err) {
		t.Fatalf("directory still exists: %v", err)
	}
	if err := manager.Delete(ctx, created.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete err = %v, want ErrNotFound", err)
	}
}

func TestResolvePath(t *testing.T) {
	manager := newTestManager(t)
	ctx := context.Background()

	created, err := manager.Create(ctx, CreateOptions{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	hostRoot := filepath.Join(manager.Root(), created.ID)

	got, err := manager.ResolvePath(created.ID, filepath.Join("src", "..", "main.go"))
	if err != nil {
		t.Fatalf("resolve inside: %v", err)
	}
	if want := filepath.Join(hostRoot, "main.go"); got != want {
		t.Fatalf("resolve = %q, want %q", got, want)
	}

	if _, err := manager.ResolvePath(created.ID, "."); err != nil {
		t.Fatalf("root itself rejected: %v", err)
	}
	if _, err := manager.ResolvePath(created.ID, "../other"); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("escape err = %v, want ErrOutsideRoot", err)
	}
	if _, err := manager.ResolvePath(created.ID, "/host/path"); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("absolute err = %v, want ErrOutsideRoot", err)
	}
}

func TestResolvePathSymlinkEscape(t *testing.T) {
	manager := newTestManager(t)
	ctx := context.Background()

	created, err := manager.Create(ctx, CreateOptions{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	outside := t.TempDir()
	hostRoot, err := manager.ResolvePath(created.ID, ".")
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(hostRoot, "escape")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := manager.ResolvePath(created.ID, filepath.Join("escape", "file")); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("symlink escape err = %v, want ErrOutsideRoot", err)
	}
}
