// Package restic provides a standalone, managed invocation boundary for restic.
// It deliberately does not depend on agent jobs or the shared wire protocol.
package restic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"unicode"
)

// PinnedVersion is the restic release this runner has been reviewed against.
// Update it deliberately alongside the JSON and exit-code compatibility tests.
const PinnedVersion = "0.19.1"

type Status string

const (
	Succeeded   Status = "succeeded"
	Partial     Status = "partial"
	Failed      Status = "failed"
	Canceled    Status = "canceled"
	Interrupted Status = "interrupted"
)

// Result is safe to persist or report. It intentionally contains no command
// output, stderr, repository address, or credential material.
type Result struct {
	Status        Status   `json:"status"`
	ResticVersion string   `json:"resticVersion"`
	SnapshotIDs   []string `json:"snapshotIds,omitempty"`
	ErrorCode     string   `json:"errorCode,omitempty"`
}

// Progress contains only aggregate values from restic's JSON status messages.
type Progress struct {
	Phase       string
	FilesDone   uint64
	FilesTotal  uint64
	BytesDone   uint64
	BytesTotal  uint64
	PercentDone float64
}

type Config struct {
	Executable string
	// Environment contains restic/backend credentials. Values are passed only
	// through the child process environment and are never included in errors.
	Environment     map[string]string
	ExpectedVersion string
}

type process interface {
	run(context.Context, string, []string, []string) ([]byte, []byte, error)
}

type progressProcess interface {
	runProgress(context.Context, string, []string, []string, func(Progress) error) ([]byte, error)
}

type execProcess struct{}

const maxOutputBytes = 16 << 20

var errOutputLimit = errors.New("restic output exceeded limit")

type boundedBuffer struct {
	bytes.Buffer
	exceeded bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	available := maxOutputBytes - b.Len()
	if available <= 0 {
		b.exceeded = true
		return len(p), nil
	}
	if len(p) > available {
		_, _ = b.Buffer.Write(p[:available])
		b.exceeded = true
		return len(p), nil
	}
	_, _ = b.Buffer.Write(p)
	return len(p), nil
}

func (execProcess) run(ctx context.Context, executable string, args, env []string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = env
	var stdout boundedBuffer
	cmd.Stdout, cmd.Stderr = &stdout, io.Discard
	err := cmd.Run()
	if stdout.exceeded {
		return stdout.Bytes(), nil, errOutputLimit
	}
	return stdout.Bytes(), nil, err
}

func (execProcess) runProgress(ctx context.Context, executable string, args, env []string, onProgress func(Progress) error) ([]byte, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = env
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	var output boundedBuffer
	reader := bufio.NewReaderSize(stdout, 64<<10)
	var readErr error
	for {
		line := make([]byte, 0, 64<<10)
		var lineErr error
		for {
			fragment, fragmentErr := reader.ReadSlice('\n')
			if len(line)+len(fragment) > 1<<20 {
				readErr = errOutputLimit
				_ = cmd.Process.Kill()
				break
			}
			line = append(line, fragment...)
			lineErr = fragmentErr
			if fragmentErr == bufio.ErrBufferFull {
				continue
			}
			break
		}
		if readErr != nil {
			break
		}
		if len(line) > 0 {
			if _, writeErr := output.Write(line); writeErr != nil {
				readErr = writeErr
				_ = cmd.Process.Kill()
				break
			}
			if output.exceeded {
				readErr = errOutputLimit
				_ = cmd.Process.Kill()
				break
			}
			var message backupMessage
			if json.Unmarshal(bytes.TrimSpace(line), &message) == nil && message.MessageType == "status" && onProgress != nil {
				if progress, ok := message.progress(); ok {
					if callbackErr := onProgress(progress); callbackErr != nil {
						readErr = callbackErr
						_ = cmd.Process.Kill()
						break
					}
				}
			}
		}
		if lineErr != nil {
			if !errors.Is(lineErr, io.EOF) {
				readErr = lineErr
			}
			break
		}
	}
	waitErr := cmd.Wait()
	if output.exceeded {
		return output.Bytes(), errOutputLimit
	}
	if readErr != nil {
		return output.Bytes(), readErr
	}
	return output.Bytes(), waitErr
}

type Runner struct {
	executable      string
	expectedVersion string
	environment     map[string]string
	process         process
}

func New(config Config) *Runner {
	executable := config.Executable
	if executable == "" {
		executable = "restic"
	}
	expected := config.ExpectedVersion
	if expected == "" {
		expected = PinnedVersion
	}
	return &Runner{executable: executable, expectedVersion: expected, environment: clone(config.Environment), process: execProcess{}}
}

