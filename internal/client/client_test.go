package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/peelar/scraps/internal/server"
	"github.com/peelar/scraps/internal/store"
	"github.com/peelar/scraps/internal/testprovider"
)

func newTestClient(t *testing.T, token string) *Client {
	t.Helper()
	dataDir := t.TempDir()
	st, err := store.Open(filepath.Join(dataDir, "scrapd.db"))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	runtime, err := testprovider.NewDirectory(st, dataDir)
	if err != nil {
		st.Close()
		t.Fatalf("new test provider: %v", err)
	}
	apiServer, err := server.New(server.Config{DataDir: dataDir, Token: token, Provider: runtime})
	if err != nil {
		st.Close()
		t.Fatalf("new server: %v", err)
	}
	t.Cleanup(func() { apiServer.Close(); st.Close() })
	testServer := httptest.NewServer(apiServer.Handler())
	t.Cleanup(testServer.Close)
	return New(testServer.URL, token)
}

func TestPingAndLifecycle(t *testing.T) {
	client := newTestClient(t, "")

	if err := client.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}

	created, err := client.CreateWorkspace(context.Background(), "demo", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == "" || created.State != "running" {
		t.Fatalf("created = %+v", created)
	}

	listed, err := client.ListWorkspaces(context.Background())
	if err != nil || len(listed) != 1 {
		t.Fatalf("list = %+v err = %v", listed, err)
	}

	found, err := client.GetWorkspace(context.Background(), created.ID)
	if err != nil || found.ID != created.ID {
		t.Fatalf("get = %+v err = %v", found, err)
	}

	if err := client.DeleteWorkspace(context.Background(), created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := client.GetWorkspace(context.Background(), created.ID); err == nil {
		t.Fatal("get after delete succeeded")
	}
}

func TestGitHubAuthUsesGitHubCompatibleLoopbackURL(t *testing.T) {
	var callbackURL string
	apiServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		var body struct {
			CallbackURL string `json:"callbackUrl"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		callbackURL = body.CallbackURL
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"state":"flow","browserUrl":"https://example.test"}`))
	}))
	defer apiServer.Close()
	if _, err := New(apiServer.URL, "").StartGitHubAuth(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(callbackURL, "127.0.0.1") || !strings.Contains(callbackURL, "localhost:") {
		t.Fatalf("callback URL = %q", callbackURL)
	}
}

func TestTokenRequired(t *testing.T) {
	apiServer, err := server.New(server.Config{DataDir: t.TempDir(), Token: "secret"})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	t.Cleanup(apiServer.Close)
	testServer := httptest.NewServer(apiServer.Handler())
	t.Cleanup(testServer.Close)

	client := New(testServer.URL, "") // no token
	err = client.Ping(context.Background())
	if err == nil {
		t.Fatal("ping without token succeeded")
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != "unauthorized" {
		t.Fatalf("err = %v, want unauthorized API error", err)
	}
}

func TestExecStreamDecoding(t *testing.T) {
	apiServer, err := server.New(server.Config{DataDir: t.TempDir(), Token: ""})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	t.Cleanup(apiServer.Close)
	testServer := httptest.NewServer(apiServer.Handler())
	t.Cleanup(testServer.Close)
	client := New(testServer.URL, "")

	created, err := client.CreateWorkspace(context.Background(), "demo", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// The CLI does not exec, but the stream contract is exercised here so the
	// client package doubles as an API conformance check.
	request, _ := http.NewRequest(http.MethodPost,
		testServer.URL+"/v1/workspaces/"+created.ID+"/exec",
		strings.NewReader(`{"command":"echo hi"}`))
	request.Header.Set("Content-Type", "application/json")
	response, err := testServer.Client().Do(request)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	defer response.Body.Close()

	var types []string
	var output strings.Builder
	decoder := json.NewDecoder(response.Body)
	for {
		var event struct {
			Type   string `json:"type"`
			Stream string `json:"stream"`
			Data   string `json:"data"`
			Code   *int   `json:"code"`
		}
		if err := decoder.Decode(&event); err != nil {
			break
		}
		types = append(types, event.Type)
		if event.Type == "output" {
			decoded, _ := base64.StdEncoding.DecodeString(event.Data)
			output.Write(decoded)
		}
		if event.Type == "exit" && event.Code != nil && *event.Code != 0 {
			t.Fatalf("exit code = %d", *event.Code)
		}
	}
	if len(types) < 3 || types[0] != "start" || types[len(types)-1] != "exit" {
		t.Fatalf("event types = %v", types)
	}
	if !strings.Contains(output.String(), "hi") {
		t.Fatalf("output = %q", output.String())
	}
}
