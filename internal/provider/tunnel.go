// Workspace port discovery and byte tunnels for the OpenShell provider.
// Tunnels reuse the gateway exec channel: the relay process inside the
// sandbox bridges the exec stdin/stdout pipes to a loopback service, so no
// sandbox port is ever published to a network.

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// openShellRelayScript bridges gateway exec pipes to a workspace service.
// The first stdout byte reports the dial result before any stream data
// flows: 0x00 connected, 0x01 failure followed by one reason line.
const openShellRelayScript = `import os,socket,sys,threading
port=int(sys.argv[1])
try:
    s=socket.create_connection(("127.0.0.1",port),timeout=10)
except OSError as e:
    sys.stdout.buffer.write(b"\x01"+("%s\n"%e).encode())
    sys.stdout.buffer.flush()
    os._exit(1)
sys.stdout.buffer.write(b"\x00")
sys.stdout.buffer.flush()
def upstream():
    try:
        while True:
            d=s.recv(65536)
            if not d: break
            sys.stdout.buffer.write(d)
            sys.stdout.buffer.flush()
    except Exception:
        pass
    os._exit(0)
threading.Thread(target=upstream,daemon=True).start()
try:
    while True:
        d=sys.stdin.buffer.read1(65536)
        if not d: break
        s.sendall(d)
except Exception:
    pass
os._exit(0)`

// openShellPortsScript lists listening TCP sockets from /proc, decoding the
// kernel address encoding into human-readable form.
const openShellPortsScript = `import json,socket,struct
seen={}
for f,v6 in (("/proc/net/tcp",False),("/proc/net/tcp6",True)):
    try: lines=open(f).read().splitlines()[1:]
    except OSError: continue
    for l in lines:
        p=l.split()
        if len(p)<4 or p[3]!='0A': continue
        a,port=p[1].rsplit(':',1)
        port=int(port,16)
        if port==0: continue
        try:
            addr=socket.inet_ntop(socket.AF_INET6,bytes.fromhex(a)) if v6 else socket.inet_ntoa(struct.pack('<I',int(a,16)))
        except Exception: addr=a
        seen[port]=addr
print(json.dumps([{'port':q,'address':seen[q]} for q in sorted(seen)]))`

// Ports lists TCP listeners inside a running workspace.
func (o *OpenShell) Ports(ctx context.Context, id string) ([]Port, error) {
	if err := o.ensure(ctx, id); err != nil {
		return nil, err
	}
	out, err := o.execOutput(ctx, id, nil, "python3", "-c", openShellPortsScript, "scrap-ports")
	if err != nil {
		return nil, err
	}
	var ports []Port
	if err := json.Unmarshal(out, &ports); err != nil {
		return nil, fmt.Errorf("decode openshell ports: %w", err)
	}
	return ports, nil
}

// Tunnel execs the relay inside the sandbox and returns its pipes as a byte
// stream to the workspace's loopback service on port.
func (o *OpenShell) Tunnel(ctx context.Context, id string, port int) (TunnelConn, error) {
	if err := o.ensure(ctx, id); err != nil {
		return nil, err
	}
	args := []string{"sandbox", "exec", "--no-tty", "--name", id, "--", "python3", "-c", openShellRelayScript, "scrap-relay", strconv.Itoa(port)}
	cmd := exec.CommandContext(ctx, "openshell", args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("openshell %s: %w", strings.Join(args, " "), err)
	}
	head := make([]byte, 1)
	if _, err := io.ReadFull(stdout, head); err != nil {
		message := diagnostics(stderr, err)
		killOpenShell(cmd)
		return nil, fmt.Errorf("tunnel relay: %s", message)
	}
	if head[0] == 0x01 {
		reason, _ := io.ReadAll(io.LimitReader(stdout, 4096))
		killOpenShell(cmd)
		return nil, &TunnelDialError{Port: port, Reason: strings.TrimSpace(string(reason))}
	}
	go func() {
		_, _ = io.Copy(io.Discard, stderr)
	}()
	return &openShellTunnel{cmd: cmd, stdin: stdin, stdout: stdout}, nil
}

type openShellTunnel struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	writeOnce sync.Once
	closeOnce sync.Once
}

func (t *openShellTunnel) Read(b []byte) (int, error)  { return t.stdout.Read(b) }
func (t *openShellTunnel) Write(b []byte) (int, error) { return t.stdin.Write(b) }

// CloseWrite ends the relay's stdin so the service observes client EOF.
// It stays a no-op after Close.
func (t *openShellTunnel) CloseWrite() error {
	var err error
	t.writeOnce.Do(func() { err = t.stdin.Close() })
	return err
}

// Close tears the relay down and reaps the gateway process.
func (t *openShellTunnel) Close() error {
	var first error
	t.closeOnce.Do(func() {
		first = t.CloseWrite()
		killOpenShell(t.cmd)
	})
	return first
}

func killOpenShell(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait()
}

// diagnostics drains stderr after a failed relay start for the error message.
func diagnostics(stderr io.Reader, cause error) string {
	reason, _ := io.ReadAll(io.LimitReader(stderr, 4096))
	if trimmed := strings.TrimSpace(string(reason)); trimmed != "" {
		return trimmed
	}
	return cause.Error()
}
