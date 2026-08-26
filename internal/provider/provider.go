// Package provider defines the workspace runtime boundary used by scrapd.
package provider

import (
	"context"
	"io/fs"

	"github.com/peelar/scraps/internal/workspace"
)

// Isolation describes the security boundary a provider supplies.
type Isolation string

const (
	IsolationNone      Isolation = "none"
	IsolationContainer Isolation = "container"
	IsolationVM        Isolation = "vm"
)

// Info identifies a provider and its effective isolation level.
type Info struct {
	Name      string
	Isolation Isolation
}

// Provider owns workspace lifecycle, execution, and filesystem operations.
// HTTP handlers must not depend on a provider's host paths or runtime.
type Provider interface {
	Info() Info
	Create(context.Context, workspace.CreateOptions) (workspace.Workspace, error)
	Get(context.Context, string) (workspace.Workspace, error)
	List(context.Context) ([]workspace.Workspace, error)
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
}

type ExecRequest struct {
	Command string
	CWD     string
	Env     map[string]string
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
