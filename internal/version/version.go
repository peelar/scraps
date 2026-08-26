package version

import (
	"fmt"
	"runtime/debug"
)

var (
	Version = "dev"
	Commit  = "unknown"
)

// init backfills Version and Commit when the binary was not built with the
// release ldflags, e.g. via `go install github.com/peelar/scraps/cmd/scrap@latest`
// or a plain `go build` inside a git checkout.
func init() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	if Version == "dev" && info.Main.Version != "" && info.Main.Version != "(devel)" {
		Version = info.Main.Version
	}
	if Commit == "unknown" {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" {
				Commit = s.Value
				break
			}
		}
	}
}

func String(component string) string {
	return fmt.Sprintf("%s %s (%s)", component, Version, Commit)
}
