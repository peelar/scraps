package provider

import (
	"path"
	"strings"

	"github.com/peelar/scraps/internal/store"
	"github.com/peelar/scraps/internal/workspace"
)

// InvalidRequestError reports a provider operation invalid for the requested path or state.
type InvalidRequestError struct{ Message string }

func (e *InvalidRequestError) Error() string { return e.Message }

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
