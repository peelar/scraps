package client

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

// buildTestArchive writes a small in-memory tar with one directory and two
// files, exercising the same import path a directory push produces.
func buildTestArchive(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	if err := writer.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: "src/", Mode: 0o755}); err != nil {
		t.Fatalf("write dir header: %v", err)
	}
	for name, body := range map[string]string{"README.md": "readme", "src/app.go": "package app"} {
		if err := writer.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     name,
			Size:     int64(len(body)),
			Mode:     0o644,
		}); err != nil {
			t.Fatalf("write header: %v", err)
		}
		if _, err := writer.Write([]byte(body)); err != nil {
			t.Fatalf("write body: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return buffer.Bytes()
}

func readPulledEntries(t *testing.T, archive []byte) map[string]string {
	t.Helper()
	contents := map[string]string{}
	reader := tar.NewReader(bytes.NewReader(archive))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return contents
		}
		if err != nil {
			t.Fatalf("read pulled archive: %v", err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("read pulled entry: %v", err)
		}
		contents[header.Name] = string(body)
	}
}

func TestPushPullArchiveRoundTrip(t *testing.T) {
	client := newTestClient(t, "")
	ctx := context.Background()

	created, err := client.CreateWorkspace(ctx, "demo", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	result, err := client.PushArchive(ctx, created.ID, bytes.NewReader(buildTestArchive(t)), false)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if result.Files != 2 || result.Bytes != int64(len("readme")+len("package app")) {
		t.Fatalf("push result = %+v", result)
	}

	// A second push without replace conflicts.
	_, err = client.PushArchive(ctx, created.ID, bytes.NewReader(buildTestArchive(t)), false)
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Status != 409 {
		t.Fatalf("second push error = %v, want 409 conflict", err)
	}

	// Replace clears the previous contents.
	result, err = client.PushArchive(ctx, created.ID, bytes.NewReader(buildTestArchive(t)), true)
	if err != nil {
		t.Fatalf("replace push: %v", err)
	}
	if result.Files != 2 {
		t.Fatalf("replace push result = %+v", result)
	}

	var pulled bytes.Buffer
	skipped, err := client.PullArchive(ctx, created.ID, &pulled)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if skipped != 0 {
		t.Fatalf("skipped = %d", skipped)
	}
	contents := readPulledEntries(t, pulled.Bytes())
	if contents["README.md"] != "readme" || contents["src/app.go"] != "package app" {
		t.Fatalf("pulled = %+v", contents)
	}
}

func TestPushArchiveErrorPassthrough(t *testing.T) {
	client := newTestClient(t, "")
	_, err := client.PushArchive(context.Background(), "missing-workspace", bytes.NewReader(buildTestArchive(t)), false)
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Status != 404 {
		t.Fatalf("push missing workspace error = %v, want 404", err)
	}
}
