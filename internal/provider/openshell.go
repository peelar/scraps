package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os/exec"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/peelar/scraps/internal/githubauth"
	"github.com/peelar/scraps/internal/store"
	"github.com/peelar/scraps/internal/workspace"
)

const (
	defaultOpenShellImage = "scraps-dev:bookworm"
	scrapsWorkspaceLabel  = "dev.scraps.workspace"
)

type OpenShell struct {
	store      *store.Store
	image      string
	executions keyedExecutionLocks
}

// keyedExecutionLocks serializes commands within one sandbox. Cancellation
// recycles that sandbox, so overlapping execs would otherwise be collateral
// damage. Entries are reference-counted so deleted workspaces do not leak
// lock state forever.
type keyedExecutionLocks struct {
	mu      sync.Mutex
	entries map[string]*keyedExecutionLock
}

type keyedExecutionLock struct {
	gate chan struct{}
	refs int
}

func (l *keyedExecutionLocks) acquire(ctx context.Context, id string) (func(), error) {
	l.mu.Lock()
	if l.entries == nil {
		l.entries = make(map[string]*keyedExecutionLock)
	}
	entry := l.entries[id]
	if entry == nil {
		entry = &keyedExecutionLock{gate: make(chan struct{}, 1)}
		entry.gate <- struct{}{}
		l.entries[id] = entry
	}
	entry.refs++
	l.mu.Unlock()

	select {
	case <-ctx.Done():
		l.releaseRef(id, entry)
		return nil, ctx.Err()
	case <-entry.gate:
		return func() {
			entry.gate <- struct{}{}
			l.releaseRef(id, entry)
		}, nil
	}
}

func (l *keyedExecutionLocks) releaseRef(id string, entry *keyedExecutionLock) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry.refs--
	if entry.refs == 0 {
		delete(l.entries, id)
	}
}

