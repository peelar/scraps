// Package client is the scrapd API client used by the scrap CLI.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
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
	// stream carries long-lived tunnel connections without the API timeout.
	stream *http.Client
}

// New builds a client for baseURL (e.g. http://127.0.0.1:8484).
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 15 * time.Minute},
		stream:  &http.Client{},
	}
}

// BaseURL returns the daemon URL used for browser callbacks.
func (c *Client) BaseURL() string { return c.baseURL }

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
	Image     string          `json:"image,omitempty"`
	Policy    provider.Policy `json:"policy"`
	Features  InfoFeatures    `json:"features"`
}

// InfoFeatures reports required worker capabilities.
type InfoFeatures struct {
	DurableRuns bool `json:"durableRuns"`
	ModelAuth   bool `json:"modelAuth"`
}

// Info fetches daemon identity.
func (c *Client) Info(ctx context.Context) (InfoResponse, error) {
	var info InfoResponse
	err := c.do(ctx, http.MethodGet, "/v1/info", nil, &info)
	return info, err
}

// GitHubAuthStart is a newly-created browser authorization flow.
type GitHubAuthStart struct {
	State      string `json:"state"`
	BrowserURL string `json:"browserUrl"`
}

// GitHubAuthStatus reports progress of the GitHub App installation.
type GitHubAuthStatus struct {
	State string `json:"state"`
	Error string `json:"error,omitempty"`
	App   string `json:"app,omitempty"`
}

// StartGitHubAuth creates a GitHub App manifest and installation flow.
func (c *Client) StartGitHubAuth(ctx context.Context) (GitHubAuthStart, error) {
	var started GitHubAuthStart
	// GitHub's App Manifest validator rejects numeric loopback hosts even though
	// they are valid URLs. The Lima forward is also reachable through localhost.
	callbackURL := c.baseURL
	if parsed, err := url.Parse(callbackURL); err == nil && (parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "::1") {
		port := parsed.Port()
		parsed.Host = "localhost"
		if port != "" {
			parsed.Host += ":" + port
		}
		callbackURL = parsed.String()
	}
	err := c.do(ctx, http.MethodPost, "/v1/auth/github/start", map[string]string{"callbackUrl": callbackURL}, &started)
	return started, err
}

