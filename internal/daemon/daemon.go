// Package daemon supervises a local scrapd process from the scrap CLI:
// start it detached, wait for health, stop it, and replace stale instances
// after rebuilds. It only manages daemons on loopback URLs; a remote
// SCRAP_DAEMON_URL is never touched.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/peelar/scraps/internal/client"
)

// Options configure a Manager.
type Options struct {
	// URL is the daemon base URL (default http://127.0.0.1:8484).
	URL string
	// Token is the bearer token, when the daemon requires one.
	Token string
	// HomeDir overrides ~/.scrap (tests).
	HomeDir string
	// ScrapdPath overrides scrapd discovery (tests).
	ScrapdPath string
	// ExtraEnv is added to the spawn environment (tests).
	ExtraEnv []string
	// External marks a loopback tunnel as remotely managed (for example, the
	// local worker VM). The client may connect, but must not spawn or kill it.
	External bool
}

// Manager owns the lifecycle of one local scrapd.
type Manager struct {
	opts    Options
	baseURL string
	host    string
	port    string
	api     *client.Client
	pidFile string
	logFile string
	local   bool
}

// New builds a Manager for the given options.
func New(opts Options) (*Manager, error) {
	raw := opts.URL
	if raw == "" {
		raw = "http://127.0.0.1:8484"
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid daemon url: %w", err)
	}
	host := parsed.Hostname()
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}

	home := opts.HomeDir
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve home: %w", err)
		}
		home = filepath.Join(userHome, ".scrap")
	}

	manager := &Manager{
		opts:    opts,
		baseURL: strings.TrimRight(raw, "/"),
		host:    host,
		port:    port,
		api:     client.New(raw, opts.Token),
		pidFile: filepath.Join(home, fmt.Sprintf("scrapd-%s.pid", port)),
		logFile: filepath.Join(home, fmt.Sprintf("scrapd-%s.log", port)),
	}
	manager.local = isLoopback(host) && !opts.External
	return manager, nil
}

func isLoopback(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1", "[::1]":
		return true
	}
	return false
}

// IsLocal reports whether the daemon URL points at this machine.
func (m *Manager) IsLocal() bool { return m.local }

// URL returns the daemon base URL.
func (m *Manager) URL() string { return m.baseURL }

// PidFile and LogFile expose supervision file locations.
func (m *Manager) PidFile() string { return m.pidFile }
func (m *Manager) LogFile() string { return m.logFile }

// Client returns an API client for the daemon URL.
func (m *Manager) Client() *client.Client { return m.api }

// State classifies what Probe found.
type State string

const (
	StateRunning     State = "running"      // healthy scrapd answered
	StateAuthError   State = "auth-error"   // answered, but rejected the token
	StateForeign     State = "foreign"      // answered, but not a scrapd
	StateStopped     State = "stopped"      // nothing listening
	StateHungProcess State = "hung-process" // pid file has a live process, not answering
)

// Status is the result of probing the daemon.
type Status struct {
	State       State
	Info        *client.InfoResponse
	PID         int    // from the pid file, 0 when absent
	StaleBinary bool   // the scrapd binary is newer than the running daemon
	Detail      string // human-readable extra, when relevant
	CheckedAt   time.Time
}

// Probe classifies the daemon's current state without side effects.
func (m *Manager) Probe(ctx context.Context) Status {
	status := Status{CheckedAt: time.Now()}
	if pid, err := ReadPIDFile(m.pidFile); err == nil {
		status.PID = pid
	}

	info, err := m.fetchInfo(ctx)
	switch {
	case err == nil:
		status.State = StateRunning
		status.Info = &info
		if status.PID == 0 {
			status.PID = info.PID
		}
		status.StaleBinary = m.binaryNewerThan(info)
	case isAuthError(err):
		status.State = StateAuthError
		status.Detail = "daemon answered but rejected SCRAP_TOKEN"
	case isHTTPAnswer(err):
		status.State = StateForeign
		status.Detail = err.Error()
	default:
		// Nothing (useful) answered. A live pid-file process is a hung daemon.
		status.State = StateStopped
		if status.PID != 0 && processAlive(status.PID) {
			status.State = StateHungProcess
		}
	}
	return status
}

