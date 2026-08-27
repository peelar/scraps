package server

import (
	"archive/tar"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/peelar/scraps/internal/workspace"
)

// pushArchive sends a raw tar body to the import endpoint.
func (ts *testServer) pushArchive(t *testing.T, id string, archive []byte, query string) *httptest.ResponseRecorder {
	t.Helper()
	target := "/v1/workspaces/" + id + "/files/archive" + query
	request := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(archive))
	request.Header.Set("Content-Type", "application/x-tar")
	request.Header.Set("Transfer-Encoding", "chunked")
	response := httptest.NewRecorder()
	ts.handler.ServeHTTP(response, request)
	return response
}

// pullArchive reads the export endpoint body into memory.
func (ts *testServer) pullArchive(t *testing.T, id string) (*httptest.ResponseRecorder, []*tar.Header, map[string]string) {
	t.Helper()
	response := ts.do(t, http.MethodGet, "/v1/workspaces/"+id+"/files/archive", nil, "")
	if response.Code != http.StatusOK {
		return response, nil, nil
	}
	reader := tar.NewReader(bytes.NewReader(response.Body.Bytes()))
	var headers []*tar.Header
	contents := map[string]string{}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read pulled archive: %v", err)
		}
		headers = append(headers, header)
		if header.Typeflag == tar.TypeReg {
			body, err := io.ReadAll(reader)
			if err != nil {
				t.Fatalf("read pulled entry: %v", err)
			}
			contents[header.Name] = string(body)
		}
	}
	return response, headers, contents
}

// buildArchive assembles a tar archive from ordered entries.
func buildArchive(t *testing.T, entries map[string]string, dirs ...string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, dir := range dirs {
		name := dir
		if !strings.HasSuffix(name, "/") {
			name += "/"
		}
		if err := writer.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: name, Mode: 0o755}); err != nil {
			t.Fatalf("write dir header: %v", err)
		}
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		body := entries[name]
		if err := writer.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     name,
			Size:     int64(len(body)),
			Mode:     0o644,
		}); err != nil {
			t.Fatalf("write header: %v", err)
		}
		if _, err := writer.Write([]byte(body)); err != nil {
			t.Fatalf("write body: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}
	return buffer.Bytes()
}

func TestArchiveRoundTrip(t *testing.T) {
	ts := newTestServer(t)
	created := ts.createWorkspace(t, workspace.CreateOptions{Project: "demo"})

	response := ts.pushArchive(t, created.ID, buildArchive(t, map[string]string{
		"hello.txt":   "hello",
		"src/main.go": "package main",
		".git/HEAD":   "ref: refs/heads/main",
	}, "src", ".git"), "")
	if response.Code != http.StatusOK {
		t.Fatalf("push = %d %s", response.Code, response.Body.String())
	}
	var imported archiveImportResponse
	if err := json.NewDecoder(response.Body).Decode(&imported); err != nil {
		t.Fatalf("decode push response: %v", err)
	}
	if imported.Files != 3 || imported.Bytes != 5+12+20 {
		t.Fatalf("push result = %+v", imported)
	}

	// Files landed inside the workspace, including the .git directory.
	root := ts.hostRoot(t, created.ID)
	for _, relative := range []string{"hello.txt", "src/main.go", ".git/HEAD"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(relative))); err != nil {
			t.Fatalf("imported file missing: %v", err)
		}
	}

	response, headers, contents := ts.pullArchive(t, created.ID)
	if response.Code != http.StatusOK {
		t.Fatalf("pull = %d %s", response.Code, response.Body.String())
	}
	if response.Header().Get("X-Scraps-Skipped-Entries") != "0" {
		t.Fatalf("skipped header = %q", response.Header().Get("X-Scraps-Skipped-Entries"))
	}
	if contents["hello.txt"] != "hello" || contents["src/main.go"] != "package main" {
		t.Fatalf("pulled contents = %+v", contents)
	}
	var directories int
	for _, header := range headers {
		if strings.HasPrefix(header.Name, ".scrap") {
			t.Fatalf("internal directory exported: %q", header.Name)
		}
		if header.Typeflag == tar.TypeDir {
			directories++
		}
	}
	if directories == 0 {
		t.Fatalf("export omitted directory entries: %+v", headers)
	}
}