func NewOpenShell(ctx context.Context, st *store.Store, image string) (*OpenShell, error) {
	if image == "" {
		image = defaultOpenShellImage
	}
	o := &OpenShell{store: st, image: image}
	if _, err := o.run(ctx, nil, "status"); err != nil {
		return nil, fmt.Errorf("openshell provider: gateway unavailable: %w", err)
	}
	return o, nil
}
func (o *OpenShell) Info() Info {
	return Info{Name: "openshell", Isolation: IsolationContainer, Image: o.image, Policy: Policy{
		Environment: "openshell-managed,minimal-command-env", Network: "openshell-policy", Resources: "cpu=2,memory=4GiB,disk=runtime-managed", Credentials: "openshell-providers", ProcessCleanup: "openshell-sandbox"}}
}
func (o *OpenShell) run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "openshell", args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("openshell %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}
func repositoryHost(repoURL string) string {
	parsed, err := url.Parse(repoURL)
	if err != nil {
		return "repository host"
	}
	if parsed.Hostname() == "" {
		return "repository host"
	}
	return strings.ToLower(parsed.Hostname())
}

func repositoryCloneError(repoURL string, cause error) error {
	reason := "clone failed"
	message := strings.ToLower(cause.Error())
	switch {
	case strings.Contains(message, "connect tunnel failed") || strings.Contains(message, "proxy connect aborted"):
		reason = "sandbox network policy denied access"
	case strings.Contains(message, "authentication failed"), strings.Contains(message, "could not read username"), strings.Contains(message, "repository not found"):
		reason = "repository was not found or authorization was denied"
	case errors.Is(cause, context.DeadlineExceeded):
		reason = "clone timed out"
	}
	return &RepositoryCloneError{Host: repositoryHost(repoURL), Reason: reason}
}

func (o *OpenShell) Create(ctx context.Context, opt workspace.CreateOptions) (workspace.Workspace, error) {
	for attempt := 0; attempt < 8; attempt++ {
		id, err := workspace.GenerateName()
		if err != nil {
			return workspace.Workspace{}, err
		}
		taken, err := o.store.WorkspaceExists(ctx, id)
		if err != nil {
			return workspace.Workspace{}, err
		}
		if taken {
			continue
		}
		args := []string{"sandbox", "create", "--name", id, "--from", o.image, "--cpu", "2", "--memory", "4Gi", "--label", scrapsWorkspaceLabel + "=" + id}
		// A configured provider contains the brokered credential in OpenShell;
		// the sandbox receives only rewritten requests, never the PAT itself.
		_, providerErr := o.run(ctx, nil, "provider", "get", githubauth.ProfileID)
		providerConfigured := providerErr == nil
		if strings.TrimSpace(opt.RepoURL) != "" && repositoryHost(opt.RepoURL) == "github.com" && !providerConfigured {
			return workspace.Workspace{}, &RepositoryAuthorizationRequiredError{Host: "github.com"}
		}
		if providerConfigured {
			args = append(args, "--provider", githubauth.ProfileID)
		}
		args = append(args, "--no-auto-providers", "--detach", "--", "sleep", "infinity")
		if _, err = o.run(ctx, nil, args...); err != nil {
			// OpenShell may persist a sandbox in Error phase even though create
			// returns non-zero. Reclaim it before surfacing the failure.
			_, _ = o.run(context.Background(), nil, "sandbox", "delete", id)
			return workspace.Workspace{}, err
		}
		cleanup := func() { _, _ = o.run(context.Background(), nil, "sandbox", "delete", id) }
		if strings.TrimSpace(opt.RepoURL) != "" {
			if !strings.HasPrefix(opt.RepoURL, "https://") && !strings.HasPrefix(opt.RepoURL, "http://") {
				cleanup()
				return workspace.Workspace{}, &InvalidRequestError{Message: "repository URL must use http or https"}
			}
			cloneCtx, cancel := context.WithTimeout(ctx, workspace.CloneTimeout)
			_, err = o.execRaw(cloneCtx, id, nil, "git", "clone", opt.RepoURL, ".")
			cancel()
			if err != nil {
				cleanup()
				return workspace.Workspace{}, repositoryCloneError(opt.RepoURL, err)
			}
		}
		rec := store.Workspace{ID: id, Project: opt.Project, RepoURL: strings.TrimSpace(opt.RepoURL), Provider: "openshell", State: "running"}
		if err = o.store.CreateWorkspace(ctx, rec); err != nil {
			cleanup()
			return workspace.Workspace{}, err
		}
		saved, err := o.store.GetWorkspace(ctx, id)
		if err != nil {
			cleanup()
			return workspace.Workspace{}, err
		}
		return publicWorkspace(saved), nil
	}
	return workspace.Workspace{}, errors.New("could not generate a unique workspace id")
}

type openShellSandbox struct {
	Name   string            `json:"name"`
	Phase  string            `json:"phase"`
	Labels map[string]string `json:"labels"`
}

func (o *OpenShell) sandboxes(ctx context.Context) ([]openShellSandbox, error) {
	listed, err := o.run(ctx, nil, "sandbox", "list", "--output", "json")
	if err != nil {
		return nil, err
	}
	var sandboxes []openShellSandbox
	if err := json.Unmarshal(listed, &sandboxes); err != nil {
		return nil, fmt.Errorf("decode openshell sandbox list: %w", err)
	}
	return sandboxes, nil
}

// Reconcile removes OpenShell sandboxes which are explicitly labelled as
// Scraps-owned but no longer have a database record. Failed creates and lost
// databases can otherwise leave running containers behind indefinitely.
func (o *OpenShell) Reconcile(ctx context.Context) error {
	rows, err := o.store.ListWorkspaces(ctx)
	if err != nil {
		return err
	}
	tracked := make(map[string]bool, len(rows))
	for _, row := range rows {
		if row.Provider == "openshell" {
			tracked[row.ID] = true
		}
	}
	sandboxes, err := o.sandboxes(ctx)
	if err != nil {
		return err
	}
	var cleanupErrors []error
	for _, sandbox := range sandboxes {
		// Require both the ownership label and matching value/name. Never infer
		// ownership from OpenShell's generated Docker container name.
		if sandbox.Labels[scrapsWorkspaceLabel] != sandbox.Name || tracked[sandbox.Name] {
			continue
		}
		if _, err := o.run(ctx, nil, "sandbox", "delete", sandbox.Name); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("delete orphaned sandbox %s: %w", sandbox.Name, err))
		}
	}
	return errors.Join(cleanupErrors...)
}

