package server

import (
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// maxFileBytes caps single-file reads and writes (100 MB).
const maxFileBytes = 100 << 20

type pathRequest struct {
	Path string `json:"path"`
}

// resolveWorkspacePath validates {path} against the workspace root.
func (s *Server) resolveWorkspacePath(response http.ResponseWriter, request *http.Request, body pathRequest) (string, bool) {
	if body.Path == "" {
		writeError(response, http.StatusBadRequest, "invalid_request", "path is required")
		return "", false
	}
	resolved, err := s.manager.ResolvePath(request.PathValue("id"), body.Path)
	if err != nil {
		writeAPIError(response, err)
		return "", false
	}
	return resolved, true
}

type fileReadResponse struct {
	Content string `json:"content"`
	Size    int64  `json:"size"`
}

func (s *Server) fileRead(response http.ResponseWriter, request *http.Request) {
	if _, ok := s.lookupWorkspace(response, request); !ok {
		return
	}
	var body pathRequest
	if err := decodeBody(request, &body); err != nil {
		writeAPIError(response, err)
		return
	}
	path, ok := s.resolveWorkspacePath(response, request, body)
	if !ok {
		return
	}

	file, err := os.Open(path)
	if err != nil {
		writeFileError(response, err)
		return
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		writeFileError(response, err)
		return
	}
	if info.IsDir() {
		writeError(response, http.StatusBadRequest, "invalid_request", "path is a directory: "+body.Path)
		return
	}
	if info.Size() > maxFileBytes {
		writeError(response, http.StatusBadRequest, "invalid_request", "file exceeds 100MB limit")
		return
	}

	content, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err != nil {
		writeFileError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, fileReadResponse{
		Content: base64.StdEncoding.EncodeToString(content),
		Size:    info.Size(),
	})
}

type fileWriteRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type fileWriteResponse struct {
	Size int64 `json:"size"`
}

func (s *Server) fileWrite(response http.ResponseWriter, request *http.Request) {
	if _, ok := s.lookupWorkspace(response, request); !ok {
		return
	}
	var body fileWriteRequest
	if err := decodeBody(request, &body); err != nil {
		writeAPIError(response, err)
		return
	}
	path, ok := s.resolveWorkspacePath(response, request, pathRequest{Path: body.Path})
	if !ok {
		return
	}
	content, err := base64.StdEncoding.DecodeString(body.Content)
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request", "content is not valid base64")
		return
	}
	if int64(len(content)) > maxFileBytes {
		writeError(response, http.StatusBadRequest, "invalid_request", "content exceeds 100MB limit")
		return
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		writeFileError(response, err)
		return
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		writeFileError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, fileWriteResponse{Size: int64(len(content))})
}

func (s *Server) fileMkdir(response http.ResponseWriter, request *http.Request) {
	if _, ok := s.lookupWorkspace(response, request); !ok {
		return
	}
	var body pathRequest
	if err := decodeBody(request, &body); err != nil {
		writeAPIError(response, err)
		return
	}
	path, ok := s.resolveWorkspacePath(response, request, body)
	if !ok {
		return
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		writeFileError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, struct{}{})
}

type fileStatResponse struct {
	Exists      bool   `json:"exists"`
	IsDirectory bool   `json:"isDirectory"`
	Size        int64  `json:"size"`
	Mode        string `json:"mode"`
	ModTimeMs   int64  `json:"modTimeMs"`
}

func (s *Server) fileStat(response http.ResponseWriter, request *http.Request) {
	if _, ok := s.lookupWorkspace(response, request); !ok {
		return
	}
	var body pathRequest
	if err := decodeBody(request, &body); err != nil {
		writeAPIError(response, err)
		return
	}
	path, ok := s.resolveWorkspacePath(response, request, body)
	if !ok {
		return
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(response, http.StatusNotFound, "not_found", "path does not exist: "+body.Path)
			return
		}
		writeFileError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, fileStatResponse{
		Exists:      true,
		IsDirectory: info.IsDir(),
		Size:        info.Size(),
		Mode:        info.Mode().String(),
		ModTimeMs:   info.ModTime().UnixMilli(),
	})
}

type fileAccessRequest struct {
	Path string `json:"path"`
	Want string `json:"want"` // "read" or "write"
}

func (s *Server) fileAccess(response http.ResponseWriter, request *http.Request) {
	if _, ok := s.lookupWorkspace(response, request); !ok {
		return
	}
	var body fileAccessRequest
	if err := decodeBody(request, &body); err != nil {
		writeAPIError(response, err)
		return
	}
	if body.Want != "read" && body.Want != "write" {
		writeError(response, http.StatusBadRequest, "invalid_request", `want must be "read" or "write"`)
		return
	}
	path, ok := s.resolveWorkspacePath(response, request, pathRequest{Path: body.Path})
	if !ok {
		return
	}

	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			writeError(response, http.StatusNotFound, "not_found", "path does not exist: "+body.Path)
			return
		}
		writeFileError(response, err)
		return
	}
	if body.Want == "read" {
		file, err := os.Open(path)
		if err != nil {
			writeError(response, http.StatusForbidden, "access_denied", "path is not readable: "+body.Path)
			return
		}
		file.Close()
	} else {
		file, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			writeError(response, http.StatusForbidden, "access_denied", "path is not writable: "+body.Path)
			return
		}
		file.Close()
	}
	writeJSON(response, http.StatusOK, struct{}{})
}

type fileReaddirResponse struct {
	Entries []string `json:"entries"`
}

func (s *Server) fileReaddir(response http.ResponseWriter, request *http.Request) {
	if _, ok := s.lookupWorkspace(response, request); !ok {
		return
	}
	var body pathRequest
	if err := decodeBody(request, &body); err != nil {
		writeAPIError(response, err)
		return
	}
	path, ok := s.resolveWorkspacePath(response, request, body)
	if !ok {
		return
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		writeFileError(response, err)
		return
	}
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	writeJSON(response, http.StatusOK, fileReaddirResponse{Entries: names})
}

// writeFileError maps os file errors onto the API error shape.
func writeFileError(response http.ResponseWriter, err error) {
	if os.IsNotExist(err) {
		writeError(response, http.StatusNotFound, "not_found", "path does not exist")
		return
	}
	if os.IsPermission(err) {
		writeError(response, http.StatusForbidden, "access_denied", "permission denied")
		return
	}
	if os.IsExist(err) {
		writeError(response, http.StatusBadRequest, "invalid_request", "path already exists")
		return
	}
	writeAPIError(response, err)
}
