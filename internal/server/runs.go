package server

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/peelar/scraps/internal/store"
)

// RunRequest is the trusted runner input. Model credentials are inherited by
// the runner process and are never included in this value or sent to a workspace.
type RunRequest struct {
	RunID, WorkspaceID, SessionKey, Prompt string
	SessionSnapshot                        json.RawMessage
}

// RunExecutor executes one durable Pi turn and emits Pi JSON-mode records.
type RunExecutor interface {
	Execute(context.Context, RunRequest, func(json.RawMessage) error) error
}

type commandRunExecutor struct {
	command, extensionPath, profilePath, dataDir, daemonURL, token string
}

func (r *commandRunExecutor) Execute(ctx context.Context, request RunRequest, emit func(json.RawMessage) error) error {
	sessionDir := filepath.Join(r.dataDir, "pi-sessions", safeKey(request.SessionKey))
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		return fmt.Errorf("create Pi session directory: %w", err)
	}
	checkpoint, err := parseSessionCheckpoint(request.SessionSnapshot)
	if err != nil {
		return err
	}
	if err := importSessionSnapshot(sessionDir, request.SessionKey, request.SessionSnapshot); err != nil {
		return err
	}
	cmd, stdout, stderr, err := r.startRemotePi(request, checkpoint)
	if err != nil {
		return err
	}
	// stopRun turns an early output failure into process-tree termination;
	// without it the child could block on a full stdout pipe and Wait would
	// never return.
	runContext, stopRun := context.WithCancel(ctx)
	defer stopRun()

	stderrDone := make(chan []byte, 1)
	go func() { data, _ := io.ReadAll(io.LimitReader(stderr, 64<<10)); stderrDone <- data }()
	processDone := make(chan struct{})
	go guardProcessGroup(runContext, cmd, processDone)

	output := emitRemotePiOutput(stdout, emit)
	if output.err != nil {
		stopRun()
	}
	waitErr := cmd.Wait()
	close(processDone)
	stderrText := strings.TrimSpace(string(<-stderrDone))
	if output.err != nil {
		return output.err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if output.scanError != nil {
		return fmt.Errorf("read remote Pi output: %w", output.scanError)
	}
	if waitErr != nil {
		if stderrText != "" {
			return fmt.Errorf("remote Pi: %s", stderrText)
		}
		return fmt.Errorf("remote Pi: %w", waitErr)
	}
	return nil
}

