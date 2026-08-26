package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigureWritesWorkerSizing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scraps", "worker.conf")
	var output bytes.Buffer
	if err := configure([]string{"--cpus", "6", "--memory", "12", "--disk", "90"}, strings.NewReader(""), &output, path); err != nil {
		t.Fatalf("configure: %v", err)
	}
	configured, err := readWorkerConfig(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	want := (workerConfig{CPUs: 6, MemoryGiB: 12, DiskGiB: 90})
	if configured != want {
		t.Fatalf("config = %+v, want %+v", configured, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %o, want 600", info.Mode().Perm())
	}
}

func TestConfigurePreservesExistingValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.conf")
	if err := writeWorkerConfig(path, workerConfig{CPUs: 8, MemoryGiB: 16, DiskGiB: 120}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	if err := configure([]string{"--memory", "24"}, strings.NewReader(""), &bytes.Buffer{}, path); err != nil {
		t.Fatalf("configure: %v", err)
	}
	configured, err := readWorkerConfig(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	want := (workerConfig{CPUs: 8, MemoryGiB: 24, DiskGiB: 120})
	if configured != want {
		t.Fatalf("config = %+v, want %+v", configured, want)
	}
}

func TestConfigureRejectsUnsafeSize(t *testing.T) {
	err := configure([]string{"--memory", "1"}, strings.NewReader(""), &bytes.Buffer{}, filepath.Join(t.TempDir(), "worker.conf"))
	if err == nil || !strings.Contains(err.Error(), "at least 2 GiB") {
		t.Fatalf("error = %v, want minimum-memory error", err)
	}
}
