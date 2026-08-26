package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/peelar/scraps/internal/store"
	"github.com/peelar/scraps/internal/workspace"
)

const defaultDockerImage = "scraps-dev:bookworm"

// Docker runs each workspace in an unprivileged container with a private named volume.
type Docker struct {
	store *store.Store
	image string
}

func NewDocker(ctx context.Context, st *store.Store, image string) (*Docker, error) {
	if image == "" {
		image = defaultDockerImage
	}
	d := &Docker{store: st, image: image}
	if _, err := d.run(ctx, nil, "version", "--format", "{{.Server.Version}}"); err != nil {
		return nil, fmt.Errorf("docker provider: Docker Engine unavailable: %w", err)
	}
	identity, err := d.run(ctx, nil, "image", "inspect", "--format", "{{.Id}}", image)
	if err != nil {
		return nil, fmt.Errorf("docker provider: image %q unavailable (run `make docker-image`): %w", image, err)
	}
	// Use the immutable image ID for every container, even if the configured
	// local tag is moved while scrapd is running.
	d.image = strings.TrimSpace(string(identity))
	return d, nil
}

func (d *Docker) Info() Info {
	return Info{Name: "docker", Isolation: IsolationContainer, Image: d.image, Policy: Policy{
		Environment: "minimal", Network: "outbound-enabled,no-published-ports",
		Resources:   "cpu=2,memory=4GiB,pids=512,disk=runtime-unlimited",
		Credentials: "none", ProcessCleanup: "container+process-group",
	}}
}
func dockerContainer(id string) string { return "scraps-" + id }
func dockerVolume(id string) string    { return "scraps-" + id + "-workspace" }

func publicWorkspace(w store.Workspace) workspace.Workspace {
	return workspace.Workspace{ID: w.ID, Project: w.Project, RepoURL: w.RepoURL, State: w.State,
		RootPath: workspace.VirtualRoot, PathContract: workspace.PathContract, CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt}
}

func (d *Docker) Create(ctx context.Context, o workspace.CreateOptions) (workspace.Workspace, error) {
	for attempt := 0; attempt < 8; attempt++ {
		id, err := workspace.GenerateName()
		if err != nil {
			return workspace.Workspace{}, err
		}
		taken, err := d.store.WorkspaceExists(ctx, id)
		if err != nil {
			return workspace.Workspace{}, err
		}
		if taken {
			continue
		}
		volume, container := dockerVolume(id), dockerContainer(id)
		if _, err = d.run(ctx, nil, "volume", "create", "--label", "dev.scraps.workspace="+id, volume); err != nil {
			return workspace.Workspace{}, err
		}
		cleanup := func() {
			_, _ = d.run(context.Background(), nil, "rm", "-f", container)
			_, _ = d.run(context.Background(), nil, "volume", "rm", "-f", volume)
		}
		if _, err = d.run(ctx, nil, "run", "--rm", "--user", "0:0", "-v", volume+":/workspace", d.image,
			"sh", "-c", "chown 1000:1000 /workspace && chmod 700 /workspace"); err != nil {
			cleanup()
			return workspace.Workspace{}, err
		}
		args := []string{"create", "--name", container, "--hostname", id,
			"--label", "dev.scraps.workspace=" + id, "--init", "--user", "1000:1000", "--workdir", "/workspace",
			"--cpus", "2", "--memory", "4g", "--memory-swap", "4g", "--pids-limit", "512",
			"--security-opt", "no-new-privileges", "--cap-drop", "ALL", "-v", volume + ":/workspace", d.image, "sleep", "infinity"}
		if _, err = d.run(ctx, nil, args...); err != nil {
			cleanup()
			return workspace.Workspace{}, err
		}
		if _, err = d.run(ctx, nil, "start", container); err != nil {
			cleanup()
			return workspace.Workspace{}, err
		}
		if strings.TrimSpace(o.RepoURL) != "" {
			if !strings.HasPrefix(o.RepoURL, "https://") && !strings.HasPrefix(o.RepoURL, "http://") {
				cleanup()
				return workspace.Workspace{}, &InvalidRequestError{Message: "repository URL must use http or https"}
			}
			cloneCtx, cancel := context.WithTimeout(ctx, workspace.CloneTimeout)
			_, err = d.execOutput(cloneCtx, id, nil, "git", "clone", o.RepoURL, ".")
			cancel()
			if err != nil {
				cleanup()
				return workspace.Workspace{}, fmt.Errorf("clone %s: %w", o.RepoURL, err)
			}
		}
		record := store.Workspace{ID: id, Project: o.Project, RepoURL: strings.TrimSpace(o.RepoURL), Provider: "docker", State: "running"}
		if err = d.store.CreateWorkspace(ctx, record); err != nil {
			cleanup()
			return workspace.Workspace{}, err
		}
		saved, err := d.store.GetWorkspace(ctx, id)
		if err != nil {
			cleanup()
			return workspace.Workspace{}, err
		}
		return publicWorkspace(saved), nil
	}
	return workspace.Workspace{}, errors.New("could not generate a unique workspace id")
}
func (d *Docker) Get(ctx context.Context, id string) (workspace.Workspace, error) {
	w, err := d.store.GetWorkspace(ctx, id)
	if err != nil {
		return workspace.Workspace{}, err
	}
	if w.Provider != "docker" {
		return workspace.Workspace{}, store.ErrNotFound
	}
	return publicWorkspace(w), nil
}
func (d *Docker) List(ctx context.Context) ([]workspace.Workspace, error) {
	rows, err := d.store.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]workspace.Workspace, 0, len(rows))
	for _, w := range rows {
		if w.Provider == "docker" {
			out = append(out, publicWorkspace(w))
		}
	}
	return out, nil
}
func (d *Docker) Start(ctx context.Context, id string) error {
	if _, err := d.Get(ctx, id); err != nil {
		return err
	}
	if _, err := d.run(ctx, nil, "start", dockerContainer(id)); err != nil {
		return err
	}
	return d.store.UpdateWorkspaceState(ctx, id, "running")
}
func (d *Docker) Stop(ctx context.Context, id string) error {
	if _, err := d.Get(ctx, id); err != nil {
		return err
	}
	if _, err := d.run(ctx, nil, "stop", "--time", "10", dockerContainer(id)); err != nil {
		return err
	}
	return d.store.UpdateWorkspaceState(ctx, id, "stopped")
}
func (d *Docker) Delete(ctx context.Context, id string) error {
	if _, err := d.Get(ctx, id); err != nil {
		return err
	}
	if _, err := d.run(ctx, nil, "rm", "-f", dockerContainer(id)); err != nil && !strings.Contains(err.Error(), "No such container") {
		return err
	}
	if _, err := d.run(ctx, nil, "volume", "rm", "-f", dockerVolume(id)); err != nil && !strings.Contains(err.Error(), "no such volume") {
		return err
	}
	return d.store.DeleteWorkspace(ctx, id)
}

