package provider

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/peelar/scraps/internal/store"
	"github.com/peelar/scraps/internal/workspace"
)

func TestOpenShellLifecycleUsesGatewayCLI(t *testing.T) {
	bin := t.TempDir()
	logPath := filepath.Join(bin, "calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$OPENSHELL_TEST_LOG\"\n"
	if err := os.WriteFile(filepath.Join(bin, "openshell"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OPENSHELL_TEST_LOG", logPath)

	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	runtime, err := NewOpenShell(context.Background(), st, "example.test/scraps@sha256:abc")
	if err != nil {
		t.Fatal(err)
	}
	created, err := runtime.Create(context.Background(), workspace.CreateOptions{Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Stop(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Delete(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(calls)
	for _, want := range []string{
		"status",
		"sandbox create --name " + created.ID,
		"--cpu 2 --memory 4Gi",
		"provider get github-push",
		"--provider github-push --no-auto-providers --detach -- sleep infinity",
		"sandbox stop " + created.ID,
		"sandbox start " + created.ID,
		"sandbox delete " + created.ID,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("calls missing %q:\n%s", want, got)
		}
	}
}

func TestOpenShellRequiresGitHubAuthorizationBeforeRepositoryCreate(t *testing.T) {
	bin := t.TempDir()
	logPath := filepath.Join(bin, "calls")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$OPENSHELL_TEST_LOG"
if [ "$*" = "provider get github-push" ]; then exit 1; fi
exit 0
`
	if err := os.WriteFile(filepath.Join(bin, "openshell"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OPENSHELL_TEST_LOG", logPath)
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	runtime, err := NewOpenShell(context.Background(), st, "test-image")
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Create(context.Background(), workspace.CreateOptions{RepoURL: "https://github.com/owner/private.git"})
	var authorization *RepositoryAuthorizationRequiredError
	if !errors.As(err, &authorization) {
		t.Fatalf("create error = %v, want RepositoryAuthorizationRequiredError", err)
	}
	calls, _ := os.ReadFile(logPath)
	if strings.Contains(string(calls), "sandbox create") {
		t.Fatalf("sandbox was created before authorization preflight:\n%s", calls)
	}
}

func TestOpenShellCloneFailureIsCallerVisible(t *testing.T) {
	bin := t.TempDir()
	script := `#!/bin/sh
case "$*" in
  *"git clone "*) printf '%s\n' 'fatal: CONNECT tunnel failed, response 403' >&2; exit 128 ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(bin, "openshell"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	runtime, err := NewOpenShell(context.Background(), st, "test-image")
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Create(context.Background(), workspace.CreateOptions{RepoURL: "https://example.com/owner/repo.git"})
	var clone *RepositoryCloneError
	if !errors.As(err, &clone) || !strings.Contains(clone.Error(), "network policy denied") {
		t.Fatalf("create error = %v, want actionable RepositoryCloneError", err)
	}
}

func TestReadyReclaimsStalePreheatedRecord(t *testing.T) {
	bin := t.TempDir()
	script := `#!/bin/sh
if [ "$*" = "sandbox list --output json" ]; then printf '[]\n'; fi
`
	if err := os.WriteFile(filepath.Join(bin, "openshell"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateWorkspace(context.Background(), store.Workspace{ID: "stale-ready", Provider: "openshell", State: "preheated"}); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewOpenShell(context.Background(), st, "test-image")
	if err != nil {
		t.Fatal(err)
	}
	ready, err := runtime.Ready(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 0 {
		t.Fatalf("ready = %+v, want none", ready)
	}
	if _, err := st.GetWorkspace(context.Background(), "stale-ready"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("stale row lookup error = %v, want not found", err)
	}
}

func TestOpenShellCancellationRecyclesSandbox(t *testing.T) {
	bin := t.TempDir()
	logPath := filepath.Join(bin, "calls")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$OPENSHELL_TEST_LOG"
case "$*" in
  *HOSTILE_CANCEL_TEST*) exec sleep 30 ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(bin, "openshell"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OPENSHELL_TEST_LOG", logPath)
	for key, value := range map[string]string{
		"AWS_SECRET_ACCESS_KEY": "sentinel-aws",
		"NPM_TOKEN":             "sentinel-npm",
		"OPENAI_API_KEY":        "sentinel-openai",
		"SCRAP_TOKEN":           "sentinel-scrap",
		"SSH_AUTH_SOCK":         "/tmp/sentinel-agent.sock",
	} {
		t.Setenv(key, value)
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const id = "hostile-cancel"
	if err := st.CreateWorkspace(context.Background(), store.Workspace{ID: id, Provider: "openshell", State: "running"}); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewOpenShell(context.Background(), st, "test-image")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var eventsMu sync.Mutex
	var events []ExecEvent
	started := time.Now()
	err = runtime.Exec(ctx, id, ExecRequest{
		Command: ": HOSTILE_CANCEL_TEST; trap '' TERM; (sleep 30) & wait",
		CWD:     ".",
		Env:     map[string]string{"DATABASE_URL": "postgres://sentinel-approved"},
	}, func(event ExecEvent) {
		eventsMu.Lock()
		defer eventsMu.Unlock()
		events = append(events, event)
	})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("cancellation took %v", elapsed)
	}
	eventsMu.Lock()
	last := events[len(events)-1]
	eventsMu.Unlock()
	if last.Type != "exit" || last.Reason != "timeout" {
		t.Fatalf("last event = %+v, want timeout exit", last)
	}

	callsBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	calls := string(callsBytes)
	stop := strings.Index(calls, "sandbox stop "+id)
	start := strings.Index(calls, "sandbox start "+id)
	if stop < 0 || start < 0 || stop >= start {
		t.Fatalf("cancellation did not stop then restart sandbox:\n%s", calls)
	}
	for _, forbidden := range []string{".scrap/run", "scrap-cleanup", "setsid", "AWS_SECRET_ACCESS_KEY", "NPM_TOKEN", "OPENAI_API_KEY", "SCRAP_TOKEN", "SSH_AUTH_SOCK"} {
		if strings.Contains(calls, forbidden) {
			t.Errorf("OpenShell arguments contain forbidden value %q:\n%s", forbidden, calls)
		}
	}
	if !strings.Contains(calls, "--env DATABASE_URL=postgres://sentinel-approved") {
		t.Fatalf("OpenShell arguments missing approved environment:\n%s", calls)
	}
	workspace, err := st.GetWorkspace(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if workspace.State != "running" {
		t.Fatalf("workspace state = %q, want running", workspace.State)
	}
}

func TestRecycleSandboxFailsClosedWhenStopFails(t *testing.T) {
	bin := t.TempDir()
	logPath := filepath.Join(bin, "calls")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$OPENSHELL_TEST_LOG"
if [ "$1 $2" = "sandbox stop" ]; then exit 42; fi
exit 0
`
	if err := os.WriteFile(filepath.Join(bin, "openshell"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OPENSHELL_TEST_LOG", logPath)

	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const id = "stop-fails"
	if err := st.CreateWorkspace(context.Background(), store.Workspace{ID: id, Provider: "openshell", State: "running"}); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewOpenShell(context.Background(), st, "test-image")
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.recycleSandbox(context.Background(), id); err == nil {
		t.Fatal("recycle succeeded even though trusted stop failed")
	}
	callsBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(callsBytes), "sandbox start "+id) {
		t.Fatalf("unsafe restart after failed stop:\n%s", callsBytes)
	}
}

func TestOpenShellMissingPathsMapToFilesystemErrors(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	bin := t.TempDir()
	// Canonicalize so realpath(probe) inside the containment script still
	// string-matches the root on macOS, where /var resolves to /private/var.
	wsRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(bin, "calls")
	if err := os.WriteFile(filepath.Join(wsRoot, "spec.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Realistic gateway double: `sandbox exec` payloads (python3 stat script,
	// cat, test, find) run against the host with every /workspace reference —
	// path arguments and the literal roots inside provider scripts —
	// rewritten to a temp dir, so error classification is exercised through
	// the same payloads a real sandbox would run.
	script := `#!/bin/bash
printf '%s\n' "$*" >> "$OPENSHELL_TEST_LOG"
if [ "$1" = sandbox ] && [ "$2" = exec ]; then
  shift 2
  while [ "$1" != -- ]; do shift; done
  shift
  rewritten=()
  for arg in "$@"; do
    rewritten+=("${arg//\/workspace/$OPENSHELL_TEST_WS_ROOT}")
  done
  exec "${rewritten[@]}"
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(bin, "openshell"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OPENSHELL_TEST_LOG", logPath)
	t.Setenv("OPENSHELL_TEST_WS_ROOT", wsRoot)

	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const id = "missing-paths"
	if err := st.CreateWorkspace(context.Background(), store.Workspace{ID: id, Provider: "openshell", State: "running"}); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewOpenShell(context.Background(), st, "test-image")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Existing paths keep working.
	info, err := runtime.Stat(ctx, id, "spec.md")
	if err != nil {
		t.Fatalf("stat existing file: %v", err)
	}
	if info.Size() != 5 {
		t.Fatalf("stat size = %d, want 5", info.Size())
	}
	content, _, err := runtime.ReadFile(ctx, id, "spec.md", 1<<20)
	if err != nil {
		t.Fatalf("read existing file: %v", err)
	}
	if string(content) != "hello" {
		t.Fatalf("read content = %q, want %q", content, "hello")
	}
	if err := runtime.Access(ctx, id, "spec.md", AccessRead); err != nil {
		t.Fatalf("access existing file: %v", err)
	}

	// Missing paths must surface as fs.ErrNotExist so the HTTP layer answers
	// 404 instead of a generic 500 "internal server error".
	for name, check := range map[string]func() error{
		"stat missing file":   func() error { _, err := runtime.Stat(ctx, id, "absent.md"); return err },
		"read missing file":   func() error { _, _, err := runtime.ReadFile(ctx, id, "absent.md", 1<<20); return err },
		"access missing file": func() error { return runtime.Access(ctx, id, "absent.md", AccessRead) },
		"stat under a file":   func() error { _, err := runtime.Stat(ctx, id, "spec.md/nested"); return err },
	} {
		if err := check(); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s: error = %v, want fs.ErrNotExist", name, err)
		}
	}

	// ReadDir classification depends on GNU find (-printf); verify support first.
	if out, err := exec.Command("find", wsRoot, "-mindepth", "1", "-maxdepth", "1", "-printf", "%f\\n").Output(); err == nil && strings.TrimSpace(string(out)) == "spec.md" {
		entries, err := runtime.ReadDir(ctx, id, ".")
		if err != nil {
			t.Fatalf("readdir root: %v", err)
		}
		if len(entries) != 1 || entries[0] != "spec.md" {
			t.Fatalf("readdir root entries = %v, want [spec.md]", entries)
		}
		if _, err := runtime.ReadDir(ctx, id, "absent-dir"); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("readdir missing dir: error = %v, want fs.ErrNotExist", err)
		}
	} else {
		t.Log("skipping ReadDir checks: host find lacks -printf support")
	}
}

func TestLiveOpenShellHostileCancellation(t *testing.T) {
	if os.Getenv("SCRAPS_LIVE_OPENSHELL_SECURITY_TEST") != "1" {
		t.Skip("set SCRAPS_LIVE_OPENSHELL_SECURITY_TEST=1 to use the configured gateway")
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	runtime, err := NewOpenShell(context.Background(), st, defaultOpenShellImage)
	if err != nil {
		t.Fatal(err)
	}
	created, err := runtime.Create(context.Background(), workspace.CreateOptions{Project: "security-cancellation-test"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := runtime.Delete(cleanupCtx, created.ID); err != nil {
			t.Errorf("delete disposable sandbox: %v", err)
		}
	}()

	const hostile = `touch /workspace/cancellation-test-persisted
rm -rf /workspace/.scrap/run
trap '' TERM
setsid /bin/sh -c ': SCRAPS_CANCEL_SURVIVOR; trap "" TERM; while :; do sleep 1; done' >/dev/null 2>&1 &
while :; do sleep 1; done`
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var lastMu sync.Mutex
	var last ExecEvent
	if err := runtime.Exec(ctx, created.ID, ExecRequest{Command: hostile, CWD: "."}, func(event ExecEvent) {
		lastMu.Lock()
		defer lastMu.Unlock()
		last = event
	}); err != nil {
		t.Fatal(err)
	}
	lastMu.Lock()
	finalEvent := last
	lastMu.Unlock()
	if finalEvent.Type != "exit" || finalEvent.Reason != "timeout" {
		t.Fatalf("last event = %+v, want timeout exit", finalEvent)
	}

	verifyCtx, verifyCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer verifyCancel()
	if _, err := runtime.execRaw(verifyCtx, created.ID, nil, "test", "-f", "/workspace/cancellation-test-persisted"); err != nil {
		t.Fatalf("persistent workspace did not survive recycle: %v", err)
	}
	if _, err := runtime.execRaw(verifyCtx, created.ID, nil, "/bin/bash", "-c", `! ps -eo args | grep -q '[S]CRAPS_CANCEL_SURVIVOR'`); err != nil {
		t.Fatalf("hostile descendant survived cancellation: %v", err)
	}
}
