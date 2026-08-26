package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/peelar/scraps/internal/store"
	"github.com/peelar/scraps/internal/workspace"
)

type createWorkspaceRequest struct {
	Project string `json:"project"`
	RepoURL string `json:"repoUrl"`
}

func (s *Server) createWorkspace(response http.ResponseWriter, request *http.Request) {
	var body createWorkspaceRequest
	if err := decodeBody(request, &body); err != nil {
		writeAPIError(response, err)
		return
	}

	created, err := s.provider.Create(request.Context(), workspace.CreateOptions{
		Project: body.Project,
		RepoURL: body.RepoURL,
	})
	if err != nil {
		writeAPIError(response, workspaceCreationError(err))
		return
	}
	writeJSON(response, http.StatusCreated, created)
}

func workspaceCreationError(err error) error {
	if errors.Is(err, workspace.ErrInvalidRepoURL) {
		return &apiError{status: http.StatusBadRequest, code: "invalid_request", message: err.Error()}
	}
	return err
}

func (s *Server) listWorkspaces(response http.ResponseWriter, request *http.Request) {
	workspaces, err := s.provider.List(request.Context())
	if err != nil {
		writeAPIError(response, err)
		return
	}
	if workspaces == nil {
		workspaces = []workspace.Workspace{}
	}
	writeJSON(response, http.StatusOK, map[string]any{"workspaces": workspaces})
}

func (s *Server) getWorkspace(response http.ResponseWriter, request *http.Request) {
	found, err := s.provider.Get(request.Context(), request.PathValue("id"))
	if err != nil {
		writeAPIError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, found)
}

func (s *Server) startWorkspace(response http.ResponseWriter, request *http.Request) {
	if err := s.provider.Start(request.Context(), request.PathValue("id")); err != nil {
		writeAPIError(response, err)
		return
	}
	found, err := s.provider.Get(request.Context(), request.PathValue("id"))
	if err != nil {
		writeAPIError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, found)
}

func (s *Server) stopWorkspace(response http.ResponseWriter, request *http.Request) {
	if err := s.provider.Stop(request.Context(), request.PathValue("id")); err != nil {
		writeAPIError(response, err)
		return
	}
	found, err := s.provider.Get(request.Context(), request.PathValue("id"))
	if err != nil {
		writeAPIError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, found)
}

func (s *Server) deleteWorkspace(response http.ResponseWriter, request *http.Request) {
	if err := s.provider.Delete(request.Context(), request.PathValue("id")); err != nil {
		writeAPIError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

// decodeBody parses a JSON request body with a size limit, mapping decode
// failures to invalid_request.
func decodeBody(request *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(request.Body, 8<<20))
	if err := decoder.Decode(target); err != nil {
		return &apiError{status: http.StatusBadRequest, code: "invalid_request", message: "invalid JSON body: " + err.Error()}
	}
	return nil
}

// lookupWorkspace resolves the workspace from the request path or writes the
// error response and returns false.
func (s *Server) lookupWorkspace(response http.ResponseWriter, request *http.Request) (workspace.Workspace, bool) {
	found, err := s.provider.Get(request.Context(), request.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(response, http.StatusNotFound, "not_found", "workspace not found")
			return found, false
		}
		writeAPIError(response, err)
		return found, false
	}
	return found, true
}
