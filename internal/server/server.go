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
	"sync"
	"time"

	"github.com/peelar/scraps/internal/githubapp"
	"github.com/peelar/scraps/internal/provider"
	"github.com/peelar/scraps/internal/schedule"
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
	// Runner overrides durable Pi execution, primarily for tests.
	Runner RunExecutor
	// PiCommand and PiExtensionPath enable the built-in durable runner.
	PiCommand       string
	PiExtensionPath string
	PiProfilePath   string
	// DaemonURL is the loopback URL remote Pi processes use for workspace tools.
	DaemonURL string
	// ModelAuthConfigured reports whether the trusted runner can authenticate a model.
	ModelAuthConfigured bool
}

// Server wires a workspace provider and HTTP routes.
type Server struct {
	provider            provider.Provider
	token               string
	handler             http.Handler
	dataDir             string
	started             time.Time
	pool                *readyPool
	github              *githubapp.Manager
	store               *store.Store
	clock               *schedule.Engine
	runner              RunExecutor
	runContext          context.Context
	cancelRuns          context.CancelFunc
	runWG               sync.WaitGroup
	runMu               sync.Mutex
	activeRuns          map[string]string
	runCancels          map[string]context.CancelFunc
	modelAuthConfigured bool
}

// New opens the store under the data dir and builds the HTTP handler.
func New(config Config) (*Server, error) {
	if config.DataDir == "" {
		return nil, errors.New("server: data dir is required")
	}
	if err := os.MkdirAll(config.DataDir, 0o755); err != nil {
		return nil, errors.New("server: create data dir")
	}

	st, err := store.Open(filepath.Join(config.DataDir, "scrapd.db"))
	if err != nil {
		return nil, err
	}
	if _, err := st.ReconcileInterruptedRuns(context.Background()); err != nil {
		st.Close()
		return nil, err
	}
	runtime := config.Provider
	if runtime == nil {
		runtime, err = provider.NewOpenShell(context.Background(), st, config.OpenShellImage)
		if err != nil {
			st.Close()
			return nil, err
		}
	}

	github, err := githubapp.New(config.DataDir)
	if err != nil {
		st.Close()
		return nil, err
	}
	runContext, cancelRuns := context.WithCancel(context.Background())
	server := &Server{
		provider:            runtime,
		token:               config.Token,
		dataDir:             config.DataDir,
		started:             time.Now(),
		github:              github,
		store:               st,
		runner:              config.Runner,
		runContext:          runContext,
		cancelRuns:          cancelRuns,
		activeRuns:          make(map[string]string),
		runCancels:          make(map[string]context.CancelFunc),
		modelAuthConfigured: config.ModelAuthConfigured,
	}
	if server.runner == nil && config.PiCommand != "" && config.PiExtensionPath != "" {
		daemonURL := config.DaemonURL
		if daemonURL == "" {
			daemonURL = "http://127.0.0.1:8484"
		}
		profilePath := config.PiProfilePath
		if profilePath == "" {
			profilePath = filepath.Join(config.DataDir, "pi-profile")
		}
		server.runner = &commandRunExecutor{command: config.PiCommand, extensionPath: config.PiExtensionPath,
			profilePath: profilePath, dataDir: config.DataDir, daemonURL: daemonURL, token: config.Token}
	}
	server.clock = schedule.NewEngine(st)
	server.pool = newReadyPool(runtime)
	server.handler = server.routes()
	return server, nil
}

