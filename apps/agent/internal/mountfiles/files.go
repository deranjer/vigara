// Package mountfiles provides bounded text access to Docker-inspected storage.
// All filesystem operations are confined with os.Root; no shell is involved.
package mountfiles

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/deranjer/vigara/internal/protocol"
)

type Resolver interface {
	StorageSource(context.Context, protocol.DockerFileParams) (string, error)
}

type Service struct {
	Resolver        Resolver
	Writable        bool
	BackupDirectory string
	mu              sync.Mutex // Serialize saves, including aliases of a shared mount.
}

func failure(message string) error {
	return &protocol.ChannelError{Code: protocol.ErrCodeFileAccess, Message: message}
}
func conflict() error {
	return &protocol.ChannelError{Code: protocol.ErrCodeConflict, Message: "The file changed since it was opened. Reopen it and review your changes before saving."}
}

func validPath(name string) bool {
	return len(name) <= 2048 && !strings.ContainsAny(name, "\\\x00") && (name == "" || fs.ValidPath(name))
}

type target struct {
	root   *os.Root
	name   string
	single bool
	source string
	info   fs.FileInfo
}

func (s *Service) open(ctx context.Context, p protocol.DockerFileParams) (*target, error) {
	if !validPath(p.Path) {
		return nil, failure("Use a relative path inside the selected mount.")
	}
	source, err := s.Resolver.StorageSource(ctx, p)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(source)
	if err != nil {
		return nil, failure("The storage is unavailable to the host agent.")
	}
	t := &target{source: source, name: p.Path, single: !info.IsDir(), info: info}
	rootPath := source
	if t.single {
		if !info.Mode().IsRegular() || p.Path != "" && p.Path != info.Name() {
			return nil, failure("This mount contains a single regular file.")
		}
		rootPath, t.name = filepath.Dir(source), filepath.Base(source)
	} else if t.name == "" {
		t.name = "."
	}
	t.root, err = os.OpenRoot(rootPath)
	if err != nil {
		return nil, failure("The storage is unavailable to the host agent.")
	}
	checkName := "."
	if t.single {
		checkName = t.name
	}
	current, err := t.root.Lstat(checkName)
	if err != nil || !os.SameFile(info, current) {
		t.root.Close()
		return nil, failure("The storage changed while opening it. Refresh and try again.")
	}
	return t, nil
}

func (s *Service) List(ctx context.Context, p protocol.DockerFileParams) (protocol.DockerFilesResult, error) {
	result := protocol.DockerFilesResult{Entries: []protocol.DockerFileEntry{}, Writable: s.Writable}
	t, err := s.open(ctx, p)
	if err != nil {
		return result, err
	}
	defer t.root.Close()
	result.SingleFile = t.single
	if t.single {
		info, err := t.root.Lstat(t.name)
		if err != nil {
			return result, failure("The file is unavailable.")
		}
		result.Entries = append(result.Entries, entry(info))
		return result, nil
	}
	info, err := t.root.Lstat(t.name)
	if err != nil || !info.IsDir() {
		return result, failure("Only folders inside the mount can be browsed. Symlinks are unavailable.")
	}
	dir, err := t.root.OpenRoot(t.name)
	if err != nil {
		return result, failure("The folder is unavailable or outside the mount.")
	}
	defer dir.Close()
	f, err := dir.Open(".")
	if err != nil {
		return result, failure("The folder could not be read.")
	}
	defer f.Close()
	infos, err := f.Readdir(protocol.MaxDockerFileEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return result, failure("The folder could not be read.")
	}
	if len(infos) > protocol.MaxDockerFileEntries {
		result.Truncated = true
		infos = infos[:protocol.MaxDockerFileEntries]
	}
	for _, info := range infos {
		result.Entries = append(result.Entries, entry(info))
	}
	sort.Slice(result.Entries, func(i, j int) bool {
		a, b := result.Entries[i], result.Entries[j]
		if (a.Kind == "directory") != (b.Kind == "directory") {
			return a.Kind == "directory"
		}
		return a.Name < b.Name
	})
	return result, nil
}

func entry(info fs.FileInfo) protocol.DockerFileEntry {
	kind := "special"
	if info.IsDir() {
		kind = "directory"
	} else if info.Mode().IsRegular() {
		kind = "file"
	} else if info.Mode()&os.ModeSymlink != 0 {
		kind = "symlink"
	}
	return protocol.DockerFileEntry{Name: info.Name(), Kind: kind, Size: info.Size()}
}

func version(info fs.FileInfo, data []byte) string {
	h := sha256.New()
	h.Write(data)
	h.Write([]byte(info.ModTime().UTC().Format("2006-01-02T15:04:05.999999999Z")))
	h.Write([]byte(info.Mode().String()))
	h.Write([]byte(fileIdentity(info)))
	return hex.EncodeToString(h.Sum(nil))
}

