// scrap push and scrap pull move a directory and a workspace over the
// archive API (ADR 0014): push streams a local directory into an empty
// workspace, pull streams a workspace into a fresh local directory. Transfers
// are one-shot and explicit; git remains the transport for repository work.
//
//	scrap push [--replace] [workspace-id] <dir>
//	scrap pull [--force] [workspace-id] [target]

package main

import (
	"archive/tar"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/peelar/scraps/internal/archive"
)

func runPush(args []string) int {
	flags := flag.NewFlagSet("push", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	usage := "usage: scrap push [--replace] [<workspace-id>] <dir>"
	flags.Usage = func() { fmt.Fprintln(flags.Output(), usage) }
	replace := flags.Bool("replace", false, "clear the workspace before pushing")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	rest := flags.Args()
	if len(rest) < 1 || len(rest) > 2 {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	idArg, dir := "", rest[0]
	if len(rest) == 2 {
		idArg, dir = rest[0], rest[1]
	}

	info, err := os.Stat(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrap: %v\n", err)
		return 1
	}
	if !info.IsDir() {
		fmt.Fprintf(os.Stderr, "scrap: %s is not a directory\n", dir)
		return 1
	}
	source, err := filepath.Abs(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrap: %v\n", err)
		return 1
	}

	api := newClientFromEnv()
	ctx := context.Background()
	workspaceID, ok := resolveOpenWorkspace(ctx, api, idArg)
	if !ok {
		return 1
	}

	reader, writer := io.Pipe()
	// Count from the walk so the summary stays right even if the push fails.
	counted := make(chan pushCounts, 1)
	go func() {
		counts, tarErr := tarDirectory(source, writer)
		counted <- counts
		_ = writer.CloseWithError(tarErr)
	}()
	result, pushErr := api.PushArchive(ctx, workspaceID, reader, *replace)
	counts := <-counted
	if pushErr != nil {
		fmt.Fprintf(os.Stderr, "scrap: push: %v\n", pushErr)
		return 1
	}
	if counts.skipped > 0 {
		fmt.Fprintf(os.Stderr, "scrap: skipped %d non-regular files (symlinks and devices are not pushed)\n", counts.skipped)
	}
	fmt.Printf("pushed %s → %s (%d files, %d bytes)\n", dir, workspaceID, result.Files, result.Bytes)
	return 0
}

func runPull(args []string) int {
	flags := flag.NewFlagSet("pull", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	usage := "usage: scrap pull [--force] [<workspace-id>] [target]"
	flags.Usage = func() { fmt.Fprintln(flags.Output(), usage) }
	force := flags.Bool("force", false, "write into an existing directory without deleting anything")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	rest := flags.Args()
	if len(rest) > 2 {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}

	api := newClientFromEnv()
	ctx := context.Background()
	workspaceID, ok := resolveOpenWorkspace(ctx, api, argAt(rest, 0))
	if !ok {
		return 1
	}
	target := argAt(rest, 1)
	if target == "" {
		target = workspaceID
	}

	if !*force {
		entries, err := os.ReadDir(target)
		if err == nil && len(entries) > 0 {
			fmt.Fprintf(os.Stderr, "scrap: %s is not empty — choose an empty target or pass --force to overlay\n", target)
			return 1
		}
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "scrap: %v\n", err)
		return 1
	}

	reader, writer := io.Pipe()
	extracted := make(chan int, 1)
	go func() {
		files, tarErr := untarArchive(reader, target)
		extracted <- files
		_ = reader.CloseWithError(tarErr)
	}()
	skipped, pullErr := api.PullArchive(ctx, workspaceID, writer)
	files := <-extracted
	if pullErr != nil {
		fmt.Fprintf(os.Stderr, "scrap: pull: %v\n", pullErr)
		return 1
	}
	if skipped > 0 {
		fmt.Fprintf(os.Stderr, "scrap: skipped %d oversized or non-regular workspace entries\n", skipped)
	}
	fmt.Printf("pulled %s → %s (%d files)\n", workspaceID, target, files)
	return 0
}

type pushCounts struct {
	files   int
	skipped int
}

// tarDirectory writes source as a tar stream, excluding Scraps' internal
// directory. Symlinks and other non-regular entries are skipped with a
// warning because the daemon only imports files and directories.
func tarDirectory(source string, w io.Writer) (pushCounts, error) {
	var counts pushCounts
	writer := tar.NewWriter(w)
	err := filepath.WalkDir(source, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, p)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		relative = filepath.ToSlash(relative)
		if relative == archive.ReservedDir && d.IsDir() {
			return fs.SkipDir
		}
		if !d.IsDir() && !d.Type().IsRegular() {
			counts.skipped++
			fmt.Fprintf(os.Stderr, "scrap: skipping non-regular file: %s\n", relative)
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = relative
		if d.IsDir() {
			header.Name += "/"
			header.Mode = 0o755
		} else {
			header.Mode = int64(info.Mode().Perm())
		}
		header.Uid, header.Gid = 0, 0
		header.Uname, header.Gname = "", ""
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if !d.IsDir() {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(writer, f)
			closeErr := f.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			counts.files++
		}
		return nil
	})
	if err != nil {
		return counts, err
	}
	return counts, writer.Close()
}

// untarArchive extracts a workspace archive into target. Names are validated
// workspace-relative before joining the target so a hostile archive cannot
// escape, using the same rules as the daemon's import path.
func untarArchive(r io.Reader, target string) (int, error) {
	files := 0
	reader := tar.NewReader(r)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return files, nil
		}
		if err != nil {
			return files, err
		}
		name, err := archive.CleanEntryName(header.Name)
		if err != nil {
			return files, err
		}
		destination := filepath.Join(target, filepath.FromSlash(name))
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(destination, 0o755); err != nil {
				return files, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
				return files, err
			}
			f, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, fs.FileMode(header.Mode).Perm())
			if err != nil {
				return files, err
			}
			_, copyErr := io.Copy(f, reader)
			closeErr := f.Close()
			if copyErr != nil {
				return files, copyErr
			}
			if closeErr != nil {
				return files, closeErr
			}
			files++
		default:
			fmt.Fprintf(os.Stderr, "scrap: skipping non-regular archive entry: %s\n", name)
		}
	}
}
