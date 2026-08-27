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

	"github.com/peelar/scraps/internal/archive"
)

const (
	// maxArchiveBytes bounds one push or pull archive. It protects workspace
	// disk, not memory: both directions stream.
	maxArchiveBytes = 1 << 30
	// internalDir is omitted from exports and rejected on import; it is
	// Scraps' own workspace directory.
	internalDir = archive.ReservedDir
)

type archiveImportResponse struct {
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
}

// archiveRequestError is an HTTP-tagged failure raised while decoding or
// importing an archive.
type archiveRequestError struct {
	status  int
	code    string
	message string
}

func (e *archiveRequestError) Error() string { return e.message }

// writeArchiveError maps an import failure to its response: HTTP-tagged
// request errors get their status and code, everything else is a provider
// failure.
func writeArchiveError(w http.ResponseWriter, err error) {
	var requestErr *archiveRequestError
	if errors.As(err, &requestErr) {
		writeError(w, requestErr.status, requestErr.code, requestErr.message)
		return
	}
	writeProviderError(w, err)
}

// prepareArchiveImport validates the request prelude: Content-Type, the
// replace parameter, and the empty-or-replace workspace precondition. It
// returns the size-bounded request body.
func (s *Server) prepareArchiveImport(w http.ResponseWriter, r *http.Request, id string) (io.Reader, error) {
	if got := r.Header.Get("Content-Type"); got != "application/x-tar" {
		return nil, &archiveRequestError{http.StatusUnsupportedMediaType, "unsupported_media_type",
			"Content-Type must be application/x-tar"}
	}
	replace := false
	switch r.URL.Query().Get("replace") {
	case "", "false":
	case "true":
		replace = true
	default:
		return nil, &archiveRequestError{400, "invalid_request", "replace must be true or false"}
	}
	if replace {
		if err := s.clearWorkspace(r.Context(), id); err != nil {
			return nil, err
		}
	} else {
		empty, err := s.workspaceIsEmpty(r.Context(), id)
		if err != nil {
			return nil, err
		}
		if !empty {
			return nil, &archiveRequestError{http.StatusConflict, "workspace_not_empty",
				"workspace is not empty; pass replace=true to clear it first"}
		}
	}
	return http.MaxBytesReader(w, r.Body, maxArchiveBytes), nil
}

// importArchiveEntry writes one tar entry into the workspace. It returns the
// bytes written for regular files. Client errors come back as
// *archiveRequestError; anything else is a provider failure.
func (s *Server) importArchiveEntry(ctx context.Context, id string, header *tar.Header, reader *tar.Reader) (int64, error) {
	// "tar -C dir ." opens with the archive root itself ("./"); the
	// workspace root already exists, so skip it rather than reject.
	if header.Typeflag == tar.TypeDir && path.Clean(strings.TrimSuffix(header.Name, "/")) == "." {
		return 0, nil
	}
	name, err := archive.CleanEntryName(header.Name)
	if err != nil {
		return 0, &archiveRequestError{400, "invalid_request", err.Error()}
	}
	switch header.Typeflag {
	case tar.TypeDir:
		if err := s.provider.Mkdir(ctx, id, name); err != nil {
			return 0, err
		}
	case tar.TypeReg:
		if header.Size > maxFileBytes {
			return 0, &archiveRequestError{400, "invalid_request",
				"archive entry exceeds the 100MB per-file limit: " + name}
		}
		content, err := io.ReadAll(io.LimitReader(reader, maxFileBytes+1))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				return 0, &archiveRequestError{http.StatusRequestEntityTooLarge, "archive_too_large",
					"archive exceeds the 1GiB limit"}
			}
			return 0, &archiveRequestError{400, "invalid_request", "unreadable tar entry: " + err.Error()}
		}
		if int64(len(content)) != header.Size {
			return 0, &archiveRequestError{400, "invalid_request", "truncated tar entry: " + name}
		}
		if err := s.provider.WriteFile(ctx, id, name, content); err != nil {
			return 0, err
		}
		return int64(len(content)), nil
	default:
		return 0, &archiveRequestError{400, "invalid_request",
			"only regular files and directories may be imported: " + name}
	}
	return 0, nil
}

// archiveImport streams a tar request body into the workspace. It backs the
// explicit directory push (ADR 0014): the workspace must be empty unless the
// caller asks for replace, entry names must stay workspace-relative, and only
// regular files and directories are imported.
func (s *Server) archiveImport(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.lookupWorkspace(w, r); !ok {
		return
	}
	id := r.PathValue("id")
	body, err := s.prepareArchiveImport(w, r, id)
	if err != nil {
		writeArchiveError(w, err)
		return
	}

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
		written, entryErr := s.importArchiveEntry(r.Context(), id, header, reader)
		if entryErr != nil {
			writeArchiveError(w, entryErr)
			return
		}
		if header.Typeflag == tar.TypeReg {
			files++
			bytes += written
		}
	}
	writeJSON(w, http.StatusOK, archiveImportResponse{Files: files, Bytes: bytes})
}

// archiveEntry is one workspace entry staged for export.
type archiveEntry struct {
	name  string
	dir   bool
	size  int64
	mode  int64
	mtime time.Time
}

// collectWorkspaceEntries walks the workspace depth-first and stages every
// exportable entry. Oversized or non-regular entries are skipped and counted
// so the response can report them before the body starts.
func (s *Server) collectWorkspaceEntries(ctx context.Context, id string) (entries []archiveEntry, skipped int, err error) {
	var walk func(dir string) error
	walk = func(dir string) error {
		names, err := s.provider.ReadDir(ctx, id, dir)
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
			info, err := s.provider.Stat(ctx, id, relative)
			if err != nil {
				return err
			}
			switch {
			case info.IsDir():
				entries = append(entries, archiveEntry{name: relative, dir: true, mtime: info.ModTime()})
				if err := walk(relative); err != nil {
					return err
				}
			case info.Mode().IsRegular():
				if info.Size() > maxFileBytes {
					skipped++
					continue
				}
				entries = append(entries, archiveEntry{
					name: relative, size: info.Size(),
					mode: int64(info.Mode().Perm()), mtime: info.ModTime(),
				})
			default:
				skipped++
			}
		}
		return nil
	}
	err = walk(".")
	return entries, skipped, err
}

// archiveExport streams the workspace as a tar response. It backs the explicit
// directory pull (ADR 0014). Oversized or non-regular entries are skipped and
// reported in X-Scraps-Skipped-Entries before the body starts.
func (s *Server) archiveExport(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.lookupWorkspace(w, r); !ok {
		return
	}
	id := r.PathValue("id")
	entries, skipped, err := s.collectWorkspaceEntries(r.Context(), id)
	if err != nil {
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
