package testprovider

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"

	"github.com/peelar/scraps/internal/provider"
	"github.com/peelar/scraps/internal/store"
	"github.com/peelar/scraps/internal/workspace"
)

// Directory is the trusted-development provider. It supplies no process isolation.
type Directory struct{ manager *workspace.Manager }

func NewDirectory(st *store.Store, dataDir string) (*Directory, error) {
	manager, err := workspace.NewManager(st, dataDir)
	if err != nil {
		return nil, err
	}
	return &Directory{manager: manager}, nil
}
func (*Directory) Info() provider.Info {
	return provider.Info{
		Name:      "directory",
		Isolation: provider.Isolation("none"),
		Policy: provider.Policy{
			Environment:    "minimal",
			Network:        "host-unrestricted",
			Resources:      "host-unlimited",
			Credentials:    "none",
			ProcessCleanup: "process-group",
		},
	}
}
func (d *Directory) Create(c context.Context, o workspace.CreateOptions) (workspace.Workspace, error) {
	return d.manager.Create(c, o)
}
func (d *Directory) Get(c context.Context, id string) (workspace.Workspace, error) {
	return d.manager.Get(c, id)
}
func (d *Directory) List(c context.Context) ([]workspace.Workspace, error) { return d.manager.List(c) }
func (d *Directory) Start(c context.Context, id string) error              { return d.manager.Start(c, id) }
func (d *Directory) Stop(c context.Context, id string) error               { return d.manager.Stop(c, id) }
func (d *Directory) Delete(c context.Context, id string) error             { return d.manager.Delete(c, id) }

func (d *Directory) path(id, path string) (string, error) { return d.manager.ResolvePath(id, path) }

func (d *Directory) Exec(ctx context.Context, id string, r provider.ExecRequest, emit func(provider.ExecEvent)) error {
	w, err := d.Get(ctx, id)
	if err != nil {
		return err
	}
	if w.State != "running" {
		return &provider.InvalidRequestError{Message: "workspace is stopped: " + id}
	}
	cwd := r.CWD
	if cwd == "" {
		cwd = "."
	}
	cwd, err = d.path(id, cwd)
	if err != nil {
		return err
	}
	if info, err := os.Stat(cwd); err != nil || !info.IsDir() {
		return &provider.InvalidRequestError{Message: "working directory does not exist: " + r.CWD}
	}
	hostRoot, err := d.path(id, ".")
	if err != nil {
		return err
	}
	// Sandboxed providers mount /workspace directly. The trusted directory
	// provider emulates it and redacts its private host root from output.
	command := strings.ReplaceAll(r.Command, workspace.VirtualRoot, hostRoot)
	commandEnv, err := directoryEnvironment(hostRoot, r.Env)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "/bin/bash", "-c", command)
	cmd.Dir = cwd
	cmd.Env = commandEnv
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	emit(provider.ExecEvent{Type: "start", PID: cmd.Process.Pid})
	var wg sync.WaitGroup
	wg.Add(2)
	go copyOutput(&wg, stdout, "stdout", hostRoot, emit)
	go copyOutput(&wg, stderr, "stderr", hostRoot, emit)
	wg.Wait()
	waitErr := cmd.Wait()
	e := provider.ExecEvent{Type: "exit"}
	var exitErr *exec.ExitError
	switch {
	case waitErr == nil:
		code := 0
		e.Code = &code
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		e.Reason = "timeout"
	case errors.Is(ctx.Err(), context.Canceled):
		e.Reason = "cancelled"
	case errors.As(waitErr, &exitErr) && exitErr.ExitCode() >= 0:
		code := exitErr.ExitCode()
		e.Code = &code
	default:
		return waitErr
	}
	emit(e)
	return nil
}

