package server

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// execTimeoutCap bounds any single command execution.
const execTimeoutCap = time.Hour

type execRequest struct {
	Command   string            `json:"command"`
	CWD       string            `json:"cwd"`
	Env       map[string]string `json:"env"`
	TimeoutMs int64             `json:"timeoutMs"`
}

type execEvent struct {
	Type       string `json:"type"`
	PID        int    `json:"pid,omitempty"`
	Stream     string `json:"stream,omitempty"`
	Data       string `json:"data,omitempty"`
	Code       *int   `json:"code"`
	Reason     string `json:"reason,omitempty"`
	DurationMs int64  `json:"durationMs,omitempty"`
}

func (s *Server) execCommand(response http.ResponseWriter, request *http.Request) {
	if _, ok := s.lookupWorkspace(response, request); !ok {
		return
	}

	var body execRequest
	if err := decodeBody(request, &body); err != nil {
		writeAPIError(response, err)
		return
	}
	if body.Command == "" {
		writeError(response, http.StatusBadRequest, "invalid_request", "command is required")
		return
	}

	id := request.PathValue("id")
	cwd := body.CWD
	if cwd == "" {
		workspace, err := s.manager.Get(request.Context(), id)
		if err != nil {
			writeAPIError(response, err)
			return
		}
		cwd = workspace.RootPath
	}
	workingDir, err := s.manager.ResolvePath(id, cwd)
	if err != nil {
		writeAPIError(response, err)
		return
	}
	if info, err := os.Stat(workingDir); err != nil || !info.IsDir() {
		writeError(response, http.StatusBadRequest, "invalid_request", "working directory does not exist: "+cwd)
		return
	}

	timeout := time.Duration(0)
	if body.TimeoutMs > 0 {
		timeout = min(time.Duration(body.TimeoutMs)*time.Millisecond, execTimeoutCap)
	}

	// Client disconnect cancels the request context, which kills the process.
	ctx := request.Context()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	response.Header().Set("Content-Type", "application/x-ndjson")
	response.Header().Set("Cache-Control", "no-cache")
	response.WriteHeader(http.StatusOK)

	writer := newEventWriter(response)

	command := exec.CommandContext(ctx, "/bin/bash", "-c", body.Command)
	command.Dir = workingDir
	command.Env = append(os.Environ(), envSlice(body.Env)...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process != nil {
			// Negative pid signals the whole process group.
			return syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}

	stdout, err := command.StdoutPipe()
	if err != nil {
		writer.event(execEvent{Type: "error", Reason: err.Error()})
		return
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		writer.event(execEvent{Type: "error", Reason: err.Error()})
		return
	}

	if err := command.Start(); err != nil {
		writer.event(execEvent{Type: "error", Reason: err.Error()})
		return
	}
	writer.event(execEvent{Type: "start", PID: command.Process.Pid, Code: nil})

	started := time.Now()
	var waitErr error
	streamDone := &sync.WaitGroup{}
	streamDone.Add(2)
	go streamOutput(writer, streamDone, "stdout", stdout)
	go streamOutput(writer, streamDone, "stderr", stderr)
	streamDone.Wait()
	waitErr = command.Wait()

	event := execEvent{Type: "exit", DurationMs: time.Since(started).Milliseconds()}
	var exitErr *exec.ExitError
	switch {
	case waitErr == nil:
		code := 0
		event.Code = &code
	case errors.As(waitErr, &exitErr) && exitErr.ExitCode() >= 0:
		code := exitErr.ExitCode()
		event.Code = &code
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		event.Code = nil
		event.Reason = "timeout"
	case errors.Is(ctx.Err(), context.Canceled):
		event.Code = nil
		event.Reason = "cancelled"
	default:
		event.Type = "error"
		event.Code = nil
		event.Reason = waitErr.Error()
	}
	writer.event(event)
}

func envSlice(env map[string]string) []string {
	entries := make([]string, 0, len(env))
	for key, value := range env {
		entries = append(entries, key+"="+value)
	}
	return entries
}

func streamOutput(writer *eventWriter, done *sync.WaitGroup, stream string, source interface{ Read([]byte) (int, error) }) {
	defer done.Done()
	buffer := make([]byte, 32*1024)
	for {
		read, err := source.Read(buffer)
		if read > 0 {
			writer.event(execEvent{
				Type:   "output",
				Stream: stream,
				Data:   base64.StdEncoding.EncodeToString(buffer[:read]),
			})
		}
		if err != nil {
			return
		}
	}
}

// eventWriter serializes NDJSON events and flushes each one.
type eventWriter struct {
	mu      sync.Mutex
	writer  *bufio.Writer
	flusher http.Flusher
}

func newEventWriter(response http.ResponseWriter) *eventWriter {
	flusher, _ := response.(http.Flusher)
	return &eventWriter{writer: bufio.NewWriter(response), flusher: flusher}
}

func (w *eventWriter) event(event execEvent) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = json.NewEncoder(w.writer).Encode(event)
	if w.flusher != nil {
		w.writer.Flush()
		w.flusher.Flush()
	}
}