func (o *OpenShell) Ready(ctx context.Context) ([]workspace.Workspace, error) {
	rows, err := o.store.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	sandboxes, err := o.sandboxes(ctx)
	if err != nil {
		return nil, err
	}
	phaseByName := make(map[string]string, len(sandboxes))
	for _, sandbox := range sandboxes {
		phaseByName[sandbox.Name] = sandbox.Phase
	}
	var out []workspace.Workspace
	for _, w := range rows {
		if w.Provider == "openshell" && w.State == "preheated" {
			phase := phaseByName[w.ID]
			if phase == "Ready" || phase == "Running" {
				out = append(out, publicWorkspace(w))
				continue
			}
			// Preheated records have no user data. A crash can leave their row
			// after OpenShell has reclaimed (or failed) the corresponding sandbox.
			if phase != "" {
				_, _ = o.run(context.Background(), nil, "sandbox", "delete", w.ID)
			}
			if err := o.store.DeleteWorkspace(ctx, w.ID); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func (o *OpenShell) Preheat(ctx context.Context) (workspace.Workspace, error) {
	created, err := o.Create(ctx, workspace.CreateOptions{})
	if err != nil {
		return workspace.Workspace{}, err
	}
	if err := o.store.UpdateWorkspaceState(ctx, created.ID, "preheated"); err != nil {
		_ = o.Delete(context.Background(), created.ID)
		return workspace.Workspace{}, err
	}
	created.State = "preheated"
	return created, nil
}

func (o *OpenShell) Checkout(ctx context.Context, id string, opt workspace.CreateOptions) (workspace.Workspace, error) {
	repoURL := strings.TrimSpace(opt.RepoURL)
	if repoURL != "" {
		if !strings.HasPrefix(repoURL, "https://") && !strings.HasPrefix(repoURL, "http://") {
			return workspace.Workspace{}, &InvalidRequestError{Message: "repository URL must use http or https"}
		}
		if repositoryHost(repoURL) == "github.com" {
			if _, err := o.run(ctx, nil, "provider", "get", githubauth.ProfileID); err != nil {
				return workspace.Workspace{}, &RepositoryAuthorizationRequiredError{Host: "github.com"}
			}
		}
		cloneCtx, cancel := context.WithTimeout(ctx, workspace.CloneTimeout)
		_, err := o.execRaw(cloneCtx, id, nil, "git", "clone", repoURL, ".")
		cancel()
		if err != nil {
			return workspace.Workspace{}, repositoryCloneError(repoURL, err)
		}
	}
	if err := o.store.AssignWorkspace(ctx, id, opt.Project, repoURL); err != nil {
		return workspace.Workspace{}, err
	}
	return o.Get(ctx, id)
}

func (o *OpenShell) Get(ctx context.Context, id string) (workspace.Workspace, error) {
	w, err := o.store.GetWorkspace(ctx, id)
	if err != nil {
		return workspace.Workspace{}, err
	}
	if w.Provider != "openshell" {
		return workspace.Workspace{}, store.ErrNotFound
	}
	return publicWorkspace(w), nil
}
func (o *OpenShell) List(ctx context.Context) ([]workspace.Workspace, error) {
	rows, err := o.store.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]workspace.Workspace, 0, len(rows))
	for _, w := range rows {
		if w.Provider == "openshell" && w.State != "preheated" {
			out = append(out, publicWorkspace(w))
		}
	}
	return out, nil
}
func (o *OpenShell) Start(ctx context.Context, id string) error {
	if _, err := o.Get(ctx, id); err != nil {
		return err
	}
	if _, err := o.run(ctx, nil, "sandbox", "start", id); err != nil {
		return err
	}
	return o.store.UpdateWorkspaceState(ctx, id, "running")
}
func (o *OpenShell) Stop(ctx context.Context, id string) error {
	if _, err := o.Get(ctx, id); err != nil {
		return err
	}
	if _, err := o.run(ctx, nil, "sandbox", "stop", id); err != nil {
		return err
	}
	return o.store.UpdateWorkspaceState(ctx, id, "stopped")
}
func (o *OpenShell) Delete(ctx context.Context, id string) error {
	if _, err := o.Get(ctx, id); err != nil {
		return err
	}
	if _, err := o.run(ctx, nil, "sandbox", "delete", id); err != nil {
		return err
	}
	return o.store.DeleteWorkspace(ctx, id)
}
func (o *OpenShell) ensure(ctx context.Context, id string) error {
	w, err := o.Get(ctx, id)
	if err != nil {
		return err
	}
	if w.State != "running" {
		return &InvalidRequestError{Message: "workspace is stopped: " + id}
	}
	return nil
}
func (o *OpenShell) execRaw(ctx context.Context, id string, stdin []byte, args ...string) ([]byte, error) {
	full := []string{"sandbox", "exec", "--no-tty", "--name", id, "--"}
	full = append(full, args...)
	return o.run(ctx, stdin, full...)
}
func (o *OpenShell) execOutput(ctx context.Context, id string, stdin []byte, args ...string) ([]byte, error) {
	if err := o.ensure(ctx, id); err != nil {
		return nil, err
	}
	return o.execRaw(ctx, id, stdin, args...)
}
func (o *OpenShell) Exec(ctx context.Context, id string, r ExecRequest, emit func(ExecEvent)) error {
	unlock, err := o.executions.acquire(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()

	if err := o.ensure(ctx, id); err != nil {
		return err
	}
	cwd, err := o.containerPath(ctx, id, r.CWD)
	if err != nil {
		return err
	}
	if _, err = o.execOutput(ctx, id, nil, "test", "-d", cwd); err != nil {
		return &InvalidRequestError{Message: "working directory does not exist: " + r.CWD}
	}
	args := []string{"sandbox", "exec", "--no-tty", "--name", id, "--workdir", cwd}
	env := []string{"HOME=/workspace", "PATH=/usr/local/bin:/usr/bin:/bin", "SHELL=/bin/bash", "TMPDIR=/workspace/.scrap/tmp", "SCRAP_WORKSPACE_ROOT=/workspace"}
	approvedNames := make([]string, 0, len(r.Env))
	for name := range r.Env {
		approvedNames = append(approvedNames, name)
	}
	sort.Strings(approvedNames)
	for _, name := range approvedNames {
		env = append(env, name+"="+r.Env[name])
	}
	for _, v := range env {
		args = append(args, "--env", v)
	}
	const runScript = `mkdir -p /workspace/.scrap/tmp; exec /bin/bash -c "$1"`
	args = append(args, "--", "/bin/bash", "-c", runScript, "scrap", r.Command)
	cmd := exec.CommandContext(ctx, "openshell", args...)
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		return e
	}
	stderr, e := cmd.StderrPipe()
	if e != nil {
		return e
	}
	if e = cmd.Start(); e != nil {
		return e
	}
	cleanupDone := make(chan error, 1)
	commandDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			cleanupDone <- o.recycleSandbox(cleanupCtx, id)
		case <-commandDone:
			cleanupDone <- nil
		}
	}()
	emit(ExecEvent{Type: "start", PID: cmd.Process.Pid})
	var wg sync.WaitGroup
	wg.Add(2)
	go copyOpenShellOutput(&wg, stdout, "stdout", emit)
	go copyOpenShellOutput(&wg, stderr, "stderr", emit)
	wg.Wait()
	waitErr := cmd.Wait()
	close(commandDone)
	cleanupErr := <-cleanupDone
	if cleanupErr != nil {
		return fmt.Errorf("cancel command and restore sandbox: %w", cleanupErr)
	}
	event := ExecEvent{Type: "exit"}
	var exitErr *exec.ExitError
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		event.Reason = "timeout"
	case errors.Is(ctx.Err(), context.Canceled):
		event.Reason = "cancelled"
	case waitErr == nil:
		code := 0
		event.Code = &code
	case errors.As(waitErr, &exitErr):
		code := exitErr.ExitCode()
		event.Code = &code
	default:
		return waitErr
	}
	emit(event)
	return nil
}

