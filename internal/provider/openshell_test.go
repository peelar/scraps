package provider

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/peelar/scraps/internal/store"
	"github.com/peelar/scraps/internal/workspace"
)

func TestOpenShellLifecycleUsesGatewayCLI(t *testing.T) {
	bin := t.TempDir()
	logPath := filepath.Join(bin, "calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$OPENSHELL_TEST_LOG\"\n"
	if err := os.WriteFile(filepath.Join(bin, "openshell"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OPENSHELL_TEST_LOG", logPath)

	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	runtime, err := NewOpenShell(context.Background(), st, "example.test/scraps@sha256:abc")
	if err != nil {
		t.Fatal(err)
	}
	created, err := runtime.Create(context.Background(), workspace.CreateOptions{Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Stop(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Delete(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(calls)
	for _, want := range []string{
		"status",
		"sandbox create --name " + created.ID,
		"--cpu 2 --memory 4Gi",
		"--no-auto-providers --detach -- sleep infinity",
		"sandbox stop " + created.ID,
		"sandbox start " + created.ID,
		"sandbox delete " + created.ID,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("calls missing %q:\n%s", want, got)
		}
	}
}