func TestArchiveImportRequiresEmptyWorkspace(t *testing.T) {
	ts := newTestServer(t)
	created := ts.createWorkspace(t, workspace.CreateOptions{Project: "demo"})

	writeResponse := ts.do(t, http.MethodPost, "/v1/workspaces/"+created.ID+"/files/write",
		map[string]string{"path": "existing.txt", "content": base64.StdEncoding.EncodeToString([]byte("before"))}, "")
	if writeResponse.Code != http.StatusOK {
		t.Fatalf("seed workspace: %d %s", writeResponse.Code, writeResponse.Body.String())
	}

	response := ts.pushArchive(t, created.ID, buildArchive(t, map[string]string{"new.txt": "x"}), "")
	if response.Code != http.StatusConflict {
		t.Fatalf("push onto occupied workspace = %d %s", response.Code, response.Body.String())
	}

	response = ts.pushArchive(t, created.ID, buildArchive(t, map[string]string{"new.txt": "x"}), "?replace=true")
	if response.Code != http.StatusOK {
		t.Fatalf("replace push = %d %s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(filepath.Join(ts.hostRoot(t, created.ID), "existing.txt")); !os.IsNotExist(err) {
		t.Fatalf("replace kept old file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ts.hostRoot(t, created.ID), "new.txt")); err != nil {
		t.Fatalf("replace did not import: %v", err)
	}
}

func TestArchiveImportToleratesInternalDirectory(t *testing.T) {
	ts := newTestServer(t)
	created := ts.createWorkspace(t, workspace.CreateOptions{Project: "demo"})

	root := ts.hostRoot(t, created.ID)
	if err := os.MkdirAll(filepath.Join(root, ".scrap", "tmp"), 0o700); err != nil {
		t.Fatalf("seed .scrap: %v", err)
	}
	response := ts.pushArchive(t, created.ID, buildArchive(t, map[string]string{"main.py": "print(1)"}), "")
	if response.Code != http.StatusOK {
		t.Fatalf("push = %d %s", response.Code, response.Body.String())
	}
}

func TestArchiveImportRejectsUnsafeEntries(t *testing.T) {
	ts := newTestServer(t)
	created := ts.createWorkspace(t, workspace.CreateOptions{Project: "demo"})

	cases := map[string]struct {
		archive []byte
		message string
	}{
		"parent traversal": {
			archive: buildArchive(t, map[string]string{"../escape.txt": "x"}),
			message: "escapes the workspace",
		},
		"absolute path": {
			archive: buildArchive(t, map[string]string{"/etc/passwd": "x"}),
			message: "absolute path",
		},
		"internal directory": {
			archive: buildArchive(t, map[string]string{".scrap/evil.txt": "x"}),
			message: "reserved .scrap",
		},
	}
	for name, testCase := range cases {
		response := ts.pushArchive(t, created.ID, testCase.archive, "")
		if response.Code != 400 {
			t.Fatalf("%s: push = %d %s", name, response.Code, response.Body.String())
		}
		if !strings.Contains(response.Body.String(), testCase.message) {
			t.Fatalf("%s: message = %s", name, response.Body.String())
		}
	}

	// Symlink entries are rejected rather than recreated.
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	if err := writer.WriteHeader(&tar.Header{
		Typeflag: tar.TypeSymlink,
		Name:     "link",
		Linkname: "/etc/passwd",
		Mode:     0o777,
	}); err != nil {
		t.Fatalf("write symlink header: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}
	response := ts.pushArchive(t, created.ID, buffer.Bytes(), "")
	if response.Code != 400 || !strings.Contains(response.Body.String(), "regular files") {
		t.Fatalf("symlink push = %d %s", response.Code, response.Body.String())
	}
	// Nothing was written before the rejection.
	entries, err := os.ReadDir(ts.hostRoot(t, created.ID))
	if err != nil {
		t.Fatalf("read workspace: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("rejected archive wrote entries: %v", entries)
	}
}

func TestArchiveImportRejectsOversizedEntryHeader(t *testing.T) {
	ts := newTestServer(t)
	created := ts.createWorkspace(t, workspace.CreateOptions{Project: "demo"})

	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	if err := writer.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     "big.bin",
		Size:     maxFileBytes + 1,
		Mode:     0o644,
	}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if _, err := writer.Write(make([]byte, 64)); err != nil {
		t.Fatalf("write body: %v", err)
	}
	// The archive is intentionally malformed past the header: the server must
	// reject on the declared size before reading the body.
	_ = writer.Close()
	response := ts.pushArchive(t, created.ID, buffer.Bytes(), "")
	if response.Code != 400 || !strings.Contains(response.Body.String(), "100MB") {
		t.Fatalf("oversized push = %d %s", response.Code, response.Body.String())
	}
}

func TestArchiveImportRejectsWrongContentType(t *testing.T) {
	ts := newTestServer(t)
	created := ts.createWorkspace(t, workspace.CreateOptions{Project: "demo"})

	request := httptest.NewRequest(http.MethodPost, "/v1/workspaces/"+created.ID+"/files/archive",
		bytes.NewReader(buildArchive(t, map[string]string{"a.txt": "a"})))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	ts.handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("json push = %d %s", response.Code, response.Body.String())
	}
}

func TestArchiveExportEmptyWorkspace(t *testing.T) {
	ts := newTestServer(t)
	created := ts.createWorkspace(t, workspace.CreateOptions{Project: "demo"})

	response, headers, _ := ts.pullArchive(t, created.ID)
	if response.Code != http.StatusOK {
		t.Fatalf("pull = %d %s", response.Code, response.Body.String())
	}
	if len(headers) != 0 {
		t.Fatalf("empty workspace exported entries: %+v", headers)
	}
}

func TestCleanArchiveName(t *testing.T) {
	valid := map[string]string{
		"a.txt":         "a.txt",
		"./a.txt":       "a.txt",
		"dir/sub/f.txt": "dir/sub/f.txt",
		"dir/":          "dir",
		"a/./b.txt":     "a/b.txt",
	}
	for input, want := range valid {
		got, err := cleanArchiveName(input)
		if err != nil || got != want {
			t.Fatalf("cleanArchiveName(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	invalid := []string{"", ".", "/", "..", "../x", "a/../../x", ".scrap", ".scrap/tmp/x"}
	for _, input := range invalid {
		if _, err := cleanArchiveName(input); err == nil {
			t.Fatalf("cleanArchiveName(%q) = nil error, want rejection", input)
		}
	}
}

func TestArchiveImportSkipsArchiveRootEntry(t *testing.T) {
	ts := newTestServer(t)
	created := ts.createWorkspace(t, workspace.CreateOptions{Project: "demo"})

	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	if err := writer.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: "./", Mode: 0o755}); err != nil {
		t.Fatalf("write root header: %v", err)
	}
	if err := writer.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "./only.txt", Size: 2, Mode: 0o644}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if _, err := writer.Write([]byte("ok")); err != nil {
		t.Fatalf("write body: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	response := ts.pushArchive(t, created.ID, buffer.Bytes(), "")
	if response.Code != http.StatusOK {
		t.Fatalf("push = %d %s", response.Code, response.Body.String())
	}
	body, err := os.ReadFile(filepath.Join(ts.hostRoot(t, created.ID), "only.txt"))
	if err != nil || string(body) != "ok" {
		t.Fatalf("only.txt = %q, %v", body, err)
	}
}