// recycleSandbox is the trusted cancellation boundary. OpenShell stop tears
// down the sandbox compute (and therefore every descendant process) while
// retaining its persistent workspace; start restores the recorded workload.
// No identifier or process metadata is read from the untrusted workspace.
func (o *OpenShell) recycleSandbox(ctx context.Context, id string) error {
	if _, err := o.run(ctx, nil, "sandbox", "stop", id); err != nil {
		return fmt.Errorf("stop sandbox: %w", err)
	}
	if err := o.store.UpdateWorkspaceState(ctx, id, "stopped"); err != nil {
		return fmt.Errorf("record stopped sandbox: %w", err)
	}
	if _, err := o.run(ctx, nil, "sandbox", "start", id); err != nil {
		return fmt.Errorf("restart sandbox: %w", err)
	}
	if err := o.store.UpdateWorkspaceState(ctx, id, "running"); err != nil {
		return fmt.Errorf("record restarted sandbox: %w", err)
	}
	return nil
}
func copyOpenShellOutput(wg *sync.WaitGroup, r io.Reader, stream string, emit func(ExecEvent)) {
	defer wg.Done()
	b := make([]byte, 32*1024)
	for {
		n, e := r.Read(b)
		if n > 0 {
			emit(ExecEvent{Type: "output", Stream: stream, Data: append([]byte(nil), b[:n]...)})
		}
		if e != nil {
			return
		}
	}
}

