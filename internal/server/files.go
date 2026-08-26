package server

import (
	"encoding/base64"
	"errors"
	"io/fs"
	"net/http"

	"github.com/peelar/scraps/internal/provider"
)

const maxFileBytes = 100 << 20

type pathRequest struct {
	Path string `json:"path"`
}
type fileReadResponse struct {
	Content string `json:"content"`
	Size    int64  `json:"size"`
}
type fileWriteRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}
type fileWriteResponse struct {
	Size int64 `json:"size"`
}
type fileStatResponse struct {
	Exists      bool   `json:"exists"`
	IsDirectory bool   `json:"isDirectory"`
	Size        int64  `json:"size"`
	Mode        string `json:"mode"`
	ModTimeMs   int64  `json:"modTimeMs"`
}
type fileAccessRequest struct {
	Path string `json:"path"`
	Want string `json:"want"`
}
type fileReaddirResponse struct {
	Entries []string `json:"entries"`
}

func requirePath(response http.ResponseWriter, path string) bool {
	if path == "" {
		writeError(response, 400, "invalid_request", "path is required")
		return false
	}
	return true
}
func (s *Server) fileRead(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.lookupWorkspace(w, r); !ok {
		return
	}
	var b pathRequest
	if decode(w, r, &b) != nil {
		return
	}
	if !requirePath(w, b.Path) {
		return
	}
	content, info, e := s.provider.ReadFile(r.Context(), r.PathValue("id"), b.Path, maxFileBytes)
	if e != nil {
		writeProviderError(w, e)
		return
	}
	writeJSON(w, 200, fileReadResponse{base64.StdEncoding.EncodeToString(content), info.Size()})
}
func (s *Server) fileWrite(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.lookupWorkspace(w, r); !ok {
		return
	}
	var b fileWriteRequest
	if decode(w, r, &b) != nil {
		return
	}
	if !requirePath(w, b.Path) {
		return
	}
	content, e := base64.StdEncoding.DecodeString(b.Content)
	if e != nil {
		writeError(w, 400, "invalid_request", "content is not valid base64")
		return
	}
	if len(content) > maxFileBytes {
		writeError(w, 400, "invalid_request", "content exceeds 100MB limit")
		return
	}
	if e = s.provider.WriteFile(r.Context(), r.PathValue("id"), b.Path, content); e != nil {
		writeProviderError(w, e)
		return
	}
	writeJSON(w, 200, fileWriteResponse{int64(len(content))})
}
func (s *Server) fileMkdir(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.lookupWorkspace(w, r); !ok {
		return
	}
	var b pathRequest
	if decode(w, r, &b) != nil {
		return
	}
	if !requirePath(w, b.Path) {
		return
	}
	if e := s.provider.Mkdir(r.Context(), r.PathValue("id"), b.Path); e != nil {
		writeProviderError(w, e)
		return
	}
	writeJSON(w, 200, struct{}{})
}
func (s *Server) fileStat(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.lookupWorkspace(w, r); !ok {
		return
	}
	var b pathRequest
	if decode(w, r, &b) != nil {
		return
	}
	if !requirePath(w, b.Path) {
		return
	}
	info, e := s.provider.Stat(r.Context(), r.PathValue("id"), b.Path)
	if e != nil {
		writeProviderError(w, e)
		return
	}
	writeJSON(w, 200, fileStatResponse{true, info.IsDir(), info.Size(), info.Mode().String(), info.ModTime().UnixMilli()})
}
func (s *Server) fileAccess(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.lookupWorkspace(w, r); !ok {
		return
	}
	var b fileAccessRequest
	if decode(w, r, &b) != nil {
		return
	}
	if !requirePath(w, b.Path) {
		return
	}
	if b.Want != "read" && b.Want != "write" {
		writeError(w, 400, "invalid_request", `want must be "read" or "write"`)
		return
	}
	if e := s.provider.Access(r.Context(), r.PathValue("id"), b.Path, provider.AccessMode(b.Want)); e != nil {
		writeProviderError(w, e)
		return
	}
	writeJSON(w, 200, struct{}{})
}
func (s *Server) fileReaddir(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.lookupWorkspace(w, r); !ok {
		return
	}
	var b pathRequest
	if decode(w, r, &b) != nil {
		return
	}
	if !requirePath(w, b.Path) {
		return
	}
	entries, e := s.provider.ReadDir(r.Context(), r.PathValue("id"), b.Path)
	if e != nil {
		writeProviderError(w, e)
		return
	}
	writeJSON(w, 200, fileReaddirResponse{entries})
}

func decode(w http.ResponseWriter, r *http.Request, v any) error {
	if e := decodeBody(r, v); e != nil {
		writeAPIError(w, e)
		return e
	}
	return nil
}
func writeProviderError(w http.ResponseWriter, e error) {
	var dial *provider.TunnelDialError
	if errors.As(e, &dial) {
		writeError(w, http.StatusBadGateway, "tunnel_dial_failed", dial.Error())
		return
	}
	var invalid *provider.InvalidRequestError
	if errors.As(e, &invalid) {
		writeError(w, 400, "invalid_request", invalid.Error())
		return
	}
	if errors.Is(e, fs.ErrNotExist) {
		writeError(w, 404, "not_found", "path does not exist")
		return
	}
	if errors.Is(e, fs.ErrPermission) {
		writeError(w, 403, "access_denied", "permission denied")
		return
	}
	writeAPIError(w, e)
}
