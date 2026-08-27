package main

import (
	"archive/tar"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// stubWorkspace answers the workspace-resolution request that runPush and
// runPull issue through resolveOpenWorkspace.
func stubWorkspace(response http.ResponseWriter, request *http.Request) bool {
	if request.Method == http.MethodGet && request.URL.Path == "/v1/workspaces/quiet-river" {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"id":"quiet-river","state":"running"}`))
		return true
	}
	return false
}

func TestRunPushStreamsDirectoryArchive(t *testing.T) {
	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"README.md":       "# demo",
		"assets/logo.svg": "<svg/>",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(source, filepath.FromSlash(name)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Scraps' internal directory is never pushed.
	if err := os.MkdirAll(filepath.Join(source, ".scrap"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".scrap", "state"), []byte("internal"), 0o644); err != nil {
		t.Fatal(err)
	}

	var pushed bytes.Buffer
	var contentType, query string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if stubWorkspace(response, request) {
			return
		}
		if request.URL.Path != "/v1/workspaces/quiet-river/files/archive" {
			t.Errorf("path = %s", request.URL.Path)
			response.WriteHeader(500)
			return
		}
		contentType = request.Header.Get("Content-Type")
		query = request.URL.RawQuery
		if _, err := io.Copy(&pushed, request.Body); err != nil {
			t.Fatal(err)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"files":2,"bytes":13}`))
	}))
	defer server.Close()
	t.Setenv("SCRAP_DAEMON_URL", server.URL)
	t.Setenv("SCRAP_TOKEN", "")

	if code := runPush([]string{"quiet-river", source}); code != 0 {
		t.Fatalf("runPush = %d", code)
	}
	if contentType != "application/x-tar" {
		t.Fatalf("content type = %q", contentType)
	}
	if query != "" {
		t.Fatalf("query = %q, want no replace", query)
	}

	entries := map[string]string{}
	reader := tar.NewReader(&pushed)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg {
			body, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			entries[header.Name] = string(body)
		}
	}
	if entries["README.md"] != "# demo" || entries["assets/logo.svg"] != "<svg/>" {
		t.Fatalf("pushed = %#v", entries)
	}
	if _, ok := entries[".scrap/state"]; ok {
		t.Fatalf("internal directory pushed: %#v", entries)
	}
}

func TestRunPushReplaceFlag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if stubWorkspace(response, request) {
			return
		}
		if request.URL.Query().Get("replace") != "true" {
			t.Fatalf("query = %q", request.URL.RawQuery)
		}
		_, _ = io.Copy(io.Discard, request.Body)
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"files":0,"bytes":0}`))
	}))
	defer server.Close()
	t.Setenv("SCRAP_DAEMON_URL", server.URL)

	source := t.TempDir()
	if code := runPush([]string{"--replace", "quiet-river", source}); code != 0 {
		t.Fatalf("runPush = %d", code)
	}
}

func TestRunPushRequiresDirectory(t *testing.T) {
	if code := runPush([]string{"quiet-river", filepath.Join(t.TempDir(), "missing")}); code != 1 {
		t.Fatalf("runPush = %d, want 1", code)
	}
}

func TestRunPullExtractsArchive(t *testing.T) {
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	if err := writer.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: "src/", Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"notes.txt": "hello", "src/main.go": "package main"} {
		if err := writer.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     name,
			Size:     int64(len(body)),
			Mode:     0o644,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if stubWorkspace(response, request) {
			return
		}
		if request.Method != http.MethodGet || request.URL.Path != "/v1/workspaces/quiet-river/files/archive" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
			response.WriteHeader(500)
			return
		}
		response.Header().Set("Content-Type", "application/x-tar")
		response.Header().Set("X-Scraps-Skipped-Entries", "2")
		_, _ = response.Write(buffer.Bytes())
	}))
	defer server.Close()
	t.Setenv("SCRAP_DAEMON_URL", server.URL)

	target := filepath.Join(t.TempDir(), "out")
	if code := runPull([]string{"quiet-river", target}); code != 0 {
		t.Fatalf("runPull = %d", code)
	}
	body, err := os.ReadFile(filepath.Join(target, "notes.txt"))
	if err != nil || string(body) != "hello" {
		t.Fatalf("notes.txt = %q, %v", body, err)
	}
	body, err = os.ReadFile(filepath.Join(target, "src", "main.go"))
	if err != nil || string(body) != "package main" {
		t.Fatalf("main.go = %q, %v", body, err)
	}
}

func TestRunPullRefusesNonEmptyTarget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if stubWorkspace(response, request) {
			return
		}
		t.Errorf("unexpected request: %s %s", request.Method, request.URL.Path)
		response.WriteHeader(500)
	}))
	defer server.Close()
	t.Setenv("SCRAP_DAEMON_URL", server.URL)

	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "occupied.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := runPull([]string{"quiet-river", target}); code != 1 {
		t.Fatalf("runPull = %d, want 1", code)
	}
}

func TestRunPullForceOverlaysExistingTarget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if stubWorkspace(response, request) {
			return
		}
		response.Header().Set("Content-Type", "application/x-tar")
		var buffer bytes.Buffer
		writer := tar.NewWriter(&buffer)
		body := "fresh"
		if err := writer.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "occupied.txt", Size: int64(len(body)), Mode: 0o644}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
		writer.Close()
		_, _ = response.Write(buffer.Bytes())
	}))
	defer server.Close()
	t.Setenv("SCRAP_DAEMON_URL", server.URL)

	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "occupied.txt"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := runPull([]string{"--force", "quiet-river", target}); code != 0 {
		t.Fatalf("runPull = %d", code)
	}
	body, err := os.ReadFile(filepath.Join(target, "occupied.txt"))
	if err != nil || string(body) != "fresh" {
		t.Fatalf("occupied.txt = %q, %v", body, err)
	}
}