const openShellContainScript = `import os,sys
root='/workspace'; p=sys.argv[1]; probe=p
while not os.path.lexists(probe) and probe != '/': probe=os.path.dirname(probe)
real=os.path.realpath(probe)
if real != root and not real.startswith(root+'/'): sys.exit(73)`

func (o *OpenShell) containerPath(ctx context.Context, id, requested string) (string, error) {
	relative, err := validateRelative(requested)
	if err != nil {
		return "", err
	}
	absolute := path.Join(workspace.VirtualRoot, relative)
	if _, err := o.execOutput(ctx, id, nil, "python3", "-c", openShellContainScript, absolute); err != nil {
		return "", &InvalidRequestError{Message: "path resolves outside workspace: " + requested}
	}
	return absolute, nil
}

const openShellStatScript = `import os,json,sys,stat
p=sys.argv[1]
try: s=os.stat(p)
except (FileNotFoundError,NotADirectoryError): print(json.dumps({'error':'not_found'}));sys.exit(0)
except PermissionError: print(json.dumps({'error':'permission'}));sys.exit(0)
print(json.dumps({'name':os.path.basename(p.rstrip('/')) or 'workspace','size':s.st_size,'mode':s.st_mode,'mtime':s.st_mtime,'dir':stat.S_ISDIR(s.st_mode)}))`

type openShellStat struct {
	Name  string  `json:"name"`
	Size  int64   `json:"size"`
	Mode  uint32  `json:"mode"`
	Mtime float64 `json:"mtime"`
	Dir   bool    `json:"dir"`
	Error string  `json:"error"`
}
type openShellFileInfo struct{ s openShellStat }