func copyOutput(wg *sync.WaitGroup, r io.Reader, stream, hostRoot string, emit func(provider.ExecEvent)) {
	defer wg.Done()
	b := make([]byte, 32*1024)
	for {
		n, err := r.Read(b)
		if n > 0 {
			data := []byte(strings.ReplaceAll(string(b[:n]), hostRoot, workspace.VirtualRoot))
			emit(provider.ExecEvent{Type: "output", Stream: stream, Data: data})
		}
		if err != nil {
			return
		}
	}
}
func directoryEnvironment(hostRoot string, requested map[string]string) ([]string, error) {
	tmp := filepath.Join(hostRoot, ".scrap", "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return nil, fmt.Errorf("create workspace temp directory: %w", err)
	}
	env := map[string]string{
		"HOME":   hostRoot,
		"PATH":   os.Getenv("PATH"),
		"SHELL":  "/bin/bash",
		"TMPDIR": tmp,
		// Directory mode emulates /workspace; output redaction preserves the
		// public value while host processes need the real path to use it.
		"SCRAP_WORKSPACE_ROOT": hostRoot,
	}
	for _, key := range []string{"LANG", "LC_ALL", "TERM"} {
		if value := os.Getenv(key); value != "" {
			env[key] = value
		}
	}
	for key, value := range requested {
		env[key] = value
	}
	out := make([]string, 0, len(env))
	for key, value := range env {
		out = append(out, key+"="+value)
	}
	return out, nil
}

