package server

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
)

const (
	// maxArchiveBytes bounds one push or pull archive. It protects workspace
	// disk, not memory: both directions stream.
	maxArchiveBytes = 1 << 30
	// internalDir is Scraps' own workspace directory; it never crosses the
	// archive boundary in either direction.
	internalDir = ".scrap"
)

type archiveImportResponse struct {
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
}

// archiveImport streams a tar request body into the workspace. It backs the
// explicit directory push (ADR 0014): the workspace must be empty unless the
// caller asks for replace, entry names must stay workspace-relative, and only
// regular files and directories are imported.
func (s *Server) archiveImport(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.lookupWorkspace(w, r); !ok {
		return
	}
	if got := r.Header.Get("Content-Type"); got != "application/x-tar" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type",
			"Content-Type must be application/x-tar")
		return
	}
	replace := false
	switch r.URL.Query().Get("replace") {
	case "", "false":
	case "true":
		replace = true
	default:
		writeError(w, 400, "invalid_request", "replace must be true or false")
		return
	}

	id := r.PathValue("id")
	if replace {
		if err := s.clearWorkspace(r.Context(), id); err != nil {
			writeProviderError(w, err)
			return
		}
	} else {
		empty, err := s.workspaceIsEmpty(r.Context(), id)
		if err != nil {
			writeProviderError(w, err)
			return
		}
		if !empty {
			writeError(w, http.StatusConflict, "workspace_not_empty",
				"workspace is not empty; pass replace=true to clear it first")
			return
		}
	}

	body := http.MaxBytesReader(w, r.Body, maxArchiveBytes)
	reader := tar.NewReader(body)
	var (
		files int
		bytes int64
	)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				writeError(w, http.StatusRequestEntityTooLarge, "archive_too_large",
					"archive exceeds the 1GiB limit")
				return
			}
			writeError(w, 400, "invalid_request", "unreadable tar archive: "+err.Error())
			return
		}
		// "tar -C dir ." opens with the archive root itself ("./"); the
		// workspace root already exists, so skip it rather than reject.
		if header.Typeflag == tar.TypeDir && path.Clean(strings.TrimSuffix(header.Name, "/")) == "." {
			continue
		}
		name, err := cleanArchiveName(header.Name)
		if err != nil {
			writeError(w, 400, "invalid_request", err.Error())
			return
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := s.provider.Mkdir(r.Context(), id, name); err != nil {
				writeProviderError(w, err)
				return
			}
		case tar.TypeReg:
			if header.Size > maxFileBytes {
				writeError(w, 400, "invalid_request",
					"archive entry exceeds the 100MB per-file limit: "+name)
				return
			}
			content, err := io.ReadAll(io.LimitReader(reader, maxFileBytes+1))
			if err != nil {
				var tooLarge *http.MaxBytesError
				if errors.As(err, &tooLarge) {
					writeError(w, http.StatusRequestEntityTooLarge, "archive_too_large",
						"archive exceeds the 1GiB limit")
					return
				}
				writeError(w, 400, "invalid_request", "unreadable tar entry: "+err.Error())
				return
			}
			if int64(len(content)) != header.Size {
				writeError(w, 400, "invalid_request", "truncated tar entry: "+name)
				return
			}
			if err := s.provider.WriteFile(r.Context(), id, name, content); err != nil {
				writeProviderError(w, err)
				return
			}
			files++
			bytes += int64(len(content))
		default:
			writeError(w, 400, "invalid_request",
				"only regular files and directories may be imported: "+name)
			return
		}
	}
	writeJSON(w, http.StatusOK, archiveImportResponse{Files: files, Bytes: bytes})
}

// archiveExport streams the workspace as a tar response. It backs the explicit
// directory pull (ADR 0014). Oversized or non-regular entries are skipped and
// reported in X-Scraps-Skipped-Entries before the body starts.
func (s *Server) archiveExport(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.lookupWorkspace(w, r); !ok {
		return
	}
	id := r.PathValue("id")

	type entry struct {
		name  string
		dir   bool
		size  int64
		mode  int64
		mtime time.Time
	}
	var entries []entry
	skipped := 0
	var walk func(dir string) error
	walk = func(dir string) error {
		names, err := s.provider.ReadDir(r.Context(), id, dir)
		if err != nil {
			return err
		}
		for _, name := range names {
			relative := name
			if dir != "." {
				relative = dir + "/" + name
			}
			if relative == internalDir || strings.HasPrefix(relative, internalDir+"/") {
				continue
			}
			info, err := s.provider.Stat(r.Context(), id, relative)
			if err != nil {
				return err
			}
			switch {
			case info.IsDir():
				entries = append(entries, entry{name: relative, dir: true, mtime: info.ModTime()})
				if err := walk(relative); err != nil {
					return err
				}
			case info.Mode().IsRegular():
				if info.Size() > maxFileBytes {
					skipped++
					continue
				}
				entries = append(entries, entry{
					name: relative, size: info.Size(),
					mode: int64(info.Mode().Perm()), mtime: info.ModTime(),
				})
			default:
				skipped++
			}
		}
		return nil
	}
	if err := walk("."); err != nil {
		writeProviderError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("X-Scraps-Skipped-Entries", strconv.Itoa(skipped))
	w.WriteHeader(http.StatusOK)

	writer := tar.NewWriter(w)
	for _, e := range entries {
		if e.dir {
			header := &tar.Header{Typeflag: tar.TypeDir, Name: e.name + "/", Mode: 0o755, ModTime: e.mtime}
			if err := writer.WriteHeader(header); err != nil {
				return
			}
			continue
		}
		content, _, err := s.provider.ReadFile(r.Context(), id, e.name, maxFileBytes)
		if err != nil {
			return
		}
		header := &tar.Header{Typeflag: tar.TypeReg, Name: e.name, Size: e.size, Mode: e.mode, ModTime: e.mtime}
		if err := writer.WriteHeader(header); err != nil {
			return
		}
		if _, err := writer.Write(content); err != nil {
			return
		}
	}
	_ = writer.Close()
}

// workspaceIsEmpty reports whether the workspace holds nothing but Scraps'
// internal directory.
func (s *Server) workspaceIsEmpty(ctx context.Context, id string) (bool, error) {
	entries, err := s.provider.ReadDir(ctx, id, ".")
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry != internalDir {
			return false, nil
		}
	}
	return true, nil
}

// clearWorkspace removes every top-level entry except the internal directory.
func (s *Server) clearWorkspace(ctx context.Context, id string) error {
	entries, err := s.provider.ReadDir(ctx, id, ".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry == internalDir {
			continue
		}
		if err := s.provider.RemoveAll(ctx, id, entry); err != nil {
			return err
		}
	}
	return nil
}

// cleanArchiveName validates a tar entry name as a workspace-relative path.
// Absolute paths, parent traversals, empty names, and Scraps' internal
// directory are rejected.
func cleanArchiveName(name string) (string, error) {
	name = strings.TrimSuffix(name, "/")
	if name == "" {
		return "", errors.New("archive entry has an empty name")
	}
	if path.IsAbs(name) {
		return "", errors.New("archive entry is an absolute path: " + name)
	}
	clean := path.Clean(name)
	if clean == "." {
		return "", errors.New("archive entry has an empty name")
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("archive entry escapes the workspace: " + name)
	}
	if clean == internalDir || strings.HasPrefix(clean, internalDir+"/") {
		return "", errors.New("archive entry writes into the reserved .scrap directory: " + name)
	}
	return clean, nil
}