func (i openShellFileInfo) Name() string       { return i.s.Name }
func (i openShellFileInfo) Size() int64        { return i.s.Size }
func (i openShellFileInfo) Mode() fs.FileMode  { return fs.FileMode(i.s.Mode) }
func (i openShellFileInfo) ModTime() time.Time { return time.UnixMilli(int64(i.s.Mtime * 1000)) }
func (i openShellFileInfo) IsDir() bool        { return i.s.Dir }
func (i openShellFileInfo) Sys() any           { return nil }
func (o *OpenShell) Stat(ctx context.Context, id, p string) (fs.FileInfo, error) {
	p, e := o.containerPath(ctx, id, p)
	if e != nil {
		return nil, e
	}
	out, e := o.execOutput(ctx, id, nil, "python3", "-c", openShellStatScript, p)
	if e != nil {
		return nil, e
	}
	var s openShellStat
	if e = json.Unmarshal(out, &s); e != nil {
		return nil, e
	}
	// The stat script reports missing and unreadable paths as structured
	// errors so the HTTP layer can answer 404/403 instead of a generic 500.
	switch s.Error {
	case "not_found":
		return nil, fmt.Errorf("stat %s: %w", p, fs.ErrNotExist)
	case "permission":
		return nil, fmt.Errorf("stat %s: %w", p, fs.ErrPermission)
	}
	return openShellFileInfo{s}, nil
}
func (o *OpenShell) ReadFile(ctx context.Context, id, p string, max int64) ([]byte, fs.FileInfo, error) {
	info, e := o.Stat(ctx, id, p)
	if e != nil {
		return nil, nil, e
	}
	if info.IsDir() {
		return nil, nil, &InvalidRequestError{Message: "path is a directory: " + p}
	}
	if info.Size() > max {
		return nil, nil, &InvalidRequestError{Message: "file exceeds 100MB limit"}
	}
	p, e = o.containerPath(ctx, id, p)
	if e != nil {
		return nil, nil, e
	}
	out, e := o.execOutput(ctx, id, nil, "cat", p)
	return out, info, e
}
func (o *OpenShell) WriteFile(ctx context.Context, id, p string, b []byte) error {
	p, e := o.containerPath(ctx, id, p)
	if e != nil {
		return e
	}
	_, e = o.execOutput(ctx, id, b, "sh", "-c", `mkdir -p "$(dirname "$1")" && cat > "$1"`, "write", p)
	return e
}
func (o *OpenShell) Mkdir(ctx context.Context, id, p string) error {
	p, e := o.containerPath(ctx, id, p)
	if e != nil {
		return e
	}
	_, e = o.execOutput(ctx, id, nil, "mkdir", "-p", p)
	return e
}
func (o *OpenShell) RemoveAll(ctx context.Context, id, p string) error {
	relative, err := validateRelative(p)
	if err != nil {
		return err
	}
	if relative == "." {
		return &InvalidRequestError{Message: "refusing to remove the workspace root itself"}
	}
	cp, e := o.containerPath(ctx, id, p)
	if e != nil {
		return e
	}
	// `rm -rf --` with the container-contained path: containerPath resolves
	// symlinks through the containment script before anything is deleted.
	_, e = o.execOutput(ctx, id, nil, "rm", "-rf", "--", cp)
	return e
}
func (o *OpenShell) Access(ctx context.Context, id, p string, m AccessMode) error {
	cp, e := o.containerPath(ctx, id, p)
	if e != nil {
		return e
	}
	flag := "-r"
	if m == AccessWrite {
		flag = "-w"
	}
	if _, e = o.execOutput(ctx, id, nil, "test", flag, cp); e != nil {
		// `test` exits 1 for both missing and unreadable paths; classify so
		// callers see 404 versus 403 instead of a generic 500.
		return classifyPathError(ctx, o, id, p, cp, e)
	}
	return nil
}
func (o *OpenShell) ReadDir(ctx context.Context, id, p string) ([]string, error) {
	cp, e := o.containerPath(ctx, id, p)
	if e != nil {
		return nil, e
	}
	out, e := o.execOutput(ctx, id, nil, "find", cp, "-mindepth", "1", "-maxdepth", "1", "-printf", "%f\\n")
	if e != nil {
		// `find` fails identically for missing and unreadable roots; classify.
		if _, se := o.Stat(ctx, id, p); errors.Is(se, fs.ErrNotExist) {
			return nil, fmt.Errorf("readdir %s: %w", cp, fs.ErrNotExist)
		}
		return nil, e
	}
	text := strings.TrimSpace(string(out))
	if text == "" {
		return []string{}, nil
	}
	return strings.Split(text, "\n"), nil
}