// ValidateRepository verifies the repository can be opened with the supplied
// credentials by reading its config. It does not initialize or modify it.
func (r *Runner) ValidateRepository(ctx context.Context, repository string) error {
	_, err := r.ValidateRepositoryVersion(ctx, repository)
	return err
}

// ValidateRepositoryVersion validates a repository and reports the pinned
// binary version observed during the validation.
func (r *Runner) ValidateRepositoryVersion(ctx context.Context, repository string) (string, error) {
	if strings.TrimSpace(repository) == "" {
		return "", errors.New("restic: repository is required")
	}
	version, err := r.version(ctx)
	if err != nil {
		return version, err
	}
	stdout, _, err := r.invoke(ctx, []string{"--repo", repository, "cat", "config"})
	if err != nil {
		return version, safeCommandError("validate repository", err)
	}
	if !json.Valid(bytes.TrimSpace(stdout)) {
		return version, errors.New("restic: repository validation returned invalid metadata")
	}
	return version, nil
}

// Backup runs one backup. Paths, repository, and credentials are never emitted
// in returned errors. Output is parsed from restic's documented JSON-lines API.
func (r *Runner) Backup(ctx context.Context, repository string, paths []string) Result {
	return r.BackupWithProgress(ctx, repository, paths, nil)
}

// BackupWithProgress streams bounded aggregate progress when supported by the
// process runner. The callback error terminates the child process.
func (r *Runner) BackupWithProgress(ctx context.Context, repository string, paths []string, onProgress func(Progress) error) Result {
	result := Result{Status: Failed}
	version, err := r.version(ctx)
	result.ResticVersion = version
	if err != nil {
		result.Status, result.ErrorCode = classifyContext(ctx, err)
		if strings.Contains(err.Error(), "unsupported version") {
			result.ErrorCode = "unsupported_version"
		}
		return result
	}
	if strings.TrimSpace(repository) == "" || len(paths) == 0 {
		result.ErrorCode = "invalid_input"
		return result
	}
	args := []string{"--repo", repository, "--json", "--quiet", "backup", "--"}
	args = append(args, paths...)
	var stdout []byte
	var runErr error
	var summary *backupSummary
	var jsonPartial bool
	var parseErr error
	if stream, ok := r.process.(progressProcess); ok {
		stdout, runErr = stream.runProgress(ctx, r.executable, args, environment(r.environment), onProgress)
		summary, jsonPartial, parseErr = parseBackup(stdout)
	} else {
		stdout, _, runErr = r.invoke(ctx, args)
		summary, jsonPartial, parseErr = parseBackupWithProgress(stdout, onProgress)
	}
	if runErr != nil {
		result.Status, result.ErrorCode = classifyRestic(ctx, runErr)
		if parseErr == nil && summary != nil && summary.SnapshotID != "" {
			result.SnapshotIDs = []string{summary.SnapshotID}
		}
		if result.Status == Partial {
			if parseErr != nil || summary == nil {
				result.Status, result.ErrorCode = Failed, "invalid_restic_output"
				return result
			}
		}
		return result
	}
	if parseErr != nil || summary == nil {
		result.Status, result.ErrorCode = Failed, "invalid_restic_output"
		return result
	}
	if summary.SnapshotID != "" {
		result.SnapshotIDs = []string{summary.SnapshotID}
	}
	if jsonPartial {
		result.Status = Partial
		result.ErrorCode = "partial_backup"
	} else {
		result.Status = Succeeded
	}
	return result
}

func (r *Runner) version(ctx context.Context) (string, error) {
	stdout, _, err := r.invoke(ctx, []string{"version"})
	if err != nil {
		return "", safeCommandError("read version", err)
	}
	match := versionPattern.FindSubmatch(stdout)
	if len(match) != 2 {
		return "", errors.New("restic: could not parse version")
	}
	version := string(match[1])
	if version != r.expectedVersion {
		return version, fmt.Errorf("restic: unsupported version %s (expected %s)", version, r.expectedVersion)
	}
	return version, nil
}

var versionPattern = regexp.MustCompile(`(?m)^restic\s+(\d+\.\d+\.\d+)\b`)
var snapshotIDPattern = regexp.MustCompile(`^[0-9a-f]{1,64}$`)

func (r *Runner) invoke(ctx context.Context, args []string) ([]byte, []byte, error) {
	return r.process.run(ctx, r.executable, args, environment(r.environment))
}

func environment(overrides map[string]string) []string {
	values := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok && inheritedEnvironmentKey(key) {
			values[key] = value
		}
	}
	for key, value := range overrides {
		if !validEnvironmentKey(key) || dangerousEnvironmentKey(key) {
			continue
		}
		values[key] = value
	}
	env := make([]string, 0, len(values))
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	return env
}

