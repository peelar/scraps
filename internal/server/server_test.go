package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/peelar/scraps/internal/provider"
	"github.com/peelar/scraps/internal/workspace"
)

type testServer struct {
	*Server
	handler http.Handler
	dataDir string
}

func (ts *testServer) hostRoot(t *testing.T, id string) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(filepath.Join(ts.dataDir, "workspaces", id))
	if err != nil {
		t.Fatalf("resolve test workspace root: %v", err)
	}
	return root
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	return newTestServerWithToken(t, "")
}

func newTestServerWithToken(t *testing.T, token string) *testServer {
	t.Helper()
	dataDir := t.TempDir()
	server, err := New(Config{DataDir: dataDir, Token: token})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	t.Cleanup(server.Close)
	return &testServer{Server: server, handler: server.Handler(), dataDir: dataDir}
}

func (ts *testServer) do(t *testing.T, method, target string, body any, token string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, target, reader)
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	ts.handler.ServeHTTP(response, request)
	return response
}

func (ts *testServer) createWorkspace(t *testing.T, options workspace.CreateOptions) workspace.Workspace {
	t.Helper()
	response := ts.do(t, http.MethodPost, "/v1/workspaces", options, "")
	if response.Code != http.StatusCreated {
		t.Fatalf("create workspace: status %d: %s", response.Code, response.Body.String())
	}
	var created workspace.Workspace
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatalf("decode workspace: %v", err)
	}
	return created
}

type fakeProvider struct {
	provider.Provider
	workspaces []workspace.Workspace
}

func (f *fakeProvider) Info() provider.Info {
	return provider.Info{Name: "fake", Isolation: provider.IsolationVM}
}
func (f *fakeProvider) List(context.Context) ([]workspace.Workspace, error) {
	return f.workspaces, nil
}

func TestServerUsesInjectedProvider(t *testing.T) {
	dataDir := t.TempDir()
	fake := &fakeProvider{workspaces: []workspace.Workspace{{ID: "fake-one", State: "running", RootPath: "/workspace"}}}
	server, err := New(Config{DataDir: dataDir, Provider: fake})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	defer server.Close()

	request := httptest.NewRequest(http.MethodGet, "/v1/workspaces", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "fake-one") {
		t.Fatalf("list through fake provider = %d %s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dataDir, "workspaces")); !os.IsNotExist(err) {
		t.Fatalf("server created host workspace directory with injected provider: %v", err)
	}
}

func TestHealth(t *testing.T) {
	ts := newTestServer(t)

	response := ts.do(t, http.MethodGet, "/healthz", nil, "")
	if response.Code != http.StatusOK || response.Body.String() != "ok\n" {
		t.Fatalf("health = %d %q", response.Code, response.Body.String())
	}
}

func TestInfo(t *testing.T) {
	ts := newTestServer(t)

	response := ts.do(t, http.MethodGet, "/v1/info", nil, "")
	if response.Code != http.StatusOK {
		t.Fatalf("info status = %d", response.Code)
	}
	var body infoResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode info: %v", err)
	}
	if body.Name != "scrapd" {
		t.Fatalf("name = %q, want scrapd", body.Name)
	}
	if body.Provider != "directory" || body.Isolation != "none" {
		t.Fatalf("provider = %q isolation = %q, want directory/none", body.Provider, body.Isolation)
	}
	if body.Policy.Environment != "minimal" || body.Policy.Network != "host-unrestricted" || body.Policy.Resources != "host-unlimited" {
		t.Fatalf("policy = %+v", body.Policy)
	}
}

func TestAuth(t *testing.T) {
	ts := newTestServerWithToken(t, "secret")

	if response := ts.do(t, http.MethodGet, "/v1/workspaces", nil, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("no token status = %d, want 401", response.Code)
	}
	if response := ts.do(t, http.MethodGet, "/v1/workspaces", nil, "wrong"); response.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token status = %d, want 401", response.Code)
	}
	if response := ts.do(t, http.MethodGet, "/v1/workspaces", nil, "secret"); response.Code != http.StatusOK {
		t.Fatalf("correct token status = %d, want 200", response.Code)
	}
	// healthz stays open
	if response := ts.do(t, http.MethodGet, "/healthz", nil, ""); response.Code != http.StatusOK {
		t.Fatalf("health with no token status = %d", response.Code)
	}
}

