// Package workspace implements workspace lifecycle on the directory-backed
// provider: one directory per workspace under the scrapd data dir.
package workspace

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/peelar/scraps/internal/store"
)

// CloneTimeout bounds repository clones during workspace creation.
const CloneTimeout = 10 * time.Minute

var adjectives = []string{
	"amber", "brave", "calm", "clever", "cosmic", "crisp", "dawn", "eager",
	"fern", "gentle", "golden", "hidden", "ivory", "jolly", "keen", "lively",
	"mellow", "misty", "noble", "olive", "plush", "quiet", "rapid", "russet",
	"silent", "solar", "swift", "tidal", "urban", "velvet", "witty", "zesty",
}

var nouns = []string{
	"atlas", "beacon", "canyon", "cedar", "cliff", "cove", "delta", "dune",
	"ember", "falcon", "forest", "glacier", "harbor", "heron", "island",
	"lagoon", "lantern", "meadow", "orbit", "peak", "prairie", "quarry",
	"reef", "ridge", "river", "summit", "thicket", "timber", "valley",
	"voyage", "willow", "zenith",
}

// GenerateName returns a random adjective-noun pair like "quiet-river".
func GenerateName() (string, error) {
	pick := func(words []string) (string, error) {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(words))))
		if err != nil {
			return "", fmt.Errorf("random word: %w", err)
		}
		return words[n.Int64()], nil
	}
	adjective, err := pick(adjectives)
	if err != nil {
		return "", err
	}
	noun, err := pick(nouns)
	if err != nil {
		return "", err
	}
	return adjective + "-" + noun, nil
}

// Manager owns workspace directories and their store records.
type Manager struct {
	store *store.Store
	root  string // absolute path of the workspaces directory
}

// NewManager creates the workspaces root directory.
func NewManager(store *store.Store, dataDir string) (*Manager, error) {
	root := filepath.Join(dataDir, "workspaces")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create workspaces root: %w", err)
	}
	// Canonicalize so path validation against caller-supplied absolute
	// paths is stable across symlinked temp/data dirs (macOS /var etc.).
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve workspaces root: %w", err)
	}
	return &Manager{store: store, root: root}, nil
}

// Root returns the absolute workspaces root path.
func (m *Manager) Root() string { return m.root }

// VirtualRoot is the stable agent-visible root for every provider.
const VirtualRoot = "/workspace"

// PathContract identifies the workspace-relative v2 path contract.
const PathContract = "workspace-relative-v1"

