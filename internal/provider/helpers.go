package provider

import (
	"fmt"
	"path"
	"strings"

	"github.com/peelar/scraps/internal/store"
	"github.com/peelar/scraps/internal/workspace"
)

// InvalidRequestError reports a provider operation invalid for the requested path or state.
type InvalidRequestError struct{ Message string }

func (e *InvalidRequestError) Error() string { return e.Message }

// RepositoryAuthorizationRequiredError reports that a sandbox cannot receive
// the network and credential capability required to access a repository.
type RepositoryAuthorizationRequiredError struct{ Host string }

func (e *RepositoryAuthorizationRequiredError) Error() string {
	return fmt.Sprintf("repository access to %s is not configured; run `scrap auth github`", e.Host)
}

// RepositoryCloneError is a safe, caller-visible clone failure. Provider
// command output remains server-side because it may contain credential or
// transport details.
type RepositoryCloneError struct {
	Host   string
	Reason string
}

func (e *RepositoryCloneError) Error() string {
	return fmt.Sprintf("could not clone repository from %s: %s", e.Host, e.Reason)
}

func publicWorkspace(w store.Workspace) workspace.Workspace {
	return workspace.Workspace{
		ID: w.ID, Project: w.Project, RepoURL: w.RepoURL, State: w.State,
		RootPath: workspace.VirtualRoot, PathContract: workspace.PathContract,
		CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt,
	}
}

func validateRelative(p string) (string, error) {
	if p == "" {
		return ".", nil
	}
	if path.IsAbs(p) {
		return "", &InvalidRequestError{Message: "absolute workspace path is not allowed: " + p}
	}
	clean := path.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", &InvalidRequestError{Message: "path escapes workspace: " + p}
	}
	return clean, nil
}
