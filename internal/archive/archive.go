// Package archive holds the rules shared by both ends of the workspace
// directory push/pull API (ADR 0014): the scrap CLI that writes and extracts
// local tar streams, and scrapd which imports and exports them. Keeping the
// entry-name rules in one place is what guarantees the client-side pull
// validation mirrors the daemon's import validation.
package archive

import (
	"errors"
	"path"
	"strings"
)

// ReservedDir never crosses the archive boundary in either direction; it is
// Scraps' own workspace directory.
const ReservedDir = ".scrap"

// CleanEntryName validates a tar entry name as a workspace-relative path and
// returns its cleaned form. Absolute paths, parent traversals, empty names,
// and Scraps' internal directory are rejected.
func CleanEntryName(name string) (string, error) {
	name = strings.TrimSuffix(name, "/")
	if name == "" {
		return "", errors.New("archive entry has an empty name")
	}
	if path.IsAbs(name) {
		return "", errors.New("archive entry is an absolute path: " + name)
	}
	clean := path.Clean(name)
	if clean == "." {
		return "", errors.New("archive entry has an empty name")
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("archive entry escapes the workspace: " + name)
	}
	if clean == ReservedDir || strings.HasPrefix(clean, ReservedDir+"/") {
		return "", errors.New("archive entry writes into the reserved .scrap directory: " + name)
	}
	return clean, nil
}