// Workspace is the API-facing workspace record.
type Workspace struct {
	ID           string    `json:"id"`
	Project      string    `json:"project"`
	RepoURL      string    `json:"repoUrl"`
	State        string    `json:"state"`
	RootPath     string    `json:"rootPath"`
	PathContract string    `json:"pathContract"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

func fromStore(w store.Workspace) Workspace {
	return Workspace{
		ID:           w.ID,
		Project:      w.Project,
		RepoURL:      w.RepoURL,
		State:        w.State,
		RootPath:     VirtualRoot,
		PathContract: PathContract,
		CreatedAt:    w.CreatedAt,
		UpdatedAt:    w.UpdatedAt,
	}
}

// CreateOptions configures workspace creation.
type CreateOptions struct {
	Project string
	RepoURL string
}

// Create generates a unique workspace, clones the repository if given, and
// persists the record.
func (m *Manager) Create(ctx context.Context, options CreateOptions) (Workspace, error) {
	repoURL, err := NormalizeRepoURL(options.RepoURL)
	if err != nil {
		return Workspace{}, err
	}

	for attempt := 0; attempt < 8; attempt++ {
		id, err := GenerateName()
		if err != nil {
			return Workspace{}, err
		}
		taken, err := m.store.WorkspaceExists(ctx, id)
		if err != nil {
			return Workspace{}, err
		}
		if taken {
			continue
		}

		root := filepath.Join(m.root, id)
		if err := os.MkdirAll(root, 0o755); err != nil {
			return Workspace{}, fmt.Errorf("create workspace directory: %w", err)
		}

		if repoURL != "" {
			if err := m.clone(ctx, repoURL, root); err != nil {
				os.RemoveAll(root)
				return Workspace{}, err
			}
		}

		record := store.Workspace{ID: id, Project: options.Project, RepoURL: repoURL, Provider: "directory", State: "running"}
		if err := m.store.CreateWorkspace(ctx, record); err != nil {
			os.RemoveAll(root)
			return Workspace{}, err
		}
		// Re-read so timestamps assigned by the store are returned.
		saved, err := m.store.GetWorkspace(ctx, id)
		if err != nil {
			return Workspace{}, err
		}
		return fromStore(saved), nil
	}
	return Workspace{}, errors.New("could not generate a unique workspace id")
}

func (m *Manager) clone(ctx context.Context, repoURL, dir string) error {
	ctx, cancel := context.WithTimeout(ctx, CloneTimeout)
	defer cancel()

	output, err := exec.CommandContext(ctx, "git", "clone", repoURL, dir).CombinedOutput()
	if err != nil {
		return fmt.Errorf("clone %s: %w\n%s", repoURL, err, strings.TrimSpace(string(output)))
	}
	return nil
}

// ErrInvalidRepoURL is returned for repository URLs that are not http(s).
var ErrInvalidRepoURL = errors.New("invalid repo url")

// NormalizeRepoURL converts common Git SSH origin forms to HTTPS and validates
// the provider-neutral repository URL accepted by scrapd. Keeping this at the
// daemon boundary gives the CLI and Pi extension identical behavior.
func NormalizeRepoURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if match := regexp.MustCompile(`^[^@/:[:space:]]+@([^/:[:space:]]+):(.+)$`).FindStringSubmatch(raw); match != nil {
		raw = "https://" + match[1] + "/" + match[2]
	} else if parsed, err := url.Parse(raw); err == nil && parsed.Scheme == "ssh" {
		if parsed.Hostname() == "" || strings.TrimPrefix(parsed.Path, "/") == "" {
			return "", fmt.Errorf("%w: invalid ssh repository URL", ErrInvalidRepoURL)
		}
		raw = "https://" + parsed.Hostname() + "/" + strings.TrimPrefix(parsed.Path, "/")
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidRepoURL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("%w: scheme must be http or https, got %q", ErrInvalidRepoURL, parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("%w: missing host", ErrInvalidRepoURL)
	}
	if parsed.User != nil {
		return "", fmt.Errorf("%w: embedded credentials are not allowed", ErrInvalidRepoURL)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("%w: query strings and fragments are not allowed", ErrInvalidRepoURL)
	}
	return parsed.String(), nil
}

// Get returns a workspace by ID.
func (m *Manager) Get(ctx context.Context, id string) (Workspace, error) {
	record, err := m.store.GetWorkspace(ctx, id)
	if err != nil {
		return Workspace{}, err
	}
	if record.Provider != "directory" {
		return Workspace{}, store.ErrNotFound
	}
	return fromStore(record), nil
}

// List returns all workspaces.
func (m *Manager) List(ctx context.Context) ([]Workspace, error) {
	records, err := m.store.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	workspaces := make([]Workspace, 0, len(records))
	for _, record := range records {
		if record.Provider == "directory" {
			workspaces = append(workspaces, fromStore(record))
		}
	}
	return workspaces, nil
}

// Start marks a directory workspace available. It does not add isolation.
func (m *Manager) Start(ctx context.Context, id string) error {
	if _, err := m.Get(ctx, id); err != nil {
		return err
	}
	return m.store.UpdateWorkspaceState(ctx, id, "running")
}

// Stop marks a directory workspace unavailable.
func (m *Manager) Stop(ctx context.Context, id string) error {
	if _, err := m.Get(ctx, id); err != nil {
		return err
	}
	return m.store.UpdateWorkspaceState(ctx, id, "stopped")
}

// Delete removes the workspace directory and record.
func (m *Manager) Delete(ctx context.Context, id string) error {
	if _, err := m.Get(ctx, id); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(m.root, id)); err != nil {
		return fmt.Errorf("remove workspace directory: %w", err)
	}
	return m.store.DeleteWorkspace(ctx, id)
}

// ErrOutsideRoot is returned for paths that escape the workspace root.
var ErrOutsideRoot = errors.New("path resolves outside the workspace root")

// ResolvePath validates a workspace-relative API path and returns its host
// path. Absolute paths are rejected so provider layout can never leak into
// or be inferred by clients. Symlinks of existing components are evaluated.
func (m *Manager) ResolvePath(id, path string) (string, error) {
	root := filepath.Join(m.root, id)
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}

	if filepath.IsAbs(path) {
		return "", fmt.Errorf("%w: %q must be workspace-relative", ErrOutsideRoot, path)
	}
	cleanRelative := filepath.Clean(path)
	if cleanRelative == ".." || strings.HasPrefix(cleanRelative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q", ErrOutsideRoot, path)
	}
	root = resolvedRoot
	clean := filepath.Join(root, cleanRelative)

	// Resolve the deepest existing component so symlinked directories are
	// caught even when the final path does not exist yet (write tool).
	if resolved, ok := resolveExisting(clean); ok && !withinRoot(resolved, root) {
		return "", fmt.Errorf("%w: %q resolves to %q", ErrOutsideRoot, path, resolved)
	}
	return clean, nil
}

func withinRoot(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

// resolveExisting evaluates symlinks of the deepest existing ancestor and
// rejoins the non-existent tail. ok is false when nothing could be resolved.
func resolveExisting(path string) (string, bool) {
	tail := ""
	for {
		if _, err := os.Lstat(path); err == nil {
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				// Dangling symlink: further resolution is impossible; the
				// literal path is what an open would use anyway.
				return filepath.Join(path, tail), true
			}
			return filepath.Join(resolved, tail), true
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", false
		}
		parent := filepath.Dir(path)
		if parent == path {
			return path, true
		}
		tail = filepath.Join(filepath.Base(path), tail)
		path = parent
	}
}
