package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// The test binary doubles as a fake scrapd: Start() spawns os.Args[0] with
// SCRAPD_FAKE=1, which lands here before any test runs.
func TestMain(m *testing.M) {
	if os.Getenv("SCRAPD_FAKE") == "1" {
		fakeScrapdMain()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeScrapdMain() {
	address := os.Getenv("SCRAPD_LISTEN_ADDR")
	pidFile := os.Getenv("SCRAPD_PID_FILE")
	startedAt := time.Now().UTC().Add(-time.Hour) // pretend we started a while ago
	if offset := os.Getenv("SCRAPD_FAKE_STARTED_OFFSET_MS"); offset != "" {
		if ms, err := strconv.Atoi(offset); err == nil {
			startedAt = time.Now().UTC().Add(-time.Duration(ms) * time.Millisecond)
		}
	}

	listener, err := net.Listen("tcp", address)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake scrapd: listen: %v\n", err)
		os.Exit(1)
	}
	if pidFile != "" {
		if err := WritePIDFile(pidFile); err != nil {
			fmt.Fprintf(os.Stderr, "fake scrapd: pid file: %v\n", err)
			os.Exit(1)
		}
		defer RemovePIDFile(pidFile)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /v1/info", func(w http.ResponseWriter, r *http.Request) {
		if secret := os.Getenv("SCRAPD_FAKE_TOKEN"); secret != "" {
			if r.Header.Get("Authorization") != "Bearer "+secret {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name":      "scrapd",
			"version":   "test",
			"commit":    "test",
			"dataDir":   "/tmp/fake",
			"startedAt": startedAt.Format(time.RFC3339),
			"pid":       os.Getpid(),
		})
	})

	server := &http.Server{Handler: mux}
	go func() {
		sigch := make(chan os.Signal, 1)
		signal.Notify(sigch, syscall.SIGTERM, syscall.SIGINT)
		<-sigch
		server.Close()
	}()
	_ = server.Serve(listener)
}

func freePort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer listener.Close()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	return port
}

func newTestManager(t *testing.T, port string) *Manager {
	t.Helper()
	manager, err := New(Options{
		URL:        "http://127.0.0.1:" + port,
		HomeDir:    t.TempDir(),
		ScrapdPath: os.Args[0],
		ExtraEnv:   []string{"SCRAPD_FAKE=1"},
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background()) })
	return manager
}

func TestLocalVsRemote(t *testing.T) {
	local, err := New(Options{URL: "http://localhost:8484", HomeDir: t.TempDir()})
	if err != nil || !local.IsLocal() {
		t.Fatalf("localhost should be managed: %v %v", local, err)
	}
	remote, err := New(Options{URL: "http://scrapd.internal:8484", HomeDir: t.TempDir()})
	if err != nil || remote.IsLocal() {
		t.Fatalf("remote host must not be managed: %v %v", remote, err)
	}
	tunnel, err := New(Options{URL: "http://127.0.0.1:8484", HomeDir: t.TempDir(), External: true})
	if err != nil || tunnel.IsLocal() {
		t.Fatalf("external loopback tunnel must not be managed: %v %v", tunnel, err)
	}
}

func TestStartStopRoundTrip(t *testing.T) {
	manager := newTestManager(t, freePort(t))
	ctx := context.Background()

	if status := manager.Probe(ctx); status.State != StateStopped {
		t.Fatalf("initial state = %s, want stopped", status.State)
	}

	action, status, err := manager.EnsureRunning(ctx, EnsureOptions{})
	if err != nil || action != ActionStarted || status.State != StateRunning {
		t.Fatalf("ensure = %s %s err=%v", action, status.State, err)
	}
	if _, err := ReadPIDFile(manager.PidFile()); err != nil {
		t.Fatalf("pid file missing after start: %v", err)
	}

	action, _, err = manager.EnsureRunning(ctx, EnsureOptions{})
	if err != nil || action != ActionNone {
		t.Fatalf("second ensure = %s err=%v, want already-running", action, err)
	}

	if err := manager.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if status := manager.Probe(ctx); status.State != StateStopped {
		t.Fatalf("post-stop state = %s", status.State)
	}
}