// GitHubAuthStatus fetches browser authorization progress.
func (c *Client) GitHubAuthStatus(ctx context.Context, state string) (GitHubAuthStatus, error) {
	var status GitHubAuthStatus
	err := c.do(ctx, http.MethodGet, "/v1/auth/github/status/"+url.PathEscape(state), nil, &status)
	return status, err
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

// StartWorkspace starts a stopped workspace.
func (c *Client) StartWorkspace(ctx context.Context, id string) (workspace.Workspace, error) {
	var found workspace.Workspace
	err := c.do(ctx, http.MethodPost, "/v1/workspaces/"+id+"/start", nil, &found)
	return found, err
}

// StopWorkspace stops a running workspace.
func (c *Client) StopWorkspace(ctx context.Context, id string) (workspace.Workspace, error) {
	var found workspace.Workspace
	err := c.do(ctx, http.MethodPost, "/v1/workspaces/"+id+"/stop", nil, &found)
	return found, err
}

// DeleteWorkspace removes a workspace.
func (c *Client) DeleteWorkspace(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/workspaces/"+id, nil, nil)
}

// ArchiveResult reports what an archive import wrote.
type ArchiveResult struct {
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
}

// PushArchive streams a tar archive from r into the workspace. When replace
// is true the daemon clears the workspace first; otherwise the workspace must
// be empty. Uses the unbounded streaming transport like tunnels.
func (c *Client) PushArchive(ctx context.Context, id string, r io.Reader, replace bool) (ArchiveResult, error) {
	path := fmt.Sprintf("/v1/workspaces/%s/files/archive", url.PathEscape(id))
	if replace {
		path += "?replace=true"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, r)
	if err != nil {
		return ArchiveResult{}, fmt.Errorf("build push request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-tar")
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, err := c.stream.Do(request)
	if err != nil {
		return ArchiveResult{}, fmt.Errorf("push archive: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		return ArchiveResult{}, decodeErrorResponse(response)
	}
	var result ArchiveResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return ArchiveResult{}, fmt.Errorf("decode push response: %w", err)
	}
	return result, nil
}

// PullArchive streams the workspace archive into w and returns the number of
// entries the daemon skipped (oversized or non-regular files).
func (c *Client) PullArchive(ctx context.Context, id string, w io.Writer) (int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/v1/workspaces/%s/files/archive", c.baseURL, url.PathEscape(id)), nil)
	if err != nil {
		return 0, fmt.Errorf("build pull request: %w", err)
	}
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, err := c.stream.Do(request)
	if err != nil {
		return 0, fmt.Errorf("pull archive: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		return 0, decodeErrorResponse(response)
	}
	if _, err := io.Copy(w, response.Body); err != nil {
		return 0, fmt.Errorf("pull archive: %w", err)
	}
	skipped, _ := strconv.Atoi(response.Header.Get("X-Scraps-Skipped-Entries"))
	return skipped, nil
}

// decodeErrorResponse parses the structured error body of a failed stream
// request, falling back to the HTTP status line.
func decodeErrorResponse(response *http.Response) error {
	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.NewDecoder(response.Body).Decode(&payload) == nil && payload.Error.Code != "" {
		return &Error{Status: response.StatusCode, Code: payload.Error.Code, Message: payload.Error.Message}
	}
	return &Error{Status: response.StatusCode, Code: "http_error", Message: response.Status}
}

// PortInfo describes a TCP listener inside a workspace.
type PortInfo struct {
	Port    int    `json:"port"`
	Address string `json:"address"`
}

// WorkspacePorts lists TCP ports listening inside a workspace.
func (c *Client) WorkspacePorts(ctx context.Context, workspaceID string) ([]PortInfo, error) {
	var response struct {
		Ports []PortInfo `json:"ports"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/workspaces/"+url.PathEscape(workspaceID)+"/ports", nil, &response)
	return response.Ports, err
}

// Tunnel is a bidirectional byte stream to a workspace service. Bytes
// written are forwarded to the service; bytes read arrive from it.
type Tunnel struct {
	body  io.ReadCloser
	write io.WriteCloser
}

func (t *Tunnel) Read(b []byte) (int, error)  { return t.body.Read(b) }
func (t *Tunnel) Write(b []byte) (int, error) { return t.write.Write(b) }

// CloseWrite ends the client-to-service direction, like TCP half-close.
func (t *Tunnel) CloseWrite() error { return t.write.Close() }

// Close ends the tunnel in both directions.
func (t *Tunnel) Close() error {
	writeErr := t.write.Close()
	bodyErr := t.body.Close()
	if writeErr != nil {
		return writeErr
	}
	return bodyErr
}

// Tunnel connects to one loopback port inside a workspace over the same
// authenticated transport as every other API call. The request body streams
// toward the service and the response body streams back.
func (c *Client) Tunnel(ctx context.Context, workspaceID string, port int) (*Tunnel, error) {
	reader, writer := io.Pipe()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/v1/workspaces/%s/tunnel/%d", c.baseURL, url.PathEscape(workspaceID), port), reader)
	if err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("build tunnel request: %w", err)
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set("Accept-Encoding", "identity")
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, err := c.stream.Do(request)
	if err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("tunnel %s:%d: %w", workspaceID, port, err)
	}
	if response.StatusCode != http.StatusOK {
		defer response.Body.Close()
		_ = writer.Close()
		code, message := "http_error", response.Status
		var payload struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.NewDecoder(response.Body).Decode(&payload) == nil && payload.Error.Code != "" {
			code, message = payload.Error.Code, payload.Error.Message
		}
		return nil, &Error{Status: response.StatusCode, Code: code, Message: message}
	}
	return &Tunnel{body: response.Body, write: writer}, nil
}
