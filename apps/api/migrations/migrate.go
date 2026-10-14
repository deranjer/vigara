// Package migrations applies the schema bundled with the server binary.
package migrations

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

//go:embed *.sql
var files embed.FS

// Apply runs pending up migrations, preserving the golang-migrate version
// history used by previous CLI/container deployments. It never forces a dirty
// version or runs down migrations.
func Apply(connectionString string) error {
	return apply(files, connectionString)
}

func apply(files fs.FS, connectionString string) (err error) {
	config, err := pgx.ParseConfig(connectionString)
	if err != nil {
		return fmt.Errorf("parse migration connection: %w", err)
	}
	if config.ConnectTimeout == 0 {
		config.ConnectTimeout = 10 * time.Second
	}
	// Also bounds the driver's initial version-table lock, which is acquired
	// before migrate.Up can enforce its own lock timeout.
	config.RuntimeParams["lock_timeout"] = "60000"
	config.RuntimeParams["statement_timeout"] = "300000"
	db := stdlib.OpenDB(*config)
	defer db.Close()
	driver, err := migratepgx.WithInstance(db, &migratepgx.Config{StatementTimeout: 5 * time.Minute})
	if err != nil {
		return fmt.Errorf("initialize migration database: %w", err)
	}
	source, err := iofs.New(files, ".")
	if err != nil {
		_ = driver.Close()
		return fmt.Errorf("load embedded migrations: %w", err)
	}
	runner, err := migrate.NewWithInstance("iofs", source, "pgx5", driver)
	if err != nil {
		_ = source.Close()
		_ = driver.Close()
		return fmt.Errorf("initialize migrations: %w", err)
	}
	defer func() {
		sourceErr, databaseErr := runner.Close()
		err = errors.Join(err, sourceErr, databaseErr)
	}()
	runner.LockTimeout = 65 * time.Second
	if err := runner.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply database migrations: %w", err)
	}
	return nil
}
