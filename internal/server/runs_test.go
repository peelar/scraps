package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/peelar/scraps/internal/store"
	"github.com/peelar/scraps/internal/testprovider"
	"github.com/peelar/scraps/internal/workspace"
)

type recordingRunner struct {
	started chan RunRequest
	release chan struct{}
}

func (r *recordingRunner) Execute(ctx context.Context, request RunRequest, emit func(json.RawMessage) error) error {
	r.started <- request
	select {
	case <-r.release:
		return emit(json.RawMessage(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`))
	case <-ctx.Done():
		return ctx.Err()
	}
}

func newRunTestServer(t *testing.T, runner RunExecutor) *testServer {
	return newRunTestServerWithAuth(t, runner, true)
}

func newRunTestServerWithAuth(t *testing.T, runner RunExecutor, modelAuth bool) *testServer {
	t.Helper()
	dataDir := t.TempDir()
	providerStore, err := store.Open(filepath.Join(dataDir, "scrapd.db"))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := testprovider.NewDirectory(providerStore, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{DataDir: dataDir, Provider: runtime, Runner: runner, ModelAuthConfigured: modelAuth})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close(); providerStore.Close() })
	return &testServer{Server: server, handler: server.Handler(), dataDir: dataDir}
}

func TestImportSessionSnapshotCreatesOneAuthoritativeRemoteSession(t *testing.T) {
	dir := t.TempDir()
	first := json.RawMessage(`[{"type":"message","id":"a1b2c3d4","parentId":null,"timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","content":"existing context","timestamp":1}}]`)
	if err := importSessionSnapshot(dir, "local-session", first); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatalf("session files = %v, %v", files, err)
	}
	contents, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), `"cwd":"/workspace"`) || !strings.Contains(string(contents), "existing context") {
		t.Fatalf("imported session = %s", contents)
	}
	second := json.RawMessage(`[{"type":"message","id":"ffffffff","parentId":null,"message":{"role":"user","content":"must not replace remote authority"}}]`)
	if err := importSessionSnapshot(dir, "local-session", second); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(files[0])
	if string(after) != string(contents) {
		t.Fatal("existing remote session was replaced by a later local snapshot")
	}
}

func TestRunAPIContinuesAfterCreateRequestEndsAndReplaysEvents(t *testing.T) {
	runner := &recordingRunner{started: make(chan RunRequest, 1), release: make(chan struct{})}
	ts := newRunTestServer(t, runner)
	ws := ts.createWorkspace(t, workspace.CreateOptions{Project: "demo"})

	createdResponse := ts.do(t, http.MethodPost, "/v1/workspaces/"+ws.ID+"/runs", map[string]any{
		"prompt": "do the work", "sessionKey": "local-session-1",
		"sessionSnapshot": []map[string]any{{"type": "message", "id": "a1b2c3d4", "parentId": nil, "message": map[string]any{"role": "user", "content": "prior context"}}},
	}, "")
	if createdResponse.Code != http.StatusAccepted {
		t.Fatalf("create = %d: %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created runResponse
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}

	select {
	case request := <-runner.started:
		if request.Prompt != "do the work" || request.WorkspaceID != ws.ID || !strings.Contains(string(request.SessionSnapshot), "prior context") {
			t.Fatalf("request = %+v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("runner did not start")
	}
	close(runner.release)

	deadline := time.Now().Add(2 * time.Second)
	for {
		response := ts.do(t, http.MethodGet, "/v1/runs/"+created.ID, nil, "")
		if response.Code != http.StatusOK {
			t.Fatalf("get = %d", response.Code)
		}
		var run runResponse
		if err := json.NewDecoder(response.Body).Decode(&run); err != nil {
			t.Fatal(err)
		}
		if run.State == "succeeded" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run stayed %s", run.State)
		}
		time.Sleep(10 * time.Millisecond)
	}

	eventsResponse := ts.do(t, http.MethodGet, "/v1/runs/"+created.ID+"/events?after=0", nil, "")
	if eventsResponse.Code != http.StatusOK {
		t.Fatalf("events = %d: %s", eventsResponse.Code, eventsResponse.Body.String())
	}
	var events struct {
		Events []struct {
			Sequence int64           `json:"sequence"`
			Data     json.RawMessage `json:"data"`
		} `json:"events"`
	}
	if err := json.NewDecoder(eventsResponse.Body).Decode(&events); err != nil {
		t.Fatal(err)
	}
	if len(events.Events) != 1 || events.Events[0].Sequence != 1 {
		t.Fatalf("events = %+v", events.Events)
	}

	after := ts.do(t, http.MethodGet, "/v1/runs/"+created.ID+"/events?after=1", nil, "")
	var empty struct {
		Events []json.RawMessage `json:"events"`
	}
	if err := json.NewDecoder(after.Body).Decode(&empty); err != nil {
		t.Fatal(err)
	}
	if len(empty.Events) != 0 {
		t.Fatalf("events after cursor = %d", len(empty.Events))
	}
}

func TestRunAPICancelsDetachedExecution(t *testing.T) {
	runner := &recordingRunner{started: make(chan RunRequest, 1), release: make(chan struct{})}
	ts := newRunTestServer(t, runner)
	ws := ts.createWorkspace(t, workspace.CreateOptions{Project: "cancel"})
	createdResponse := ts.do(t, http.MethodPost, "/v1/workspaces/"+ws.ID+"/runs", map[string]string{
		"prompt": "keep working", "sessionKey": "cancel-session",
	}, "")
	var created runResponse
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("runner did not start")
	}

	cancelled := ts.do(t, http.MethodPost, "/v1/runs/"+created.ID+"/cancel", nil, "")
	if cancelled.Code != http.StatusAccepted {
		t.Fatalf("cancel = %d: %s", cancelled.Code, cancelled.Body.String())
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		response := ts.do(t, http.MethodGet, "/v1/runs/"+created.ID, nil, "")
		var run runResponse
		if err := json.NewDecoder(response.Body).Decode(&run); err != nil {
			t.Fatal(err)
		}
		if run.State == "cancelled" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run stayed %s", run.State)
		}
		time.Sleep(10 * time.Millisecond)
	}

	again := ts.do(t, http.MethodPost, "/v1/runs/"+created.ID+"/cancel", nil, "")
	if again.Code != http.StatusOK {
		t.Fatalf("idempotent cancel = %d: %s", again.Code, again.Body.String())
	}
}

func TestRunAPIRequiresModelAuthorization(t *testing.T) {
	runner := &recordingRunner{started: make(chan RunRequest, 1), release: make(chan struct{})}
	ts := newRunTestServerWithAuth(t, runner, false)
	ws := ts.createWorkspace(t, workspace.CreateOptions{})
	response := ts.do(t, http.MethodPost, "/v1/workspaces/"+ws.ID+"/runs", map[string]string{
		"prompt": "work", "sessionKey": "session",
	}, "")
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "model_auth_unavailable") {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
}

func TestRunAPIIsExplicitlyUnavailableWithoutRunner(t *testing.T) {
	ts := newTestServer(t)
	ws := ts.createWorkspace(t, workspace.CreateOptions{})
	response := ts.do(t, http.MethodPost, "/v1/workspaces/"+ws.ID+"/runs", map[string]string{
		"prompt": "work", "sessionKey": "session",
	}, "")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
}

// The SSE stream must deliver persisted events the moment they land and
// close the connection once the run settles; clients rely on both to render
// live progress and to stop following.
func TestRunEventStreamPushesEventsAndClosesOnTerminalState(t *testing.T) {
	runner := &recordingRunner{started: make(chan RunRequest, 1), release: make(chan struct{})}
	ts := newRunTestServer(t, runner)
	ws := ts.createWorkspace(t, workspace.CreateOptions{Project: "demo"})

	createdResponse := ts.do(t, http.MethodPost, "/v1/workspaces/"+ws.ID+"/runs", map[string]any{
		"prompt": "stream please", "sessionKey": "local-session-1",
	}, "")
	if createdResponse.Code != http.StatusAccepted {
		t.Fatalf("create = %d: %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created runResponse
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}

	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("runner did not start")
	}

	api := httptest.NewServer(ts.handler)
	t.Cleanup(api.Close)

	request, err := http.NewRequest(http.MethodGet, api.URL+"/v1/runs/"+created.ID+"/events/stream?after=0", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := api.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if contentType := response.Header.Get("Content-Type"); contentType != "text/event-stream" {
		t.Fatalf("content type = %q", contentType)
	}
	// Headers arrive before the first event. Releasing the runner now verifies
	// that the append wakes the already-blocked stream without a poll interval.
	close(runner.release)

	frames := make(chan string, 64)
	go func() {
		defer close(frames)
		scanner := bufio.NewScanner(response.Body)
		for scanner.Scan() {
			frames <- scanner.Text()
			if strings.HasPrefix(scanner.Text(), "event: done") {
				return
			}
		}
	}()

	var sawEventID, sawEventData, sawDone bool
	deadline := time.After(5 * time.Second)
	for {
		select {
		case frame, ok := <-frames:
			if !ok {
				t.Fatalf("stream ended before done frame (sawEventID=%v sawEventData=%v sawDone=%v)", sawEventID, sawEventData, sawDone)
			}
			switch {
			case strings.HasPrefix(frame, "id: "):
				sawEventID = true
			case strings.HasPrefix(frame, `data: {"sequence":1,"data":`):
				if !strings.Contains(frame, "message_end") {
					t.Fatalf("event frame = %q", frame)
				}
				sawEventData = true
			case frame == "event: done":
				sawDone = true
			}
			if sawDone {
				if !sawEventID || !sawEventData {
					t.Fatalf("stream closed without full payload (id=%v data=%v)", sawEventID, sawEventData)
				}
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for done frame (sawEventID=%v sawEventData=%v sawDone=%v)", sawEventID, sawEventData, sawDone)
		}
	}
}

// A reconnecting client resumes exactly at its cursor: no replay of already
// seen events, and the stream still terminates once the run settles.
func TestRunEventStreamResumesFromCursor(t *testing.T) {
	runner := &recordingRunner{started: make(chan RunRequest, 1), release: make(chan struct{})}
	ts := newRunTestServer(t, runner)
	ws := ts.createWorkspace(t, workspace.CreateOptions{Project: "demo"})
	createdResponse := ts.do(t, http.MethodPost, "/v1/workspaces/"+ws.ID+"/runs", map[string]any{
		"prompt": "resume please", "sessionKey": "local-session-1",
	}, "")
	var created runResponse
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("runner did not start")
	}
	close(runner.release)

	deadline := time.Now().Add(2 * time.Second)
	for {
		response := ts.do(t, http.MethodGet, "/v1/runs/"+created.ID, nil, "")
		var run runResponse
		if err := json.NewDecoder(response.Body).Decode(&run); err != nil {
			t.Fatal(err)
		}
		if run.State == "succeeded" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run stayed %s", run.State)
		}
		time.Sleep(10 * time.Millisecond)
	}

	api := httptest.NewServer(ts.handler)
	t.Cleanup(api.Close)
	request, err := http.NewRequest(http.MethodGet, api.URL+"/v1/runs/"+created.ID+"/events/stream?after=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := api.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	scanner := bufio.NewScanner(response.Body)
	sawReplay := false
	sawDone := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: {\"sequence\":1,") {
			sawReplay = true
		}
		if line == "event: done" {
			sawDone = true
			break
		}
	}
	if sawReplay {
		t.Fatal("stream replayed events at or before the cursor")
	}
	if !sawDone {
		t.Fatal("stream did not terminate after the run settled")
	}
}
