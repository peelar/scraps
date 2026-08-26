// Package extension embeds the Scraps Pi extension so `scrap setup` can
// install the global /scrap command without a source checkout.
//
// The files under files/ are synced from packages/pi-extension/src by
// `make sync-extension` (wired into `make build`); commit the synced copy so
// plain `go build` produces a complete binary.
package extension

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed all:files
var files embed.FS

// ErrNoExtension is returned when the embedded extension is missing.
var ErrNoExtension = errors.New("embedded pi extension is missing")

// Install extracts the embedded extension into dir (replacing prior
// contents) and returns the entry point path.
func Install(dir string) (string, error) {
	entries, err := fs.ReadDir(files, "files")
	if err != nil || len(entries) == 0 {
		return "", ErrNoExtension
	}

	if err := os.RemoveAll(dir); err != nil {
		return "", fmt.Errorf("reset extension dir: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create extension dir: %w", err)
	}

	err = fs.WalkDir(files, "files", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel("files", path)
		if err != nil {
			return err
		}
		target := filepath.Join(dir, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		content, err := files.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, content, 0o644)
	})
	if err != nil {
		return "", fmt.Errorf("extract extension: %w", err)
	}
	return filepath.Join(dir, "index.ts"), nil
}
