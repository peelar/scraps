package githubauth

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigureKeepsTokenOutOfArguments(t *testing.T) {
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$BROKER_TEST_CALLS"
if [ "$1 $2 $3" = "provider profile export" ]; then exit 1; fi
if [ "$1 $2 $3" = "provider get github-push" ]; then exit 1; fi
if [ "$1 $2" = "provider create" ] && [ "$GH_TOKEN" != "installation-secret" ]; then exit 9; fi
exit 0
`
	if err := os.WriteFile(filepath.Join(bin, "openshell"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BROKER_TEST_CALLS", calls)
	t.Setenv("GITHUB_TOKEN", "unrelated-host-token")
	if err := Configure(context.Background(), "installation-secret"); err != nil {
		t.Fatal(err)
	}
	logged, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logged), "installation-secret") || strings.Contains(string(logged), "unrelated-host-token") {
		t.Fatalf("credential appeared in argv: %s", logged)
	}
}
