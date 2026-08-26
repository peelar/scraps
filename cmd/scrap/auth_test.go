package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeToken(t *testing.T) {
	got, err := normalizeToken([]byte("  github_pat_example\n"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "github_pat_example" {
		t.Fatalf("normalizeToken() = %q", got)
	}
	if _, err := normalizeToken([]byte(" \n")); err == nil {
		t.Fatal("normalizeToken(empty) succeeded")
	}
}

func TestCredentialEnvironmentReplacesHostGitHubTokens(t *testing.T) {
	t.Setenv("GH_TOKEN", "host-gh")
	t.Setenv("GITHUB_TOKEN", "host-github")
	env := credentialEnvironment([]string{"GH_TOKEN=replacement"})
	joined := "\n" + strings.Join(env, "\n") + "\n"
	if strings.Contains(joined, "host-gh") || strings.Contains(joined, "host-github") {
		t.Fatalf("credentialEnvironment leaked host token: %q", joined)
	}
	if strings.Count(joined, "\nGH_TOKEN=replacement\n") != 1 {
		t.Fatalf("replacement token missing or duplicated: %q", joined)
	}
}

func TestConfigureGitHubProviderDoesNotPutTokenInArguments(t *testing.T) {
	bin := t.TempDir()
	logPath := filepath.Join(bin, "calls")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$AUTH_TEST_LOG"
if [ "$1 $2 $3" = "provider profile export" ]; then exit 1; fi
if [ "$1 $2 $3" = "provider get github-push" ]; then exit 1; fi
exit 0
`
	if err := os.WriteFile(filepath.Join(bin, "openshell"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AUTH_TEST_LOG", logPath)
	const token = "github_pat_super_secret"
	if err := configureGitHubProvider(context.Background(), []byte(token)); err != nil {
		t.Fatal(err)
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(calls), token) {
		t.Fatalf("token appeared in command arguments:\n%s", calls)
	}
	for _, want := range []string{
		"provider profile import --file",
		"provider create --name github-push --type github-push --from-existing",
	} {
		if !strings.Contains(string(calls), want) {
			t.Errorf("calls missing %q:\n%s", want, calls)
		}
	}
}

func TestAuthHelp(t *testing.T) {
	if code := runAuth([]string{"github", "--help"}); code != 0 {
		t.Fatalf("runAuth(--help) = %d", code)
	}
}
