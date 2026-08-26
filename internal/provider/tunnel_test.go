package provider

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/peelar/scraps/internal/store"
	"github.com/peelar/scraps/internal/workspace"
)

// fakeGateway installs a script as the openshell binary. It logs arguments
// and behaves as a functional relay for the given mode: echo acks a dial and
// echoes stdin back to stdout, refuse reports a failed dial with the 0x01
// status byte, ports reports fixed listening ports.
func fakeGateway(t *testing.T, mode string) string {
	t.Helper()
	bin := t.TempDir()
	logPath := filepath.Join(bin, "calls")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$OPENSHELL_TEST_LOG"
case "$*" in *scrap-relay*)
  case "$OPENSHELL_TEST_MODE" in
    refuse) printf '\001'; printf 'connection refused\n'; exit 1 ;;
  esac ;;
esac
case "$OPENSHELL_TEST_MODE" in
  echo) printf '\000'; cat ;;
  ports) printf '[{"port":3000,"address":"127.0.0.1"},{"port":5173,"address":"0.0.0.0"}]' ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "openshell"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OPENSHELL_TEST_LOG", logPath)
	t.Setenv("OPENSHELL_TEST_MODE", mode)
	return logPath
}

func newTestOpenShell(t *testing.T) *OpenShell {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	runtime, err := NewOpenShell(context.Background(), st, "example.test/scraps@sha256:abc")
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func TestOpenShellTunnelStreamsThroughGateway(t *testing.T) {
	logPath := fakeGateway(t, "echo")
	runtime := newTestOpenShell(t)
	created, err := runtime.Create(context.Background(), workspace.CreateOptions{Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}

	conn, err := runtime.Tunnel(context.Background(), created.ID, 8080)
	if err != nil {
		t.Fatalf("tunnel: %v", err)
	}
	if _, err := conn.Write([]byte("hello tunnel\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	echoed := make([]byte, len("hello tunnel\n"))
	if _, err := io.ReadFull(conn, echoed); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(echoed) != "hello tunnel\n" {
		t.Fatalf("echo = %q", echoed)
	}
	if err := conn.CloseWrite(); err != nil {
		t.Fatalf("close write: %v", err)
	}
	if _, err := conn.Read(echoed); err != io.EOF {
		t.Fatalf("read after close write = %v, want EOF", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(calls)
	if !strings.Contains(got, "sandbox exec --no-tty --name "+created.ID+" -- python3 -c ") {
		t.Fatalf("tunnel call = %q", got)
	}
	if !strings.Contains(got, " scrap-relay 8080") {
		t.Fatalf("relay port argument missing: %q", got)
	}
}

func TestOpenShellTunnelReportsDialFailure(t *testing.T) {
	fakeGateway(t, "refuse")
	runtime := newTestOpenShell(t)
	created, err := runtime.Create(context.Background(), workspace.CreateOptions{Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}

	_, err = runtime.Tunnel(context.Background(), created.ID, 5173)
	var dial *TunnelDialError
	if err == nil || !errors.As(err, &dial) {
		t.Fatalf("err = %v, want TunnelDialError", err)
	}
	if dial.Port != 5173 || !strings.Contains(dial.Error(), "connection refused") {
		t.Fatalf("dial error = %+v", dial)
	}
}

func TestOpenShellTunnelRequiresRunningWorkspace(t *testing.T) {
	fakeGateway(t, "echo")
	runtime := newTestOpenShell(t)
	created, err := runtime.Create(context.Background(), workspace.CreateOptions{Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Stop(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Tunnel(context.Background(), created.ID, 8080); err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Fatalf("err = %v, want stopped workspace error", err)
	}
}

func TestOpenShellPortsParsesGatewayJSON(t *testing.T) {
	fakeGateway(t, "ports")
	runtime := newTestOpenShell(t)
	created, err := runtime.Create(context.Background(), workspace.CreateOptions{Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}

	ports, err := runtime.Ports(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("ports: %v", err)
	}
	if len(ports) != 2 || ports[0].Port != 3000 || ports[1].Port != 5173 || ports[1].Address != "0.0.0.0" {
		t.Fatalf("ports = %+v", ports)
	}
}
