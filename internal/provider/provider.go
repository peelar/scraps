// Package provider defines the workspace runtime boundary used by scrapd.
package provider

import (
	"context"
	"fmt"
	"io"
	"io/fs"

	"github.com/peelar/scraps/internal/workspace"
)

// Isolation describes the security boundary a provider supplies.
type Isolation string

const (
	IsolationContainer Isolation = "container"
	IsolationVM        Isolation = "vm"
)

// Policy reports the effective sandbox controls, including limitations.
type Policy struct {
	Environment    string `json:"environment"`
	Network        string `json:"network"`
	Resources      string `json:"resources"`
	Credentials    string `json:"credentials"`
	ProcessCleanup string `json:"processCleanup"`
}

// Info identifies a provider, its effective isolation, and enforced policy.
type Info struct {
	Name      string
	Isolation Isolation
	// Image is the immutable runtime image identity when applicable.
	Image  string
	Policy Policy
}

// Provider owns workspace lifecycle, execution, and filesystem operations.
// HTTP handlers must not depend on a provider's host paths or runtime.
// Preheater is implemented by providers that can reserve a clean running
// runtime and later assign it without recreating the runtime.
type Preheater interface {
	Preheat(context.Context) (workspace.Workspace, error)
	Checkout(context.Context, string, workspace.CreateOptions) (workspace.Workspace, error)
	Ready(context.Context) ([]workspace.Workspace, error)
}

type Provider interface {
	Info() Info
	Create(context.Context, workspace.CreateOptions) (workspace.Workspace, error)
	Get(context.Context, string) (workspace.Workspace, error)
	List(context.Context) ([]workspace.Workspace, error)
	Start(context.Context, string) error
	Stop(context.Context, string) error
	Delete(context.Context, string) error

	Exec(context.Context, string, ExecRequest, func(ExecEvent)) error
	ReadFile(context.Context, string, string, int64) ([]byte, fs.FileInfo, error)
	WriteFile(context.Context, string, string, []byte) error
	Mkdir(context.Context, string, string) error
	Stat(context.Context, string, string) (fs.FileInfo, error)
	Access(context.Context, string, string, AccessMode) error
	ReadDir(context.Context, string, string) ([]string, error)
	Glob(context.Context, string, GlobRequest) ([]string, error)
	Grep(context.Context, string, GrepRequest) (GrepResult, error)

	// Ports lists TCP ports listening inside a running workspace.
	Ports(context.Context, string) ([]Port, error)
	// Tunnel opens a byte stream to one loopback port inside the workspace.
	// Tunnels are explicit, per-request, and reach only the workspace's own
	// loopback interface; providers must never publish ports implicitly.
	Tunnel(context.Context, string, int) (TunnelConn, error)
}

type ExecRequest struct {
	Command string
	CWD     string
	// Env contains only values explicitly approved by the authenticated client.
	// Providers must add these to their minimal environment, never the daemon's
	// ambient process environment.
	Env map[string]string
}

type ExecEvent struct {
	Type   string
	PID    int
	Stream string
	Data   []byte
	Code   *int
	Reason string
}

type AccessMode string

const (
	AccessRead  AccessMode = "read"
	AccessWrite AccessMode = "write"
)

type GlobRequest struct {
	Pattern string
	Path    string
	Limit   int
}

type GrepRequest struct {
	Pattern    string
	Path       string
	Glob       string
	IgnoreCase bool
	Literal    bool
	Context    int
	Limit      int
}

type GrepLine struct {
	N     int    `json:"n"`
	Text  string `json:"text"`
	Match bool   `json:"match"`
}

type GrepMatch struct {
	Path       string     `json:"path"`
	LineNumber int        `json:"lineNumber"`
	LineText   string     `json:"lineText"`
	Lines      []GrepLine `json:"lines"`
}

type GrepResult struct {
	Matches      []GrepMatch `json:"matches"`
	LimitReached bool        `json:"limitReached"`
}

// Port describes a TCP listener inside a workspace.
type Port struct {
	Port    int    `json:"port"`
	Address string `json:"address"`
}

// TunnelConn is a live byte stream to one TCP port inside a workspace.
type TunnelConn interface {
	io.ReadWriteCloser
	// CloseWrite signals that no more bytes will be sent, like a TCP
	// half-close, so the tunneled service can observe client EOF.
	CloseWrite() error
}

// TunnelDialError reports a workspace service that could not be reached.
type TunnelDialError struct {
	Port   int
	Reason string
}

func (e *TunnelDialError) Error() string {
	return fmt.Sprintf("dial 127.0.0.1:%d: %s", e.Port, e.Reason)
}