// startRemotePi builds and starts one detached remote Pi process in JSON
// mode. The process gets its own process group so cancellation can kill the
// whole tree.
func (r *commandRunExecutor) startRemotePi(request RunRequest, checkpoint sessionCheckpoint) (*exec.Cmd, io.ReadCloser, io.ReadCloser, error) {
	args := []string{"--mode", "json", "--session-dir", filepath.Join(r.dataDir, "pi-sessions", safeKey(request.SessionKey)), "-c", "--no-extensions", "-e", r.extensionPath,
		"--scrap", "--workspace", request.WorkspaceID}
	if checkpoint.Provider != "" {
		args = append(args, "--provider", checkpoint.Provider)
	}
	if checkpoint.Model != "" {
		args = append(args, "--model", checkpoint.Model)
	}
	if checkpoint.ThinkingLevel != "" {
		args = append(args, "--thinking", checkpoint.ThinkingLevel)
	}
	args = append(args, "--", request.Prompt)
	cmd := exec.Command(r.command, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = append(os.Environ(),
		"PI_CODING_AGENT_DIR="+r.profilePath,
		"SCRAP_DAEMON_URL="+r.daemonURL,
		"SCRAP_WORKSPACE_ID="+request.WorkspaceID,
		"SCRAP_TOKEN="+r.token,
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, fmt.Errorf("start remote Pi: %w", err)
	}
	return cmd, stdout, stderr, nil
}

// guardProcessGroup stops the remote Pi process tree when the run context is
// cancelled: SIGTERM first, SIGKILL after a short grace period.
func guardProcessGroup(ctx context.Context, cmd *exec.Cmd, processDone <-chan struct{}) {
	select {
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-processDone:
		case <-time.After(2 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	case <-processDone:
	}
}

// remotePiOutput reports how streaming the child's stdout ended.
type remotePiOutput struct {
	err       error // emit failure or invalid JSON
	scanError error // scanner failure (I/O, oversized line)
}

// emitRemotePiOutput scans the remote Pi JSON stream and forwards every line
// to emit. It stops at the first invalid record or emit failure.
func emitRemotePiOutput(stdout io.Reader, emit func(json.RawMessage) error) remotePiOutput {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if !json.Valid(line) {
			return remotePiOutput{err: errors.New("remote Pi emitted invalid JSON")}
		}
		if err := emit(line); err != nil {
			return remotePiOutput{err: err}
		}
	}
	return remotePiOutput{scanError: scanner.Err()}
}

type sessionCheckpoint struct {
	Type          string `json:"type"`
	Version       int    `json:"version"`
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	ThinkingLevel string `json:"thinkingLevel"`
}

func parseSessionCheckpoint(snapshot json.RawMessage) (sessionCheckpoint, error) {
	var entries []json.RawMessage
	if len(snapshot) == 0 {
		return sessionCheckpoint{}, nil
	}
	if err := json.Unmarshal(snapshot, &entries); err != nil {
		return sessionCheckpoint{}, fmt.Errorf("decode local Pi session checkpoint: %w", err)
	}
	for _, entry := range entries {
		var checkpoint sessionCheckpoint
		if json.Unmarshal(entry, &checkpoint) == nil && checkpoint.Type == "scraps_checkpoint" {
			if checkpoint.Version != 1 || len(checkpoint.Provider) > 100 || len(checkpoint.Model) > 500 || len(checkpoint.ThinkingLevel) > 20 {
				return sessionCheckpoint{}, errors.New("local Pi session checkpoint is invalid")
			}
			return checkpoint, nil
		}
	}
	return sessionCheckpoint{}, nil
}

func importSessionSnapshot(sessionDir, sessionKey string, snapshot json.RawMessage) error {
	existing, err := filepath.Glob(filepath.Join(sessionDir, "*.jsonl"))
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil // The remote session is already authoritative.
	}
	var entries []json.RawMessage
	if len(snapshot) == 0 {
		snapshot = json.RawMessage("[]")
	}
	if err := json.Unmarshal(snapshot, &entries); err != nil {
		return fmt.Errorf("decode local Pi session snapshot: %w", err)
	}
	digest := sha256.Sum256([]byte(sessionKey))
	sessionID := fmt.Sprintf("%x-%x-%x-%x-%x", digest[0:4], digest[4:6], digest[6:8], digest[8:10], digest[10:16])
	path := filepath.Join(sessionDir, time.Now().UTC().Format("20060102T150405.000Z")+"_"+sessionID+".jsonl")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create imported Pi session: %w", err)
	}
	encoder := json.NewEncoder(file)
	header := map[string]any{"type": "session", "version": 3, "id": sessionID, "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "cwd": "/workspace"}
	if err := encoder.Encode(header); err != nil {
		file.Close()
		os.Remove(path)
		return err
	}
	for _, entry := range entries {
		if !json.Valid(entry) {
			file.Close()
			os.Remove(path)
			return errors.New("local Pi session snapshot contains invalid JSON")
		}
		var envelope struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(entry, &envelope)
		if envelope.Type == "scraps_checkpoint" {
			continue
		}
		if _, err := file.Write(append(entry, '\n')); err != nil {
			file.Close()
			os.Remove(path)
			return err
		}
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return err
	}
	return nil
}

func safeKey(value string) string {
	if value == "" {
		return "default"
	}
	var b strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "default"
	}
	return b.String()
}

const (
	maxRunPromptBytes         = 256 << 10
	maxSessionSnapshotBytes   = 8 << 20
	maxSessionSnapshotEntries = 10_000
	maxSessionKeyBytes        = 200
	maxRunEvents              = 20_000
	maxRunEventBytes          = 2 << 20
	maxRunOutputBytes         = 64 << 20
	maxRunDuration            = 2 * time.Hour
)

type createRunRequest struct {
	Prompt          string          `json:"prompt"`
	SessionKey      string          `json:"sessionKey"`
	SessionSnapshot json.RawMessage `json:"sessionSnapshot,omitempty"`
}