func readFile(t *target, write bool) (*os.File, fs.FileInfo, []byte, error) {
	info, err := t.root.Lstat(t.name)
	if err != nil || !info.Mode().IsRegular() || t.single && !os.SameFile(t.info, info) {
		return nil, nil, nil, failure("Only regular text files can be opened. Symlinks and special files are unavailable.")
	}
	flag := os.O_RDONLY
	if write {
		flag = os.O_RDWR
	}
	f, err := t.root.OpenFile(t.name, flag|safeOpenFlags, 0)
	if err != nil {
		return nil, nil, nil, failure("The file could not be opened with the agent's permissions.")
	}
	current, err := f.Stat()
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(info, current) || current.Size() > protocol.MaxDockerFileBytes {
		f.Close()
		return nil, nil, nil, failure("Only regular text files up to 64 KiB can be opened.")
	}
	data, err := io.ReadAll(io.LimitReader(f, protocol.MaxDockerFileBytes+1))
	if err != nil || len(data) > protocol.MaxDockerFileBytes || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		f.Close()
		return nil, nil, nil, failure("Only UTF-8 text files up to 64 KiB can be opened.")
	}
	return f, current, data, nil
}

func (s *Service) Read(ctx context.Context, p protocol.DockerFileParams) (protocol.DockerFileResult, error) {
	var result protocol.DockerFileResult
	t, err := s.open(ctx, p)
	if err != nil {
		return result, err
	}
	defer t.root.Close()
	f, info, data, err := readFile(t, false)
	if err != nil {
		return result, err
	}
	defer f.Close()
	return protocol.DockerFileResult{Content: string(data), Version: version(info, data), Writable: s.Writable}, nil
}

func (s *Service) Write(ctx context.Context, p protocol.DockerFileParams) (protocol.DockerFileResult, error) {
	var result protocol.DockerFileResult
	if !s.Writable {
		return result, failure("File editing is disabled on this host. Set VIGIRA_DOCKER_FILES=write on the agent service to enable it.")
	}
	if p.Version == "" || len(p.Content) > protocol.MaxDockerFileBytes || !utf8.ValidString(p.Content) || strings.IndexByte(p.Content, 0) >= 0 {
		return result, failure("Saving requires the opened file version and UTF-8 text up to 64 KiB.")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.open(ctx, p)
	if err != nil {
		return result, err
	}
	defer t.root.Close()
	// Keep an open descriptor and write in place: replacing the inode would leave
	// a single-file bind mount pointing at the old contents.
	f, info, original, err := readFile(t, true)
	if err != nil {
		return result, err
	}
	defer f.Close()
	if version(info, original) != p.Version {
		return result, conflict()
	}
	backupPath := p.Path
	if t.single {
		backupPath = t.name
	}
	if err := s.backup(t.source, backupPath, original); err != nil {
		return result, failure("The backup could not be saved. The file has not been changed.")
	}
	// Check again after the backup; applications may update files independently.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return result, failure("The file could not be read before saving.")
	}
	latest, err := io.ReadAll(io.LimitReader(f, protocol.MaxDockerFileBytes+1))
	current, statErr := f.Stat()
	named, namedErr := t.root.Lstat(t.name)
	if err != nil || statErr != nil || namedErr != nil || !os.SameFile(info, named) || version(current, latest) != p.Version {
		return result, conflict()
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := replaceContents(f, []byte(p.Content)); err != nil {
		// Best effort restoration; the complete previous bytes are also durable
		// in the separate agent backup directory even if storage refuses writes.
		_ = replaceContents(f, original)
		return result, failure("The save failed. A backup of the previous version is available on the host.")
	}
	current, err = f.Stat()
	if err != nil {
		return result, failure("The file was written, but its version could not be read. Reopen it before saving again.")
	}
	return protocol.DockerFileResult{Content: p.Content, Version: version(current, []byte(p.Content)), Writable: true, BackedUp: true}, nil
}

func replaceContents(f *os.File, data []byte) error {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Truncate(int64(len(data))); err != nil {
		return err
	}
	return f.Sync()
}

// Keep one previous version per source/path, outside application configuration
// directories so wildcard imports cannot accidentally load backups.
func (s *Service) backup(source, name string, data []byte) error {
	if !filepath.IsAbs(s.BackupDirectory) {
		return errors.New("backup directory missing")
	}
	if err := os.MkdirAll(s.BackupDirectory, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(s.BackupDirectory)
	if err != nil || !info.IsDir() || !privateBackup(info) {
		return errors.New("backup directory must be private")
	}
	root, err := os.OpenRoot(s.BackupDirectory)
	if err != nil {
		return err
	}
	defer root.Close()
	key := sha256.Sum256([]byte(source + "\x00" + name))
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	tmp := hex.EncodeToString(nonce[:]) + ".tmp"
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := root.Rename(tmp, hex.EncodeToString(key[:])+".bak"); err != nil {
		return err
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
