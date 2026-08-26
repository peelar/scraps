// Package server implements the scrapd HTTP API.
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/peelar/scraps/internal/provider"
	"github.com/peelar/scraps/internal/store"
	"github.com/peelar/scraps/internal/version"
	"github.com/peelar/scraps/internal/workspace"
)

// Config configures the scrapd HTTP server.
type Config struct {
	// DataDir holds the SQLite database and workspace directories.
	DataDir string
	// Token, when set, requires `Authorization: Bearer <token>` on /v1.
	Token string
	// Provider overrides provider construction, primarily for tests.
	Provider provider.Provider
	// OpenShellImage overrides the image used for OpenShell sandboxes.
	OpenShellImage string
}

// Server wires a workspace provider and HTTP routes.
type Server struct {
	provider provider.Provider
	token    string
	handler  http.Handler
	dataDir  string
	started  time.Time
	shutdown func()
	pool     *readyPool
}

// New opens the store under the data dir and builds the HTTP handler.
func New(config Config) (*Server, error) {
	if config.DataDir == "" {
		return nil, errors.New("server: data dir is required")
	}
	if err := os.MkdirAll(config.DataDir, 0o755); err != nil {
		return nil, errors.New("server: create data dir")
	}

	runtime := config.Provider
	shutdown := func() {}
	if runtime == nil {
		st, err := store.Open(filepath.Join(config.DataDir, "scrapd.db"))
		if err != nil {
			return nil, err
		}
		runtime, err = provider.NewOpenShell(context.Background(), st, config.OpenShellImage)
		if err != nil {
			st.Close()
			return nil, err
		}
		shutdown = func() { st.Close() }
	}

	server := &Server{
		provider: runtime,
		token:    config.Token,
		dataDir:  config.DataDir,
		started:  time.Now(),
		shutdown: shutdown,
	}
	server.pool = newReadyPool(runtime)
	server.handler = server.routes()
	return server, nil
}

// Close releases server resources.
func (s *Server) Close() {
	s.pool.close()
	s.shutdown()
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("GET /v1/info", s.requireAuth(info(s)))

	mux.HandleFunc("POST /v1/workspaces", s.requireAuth(s.createWorkspace))
	mux.HandleFunc("GET /v1/workspaces", s.requireAuth(s.listWorkspaces))
	mux.HandleFunc("GET /v1/workspaces/{id}", s.requireAuth(s.getWorkspace))
	mux.HandleFunc("POST /v1/workspaces/{id}/start", s.requireAuth(s.startWorkspace))
	mux.HandleFunc("POST /v1/workspaces/{id}/stop", s.requireAuth(s.stopWorkspace))
	mux.HandleFunc("DELETE /v1/workspaces/{id}", s.requireAuth(s.deleteWorkspace))

	mux.HandleFunc("POST /v1/workspaces/{id}/exec", s.requireAuth(s.execCommand))

	mux.HandleFunc("POST /v1/workspaces/{id}/files/read", s.requireAuth(s.fileRead))
	mux.HandleFunc("POST /v1/workspaces/{id}/files/write", s.requireAuth(s.fileWrite))
	mux.HandleFunc("POST /v1/workspaces/{id}/files/mkdir", s.requireAuth(s.fileMkdir))
	mux.HandleFunc("POST /v1/workspaces/{id}/files/stat", s.requireAuth(s.fileStat))
	mux.HandleFunc("POST /v1/workspaces/{id}/files/access", s.requireAuth(s.fileAccess))
	mux.HandleFunc("POST /v1/workspaces/{id}/files/readdir", s.requireAuth(s.fileReaddir))

	mux.HandleFunc("POST /v1/workspaces/{id}/files/glob", s.requireAuth(s.fileGlob))
	mux.HandleFunc("POST /v1/workspaces/{id}/files/grep", s.requireAuth(s.fileGrep))
	return mux
}

func health(response http.ResponseWriter, _ *http.Request) {
	response.Header().Set("Content-Type", "text/plain; charset=utf-8")
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write([]byte("ok\n"))
}

func info(server *Server) http.HandlerFunc {
	return func(response http.ResponseWriter, _ *http.Request) {
		providerInfo := server.provider.Info()
		writeJSON(response, http.StatusOK, infoResponse{
			Name:      "scrapd",
			Version:   version.Version,
			Commit:    version.Commit,
			DataDir:   server.dataDir,
			StartedAt: server.started.UTC().Format(time.RFC3339),
			PID:       os.Getpid(),
			Provider:  providerInfo.Name,
			Isolation: string(providerInfo.Isolation),
			Image:     providerInfo.Image,
			Policy:    providerInfo.Policy,
		})
	}
}

type infoResponse struct {
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
}

// requireAuth enforces the bearer token when one is configured.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		if s.token != "" {
			provided := request.Header.Get("Authorization")
			const prefix = "Bearer "
			if !strings.HasPrefix(provided, prefix) ||
				subtle.ConstantTimeCompare([]byte(provided[len(prefix):]), []byte(s.token)) != 1 {
				writeError(response, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
				return
			}
		}
		next(response, request)
	}
}

// apiError carries an HTTP status and error code through handler chains.
type apiError struct {
	status  int
	code    string
	message string
}

func (e *apiError) Error() string { return e.message }

func writeError(response http.ResponseWriter, status int, code, message string) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}

// writeAPIError maps known errors onto the HTTP error shape.
func writeAPIError(response http.ResponseWriter, err error) {
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		writeError(response, apiErr.status, apiErr.code, apiErr.message)
		return
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(response, http.StatusNotFound, "not_found", "workspace not found")
	case errors.Is(err, workspace.ErrOutsideRoot):
		writeError(response, http.StatusBadRequest, "invalid_path", err.Error())
	default:
		slog.Error("internal error", "error", err)
		writeError(response, http.StatusInternalServerError, "internal", "internal server error")
	}
}

func writeJSON(response http.ResponseWriter, status int, body any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(body)
}