// Close releases server resources.
func (s *Server) Close() {
	s.cancelRuns()
	s.runWG.Wait()
	s.pool.close()
	s.clock.Close()
	s.github.Close()
	_ = s.store.Close()
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("GET /v1/info", s.requireAuth(info(s)))
	mux.HandleFunc("POST /v1/auth/github/start", s.requireAuth(s.startGitHubAuth))
	mux.HandleFunc("GET /v1/auth/github/status/{state}", s.requireAuth(s.githubAuthStatus))
	// GitHub opens these callback routes in the user's browser. Their random,
	// short-lived state value is the authorization capability.
	mux.HandleFunc("GET /v1/auth/github/manifest", s.githubManifest)
	mux.HandleFunc("GET /v1/auth/github/manifest/callback", s.githubManifestCallback)
	mux.HandleFunc("GET /v1/auth/github/install/callback/{key}", s.githubInstallCallback)

	mux.HandleFunc("POST /v1/schedules", s.requireAuth(s.createSchedule))
	mux.HandleFunc("GET /v1/schedules", s.requireAuth(s.listSchedules))
	mux.HandleFunc("GET /v1/schedules/{id}", s.requireAuth(s.getSchedule))
	mux.HandleFunc("PATCH /v1/schedules/{id}", s.requireAuth(s.updateSchedule))
	mux.HandleFunc("DELETE /v1/schedules/{id}", s.requireAuth(s.deleteSchedule))
	mux.HandleFunc("GET /v1/schedule-occurrences", s.requireAuth(s.listScheduleOccurrences))
	mux.HandleFunc("POST /v1/schedule-occurrences/claim", s.requireAuth(s.claimScheduleOccurrence))
	mux.HandleFunc("POST /v1/schedule-occurrences/{id}/renew", s.requireAuth(s.renewScheduleOccurrence))
	mux.HandleFunc("POST /v1/schedule-occurrences/{id}/complete", s.requireAuth(s.completeScheduleOccurrence))

	mux.HandleFunc("POST /v1/workspaces", s.requireAuth(s.createWorkspace))
	mux.HandleFunc("GET /v1/workspaces", s.requireAuth(s.listWorkspaces))
	mux.HandleFunc("GET /v1/workspaces/{id}", s.requireAuth(s.getWorkspace))
	mux.HandleFunc("POST /v1/workspaces/{id}/start", s.requireAuth(s.startWorkspace))
	mux.HandleFunc("POST /v1/workspaces/{id}/stop", s.requireAuth(s.stopWorkspace))
	mux.HandleFunc("DELETE /v1/workspaces/{id}", s.requireAuth(s.deleteWorkspace))

	mux.HandleFunc("POST /v1/workspaces/{id}/exec", s.requireAuth(s.execCommand))
	mux.HandleFunc("POST /v1/workspaces/{id}/runs", s.requireAuth(s.createRun))
	mux.HandleFunc("GET /v1/runs/{id}", s.requireAuth(s.getRun))
	mux.HandleFunc("GET /v1/runs/{id}/events", s.requireAuth(s.listRunEvents))
	mux.HandleFunc("GET /v1/runs/{id}/events/stream", s.requireAuth(s.streamRunEvents))
	mux.HandleFunc("POST /v1/runs/{id}/cancel", s.requireAuth(s.cancelRun))

	mux.HandleFunc("GET /v1/workspaces/{id}/ports", s.requireAuth(s.workspacePorts))
	mux.HandleFunc("POST /v1/workspaces/{id}/tunnel/{port}", s.requireAuth(s.tunnel))

	mux.HandleFunc("POST /v1/workspaces/{id}/files/archive", s.requireAuth(s.archiveImport))
	mux.HandleFunc("GET /v1/workspaces/{id}/files/archive", s.requireAuth(s.archiveExport))
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
			Features: infoFeatures{
				DurableRuns:    server.runner != nil,
				ModelAuth:      server.modelAuthConfigured,
				RunEventStream: server.runner != nil,
			},
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
	Features  infoFeatures    `json:"features"`
}

type infoFeatures struct {
	DurableRuns bool `json:"durableRuns"`
	ModelAuth   bool `json:"modelAuth"`
	// RunEventStream advertises the SSE run-event stream; clients fall back
	// to polling when absent.
	RunEventStream bool `json:"runEventStream"`
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
		writeError(response, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, store.ErrConflict):
		writeError(response, http.StatusConflict, "conflict", "resource state changed; retry the operation")
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
