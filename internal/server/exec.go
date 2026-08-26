package server

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/peelar/scraps/internal/provider"
)

const execTimeoutCap = time.Hour

const (
	maxExecEnvVariables = 64
	maxExecEnvValueSize = 64 << 10
	maxExecEnvTotalSize = 256 << 10
)

var execEnvNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var reservedExecEnvironment = map[string]struct{}{
	"HOME": {}, "PATH": {}, "SHELL": {}, "TMPDIR": {},
	"SCRAP_WORKSPACE_ROOT": {}, "SCRAP_TOKEN": {}, "SCRAPS_CLIENT_CONFIG": {},
}

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

func (s *Server) execCommand(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.lookupWorkspace(w, r); !ok {
		return
	}
	var b execRequest
	if decode(w, r, &b) != nil {
		return
	}
	if b.Command == "" {
		writeError(w, 400, "invalid_request", "command is required")
		return
	}
	if err := validateExecEnvironment(b.Env); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	ctx := r.Context()
	if b.TimeoutMs > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, min(time.Duration(b.TimeoutMs)*time.Millisecond, execTimeoutCap))
		defer cancel()
	}
	var writer *eventWriter
	started := time.Now()
	err := s.provider.Exec(ctx, r.PathValue("id"), provider.ExecRequest{Command: b.Command, CWD: b.CWD, Env: b.Env}, func(e provider.ExecEvent) {
		if writer == nil {
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			writer = newEventWriter(w)
		}
		event := execEvent{Type: e.Type, PID: e.PID, Stream: e.Stream, Code: e.Code, Reason: e.Reason}
		if len(e.Data) > 0 {
			event.Data = base64.StdEncoding.EncodeToString(e.Data)
		}
		if e.Type == "exit" {
			event.DurationMs = time.Since(started).Milliseconds()
		}
		writer.event(event)
	})
	if err != nil {
		if writer == nil {
			writeProviderError(w, err)
		} else {
			writer.event(execEvent{Type: "error", Reason: err.Error()})
		}
	}
}

func validateExecEnvironment(env map[string]string) error {
	if len(env) > maxExecEnvVariables {
		return fmt.Errorf("environment has %d variables; maximum is %d", len(env), maxExecEnvVariables)
	}
	total := 0
	for name, value := range env {
		if !execEnvNamePattern.MatchString(name) {
			return fmt.Errorf("invalid environment variable name %q", name)
		}
		if _, reserved := reservedExecEnvironment[name]; reserved || strings.HasPrefix(name, "OPENSHELL_") {
			return fmt.Errorf("environment variable %q is reserved by Scraps", name)
		}
		if len(name) > 128 {
			return fmt.Errorf("environment variable name %q exceeds 128 bytes", name)
		}
		if len(value) > maxExecEnvValueSize {
			return fmt.Errorf("environment variable %q exceeds 64 KiB", name)
		}
		if strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("environment variable %q contains a NUL or newline", name)
		}
		total += len(name) + len(value)
		if total > maxExecEnvTotalSize {
			return fmt.Errorf("environment exceeds 256 KiB")
		}
	}
	return nil
}

type eventWriter struct {
	mu      sync.Mutex
	writer  *bufio.Writer
	flusher http.Flusher
}

func newEventWriter(w http.ResponseWriter) *eventWriter {
	flusher, _ := w.(http.Flusher)
	return &eventWriter{writer: bufio.NewWriter(w), flusher: flusher}
}
func (w *eventWriter) event(e execEvent) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = json.NewEncoder(w.writer).Encode(e)
	if w.flusher != nil {
		w.writer.Flush()
		w.flusher.Flush()
	}
}
