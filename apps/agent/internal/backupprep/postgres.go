// Package backupprep prepares workload-consistent local backup inputs.
package backupprep

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
)

// PostgreSQLConfig contains only trusted local operator configuration. The
// environment is restricted to libpq connection variables; passwords belong
// in a protected PGPASSFILE.
type PostgreSQLConfig struct {
	Environment         map[string]string
	PGDumpExecutable    string
	PGRestoreExecutable string
	StagingDirectory    string
}

// Prepared owns one private, uniquely named dump directory. Call Cleanup after
// the consumer has finished reading Path.
type Prepared struct {
	Path      string
	directory string
}

// PreparationError exposes only a stable, safe failure code.
type PreparationError struct{ Code string }

func (e *PreparationError) Error() string { return e.Code }

// ValidatePostgreSQLConfig checks that configured libpq values are from the
// supported local-only allowlist.
func ValidatePostgreSQLConfig(config PostgreSQLConfig) error {
	if _, err := postgresEnvironment(config.Environment); err != nil {
		return err
	}
	if strings.TrimSpace(config.Environment["PGDATABASE"]) == "" || strings.TrimSpace(config.Environment["PGUSER"]) == "" {
		return errors.New("PostgreSQL database and user must be configured")
	}
	return nil
}

// Cleanup removes only the unique directory created for this preparation.
func (p *Prepared) Cleanup() error {
	if p == nil || p.directory == "" {
		return nil
	}
	if err := os.RemoveAll(p.directory); err != nil {
		return errors.New("could not remove prepared PostgreSQL archive")
	}
	return nil
}

// PostgreSQL creates a private custom-format archive, validates it with
// pg_restore --list, then atomically renames the validated archive into place.
func PostgreSQL(ctx context.Context, config PostgreSQLConfig) (*Prepared, error) {
	if err := ValidatePostgreSQLConfig(config); err != nil {
		return nil, err
	}
	environment, err := postgresEnvironment(config.Environment)
	if err != nil {
		return nil, err
	}
	parent := config.StagingDirectory
	if parent != "" {
		info, statErr := os.Stat(parent)
		if statErr != nil || !info.IsDir() {
			return nil, errors.New("PostgreSQL staging directory must exist")
		}
	}
	directory, err := os.MkdirTemp(parent, "vigira-pg-")
	if err != nil {
		return nil, errors.New("could not create private PostgreSQL staging directory")
	}
	if err := os.Chmod(directory, 0700); err != nil {
		return failPreparation(directory, "preparation_failed")
	}
	if err := securePrivatePath(directory, true); err != nil {
		return failPreparation(directory, "preparation_failed")
	}
	partial := filepath.Join(directory, "database.dump.partial")
	archive := filepath.Join(directory, "database.dump")
	if err := run(ctx, config.PGDumpExecutable, environment, "pg_dump", "--format=custom", "--file", partial); err != nil {
		if ctx.Err() != nil {
			return failPreparation(directory, "preparation_canceled")
		}
		return failPreparation(directory, "preparation_failed")
	}
	info, err := os.Lstat(partial)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return failPreparation(directory, "preparation_failed")
	}
	if err := os.Chmod(partial, 0600); err != nil {
		return failPreparation(directory, "preparation_failed")
	}
	if err := securePrivatePath(partial, false); err != nil {
		return failPreparation(directory, "preparation_failed")
	}
	if err := run(ctx, config.PGRestoreExecutable, environment, "pg_restore", "--list", partial); err != nil {
		if ctx.Err() != nil {
			return failPreparation(directory, "preparation_canceled")
		}
		return failPreparation(directory, "preparation_failed")
	}
	if err := os.Rename(partial, archive); err != nil {
		return failPreparation(directory, "preparation_failed")
	}
	return &Prepared{Path: archive, directory: directory}, nil
}

func failPreparation(directory, code string) (*Prepared, error) {
	if err := os.RemoveAll(directory); err != nil {
		return nil, &PreparationError{Code: "preparation_cleanup_failed"}
	}
	return nil, &PreparationError{Code: code}
}

func run(ctx context.Context, executable string, environment []string, name string, args ...string) error {
	if executable == "" {
		executable = name
	}
	command := exec.CommandContext(ctx, executable, args...)
	command.Env = make([]string, 0, len(os.Environ())+len(environment))
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if ok && !strings.HasPrefix(strings.ToUpper(key), "PG") {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env, environment...)
	// Child output can contain database names, hosts, or other private details.
	command.Stdout, command.Stderr = nil, nil
	if err := command.Run(); err != nil {
		return errors.New("PostgreSQL preparation command failed")
	}
	return nil
}

var allowedEnvironment = map[string]bool{
	"PGHOST": true, "PGPORT": true, "PGDATABASE": true, "PGUSER": true,
	"PGPASSFILE": true,
	"PGSSLMODE":  true, "PGSSLROOTCERT": true, "PGSSLCERT": true, "PGSSLKEY": true,
}

func postgresEnvironment(input map[string]string) ([]string, error) {
	if len(input) > len(allowedEnvironment) {
		return nil, errors.New("invalid PostgreSQL connection environment")
	}
	result := make([]string, 0, len(input))
	for key, value := range input {
		badValue := len(value) > 8192
		for _, char := range value {
			badValue = badValue || unicode.IsControl(char)
		}
		if !allowedEnvironment[key] || badValue {
			return nil, errors.New("invalid PostgreSQL connection environment")
		}
		result = append(result, key+"="+value)
	}
	return result, nil
}