func validateRelative(p string) (string, error) {
	if p == "" {
		return ".", nil
	}
	if path.IsAbs(p) {
		return "", &InvalidRequestError{Message: "absolute workspace path is not allowed: " + p}
	}
	clean := path.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", &InvalidRequestError{Message: "path escapes workspace: " + p}
	}
	return clean, nil
}
func (d *Docker) ensure(ctx context.Context, id string) error {
	w, err := d.Get(ctx, id)
	if err != nil {
		return err
	}
	if w.State != "running" {
		return &InvalidRequestError{Message: "workspace is stopped: " + id}
	}
	state, err := d.run(ctx, nil, "inspect", "--format", "{{.State.Running}}", dockerContainer(id))
	if err != nil {
		return fmt.Errorf("workspace runtime %s is missing: %w", id, err)
	}
	if strings.TrimSpace(string(state)) != "true" {
		return fmt.Errorf("workspace runtime %s is not running; use scrap start %s", id, id)
	}
	return nil
}

const containScript = `import os,sys
root='/workspace'; p=sys.argv[1]; probe=p
while not os.path.lexists(probe) and probe != '/': probe=os.path.dirname(probe)
real=os.path.realpath(probe)
if real != root and not real.startswith(root+'/'): sys.exit(73)`

func (d *Docker) containerPath(ctx context.Context, id, requested string) (string, error) {
	relative, err := validateRelative(requested)
	if err != nil {
		return "", err
	}
	absolute := path.Join(workspace.VirtualRoot, relative)
	if _, err := d.execOutput(ctx, id, nil, "python3", "-c", containScript, absolute); err != nil {
		return "", &InvalidRequestError{Message: "path resolves outside workspace: " + requested}
	}
	return absolute, nil
}

