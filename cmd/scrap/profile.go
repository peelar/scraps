package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const (
	maxRunnerProfileFileBytes = 2 << 20
	maxRunnerProfileBytes     = 16 << 20
)

type runnerProfileManifest struct {
	Version    int               `json:"version"`
	Generation string            `json:"generation"`
	Files      map[string]string `json:"files"`
}

type profileFile struct {
	path string
	data []byte
}

func localPiProfileDir() string {
	if configured := os.Getenv("PI_CODING_AGENT_DIR"); configured != "" {
		return configured
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".pi", "agent")
}

// buildRunnerProfile clones only the trusted, portable Pi profile surface.
// Executable extensions, packages, UI state, caches, and sessions are excluded.
func buildRunnerProfile(root string) ([]byte, runnerProfileManifest, error) {
	if root == "" {
		return nil, runnerProfileManifest{}, errors.New("cannot locate the local Pi profile")
	}
	files, err := collectRunnerProfileFiles(root)
	if err != nil {
		return nil, runnerProfileManifest{}, err
	}
	manifest := buildRunnerProfileManifest(files)
	archive, err := writeRunnerProfileArchive(files, manifest)
	if err != nil {
		return nil, runnerProfileManifest{}, err
	}
	return archive, manifest, nil
}

// runnerProfileAllowedRoots are the only top-level Pi profile entries that are
// cloned to the worker.
var runnerProfileAllowedRoots = []string{"auth.json", "models.json", "AGENTS.md", "skills", "prompts"}

// collectRunnerProfileFiles walks the allowed Pi profile surface and returns
// every regular file as portable bytes. Symlinks anywhere under an allowed
// root abort the clone: they could redirect the worker to host-only state.
func collectRunnerProfileFiles(root string) ([]profileFile, error) {
	var files []profileFile
	total := 0
	for _, allowed := range runnerProfileAllowedRoots {
		base := filepath.Join(root, allowed)
		info, err := os.Lstat(base)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect Pi profile %s: %w", allowed, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("refusing to clone symlinked Pi profile path %s", allowed)
		}
		err = filepath.WalkDir(base, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("refusing to clone symlinked Pi profile path %s", path)
			}
			if entry.IsDir() {
				return nil
			}
			if !entry.Type().IsRegular() {
				return fmt.Errorf("unsupported Pi profile entry %s: not a regular file", path)
			}
			relative, err := filepath.Rel(root, path)
			if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return fmt.Errorf("path escapes the Pi profile root: %s", path)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if relative == "auth.json" || relative == "models.json" {
				if err := validatePortableCredentialJSON(data); err != nil {
					return fmt.Errorf("profile file %s is not independently usable on the worker: %w", relative, err)
				}
			}
			if len(data) > maxRunnerProfileFileBytes {
				return fmt.Errorf("profile file %s exceeds %d bytes", relative, maxRunnerProfileFileBytes)
			}
			total += len(data)
			if total > maxRunnerProfileBytes {
				return fmt.Errorf("cloned Pi profile exceeds %d bytes", maxRunnerProfileBytes)
			}
			files = append(files, profileFile{path: filepath.ToSlash(relative), data: data})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	return files, nil
}

// buildRunnerProfileManifest hashes the collected files into a manifest whose
// generation id changes whenever any file content changes.
func buildRunnerProfileManifest(files []profileFile) runnerProfileManifest {
	manifest := runnerProfileManifest{Version: 1, Files: make(map[string]string, len(files))}
	generationHash := sha256.New()
	for _, file := range files {
		digest := sha256.Sum256(file.data)
		manifest.Files[file.path] = hex.EncodeToString(digest[:])
		generationHash.Write([]byte(file.path))
		generationHash.Write([]byte{0})
		generationHash.Write(digest[:])
	}
	manifest.Generation = hex.EncodeToString(generationHash.Sum(nil))
	return manifest
}

// writeRunnerProfileArchive packs the manifest and files into the gzipped tar
// consumed by scraps-worker profile-install.
func writeRunnerProfileArchive(files []profileFile, manifest runnerProfileManifest) ([]byte, error) {
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	var archive bytes.Buffer
	gzipWriter := gzip.NewWriter(&archive)
	tarWriter := tar.NewWriter(gzipWriter)
	writeFile := func(name string, data []byte) error {
		header := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(data))}
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		_, err := tarWriter.Write(data)
		return err
	}
	if err := writeFile("scraps-profile-manifest.json", manifestData); err != nil {
		return nil, err
	}
	for _, file := range files {
		if err := writeFile(file.path, file.data); err != nil {
			return nil, err
		}
	}
	if err := tarWriter.Close(); err != nil {
		return nil, err
	}
	if err := gzipWriter.Close(); err != nil {
		return nil, err
	}
	return archive.Bytes(), nil
}

func validatePortableCredentialJSON(data []byte) error {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	var inspect func(any) error
	inspect = func(current any) error {
		switch typed := current.(type) {
		case map[string]any:
			for key, child := range typed {
				if key == "key" || key == "apiKey" {
					if text, ok := child.(string); ok && (strings.HasPrefix(text, "!") || strings.HasPrefix(text, "$")) {
						return fmt.Errorf("%s references a local command or environment variable; store a portable Pi credential before attaching", key)
					}
				}
				if err := inspect(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range typed {
				if err := inspect(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return inspect(value)
}

func syncRunnerProfile(target string) (runnerProfileManifest, error) {
	archive, manifest, err := buildRunnerProfile(localPiProfileDir())
	if err != nil {
		return runnerProfileManifest{}, err
	}
	args := []string{"-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "ForwardAgent=no", "-o", "ClearAllForwardings=yes"}
	if config := os.Getenv("SCRAPS_SSH_CONFIG"); config != "" {
		args = append(args, "-F", config)
	}
	args = append(args, target, "sudo", "-n", "/usr/local/bin/scraps-worker", "profile-install")
	command := exec.Command("ssh", args...)
	command.Stdin = bytes.NewReader(archive)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return runnerProfileManifest{}, fmt.Errorf("install cloned Pi profile on %s: %w", target, err)
	}
	return manifest, nil
}
