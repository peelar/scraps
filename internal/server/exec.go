package server

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/peelar/scraps/internal/provider"
)

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
