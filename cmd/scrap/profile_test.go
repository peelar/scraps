package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildRunnerProfileAllowlist(t *testing.T) {
	root := t.TempDir()
	write := func(name, value string) {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("auth.json", `{"anthropic":{"type":"api_key","key":"secret"}}`)
	write("models.json", `{"providers":{}}`)
	write("skills/review/SKILL.md", "review safely")
	write("prompts/ship.md", "ship it")
	write("settings.json", `{"theme":"local-only"}`)
	write("extensions/unsafe.ts", "throw new Error('do not clone')")
	write("sessions/local.jsonl", "private session")

	archive, manifest, err := buildRunnerProfile(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"auth.json", "models.json", "skills/review/SKILL.md", "prompts/ship.md"} {
		if _, ok := manifest.Files[expected]; !ok {
			t.Errorf("manifest omitted %s", expected)
		}
	}
	for _, excluded := range []string{"settings.json", "extensions/unsafe.ts", "sessions/local.jsonl"} {
		if _, ok := manifest.Files[excluded]; ok {
			t.Errorf("manifest included excluded path %s", excluded)
		}
	}

	gzipReader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	tarReader := tar.NewReader(gzipReader)
	seen := map[string]bool{}
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		seen[header.Name] = true
	}
	if !seen["scraps-profile-manifest.json"] || seen["settings.json"] {
		t.Fatalf("archive entries = %+v", seen)
	}
}

func TestBuildRunnerProfileRejectsLaptopBoundCredentials(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "auth.json"), []byte(`{"anthropic":{"type":"api_key","key":"!security find-generic-password -w"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := buildRunnerProfile(root); err == nil {
		t.Fatal("Mac-keychain credential command was accepted as a portable worker credential")
	}
}

func TestBuildRunnerProfileRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink("/tmp", filepath.Join(root, "skills")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := buildRunnerProfile(root); err == nil {
		t.Fatal("symlinked skills directory was accepted")
	}
}
