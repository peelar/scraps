package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRunNewPassesRepositoryAndProject(t *testing.T) {
	var body map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/workspaces" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusCreated)
		_, _ = response.Write([]byte(`{"id":"quiet-river","state":"running","repoUrl":"https://github.com/owner/repo.git"}`))
	}))
	defer server.Close()
	t.Setenv("SCRAP_DAEMON_URL", server.URL)
	t.Setenv("SCRAP_TOKEN", "")

	if code := runNew([]string{"--repo", "git@github.com:owner/repo.git", "demo"}); code != 0 {
		t.Fatalf("runNew = %d", code)
	}
	if body["project"] != "demo" || body["repoUrl"] != "git@github.com:owner/repo.git" {
		t.Fatalf("body = %#v", body)
	}
}

func TestRunNewRejectsExtraArguments(t *testing.T) {
	if code := runNew([]string{"one", "two"}); code != 2 {
		t.Fatalf("runNew = %d, want 2", code)
	}
}