func TestRestartOnStaleBinary(t *testing.T) {
	port := freePort(t)
	manager, err := New(Options{
		URL:        "http://127.0.0.1:" + port,
		HomeDir:    t.TempDir(),
		ScrapdPath: os.Args[0],
		ExtraEnv:   []string{"SCRAPD_FAKE=1", "SCRAPD_FAKE_STARTED_OFFSET_MS=3600000"},
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background()) })
	ctx := context.Background()

	if _, _, err := manager.EnsureRunning(ctx, EnsureOptions{}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if status := manager.Probe(ctx); !status.StaleBinary {
		t.Fatalf("expected stale detection (binary newer than 1h-old daemon)")
	}

	// Auto mode must not restart a stale-but-healthy daemon.
	action, _, err := manager.EnsureRunning(ctx, EnsureOptions{})
	if err != nil || action != ActionNone {
		t.Fatalf("auto ensure on stale = %s err=%v, want no action", action, err)
	}

	// Explicit mode restarts it.
	action, _, err = manager.EnsureRunning(ctx, EnsureOptions{RestartStale: true})
	if err != nil || action != ActionRestartedStale {
		t.Fatalf("explicit ensure on stale = %s err=%v, want restart", action, err)
	}
}

func TestStopWithoutPidFileFindsPortOwner(t *testing.T) {
	// The spawned fake daemon is this test binary, not a real "scrapd".
	t.Setenv("SCRAPD_PORT_MATCH", "daemon")

	port := freePort(t)
	manager := newTestManager(t, port)
	ctx := context.Background()

	if _, _, err := manager.EnsureRunning(ctx, EnsureOptions{}); err != nil {
		t.Fatalf("start: %v", err)
	}
	RemovePIDFile(manager.PidFile()) // simulate a daemon started without one

	if err := manager.Stop(ctx); err != nil {
		t.Fatalf("stop via port: %v", err)
	}
	if status := manager.Probe(ctx); status.State != StateStopped {
		t.Fatalf("state after port stop = %s", status.State)
	}
}

func TestAuthMismatchIsReportedNotKilled(t *testing.T) {
	port := freePort(t)

	// Start a token-protected daemon through a correctly configured manager.
	owner, err := New(Options{
		URL:        "http://127.0.0.1:" + port,
		Token:      "secret",
		HomeDir:    t.TempDir(),
		ScrapdPath: os.Args[0],
		ExtraEnv:   []string{"SCRAPD_FAKE=1", "SCRAPD_FAKE_TOKEN=secret"},
	})
	if err != nil {
		t.Fatalf("new owner manager: %v", err)
	}
	t.Cleanup(func() { _ = owner.Stop(context.Background()) })
	if _, _, err := owner.EnsureRunning(context.Background(), EnsureOptions{}); err != nil {
		t.Fatalf("start token-protected daemon: %v", err)
	}

	// A manager with the wrong token must report, not manage or kill.
	wrongToken, err := New(Options{
		URL:     "http://127.0.0.1:" + port,
		Token:   "wrong-token",
		HomeDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("new wrong-token manager: %v", err)
	}
	ctx := context.Background()

	_, status, err := wrongToken.EnsureRunning(ctx, EnsureOptions{ForceRestart: true})
	if err == nil {
		t.Fatal("auth mismatch must not be silently managed")
	}
	if status.State != StateAuthError {
		t.Fatalf("state = %s, want auth-error", status.State)
	}
	// The daemon is untouched.
	if ownerStatus := owner.Probe(ctx); ownerStatus.State != StateRunning {
		t.Fatalf("owner-visible state = %s, want running", ownerStatus.State)
	}
}

func TestPIDFileHelpers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "scrapd.pid")
	if _, err := ReadPIDFile(path); err == nil {
		t.Fatal("read of missing pid file should fail")
	}
	if err := WritePIDFile(path); err != nil {
		t.Fatalf("write: %v", err)
	}
	pid, err := ReadPIDFile(path)
	if err != nil || pid != os.Getpid() {
		t.Fatalf("read = %d err=%v", pid, err)
	}
	RemovePIDFile(path)
	if _, err := ReadPIDFile(path); err == nil {
		t.Fatal("pid file should be gone")
	}
}