func (d *Docker) execOutput(ctx context.Context, id string, stdin []byte, args ...string) ([]byte, error) {
	if err := d.ensure(ctx, id); err != nil {
		return nil, err
	}
	full := append([]string{"exec", "-i", dockerContainer(id)}, args...)
	return d.run(ctx, stdin, full...)
}
func (d *Docker) run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func (d *Docker) Exec(ctx context.Context, id string, r ExecRequest, emit func(ExecEvent)) error {
	if err := d.ensure(ctx, id); err != nil {
		return err
	}
	cwd, err := d.containerPath(ctx, id, r.CWD)
	if err != nil {
		return err
	}
	if _, err = d.execOutput(ctx, id, nil, "test", "-d", cwd); err != nil {
		return &InvalidRequestError{Message: "working directory does not exist: " + r.CWD}
	}
	marker := fmt.Sprintf("/tmp/scrap-exec-%d.pid", time.Now().UnixNano())
	env := []string{"HOME=/workspace", "PATH=/usr/local/bin:/usr/bin:/bin", "SHELL=/bin/bash", "TMPDIR=/workspace/.scrap/tmp", "SCRAP_WORKSPACE_ROOT=/workspace"}
	for k, v := range r.Env {
		env = append(env, k+"="+v)
	}
	args := []string{"exec", "-i", "--workdir", cwd}
	for _, v := range env {
		args = append(args, "--env", v)
	}
	wrapper := `mkdir -p /workspace/.scrap/tmp; echo $$ > "$1"; shift; exec /bin/bash -c "$1"`
	args = append(args, dockerContainer(id), "/bin/bash", "-c", wrapper, "scrap", marker, r.Command)
	// Do not attach ctx directly: killing the Docker CLI first reparents the
	// container process and loses the process-tree identity. The cancellation
	// goroutine below kills the in-container tree, which then closes the CLI.
	cmd := exec.Command("docker", args...)
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
	execFinished := make(chan struct{})
	killFinished := make(chan struct{})
	go func() {
		defer close(killFinished)
		select {
		case <-ctx.Done():
			killCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, killErr := d.run(killCtx, nil, "exec", "--user", "1000:1000", dockerContainer(id), "sh", "-c",
				`killtree(){ for c in $(pgrep -P "$1"); do killtree "$c"; done; kill -KILL "$1" 2>/dev/null || true; }; p=$(cat "$1" 2>/dev/null) && killtree "$p"`, "kill", marker)
			cancel()
			if killErr != nil {
				slog.Warn("terminate docker exec", "workspace", id, "error", killErr)
			}
			// The remote process is gone; close the CLI even if the Docker
			// transport has not noticed the exec session ending yet.
			_ = cmd.Process.Kill()
		case <-execFinished:
		}
	}()
	var wg sync.WaitGroup
	wg.Add(2)
	go copyDockerOutput(&wg, stdout, "stdout", emit)
	go copyDockerOutput(&wg, stderr, "stderr", emit)
	wg.Wait()
	waitErr := cmd.Wait()
	close(execFinished)
	<-killFinished
	_, _ = d.run(context.Background(), nil, "exec", dockerContainer(id), "rm", "-f", marker)
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
func copyDockerOutput(wg *sync.WaitGroup, r io.Reader, stream string, emit func(ExecEvent)) {
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

const statScript = `import os,json,sys,stat
p=sys.argv[1]
s=os.stat(p)
print(json.dumps({'name':os.path.basename(p.rstrip('/')) or 'workspace','size':s.st_size,'mode':s.st_mode,'mtime':s.st_mtime,'dir':stat.S_ISDIR(s.st_mode)}))`

type dockerStat struct {
	Name  string  `json:"name"`
	Size  int64   `json:"size"`
	Mode  uint32  `json:"mode"`
	Mtime float64 `json:"mtime"`
	Dir   bool    `json:"dir"`
}
type dockerFileInfo struct{ s dockerStat }

func (i dockerFileInfo) Name() string       { return i.s.Name }
func (i dockerFileInfo) Size() int64        { return i.s.Size }
func (i dockerFileInfo) Mode() fs.FileMode  { return fs.FileMode(i.s.Mode) }
func (i dockerFileInfo) ModTime() time.Time { return time.UnixMilli(int64(i.s.Mtime * 1000)) }
func (i dockerFileInfo) IsDir() bool        { return i.s.Dir }
func (i dockerFileInfo) Sys() any           { return nil }
func (d *Docker) Stat(ctx context.Context, id, p string) (fs.FileInfo, error) {
	p, e := d.containerPath(ctx, id, p)
	if e != nil {
		return nil, e
	}
	out, e := d.execOutput(ctx, id, nil, "python3", "-c", statScript, p)
	if e != nil {
		return nil, e
	}
	var s dockerStat
	if e = json.Unmarshal(out, &s); e != nil {
		return nil, e
	}
	return dockerFileInfo{s}, nil
}
func (d *Docker) ReadFile(ctx context.Context, id, p string, max int64) ([]byte, fs.FileInfo, error) {
	info, e := d.Stat(ctx, id, p)
	if e != nil {
		return nil, nil, e
	}
	if info.IsDir() {
		return nil, nil, &InvalidRequestError{Message: "path is a directory: " + p}
	}
	if info.Size() > max {
		return nil, nil, &InvalidRequestError{Message: "file exceeds 100MB limit"}
	}
	p, e = d.containerPath(ctx, id, p)
	if e != nil {
		return nil, nil, e
	}
	out, e := d.execOutput(ctx, id, nil, "cat", p)
	return out, info, e
}
func (d *Docker) WriteFile(ctx context.Context, id, p string, b []byte) error {
	p, e := d.containerPath(ctx, id, p)
	if e != nil {
		return e
	}
	_, e = d.execOutput(ctx, id, b, "sh", "-c", `mkdir -p "$(dirname "$1")" && cat > "$1"`, "write", p)
	return e
}
func (d *Docker) Mkdir(ctx context.Context, id, p string) error {
	p, e := d.containerPath(ctx, id, p)
	if e != nil {
		return e
	}
	_, e = d.execOutput(ctx, id, nil, "mkdir", "-p", p)
	return e
}
func (d *Docker) Access(ctx context.Context, id, p string, m AccessMode) error {
	p, e := d.containerPath(ctx, id, p)
	if e != nil {
		return e
	}
	flag := "-r"
	if m == AccessWrite {
		flag = "-w"
	}
	_, e = d.execOutput(ctx, id, nil, "test", flag, p)
	return e
}
func (d *Docker) ReadDir(ctx context.Context, id, p string) ([]string, error) {
	p, e := d.containerPath(ctx, id, p)
	if e != nil {
		return nil, e
	}
	out, e := d.execOutput(ctx, id, nil, "find", p, "-mindepth", "1", "-maxdepth", "1", "-printf", "%f\\n")
	if e != nil {
		return nil, e
	}
	text := strings.TrimSpace(string(out))
	if text == "" {
		return []string{}, nil
	}
	return strings.Split(text, "\n"), nil
}

const globScript = `import os,sys,fnmatch,json
root,pat,limit=sys.argv[1],sys.argv[2],int(sys.argv[3]); out=[]
for base,dirs,files in os.walk(root):
 dirs[:]=[d for d in dirs if d not in ('.git','node_modules')]
 for f in files:
  p=os.path.join(base,f); rel=os.path.relpath(p,'/workspace')
  if fnmatch.fnmatch(os.path.relpath(p,root),pat) or fnmatch.fnmatch(f,pat): out.append(rel)
  if len(out)>=limit: print(json.dumps(out));sys.exit()
print(json.dumps(out))`

func (d *Docker) Glob(ctx context.Context, id string, r GlobRequest) ([]string, error) {
	p, e := d.containerPath(ctx, id, r.Path)
	if e != nil {
		return nil, e
	}
	limit := r.Limit
	if limit <= 0 {
		limit = 200
	}
	out, e := d.execOutput(ctx, id, nil, "python3", "-c", globScript, p, r.Pattern, strconv.Itoa(limit))
	if e != nil {
		return nil, e
	}
	var paths []string
	e = json.Unmarshal(out, &paths)
	return paths, e
}

const grepScript = `import os,sys,re,fnmatch,json
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

func (d *Docker) Grep(ctx context.Context, id string, r GrepRequest) (GrepResult, error) {
	p, e := d.containerPath(ctx, id, r.Path)
	if e != nil {
		return GrepResult{}, e
	}
	limit := r.Limit
	if limit <= 0 {
		limit = 100
	}
	out, e := d.execOutput(ctx, id, nil, "python3", "-c", grepScript, p, r.Pattern, r.Glob, boolArg(r.IgnoreCase), boolArg(r.Literal), strconv.Itoa(max(r.Context, 0)), strconv.Itoa(limit))
	if e != nil {
		return GrepResult{}, &InvalidRequestError{Message: e.Error()}
	}
	var result GrepResult
	e = json.Unmarshal(out, &result)
	return result, e
}
func boolArg(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

var _ Provider = (*Docker)(nil)