type runResponse struct {
	ID          string     `json:"id"`
	WorkspaceID string     `json:"workspaceId"`
	SessionKey  string     `json:"sessionKey"`
	State       string     `json:"state"`
	Error       string     `json:"error,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

func responseRun(run store.Run) runResponse {
	return runResponse{ID: run.ID, WorkspaceID: run.WorkspaceID, SessionKey: run.SessionKey,
		State: run.State, Error: run.Error, CreatedAt: run.CreatedAt, StartedAt: run.StartedAt,
		FinishedAt: run.FinishedAt, UpdatedAt: run.UpdatedAt}
}

// parseCreateRunRequest decodes and validates a create-run body. On invalid
// input it writes the error response and returns ok=false.
func (s *Server) parseCreateRunRequest(response http.ResponseWriter, request *http.Request) (createRunRequest, bool) {
	var input createRunRequest
	if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 10<<20)).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request", "body must contain prompt and sessionKey")
		return input, false
	}
	input.Prompt = strings.TrimSpace(input.Prompt)
	input.SessionKey = strings.TrimSpace(input.SessionKey)
	if input.Prompt == "" || input.SessionKey == "" {
		writeError(response, http.StatusBadRequest, "invalid_request", "prompt and sessionKey are required")
		return input, false
	}
	if len(input.Prompt) > maxRunPromptBytes || len(input.SessionKey) > maxSessionKeyBytes || len(input.SessionSnapshot) > maxSessionSnapshotBytes {
		writeError(response, http.StatusRequestEntityTooLarge, "run_input_too_large", "prompt, sessionKey, or session snapshot exceeds the durable run limit")
		return input, false
	}
	if len(input.SessionSnapshot) == 0 {
		input.SessionSnapshot = json.RawMessage("[]")
	}
	var snapshotEntries []json.RawMessage
	if err := json.Unmarshal(input.SessionSnapshot, &snapshotEntries); err != nil || len(snapshotEntries) > maxSessionSnapshotEntries {
		writeError(response, http.StatusBadRequest, "invalid_session_snapshot", "sessionSnapshot must be a bounded array of Pi session entries")
		return input, false
	}
	for _, entry := range snapshotEntries {
		var envelope struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(entry, &envelope) != nil || envelope.Type == "" || envelope.Type == "session" {
			writeError(response, http.StatusBadRequest, "invalid_session_snapshot", "sessionSnapshot contains an invalid entry")
			return input, false
		}
	}
	if _, err := parseSessionCheckpoint(input.SessionSnapshot); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_session_snapshot", err.Error())
		return input, false
	}
	return input, true
}

func (s *Server) createRun(response http.ResponseWriter, request *http.Request) {
	if s.runner == nil {
		writeError(response, http.StatusServiceUnavailable, "runner_unavailable", "durable Pi runner is not configured on this worker")
		return
	}
	if !s.modelAuthConfigured {
		writeError(response, http.StatusServiceUnavailable, "model_auth_unavailable", "durable Pi runner has no configured model authorization")
		return
	}
	workspaceID := request.PathValue("id")
	workspaceRecord, err := s.store.GetWorkspace(request.Context(), workspaceID)
	if err != nil {
		writeAPIError(response, err)
		return
	}
	if workspaceRecord.State != "running" {
		writeError(response, http.StatusConflict, "workspace_not_running", "workspace must be running before starting a run")
		return
	}
	input, ok := s.parseCreateRunRequest(response, request)
	if !ok {
		return
	}

	s.runMu.Lock()
	if _, busy := s.activeRuns[workspaceID]; busy {
		s.runMu.Unlock()
		writeError(response, http.StatusConflict, "run_active", "workspace already has an active run")
		return
	}
	id, err := randomRunID()
	if err != nil {
		s.runMu.Unlock()
		writeAPIError(response, err)
		return
	}
	run := store.Run{ID: id, WorkspaceID: workspaceID, SessionKey: input.SessionKey, Prompt: input.Prompt,
		SessionSnapshot: input.SessionSnapshot, State: "queued"}
	if err := s.store.CreateRun(request.Context(), run); err != nil {
		s.runMu.Unlock()
		if errors.Is(err, store.ErrConflict) {
			writeError(response, http.StatusConflict, "run_active", "workspace already has an active run")
		} else {
			writeAPIError(response, err)
		}
		return
	}
	runContext, cancelRun := context.WithTimeout(s.runContext, maxRunDuration)
	s.activeRuns[workspaceID] = id
	s.runCancels[id] = cancelRun
	s.runWG.Add(1)
	s.runMu.Unlock()

	go s.executeRun(runContext, cancelRun, run)
	created, _ := s.store.GetRun(context.Background(), id)
	writeJSON(response, http.StatusAccepted, responseRun(created))
}

func (s *Server) executeRun(ctx context.Context, cancel context.CancelFunc, run store.Run) {
	defer s.runWG.Done()
	defer cancel()
	defer func() {
		s.runMu.Lock()
		delete(s.activeRuns, run.WorkspaceID)
		delete(s.runCancels, run.ID)
		s.runMu.Unlock()
	}()
	if err := s.store.StartRun(context.Background(), run.ID); err != nil {
		return
	}
	eventCount, outputBytes := 0, 0
	err := s.runner.Execute(ctx, RunRequest{RunID: run.ID, WorkspaceID: run.WorkspaceID, SessionKey: run.SessionKey,
		Prompt: run.Prompt, SessionSnapshot: run.SessionSnapshot}, func(data json.RawMessage) error {
		if len(data) > maxRunEventBytes {
			return errors.New("remote Pi event exceeds the durable run size limit")
		}
		eventCount++
		outputBytes += len(data)
		if eventCount > maxRunEvents || outputBytes > maxRunOutputBytes {
			return errors.New("remote Pi output exceeds the durable run limit")
		}
		_, err := s.store.AppendRunEvent(ctx, run.ID, data)
		return err
	})
	if err != nil {
		state := "failed"
		if errors.Is(err, context.Canceled) {
			state = "cancelled"
		}
		_ = s.store.FinishRun(context.Background(), run.ID, state, err.Error())
		return
	}
	_ = s.store.FinishRun(context.Background(), run.ID, "succeeded", "")
}

func (s *Server) getRun(response http.ResponseWriter, request *http.Request) {
	run, err := s.store.GetRun(request.Context(), request.PathValue("id"))
	if err != nil {
		writeAPIError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, responseRun(run))
}

func (s *Server) cancelRun(response http.ResponseWriter, request *http.Request) {
	run, err := s.store.GetRun(request.Context(), request.PathValue("id"))
	if err != nil {
		writeAPIError(response, err)
		return
	}
	if run.State == "succeeded" || run.State == "failed" || run.State == "cancelled" {
		writeJSON(response, http.StatusOK, responseRun(run))
		return
	}
	s.runMu.Lock()
	cancel := s.runCancels[run.ID]
	s.runMu.Unlock()
	if cancel == nil {
		writeError(response, http.StatusConflict, "run_not_active", "run is not active in this daemon process")
		return
	}
	cancel()
	writeJSON(response, http.StatusAccepted, map[string]string{"id": run.ID, "state": "cancelling"})
}

func (s *Server) listRunEvents(response http.ResponseWriter, request *http.Request) {
	after := int64(0)
	if raw := request.URL.Query().Get("after"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 0 {
			writeError(response, http.StatusBadRequest, "invalid_after", "after must be a non-negative integer")
			return
		}
		after = value
	}
	events, err := s.store.ListRunEvents(request.Context(), request.PathValue("id"), after)
	if err != nil {
		writeAPIError(response, err)
		return
	}
	type eventResponse struct {
		Sequence  int64           `json:"sequence"`
		Data      json.RawMessage `json:"data"`
		CreatedAt time.Time       `json:"createdAt"`
	}
	body := make([]eventResponse, 0, len(events))
	for _, event := range events {
		body = append(body, eventResponse{event.Sequence, event.Data, event.CreatedAt})
	}
	writeJSON(response, http.StatusOK, map[string]any{"events": body})
}

func randomRunID() (string, error) {
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return "run-" + hex.EncodeToString(value[:]), nil
}
