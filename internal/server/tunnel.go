// Workspace port listing and the byte tunnel endpoint. The tunnel is a
// full-duplex HTTP stream: the request body carries client-to-service bytes
// and the response body carries service-to-client bytes. Nothing is
// published on any network; the stream reaches only the workspace's own
// loopback interface and requires the daemon bearer token.

package server

import (
	"io"
	"net/http"
	"strconv"

	"github.com/peelar/scraps/internal/provider"
)

type portsResponse struct {
	Ports []provider.Port `json:"ports"`
}

func (s *Server) workspacePorts(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.lookupWorkspace(w, r); !ok {
		return
	}
	ports, err := s.provider.Ports(r.Context(), id)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if ports == nil {
		ports = []provider.Port{}
	}
	writeJSON(w, http.StatusOK, portsResponse{Ports: ports})
}

func (s *Server) tunnel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// The request body streams for the whole tunnel lifetime, so the server
	// must never try to drain it before writing response headers — including
	// error responses. EnableFullDuplex switches off that drain.
	_ = http.NewResponseController(w).EnableFullDuplex()
	if _, ok := s.lookupWorkspace(w, r); !ok {
		return
	}
	port, err := strconv.Atoi(r.PathValue("port"))
	if err != nil || port < 1 || port > 65535 {
		writeError(w, http.StatusBadRequest, "invalid_request", "port must be an integer between 1 and 65535")
		return
	}
	conn, err := s.provider.Tunnel(r.Context(), id, port)
	if err != nil {
		writeProviderError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-cache")
	// Ask proxies in the path not to buffer the stream.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}
	// Client to service. Closing only the write side propagates a client
	// half-close to the service instead of tearing down the tunnel.
	go func() {
		_, _ = io.Copy(conn, r.Body)
		_ = conn.CloseWrite()
	}()
	// Service to client, flushing so interactive traffic (HMR websockets and
	// friends) is not delayed behind buffering.
	_, _ = io.Copy(&flushWriter{w: w, flusher: flusher}, conn)
	_ = conn.Close()
}

// flushWriter flushes after every write so tunneled bytes reach the client
// as they arrive.
type flushWriter struct {
	w       io.Writer
	flusher http.Flusher
}

func (fw *flushWriter) Write(b []byte) (int, error) {
	n, err := fw.w.Write(b)
	if fw.flusher != nil {
		fw.flusher.Flush()
	}
	return n, err
}