func (m *Manager) fetchInfo(ctx context.Context) (client.InfoResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, m.baseURL+"/v1/info", nil)
	if err != nil {
		return client.InfoResponse{}, err
	}
	if m.opts.Token != "" {
		request.Header.Set("Authorization", "Bearer "+m.opts.Token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return client.InfoResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		return client.InfoResponse{}, &client.Error{Status: 401, Code: "unauthorized", Message: "rejected token"}
	}
	if response.StatusCode != http.StatusOK {
		return client.InfoResponse{}, &client.Error{Status: response.StatusCode, Code: "http_error", Message: response.Status}
	}
	var info client.InfoResponse
	if err := json.NewDecoder(response.Body).Decode(&info); err != nil {
		return client.InfoResponse{}, err
	}
	if info.Name != "scrapd" {
		return client.InfoResponse{}, fmt.Errorf("unexpected daemon %q", info.Name)
	}
	return info, nil
}

func isAuthError(err error) bool {
	var apiErr *client.Error
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusUnauthorized
}

func isHTTPAnswer(err error) bool {
	var apiErr *client.Error
	return errors.As(err, &apiErr)
}

// binaryNewerThan reports whether the scrapd binary on disk was built after
// the daemon started (i.e. the daemon runs stale code).
func (m *Manager) binaryNewerThan(info client.InfoResponse) bool {
	path, err := m.scrapdPath()
	if err != nil {
		return false
	}
	stat, err := os.Stat(path)
	if err != nil {
		return false
	}
	startedAt, err := time.Parse(time.RFC3339, info.StartedAt)
	if err != nil {
		return false
	}
	return stat.ModTime().After(startedAt.Add(time.Second))
}

// EnsureOptions control EnsureRunning.
type EnsureOptions struct {
	// RestartStale restarts a healthy daemon whose binary is newer than the
	// daemon itself (explicit `scrap up`). Auto mode leaves it alone.
	RestartStale bool
	// ForceRestart stops a healthy daemon unconditionally.
	ForceRestart bool
}

// Action describes what EnsureRunning did.
type Action string

const (
	ActionNone            Action = "already-running"
	ActionStarted         Action = "started"
	ActionRestartedStale  Action = "restarted-stale"
	ActionRestartedForced Action = "restarted"
)

// EnsureRunning brings the daemon up, returning the action taken.
func (m *Manager) EnsureRunning(ctx context.Context, options EnsureOptions) (Action, Status, error) {
	status := m.Probe(ctx)

	switch status.State {
	case StateRunning:
		switch {
		case options.ForceRestart:
			if err := m.Stop(ctx); err != nil {
				return ActionNone, status, err
			}
		case status.StaleBinary && options.RestartStale:
			if err := m.Stop(ctx); err != nil {
				return ActionNone, status, err
			}
		default:
			return ActionNone, status, nil
		}
		started, err := m.Start(ctx)
		if err != nil {
			return ActionNone, status, err
		}
		if options.ForceRestart {
			return ActionRestartedForced, started, nil
		}
		return ActionRestartedStale, started, nil

	case StateAuthError, StateForeign:
		return ActionNone, status, fmt.Errorf("cannot manage daemon at %s: %s", m.baseURL, status.Detail)

	case StateHungProcess:
		// Kill the hung instance, then start fresh.
		if err := killProcess(status.PID); err != nil {
			return ActionNone, status, fmt.Errorf("stop hung scrapd (pid %d): %w", status.PID, err)
		}
		RemovePIDFile(m.pidFile)

	default: // StateStopped
	}
	started, err := m.Start(ctx)
	if err != nil {
		return ActionNone, status, err
	}
	return ActionStarted, started, nil
}

// scrapdPath locates the daemon binary: explicit override, SCRAPD_PATH,
// next to the scrap executable, or ./bin/scrapd for repo checkouts.
func (m *Manager) scrapdPath() (string, error) {
	candidates := []string{}
	if m.opts.ScrapdPath != "" {
		candidates = append(candidates, m.opts.ScrapdPath)
	}
	if fromEnv := os.Getenv("SCRAPD_PATH"); fromEnv != "" {
		candidates = append(candidates, fromEnv)
	}
	if self, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(self), "scrapd"))
	}
	candidates = append(candidates, filepath.Join("bin", "scrapd"))

	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", errors.New("scrapd binary not found (looked in: " + strings.Join(candidates, ", ") + "); build it with `make build` or set SCRAPD_PATH")
}

