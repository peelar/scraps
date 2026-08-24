package version

import "fmt"

var (
	Version = "dev"
	Commit  = "unknown"
)

func String(component string) string {
	return fmt.Sprintf("%s %s (%s)", component, Version, Commit)
}