func (d *Directory) ReadFile(_ context.Context, id, path string, max int64) ([]byte, fs.FileInfo, error) {
	p, err := d.path(id, path)
	if err != nil {
		return nil, nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if info.IsDir() {
		return nil, nil, &provider.InvalidRequestError{Message: "path is a directory: " + path}
	}
	if info.Size() > max {
		return nil, nil, &provider.InvalidRequestError{Message: "file exceeds 100MB limit"}
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	return b, info, err
}
func (d *Directory) WriteFile(_ context.Context, id, path string, b []byte) error {
	p, e := d.path(id, path)
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(p), 0755); e != nil {
		return e
	}
	return os.WriteFile(p, b, 0644)
}
func (d *Directory) Mkdir(_ context.Context, id, path string) error {
	p, e := d.path(id, path)
	if e != nil {
		return e
	}
	return os.MkdirAll(p, 0755)
}
func (d *Directory) Stat(_ context.Context, id, path string) (fs.FileInfo, error) {
	p, e := d.path(id, path)
	if e != nil {
		return nil, e
	}
	return os.Stat(p)
}
func (d *Directory) Access(_ context.Context, id, path string, mode provider.AccessMode) error {
	p, e := d.path(id, path)
	if e != nil {
		return e
	}
	if mode == provider.AccessRead {
		f, e := os.Open(p)
		if e != nil {
			return e
		}
		return f.Close()
	}
	f, e := os.OpenFile(p, os.O_WRONLY, 0)
	if e != nil {
		return e
	}
	return f.Close()
}
func (d *Directory) ReadDir(_ context.Context, id, path string) ([]string, error) {
	p, e := d.path(id, path)
	if e != nil {
		return nil, e
	}
	entries, e := os.ReadDir(p)
	if e != nil {
		return nil, e
	}
	names := make([]string, len(entries))
	for i, v := range entries {
		names[i] = v.Name()
	}
	return names, nil
}

var ignored = map[string]bool{".git": true, "node_modules": true, ".DS_Store": true}

func (d *Directory) Glob(ctx context.Context, id string, r provider.GlobRequest) ([]string, error) {
	if _, e := d.Get(ctx, id); e != nil {
		return nil, e
	}
	root := r.Path
	if root == "" {
		root = "."
	}
	root, e := d.path(id, root)
	if e != nil {
		return nil, e
	}
	info, e := os.Stat(root)
	if e != nil || !info.IsDir() {
		return nil, &provider.InvalidRequestError{Message: "search path is not a directory: " + r.Path}
	}
	limit := r.Limit
	if limit <= 0 {
		limit = 200
	}
	match := compileGlob(r.Pattern)
	workspaceRoot, e := d.path(id, ".")
	if e != nil {
		return nil, e
	}
	paths := []string{}
	e = filepath.WalkDir(root, func(path string, de fs.DirEntry, e error) error {
		if e != nil {
			return nil
		}
		if de.IsDir() {
			if ignored[de.Name()] && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		rel := filepath.ToSlash(strings.TrimPrefix(path, root+string(filepath.Separator)))
		if match(rel) || match(filepath.Base(path)) {
			paths = append(paths, filepath.ToSlash(strings.TrimPrefix(path, workspaceRoot+string(filepath.Separator))))
			if len(paths) >= limit {
				return fs.SkipAll
			}
		}
		return nil
	})
	return paths, e
}
func compileGlob(pattern string) func(string) bool {
	x := regexp.QuoteMeta(pattern)
	x = strings.ReplaceAll(x, `\*\*/`, `(?:.*/)?`)
	x = strings.ReplaceAll(x, `\*\*`, `.*`)
	x = strings.ReplaceAll(x, `\*`, `[^/]*`)
	x = strings.ReplaceAll(x, `\?`, `[^/]`)
	m := regexp.MustCompile(`^` + x + `$`)
	return m.MatchString
}

func (d *Directory) Grep(ctx context.Context, id string, r provider.GrepRequest) (provider.GrepResult, error) {
	var matcher func(string) bool
	if r.Literal {
		needle := r.Pattern
		if r.IgnoreCase {
			needle = strings.ToLower(needle)
			matcher = func(s string) bool { return strings.Contains(strings.ToLower(s), needle) }
		} else {
			matcher = func(s string) bool { return strings.Contains(s, needle) }
		}
	} else {
		flags := ""
		if r.IgnoreCase {
			flags = "(?i)"
		}
		re, e := regexp.Compile(flags + r.Pattern)
		if e != nil {
			return provider.GrepResult{}, &provider.InvalidRequestError{Message: "invalid pattern: " + e.Error()}
		}
		matcher = re.MatchString
	}
	if _, e := d.Get(ctx, id); e != nil {
		return provider.GrepResult{}, e
	}
	root := r.Path
	if root == "" {
		root = "."
	}
	root, e := d.path(id, root)
	if e != nil {
		return provider.GrepResult{}, e
	}
	info, e := os.Stat(root)
	if e != nil || !info.IsDir() {
		return provider.GrepResult{}, &provider.InvalidRequestError{Message: "search path is not a directory: " + r.Path}
	}
	limit := r.Limit
	if limit <= 0 {
		limit = 100
	}
	contextLines := max(r.Context, 0)
	var gm func(string) bool
	if r.Glob != "" {
		gm = compileGlob(r.Glob)
	}
	result := provider.GrepResult{Matches: []provider.GrepMatch{}}
	workspaceRoot, e := d.path(id, ".")
	if e != nil {
		return provider.GrepResult{}, e
	}
	_ = filepath.WalkDir(root, func(path string, de fs.DirEntry, e error) error {
		if e != nil {
			return nil
		}
		if de.IsDir() {
			if ignored[de.Name()] && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if !searchable(path, gm) {
			return nil
		}
		displayPath := filepath.ToSlash(strings.TrimPrefix(path, workspaceRoot+string(filepath.Separator)))
		matches := grepFile(path, displayPath, matcher, contextLines, limit-len(result.Matches))
		result.Matches = append(result.Matches, matches...)
		if len(result.Matches) >= limit {
			result.LimitReached = true
			return fs.SkipAll
		}
		return nil
	})
	return result, nil
}
func searchable(path string, gm func(string) bool) bool {
	if gm != nil {
		return gm(filepath.ToSlash(path)) || gm(filepath.Base(path))
	}
	f, e := os.Open(path)
	if e != nil {
		return false
	}
	defer f.Close()
	b := make([]byte, 1024)
	n, _ := f.Read(b)
	return !strings.ContainsRune(string(b[:n]), 0)
}
func grepFile(path, displayPath string, matcher func(string) bool, c, budget int) []provider.GrepMatch {
	f, e := os.Open(path)
	if e != nil {
		return nil
	}
	defer f.Close()
	var lines []string
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for s.Scan() {
		lines = append(lines, s.Text())
	}
	if s.Err() != nil {
		return nil
	}
	var out []provider.GrepMatch
	for i, line := range lines {
		if !matcher(line) {
			continue
		}
		m := provider.GrepMatch{Path: displayPath, LineNumber: i + 1, LineText: line, Lines: []provider.GrepLine{}}
		if c > 0 {
			start := max(i+1-c, 1)
			end := min(i+1+c, len(lines))
			for n := start; n <= end; n++ {
				m.Lines = append(m.Lines, provider.GrepLine{N: n, Text: lines[n-1], Match: n == i+1})
			}
		}
		out = append(out, m)
		if len(out) >= budget {
			break
		}
	}
	return out
}