// Start spawns a detached scrapd and waits until it is healthy.
func (m *Manager) Start(ctx context.Context) (Status, error) {
	if err := os.MkdirAll(filepath.Dir(m.pidFile), 0o755); err != nil {
		return Status{}, fmt.Errorf("prepare scrap home: %w", err)
	}

	path, err := m.scrapdPath()
	if err != nil {
		return Status{}, err
	}

	// A foreign process on the port must not be killed blindly; surface it.
	if somethingListening(m.host, m.port) && m.Probe(ctx).State == StateStopped {
		return Status{}, fmt.Errorf("another process is already listening on %s:%s (not scrapd); free the port or set SCRAP_DAEMON_URL", m.host, m.port)
	}

	logFile, err := os.OpenFile(m.logFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return Status{}, fmt.Errorf("open log file: %w", err)
	}
	defer logFile.Close()

	command := exec.Command(path)
	command.Env = append(os.Environ(),
		"SCRAPD_LISTEN_ADDR="+m.host+":"+m.port,
		"SCRAPD_PID_FILE="+m.pidFile,
	)
	command.Env = append(command.Env, m.opts.ExtraEnv...)
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return Status{}, fmt.Errorf("start scrapd: %w", err)
	}

	// Wait for health (daemon start is fast, but SQLite + dirs take a moment).
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return Status{}, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		status := m.Probe(context.Background())
		switch status.State {
		case StateRunning:
			return status, nil
		case StateAuthError, StateForeign:
			return status, fmt.Errorf("scrapd started but %s", status.Detail)
		}
	}

	command.Process.Kill()
	return Status{}, fmt.Errorf("scrapd did not become healthy within 10s; last log lines:\n%s", tailFile(m.logFile, 15))
}

// Stop terminates the daemon tracked by the pid file (or discovered on the
// port), falling back to lsof for daemons started without a pid file.
func (m *Manager) Stop(ctx context.Context) error {
	pids := []int{}
	if pid, err := ReadPIDFile(m.pidFile); err == nil {
		pids = append(pids, pid)
	}
	pids = append(pids, portOwners(m.port)...)

	stoppedAny := false
	seen := map[int]bool{}
	for _, pid := range pids {
		if pid == 0 || seen[pid] || !processAlive(pid) {
			continue
		}
		seen[pid] = true
		if err := killProcess(pid); err != nil {
			return fmt.Errorf("stop scrapd (pid %d): %w", pid, err)
		}
		stoppedAny = true
	}
	RemovePIDFile(m.pidFile)

	if !stoppedAny && m.Probe(ctx).State == StateRunning {
		return errors.New("scrapd still running but its pid could not be determined")
	}
	return nil
}

func killProcess(pid int) error {
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.EPERM) {
		return err
	}
	if waitExit(pid, 5*time.Second) {
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	waitExit(pid, 3*time.Second)
	return nil
}

func waitExit(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return !processAlive(pid)
}

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// portOwners finds scrapd pids listening on the port via lsof, when
// available. Only processes whose name matches are returned (SCRAPD_PORT_MATCH
// overrides the pattern for tests).
func portOwners(port string) []int {
	match := strings.ToLower(os.Getenv("SCRAPD_PORT_MATCH"))
	if match == "" {
		match = "scrapd"
	}
	output, err := exec.Command("lsof", "-nP", "-iTCP:"+port, "-sTCP:LISTEN").Output()
	if err != nil {
		return nil
	}
	var pids []int
	seen := map[int]bool{}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.Contains(strings.ToLower(fields[0]), match) {
			continue
		}
		if pid, err := strconv.Atoi(fields[1]); err == nil && !seen[pid] {
			seen[pid] = true
			pids = append(pids, pid)
		}
	}
	return pids
}

func somethingListening(host, port string) bool {
	connection, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 250*time.Millisecond)
	if err != nil {
		return false
	}
	connection.Close()
	return true
}

// --- pid file helpers (also used by cmd/scrapd) ---

// WritePIDFile records a pid.
func WritePIDFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o644)
}

// ReadPIDFile loads a pid. Errors when missing or malformed.
func ReadPIDFile(path string) (int, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(content)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("invalid pid file %s", path)
	}
	return pid, nil
}

// RemovePIDFile deletes a pid file, ignoring absence.
func RemovePIDFile(path string) {
	_ = os.Remove(path)
}

func tailFile(path string, lines int) string {
	content, err := os.ReadFile(path)
	if err != nil {
		return "(log unavailable: " + err.Error() + ")"
	}
	all := strings.Split(strings.TrimRight(string(content), "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n")
}