func TestWorkspaceLifecycle(t *testing.T) {
	ts := newTestServer(t)

	created := ts.createWorkspace(t, workspace.CreateOptions{Project: "demo"})
	if created.ID == "" || created.State != "running" || created.RootPath != "/workspace" || created.PathContract != "workspace-relative-v1" {
		t.Fatalf("created = %+v", created)
	}

	response := ts.do(t, http.MethodGet, "/v1/workspaces/"+created.ID, nil, "")
	if response.Code != http.StatusOK {
		t.Fatalf("get status = %d", response.Code)
	}

	list := ts.do(t, http.MethodGet, "/v1/workspaces", nil, "")
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d", list.Code)
	}
	var listed struct{ Workspaces []workspace.Workspace }
	if err := json.NewDecoder(list.Body).Decode(&listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed.Workspaces) != 1 || listed.Workspaces[0].ID != created.ID {
		t.Fatalf("listed = %+v", listed.Workspaces)
	}

	if response := ts.do(t, http.MethodDelete, "/v1/workspaces/"+created.ID, nil, ""); response.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d", response.Code)
	}
	if response := ts.do(t, http.MethodGet, "/v1/workspaces/"+created.ID, nil, ""); response.Code != http.StatusNotFound {
		t.Fatalf("get after delete status = %d, want 404", response.Code)
	}
}

func TestCreateRejectsBadRepoURL(t *testing.T) {
	ts := newTestServer(t)

	response := ts.do(t, http.MethodPost, "/v1/workspaces", workspace.CreateOptions{RepoURL: "ssh://git@example.com/x.git"}, "")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", response.Code, response.Body.String())
	}
}

// runExec performs an exec request and returns the decoded event stream.
func runExec(t *testing.T, ts *testServer, id string, body map[string]any) []execEvent {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal exec: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/workspaces/"+id+"/exec", bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	ts.handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("exec status = %d: %s", response.Code, response.Body.String())
	}

	var events []execEvent
	decoder := json.NewDecoder(response.Body)
	for {
		var event execEvent
		if err := decoder.Decode(&event); err != nil {
			break
		}
		events = append(events, event)
	}
	if len(events) == 0 {
		t.Fatal("no exec events")
	}
	return events
}

func execOutput(events []execEvent) string {
	var builder strings.Builder
	for _, event := range events {
		if event.Type == "output" {
			decoded, _ := base64.StdEncoding.DecodeString(event.Data)
			builder.Write(decoded)
		}
	}
	return builder.String()
}

func TestExecStreamsOutputAndExitCode(t *testing.T) {
	ts := newTestServer(t)
	created := ts.createWorkspace(t, workspace.CreateOptions{})

	events := runExec(t, ts, created.ID, map[string]any{
		"command": "echo hello && echo err >&2 && exit 3",
	})

	if events[0].Type != "start" {
		t.Fatalf("first event = %+v, want start", events[0])
	}
	last := events[len(events)-1]
	if last.Type != "exit" || last.Code == nil || *last.Code != 3 {
		t.Fatalf("last event = %+v, want exit 3", last)
	}
	output := execOutput(events)
	if !strings.Contains(output, "hello") || !strings.Contains(output, "err") {
		t.Fatalf("output = %q", output)
	}
}

func TestExecUsesVirtualWorkspaceRoot(t *testing.T) {
	t.Setenv("SCRAPS_TEST_HOST_SECRET", "must-not-leak")
	ts := newTestServer(t)
	created := ts.createWorkspace(t, workspace.CreateOptions{})

	events := runExec(t, ts, created.ID, map[string]any{
		"command": "pwd; test -d /workspace; echo /workspace/file.txt; printf 'secret=%s\\n' \"${SCRAPS_TEST_HOST_SECRET-unset}\"; printf 'explicit=%s\\n' \"$EXPLICIT_SAFE\"; printf 'home=%s\\n' \"$HOME\"; printf 'root=%s\\n' \"$SCRAP_WORKSPACE_ROOT\"",
		"cwd":     ".",
		"env":     map[string]string{"EXPLICIT_SAFE": "present"},
	})
	output := execOutput(events)
	if output != "/workspace\n/workspace/file.txt\nsecret=unset\nexplicit=present\nhome=/workspace\nroot=/workspace\n" {
		t.Fatalf("virtualized output = %q", output)
	}
	if strings.Contains(output, ts.dataDir) {
		t.Fatalf("output leaked host data directory: %q", output)
	}
}

