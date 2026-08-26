package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/peelar/scraps/internal/githubauth"
	"github.com/peelar/scraps/internal/store"
	"github.com/peelar/scraps/internal/workspace"
)

const defaultOpenShellImage = "scraps-dev:bookworm"

type OpenShell struct {
	store *store.Store
	image string
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
		args := []string{"sandbox", "create", "--name", id, "--from", o.image, "--cpu", "2", "--memory", "4Gi", "--label", "dev.scraps.workspace=" + id}
		// A configured provider contains the brokered credential in OpenShell;
		// the sandbox receives only rewritten requests, never the PAT itself.
		if _, providerErr := o.run(ctx, nil, "provider", "get", githubauth.ProfileID); providerErr == nil {
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
				return workspace.Workspace{}, fmt.Errorf("clone %s: %w", opt.RepoURL, err)
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
func (o *OpenShell) Ready(ctx context.Context) ([]workspace.Workspace, error) {
	rows, err := o.store.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	var out []workspace.Workspace
	for _, w := range rows {
		if w.Provider == "openshell" && w.State == "preheated" {
			out = append(out, publicWorkspace(w))
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
		cloneCtx, cancel := context.WithTimeout(ctx, workspace.CloneTimeout)
		_, err := o.execRaw(cloneCtx, id, nil, "git", "clone", repoURL, ".")
		cancel()
		if err != nil {
			return workspace.Workspace{}, fmt.Errorf("clone %s: %w", repoURL, err)
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
	for k, v := range r.Env {
		env = append(env, k+"="+v)
	}
	for _, v := range env {
		args = append(args, "--env", v)
	}
	args = append(args, "--", "/bin/bash", "-c", "mkdir -p /workspace/.scrap/tmp; exec /bin/bash -c \"$1\"", "scrap", r.Command)
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
	emit(ExecEvent{Type: "start", PID: cmd.Process.Pid})
	var wg sync.WaitGroup
	wg.Add(2)
	go copyOpenShellOutput(&wg, stdout, "stdout", emit)
	go copyOpenShellOutput(&wg, stderr, "stderr", emit)
	wg.Wait()
	waitErr := cmd.Wait()
	event := ExecEvent{Type: "exit"}
	var exitErr *exec.ExitError
	switch {
	case waitErr == nil:
		code := 0
		event.Code = &code
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		event.Reason = "timeout"
	case errors.Is(ctx.Err(), context.Canceled):
		event.Reason = "cancelled"
	case errors.As(waitErr, &exitErr):
		code := exitErr.ExitCode()
		event.Code = &code
	default:
		return waitErr
	}
	emit(event)
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
s=os.stat(p)
print(json.dumps({'name':os.path.basename(p.rstrip('/')) or 'workspace','size':s.st_size,'mode':s.st_mode,'mtime':s.st_mtime,'dir':stat.S_ISDIR(s.st_mode)}))`

type openShellStat struct {
	Name  string  `json:"name"`
	Size  int64   `json:"size"`
	Mode  uint32  `json:"mode"`
	Mtime float64 `json:"mtime"`
	Dir   bool    `json:"dir"`
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
func (o *OpenShell) Access(ctx context.Context, id, p string, m AccessMode) error {
	p, e := o.containerPath(ctx, id, p)
	if e != nil {
		return e
	}
	flag := "-r"
	if m == AccessWrite {
		flag = "-w"
	}
	_, e = o.execOutput(ctx, id, nil, "test", flag, p)
	return e
}
func (o *OpenShell) ReadDir(ctx context.Context, id, p string) ([]string, error) {
	p, e := o.containerPath(ctx, id, p)
	if e != nil {
		return nil, e
	}
	out, e := o.execOutput(ctx, id, nil, "find", p, "-mindepth", "1", "-maxdepth", "1", "-printf", "%f\\n")
	if e != nil {
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

var _ Provider = (*OpenShell)(nil)
