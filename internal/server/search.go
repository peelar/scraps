package server

import (
	"net/http"

	"github.com/peelar/scraps/internal/provider"
)

type fileGlobRequest struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
	Limit   int    `json:"limit"`
}
type fileGlobResponse struct {
	Paths []string `json:"paths"`
}

func (s *Server) fileGlob(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.lookupWorkspace(w, r); !ok {
		return
	}
	var b fileGlobRequest
	if decode(w, r, &b) != nil {
		return
	}
	if b.Pattern == "" {
		writeError(w, 400, "invalid_request", "pattern is required")
		return
	}
	paths, e := s.provider.Glob(r.Context(), r.PathValue("id"), provider.GlobRequest{Pattern: b.Pattern, Path: b.Path, Limit: b.Limit})
	if e != nil {
		writeProviderError(w, e)
		return
	}
	if paths == nil {
		paths = []string{}
	}
	writeJSON(w, 200, fileGlobResponse{paths})
}

type fileGrepRequest struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	Glob       string `json:"glob"`
	IgnoreCase bool   `json:"ignoreCase"`
	Literal    bool   `json:"literal"`
	Context    int    `json:"context"`
	Limit      int    `json:"limit"`
}
type fileGrepResponse = provider.GrepResult

func (s *Server) fileGrep(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.lookupWorkspace(w, r); !ok {
		return
	}
	var b fileGrepRequest
	if decode(w, r, &b) != nil {
		return
	}
	if b.Pattern == "" {
		writeError(w, 400, "invalid_request", "pattern is required")
		return
	}
	result, e := s.provider.Grep(r.Context(), r.PathValue("id"), provider.GrepRequest{Pattern: b.Pattern, Path: b.Path, Glob: b.Glob, IgnoreCase: b.IgnoreCase, Literal: b.Literal, Context: b.Context, Limit: b.Limit})
	if e != nil {
		writeProviderError(w, e)
		return
	}
	writeJSON(w, 200, result)
}