func inheritedEnvironmentKey(key string) bool {
	key = strings.ToUpper(key)
	if strings.HasPrefix(key, "RESTIC_") {
		return false
	}
	switch key {
	case "PATH", "SYSTEMROOT", "WINDIR", "TEMP", "TMP", "HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA":
		return true
	}
	return false
}

func validEnvironmentKey(key string) bool {
	if key == "" {
		return false
	}
	for _, char := range key {
		if char == '=' || unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func dangerousEnvironmentKey(key string) bool {
	switch strings.ToUpper(key) {
	case "RESTIC_PASSWORD_COMMAND", "RESTIC_FROM_PASSWORD_COMMAND", "RESTIC_REPOSITORY_FILE", "RESTIC_FROM_REPOSITORY_FILE":
		return true
	default:
		return false
	}
}

func clone(source map[string]string) map[string]string {
	copy := make(map[string]string, len(source))
	for key, value := range source {
		copy[key] = value
	}
	return copy
}

type exitCoder interface{ ExitCode() int }

func safeCommandError(operation string, err error) error {
	var exit exitCoder
	if errors.As(err, &exit) {
		return fmt.Errorf("restic: %s failed (exit code %d)", operation, exit.ExitCode())
	}
	return fmt.Errorf("restic: %s failed", operation)
}

func classifyContext(ctx context.Context, err error) (Status, string) {
	if errors.Is(ctx.Err(), context.Canceled) {
		return Canceled, "canceled"
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return Interrupted, "deadline_exceeded"
	}
	return Failed, errorCode(err)
}

func classifyRestic(ctx context.Context, err error) (Status, string) {
	if ctx.Err() != nil {
		return classifyContext(ctx, err)
	}
	var exit exitCoder
	if errors.As(err, &exit) {
		switch exit.ExitCode() {
		case 3:
			return Partial, "partial_backup"
		case 130:
			return Interrupted, "interrupted"
		default:
			return Failed, fmt.Sprintf("restic_exit_%d", exit.ExitCode())
		}
	}
	return Failed, "process_failed"
}

func errorCode(err error) string {
	if err == nil {
		return ""
	}
	var exit exitCoder
	if errors.As(err, &exit) {
		return fmt.Sprintf("restic_exit_%d", exit.ExitCode())
	}
	return "process_failed"
}

type backupMessage struct {
	MessageType string  `json:"message_type"`
	SnapshotID  string  `json:"snapshot_id"`
	ErrorCount  uint64  `json:"error_count"`
	FilesDone   uint64  `json:"files_done"`
	TotalFiles  uint64  `json:"total_files"`
	BytesDone   uint64  `json:"bytes_done"`
	TotalBytes  uint64  `json:"total_bytes"`
	PercentDone float64 `json:"percent_done"`
}

func (m backupMessage) progress() (Progress, bool) {
	if !finite(m.PercentDone) || m.PercentDone < 0 || m.PercentDone > 100 || m.FilesDone > 1_000_000_000_000 || m.TotalFiles > 1_000_000_000_000 || m.BytesDone > 1<<60 || m.TotalBytes > 1<<60 || m.TotalFiles > 0 && m.FilesDone > m.TotalFiles || m.TotalBytes > 0 && m.BytesDone > m.TotalBytes {
		return Progress{}, false
	}
	return Progress{Phase: "backup", FilesDone: m.FilesDone, FilesTotal: m.TotalFiles, BytesDone: m.BytesDone, BytesTotal: m.TotalBytes, PercentDone: m.PercentDone}, true
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

type backupSummary struct {
	SnapshotID string `json:"snapshot_id"`
}

func parseBackup(data []byte) (*backupSummary, bool, error) {
	return parseBackupWithProgress(data, nil)
}

func parseBackupWithProgress(data []byte, onProgress func(Progress) error) (*backupSummary, bool, error) {
	var summary *backupSummary
	partial := false
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var message backupMessage
		if err := json.Unmarshal(line, &message); err != nil {
			return nil, false, err
		}
		switch message.MessageType {
		case "status":
			if onProgress != nil {
				if progress, ok := message.progress(); ok {
					if err := onProgress(progress); err != nil {
						return nil, false, err
					}
				}
			}
			if message.ErrorCount > 0 {
				partial = true
			}
		case "summary":
			if message.SnapshotID != "" && !snapshotIDPattern.MatchString(message.SnapshotID) {
				return nil, false, errors.New("restic: invalid snapshot identifier")
			}
			summary = &backupSummary{SnapshotID: message.SnapshotID}
		case "error":
			partial = true
		}
	}
	return summary, partial, nil
}
