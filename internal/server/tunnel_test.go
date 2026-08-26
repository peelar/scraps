package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/peelar/scraps/internal/client"
	"github.com/peelar/scraps/internal/testprovider"
	"github.com/peelar/scraps/internal/workspace"
)

// startEchoService runs a line-echo TCP service on the daemon host loopback
// and returns its port. It half-closes its side after seeing the sentinel.
func startEchoService(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				reader := bufio.NewReader(conn)
				for {
					line, err := reader.ReadString('\n')
					if line != "" {
						if _, werr := io.WriteString(conn, "echo:"+line); werr != nil {
							return
						}
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	return listener.Addr().(*net.TCPAddr).Port
}

func newLiveTestServer(t *testing.T) (*testServer, *client.Client) {
	t.Helper()
	ts := newTestServer(t)
	live := httptest.NewServer(ts.handler)
	t.Cleanup(live.Close)
	return ts, client.New(live.URL, "")
}

func TestTunnelRoundTrip(t *testing.T) {
	ts, api := newLiveTestServer(t)
	created := ts.createWorkspace(t, workspace.CreateOptions{Project: "demo"})
	port := startEchoService(t)

	tunnel, err := api.Tunnel(context.Background(), created.ID, port)
	if err != nil {
		t.Fatalf("tunnel: %v", err)
	}
	defer tunnel.Close()

	// Concurrent bidirectional traffic: write while reading, like an
	// interactive browser session.
	go func() {
		for i := 0; i < 5; i++ {
			if _, err := tunnel.Write([]byte("ping\n")); err != nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		_ = tunnel.CloseWrite()
	}()
	reader := bufio.NewReader(tunnel)
	for i := 0; i < 5; i++ {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if line != "echo:ping\n" {
			t.Fatalf("line %d = %q", i, line)
		}
	}
	if _, err := reader.ReadByte(); err != io.EOF {
		t.Fatalf("read after service close = %v, want EOF", err)
	}
}

func TestTunnelDialFailureReturnsTunnelError(t *testing.T) {
	ts, api := newLiveTestServer(t)
	created := ts.createWorkspace(t, workspace.CreateOptions{Project: "demo"})

	// Reserve then release a port so nothing listens on it.
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	reserved.Close()

	_, err = api.Tunnel(context.Background(), created.ID, port)
	var apiErr *client.Error
	if err == nil || !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want client.Error", err)
	}
	if apiErr.Status != http.StatusBadGateway || apiErr.Code != "tunnel_dial_failed" {
		t.Fatalf("api error = %+v", apiErr)
	}
	if !strings.Contains(apiErr.Message, "dial 127.0.0.1:") {
		t.Fatalf("message = %q", apiErr.Message)
	}
}

func TestWorkspacePortsEndpoint(t *testing.T) {
	ts, api := newLiveTestServer(t)
	created := ts.createWorkspace(t, workspace.CreateOptions{Project: "demo"})
	port := startEchoService(t)

	previous := testprovider.ProbePorts
	testprovider.ProbePorts = []int{port}
	defer func() { testprovider.ProbePorts = previous }()

	response := ts.do(t, http.MethodGet, "/v1/workspaces/"+created.ID+"/ports", nil, "")
	if response.Code != http.StatusOK {
		t.Fatalf("ports status = %d: %s", response.Code, response.Body.String())
	}
	var body portsResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Ports) != 1 || body.Ports[0].Port != port {
		t.Fatalf("ports = %+v", body.Ports)
	}

	ports, err := api.WorkspacePorts(context.Background(), created.ID)
	if err != nil || len(ports) != 1 || ports[0].Port != port {
		t.Fatalf("client ports = %+v err = %v", ports, err)
	}
}

func TestTunnelRejectsInvalidPort(t *testing.T) {
	ts, _ := newLiveTestServer(t)
	created := ts.createWorkspace(t, workspace.CreateOptions{Project: "demo"})

	response := ts.do(t, http.MethodPost, "/v1/workspaces/"+created.ID+"/tunnel/99999", nil, "")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	response = ts.do(t, http.MethodPost, "/v1/workspaces/missing/tunnel/8080", nil, "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
}