const openShellGlobScript = `import os,sys,fnmatch,json
root,pat,limit=sys.argv[1],sys.argv[2],int(sys.argv[3]); out=[]
for base,dirs,files in os.walk(root):
 dirs[:]=[d for d in dirs if d not in ('.git','node_modules')]
 for f in files:
  p=os.path.join(base,f); rel=os.path.relpath(p,'/workspace')
  if fnmatch.fnmatch(os.path.relpath(p,root),pat) or fnmatch.fnmatch(f,pat): out.append(rel)
  if len(out)>=limit: print(json.dumps(out));sys.exit()
print(json.dumps(out))`

func (o *OpenShell) Glob(ctx context.Context, id string, r GlobRequest) ([]string, error) {
	p, e := o.containerPath(ctx, id, r.Path)
	if e != nil {
		return nil, e
	}
	limit := r.Limit
	if limit <= 0 {
		limit = 200
	}
	out, e := o.execOutput(ctx, id, nil, "python3", "-c", openShellGlobScript, p, r.Pattern, strconv.Itoa(limit))
	if e != nil {
		return nil, e
	}
	var paths []string
	e = json.Unmarshal(out, &paths)
	return paths, e
}

const openShellGrepScript = `import os,sys,re,fnmatch,json
root,pat,g,ic,lit,context,limit=sys.argv[1],sys.argv[2],sys.argv[3],sys.argv[4]=='1',sys.argv[5]=='1',int(sys.argv[6]),int(sys.argv[7]); flags=re.I if ic else 0
try: rx=re.compile(re.escape(pat) if lit else pat,flags)
except Exception as e: print(json.dumps({'error':str(e)}));sys.exit(2)
out=[]
for base,dirs,files in os.walk(root):
 dirs[:]=[d for d in dirs if d not in ('.git','node_modules')]
 for f in files:
  p=os.path.join(base,f); rel=os.path.relpath(p,'/workspace')
  if g and not (fnmatch.fnmatch(rel,g) or fnmatch.fnmatch(f,g)): continue
  try:
   if b'\0' in open(p,'rb').read(1024): continue
   lines=open(p,errors='replace').read().splitlines()
  except: continue
  for i,line in enumerate(lines):
   if rx.search(line):
    ls=[]
    if context:
     for n in range(max(0,i-context),min(len(lines),i+context+1)): ls.append({'n':n+1,'text':lines[n],'match':n==i})
    out.append({'path':rel,'lineNumber':i+1,'lineText':line,'lines':ls})
    if len(out)>=limit: print(json.dumps({'matches':out,'limitReached':True}));sys.exit()
print(json.dumps({'matches':out,'limitReached':False}))`

func (o *OpenShell) Grep(ctx context.Context, id string, r GrepRequest) (GrepResult, error) {
	p, e := o.containerPath(ctx, id, r.Path)
	if e != nil {
		return GrepResult{}, e
	}
	limit := r.Limit
	if limit <= 0 {
		limit = 100
	}
	out, e := o.execOutput(ctx, id, nil, "python3", "-c", openShellGrepScript, p, r.Pattern, r.Glob, openShellBoolArg(r.IgnoreCase), openShellBoolArg(r.Literal), strconv.Itoa(max(r.Context, 0)), strconv.Itoa(limit))
	if e != nil {
		return GrepResult{}, &InvalidRequestError{Message: e.Error()}
	}
	var result GrepResult
	e = json.Unmarshal(out, &result)
	return result, e
}
func openShellBoolArg(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

// classifyPathError distinguishes missing paths from unreadable ones after a
// shell primitive (test, find) failed without a reason. requested is the
// workspace-relative path (accepted by Stat); display is the agent-visible
// container path used in messages. The extra stat keeps the happy path at one
// exec and only runs on failure.
func classifyPathError(ctx context.Context, o *OpenShell, id, requested, display string, fallback error) error {
	if _, se := o.Stat(ctx, id, requested); se != nil {
		if errors.Is(se, fs.ErrNotExist) {
			return fmt.Errorf("access %s: %w", display, fs.ErrNotExist)
		}
		if errors.Is(se, fs.ErrPermission) {
			return fmt.Errorf("access %s: %w", display, fs.ErrPermission)
		}
	}
	return fallback
}

var _ Provider = (*OpenShell)(nil)
