// Package client is the scrapd API client used by the scrap CLI.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/peelar/scraps/internal/provider"
	"github.com/peelar/scraps/internal/workspace"
)

// Client talks to a scrapd daemon.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New builds a client for baseURL (e.g. http://127.0.0.1:8484).
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 15 * time.Minute},
	}
}

// Error is a structured API error response.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}

	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("request %s %s: %w", method, path, err)
	}
	defer response.Body.Close()

	if response.StatusCode >= 400 {
		var payload struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.NewDecoder(response.Body).Decode(&payload); err == nil && payload.Error.Code != "" {
			return &Error{Status: response.StatusCode, Code: payload.Error.Code, Message: payload.Error.Message}
		}
		return &Error{Status: response.StatusCode, Code: "http_error", Message: response.Status}
	}
	if out != nil {
		if err := json.NewDecoder(response.Body).Decode(out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// Ping checks daemon reachability.
func (c *Client) Ping(ctx context.Context) error {
	info, err := c.Info(ctx)
	if err != nil {
		return err
	}
	if info.Name != "scrapd" {
		return fmt.Errorf("unexpected daemon %q at %s", info.Name, c.baseURL)
	}
	return nil
}

// InfoResponse is the daemon identity payload from /v1/info.
type InfoResponse struct {
	Name      string          `json:"name"`
	Version   string          `json:"version"`
	Commit    string          `json:"commit"`
	DataDir   string          `json:"dataDir"`
	StartedAt string          `json:"startedAt"`
	PID       int             `json:"pid"`
	Provider  string          `json:"provider"`
	Isolation string          `json:"isolation"`
	Policy    provider.Policy `json:"policy"`
}

// Info fetches daemon identity.
func (c *Client) Info(ctx context.Context) (InfoResponse, error) {
	var info InfoResponse
	err := c.do(ctx, http.MethodGet, "/v1/info", nil, &info)
	return info, err
}

// CreateWorkspace creates a workspace, cloning repoURL when given.
func (c *Client) CreateWorkspace(ctx context.Context, project, repoURL string) (workspace.Workspace, error) {
	var created workspace.Workspace
	err := c.do(ctx, http.MethodPost, "/v1/workspaces", map[string]string{
		"project": project,
		"repoUrl": repoURL,
	}, &created)
	return created, err
}

// GetWorkspace fetches a workspace by ID.
func (c *Client) GetWorkspace(ctx context.Context, id string) (workspace.Workspace, error) {
	var found workspace.Workspace
	err := c.do(ctx, http.MethodGet, "/v1/workspaces/"+id, nil, &found)
	return found, err
}

// ListWorkspaces returns all workspaces.
func (c *Client) ListWorkspaces(ctx context.Context) ([]workspace.Workspace, error) {
	var listed struct {
		Workspaces []workspace.Workspace `json:"workspaces"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/workspaces", nil, &listed)
	return listed.Workspaces, err
}

// DeleteWorkspace removes a workspace.
func (c *Client) DeleteWorkspace(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/workspaces/"+id, nil, nil)
}
