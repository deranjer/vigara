package restic

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path"
	"strings"
)

// InitializeMissing initializes only on Restic's specific missing-repository exit
// code. Wrong passwords, locks and inaccessible storage never trigger init.
func (r *Runner) InitializeMissing(ctx context.Context, repository string) error {
	if err := r.validateForRead(ctx, repository); err != nil {
		return err
	}
	_, _, err := r.invoke(ctx, []string{"--repo", repository, "cat", "config"})
	if err == nil {
		return nil
	}
	var exit exitCoder
	if !errors.As(err, &exit) || exit.ExitCode() != 10 {
		return errors.New("restic: repository unavailable")
	}
	// Provision only a new, empty directory under an existing parent. A missing
	// config file in a populated repository must never cause reinitialization.
	if err = os.Mkdir(repository, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return errors.New("restic: repository directory unavailable")
	}
	entries, err := os.ReadDir(repository)
	if err != nil || len(entries) != 0 {
		return errors.New("restic: new repository directory must be empty")
	}
	_, _, err = r.invoke(ctx, []string{"--repo", repository, "init", "--json"})
	if err != nil {
		return errors.New("restic: repository initialization failed")
	}
	return nil
}

func snapshotPath(value string) (string, error) {
	if value == "" {
		value = "/"
	}
	if len(value) > 4096 || !strings.HasPrefix(value, "/") || strings.ContainsRune(value, 0) || path.Clean(value) != value {
		return "", errors.New("restic: invalid snapshot path")
	}
	return value, nil
}

// BrowseDirectory uses a path-scoped listing rather than reading the whole tree.
func (r *Runner) BrowseDirectory(ctx context.Context, repository, snapshotID, directory string) ([]SnapshotEntry, error) {
	if !fullSnapshotIDPattern.MatchString(snapshotID) {
		return nil, errors.New("restic: full snapshot ID required")
	}
	directory, err := snapshotPath(directory)
	if err != nil {
		return nil, err
	}
	if err = r.validateForRead(ctx, repository); err != nil {
		return nil, err
	}
	data, _, err := r.invoke(ctx, []string{"--repo", repository, "ls", "--json", snapshotID, directory})
	if err != nil {
		return nil, errors.New("restic: directory unavailable")
	}
	entries, err := parseEntries(data)
	if err != nil {
		return nil, errors.New("restic: invalid directory listing")
	}
	result := make([]SnapshotEntry, 0)
	for _, entry := range entries {
		if entry.Path != directory && path.Dir(entry.Path) == directory {
			result = append(result, entry)
		}
	}
	return result, nil
}

type limitedWriter struct {
	writer    io.Writer
	remaining int64
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, errors.New("download limit")
	}
	n, e := w.writer.Write(p)
	w.remaining -= int64(n)
	return n, e
}

// DumpSnapshot streams a file (raw bytes) or directory (ZIP) into a bounded sink.
func (r *Runner) DumpSnapshot(ctx context.Context, repository, snapshotID, source string, destination io.Writer, limit int64) error {
	if !fullSnapshotIDPattern.MatchString(snapshotID) {
		return errors.New("restic: full snapshot ID required")
	}
	source, err := snapshotPath(source)
	if err != nil {
		return err
	}
	if err = r.validateForRead(ctx, repository); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, r.executable, "--repo", repository, "dump", "--archive", "zip", snapshotID, source)
	cmd.Env = environment(r.environment)
	cmd.Stdout = &limitedWriter{writer: destination, remaining: limit}
	cmd.Stderr = io.Discard
	if cmd.Run() != nil {
		return errors.New("restic: download failed")
	}
	return nil
}