func TestExecTimeoutKillsProcess(t *testing.T) {
	ts := newTestServer(t)
	created := ts.createWorkspace(t, workspace.CreateOptions{})

	start := time.Now()
	events := runExec(t, ts, created.ID, map[string]any{
		"command":   "sleep 30",
		"timeoutMs": 200,
	})
	elapsed := time.Since(start)

	last := events[len(events)-1]
	if last.Type != "exit" || last.Code != nil || last.Reason != "timeout" {
		t.Fatalf("last event = %+v, want timeout exit", last)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("timeout took %v", elapsed)
	}
}

func TestExecRejectsOutsideCWD(t *testing.T) {
	ts := newTestServer(t)
	created := ts.createWorkspace(t, workspace.CreateOptions{})

	encoded, _ := json.Marshal(map[string]any{"command": "true", "cwd": "/etc"})
	request := httptest.NewRequest(http.MethodPost, "/v1/workspaces/"+created.ID+"/exec", bytes.NewReader(encoded))
	response := httptest.NewRecorder()
	ts.handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

func TestFileRoundTrip(t *testing.T) {
	ts := newTestServer(t)
	created := ts.createWorkspace(t, workspace.CreateOptions{})
	target := "src/app.ts"

	// stat before creation -> 404
	response := ts.do(t, http.MethodPost, "/v1/workspaces/"+created.ID+"/files/stat", pathRequest{Path: target}, "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("stat status = %d, want 404", response.Code)
	}

	// write
	response = ts.do(t, http.MethodPost, "/v1/workspaces/"+created.ID+"/files/write",
		fileWriteRequest{Path: target, Content: base64.StdEncoding.EncodeToString([]byte("binary\x00safe"))}, "")
	if response.Code != http.StatusOK {
		t.Fatalf("write status = %d: %s", response.Code, response.Body.String())
	}

	// read back
	response = ts.do(t, http.MethodPost, "/v1/workspaces/"+created.ID+"/files/read", pathRequest{Path: target}, "")
	if response.Code != http.StatusOK {
		t.Fatalf("read status = %d", response.Code)
	}
	var read fileReadResponse
	if err := json.NewDecoder(response.Body).Decode(&read); err != nil {
		t.Fatalf("decode read: %v", err)
	}
	decoded, _ := base64.StdEncoding.DecodeString(read.Content)
	if string(decoded) != "binary\x00safe" {
		t.Fatalf("content = %q", decoded)
	}

	// stat
	response = ts.do(t, http.MethodPost, "/v1/workspaces/"+created.ID+"/files/stat", pathRequest{Path: target}, "")
	if response.Code != http.StatusOK {
		t.Fatalf("stat status = %d", response.Code)
	}
	var stat fileStatResponse
	if err := json.NewDecoder(response.Body).Decode(&stat); err != nil {
		t.Fatalf("decode stat: %v", err)
	}
	if !stat.Exists || stat.IsDirectory || stat.Size != 11 {
		t.Fatalf("stat = %+v", stat)
	}

	// readdir
	response = ts.do(t, http.MethodPost, "/v1/workspaces/"+created.ID+"/files/readdir", pathRequest{Path: "src"}, "")
	if response.Code != http.StatusOK {
		t.Fatalf("readdir status = %d", response.Code)
	}
	var listing fileReaddirResponse
	if err := json.NewDecoder(response.Body).Decode(&listing); err != nil {
		t.Fatalf("decode readdir: %v", err)
	}
	if len(listing.Entries) != 1 || listing.Entries[0] != "app.ts" {
		t.Fatalf("entries = %+v", listing.Entries)
	}

	// access
	response = ts.do(t, http.MethodPost, "/v1/workspaces/"+created.ID+"/files/access", fileAccessRequest{Path: target, Want: "read"}, "")
	if response.Code != http.StatusOK {
		t.Fatalf("access status = %d", response.Code)
	}
}

func TestFilesRejectEscape(t *testing.T) {
	ts := newTestServer(t)
	created := ts.createWorkspace(t, workspace.CreateOptions{})

	escape := "../escape.txt"
	for method, target := range map[string]string{
		"read":    "/v1/workspaces/" + created.ID + "/files/read",
		"write":   "/v1/workspaces/" + created.ID + "/files/write",
		"readdir": "/v1/workspaces/" + created.ID + "/files/readdir",
	} {
		_ = method
		response := ts.do(t, http.MethodPost, target, pathRequest{Path: escape}, "")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s escape status = %d, want 400", target, response.Code)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(ts.hostRoot(t, created.ID)), "escape.txt")); err == nil {
		t.Fatal("escape file was written")
	}
}

func TestGlob(t *testing.T) {
	ts := newTestServer(t)
	created := ts.createWorkspace(t, workspace.CreateOptions{})
	for _, file := range []string{"a.ts", "b.js", "src/nested/a.ts"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(ts.hostRoot(t, created.ID), file)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ts.hostRoot(t, created.ID), file), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(ts.hostRoot(t, created.ID), "node_modules", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ts.hostRoot(t, created.ID), "node_modules", "pkg", "a.ts"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	response := ts.do(t, http.MethodPost, "/v1/workspaces/"+created.ID+"/files/glob",
		fileGlobRequest{Pattern: "**/*.ts"}, "")
	if response.Code != http.StatusOK {
		t.Fatalf("glob status = %d: %s", response.Code, response.Body.String())
	}
	var result fileGlobResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode glob: %v", err)
	}
	paths := strings.Join(result.Paths, " ")
	if !strings.Contains(paths, "a.ts") || !strings.Contains(paths, "src/nested/a.ts") {
		t.Fatalf("paths = %v", result.Paths)
	}
	for _, path := range result.Paths {
		if path == "a.ts" {
			continue // root-level match: ** must match zero segments
		}
		if strings.Contains(path, "node_modules") || strings.HasSuffix(path, "b.js") {
			t.Fatalf("unexpected path %q", path)
		}
	}
}

func TestGrep(t *testing.T) {
	ts := newTestServer(t)
	created := ts.createWorkspace(t, workspace.CreateOptions{})
	source := "one\ntwo needle three\nfour\nfive needle six"
	if err := os.WriteFile(filepath.Join(ts.hostRoot(t, created.ID), "doc.txt"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ts.hostRoot(t, created.ID), "other.txt"), []byte("nothing here"), 0o644); err != nil {
		t.Fatal(err)
	}

	response := ts.do(t, http.MethodPost, "/v1/workspaces/"+created.ID+"/files/grep",
		fileGrepRequest{Pattern: "needle", Context: 1}, "")
	if response.Code != http.StatusOK {
		t.Fatalf("grep status = %d: %s", response.Code, response.Body.String())
	}
	var result fileGrepResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode grep: %v", err)
	}
	if len(result.Matches) != 2 || result.LimitReached {
		t.Fatalf("matches = %+v", result.Matches)
	}
	first := result.Matches[0]
	if first.LineNumber != 2 || first.LineText != "two needle three" {
		t.Fatalf("first match = %+v", first)
	}
	if len(first.Lines) != 3 || !first.Lines[0].Match == false || first.Lines[1].Match != true {
		t.Fatalf("context lines = %+v", first.Lines)
	}

	// ignoreCase + literal
	response = ts.do(t, http.MethodPost, "/v1/workspaces/"+created.ID+"/files/grep",
		fileGrepRequest{Pattern: "NEEDLE", IgnoreCase: true, Literal: true}, "")
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode grep: %v", err)
	}
	if len(result.Matches) != 2 {
		t.Fatalf("case-insensitive matches = %+v", result.Matches)
	}

	// limit
	response = ts.do(t, http.MethodPost, "/v1/workspaces/"+created.ID+"/files/grep",
		fileGrepRequest{Pattern: "needle", Limit: 1}, "")
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode grep: %v", err)
	}
	if len(result.Matches) != 1 || !result.LimitReached {
		t.Fatalf("limited matches = %+v reached=%v", result.Matches, result.LimitReached)
	}

	// invalid regex
	response = ts.do(t, http.MethodPost, "/v1/workspaces/"+created.ID+"/files/grep",
		fileGrepRequest{Pattern: "("}, "")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid pattern status = %d, want 400", response.Code)
	}
}
