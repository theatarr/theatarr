package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// MigrationTable is goose's version table.
const MigrationTable = "goose_db_version"

// latestSchemaVersion is derived from the embedded files, including gaps between migration numbers.
func latestSchemaVersion() (int64, error) {
	files, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return 0, err
	}
	var latest int64
	for _, file := range files {
		version, err := goose.NumericComponent(file.Name())
		if err != nil {
			return 0, err
		}
		latest = max(latest, version)
	}
	return latest, nil
}

// CheckSchemaVersion refuses a schema newer than this binary's newest embedded migration. Backup restore also
// checks the manifest's schema through this guard, independently of the manifest format and the database contents.
func CheckSchemaVersion(current int64) error {
	target, err := latestSchemaVersion()
	if err != nil {
		return fmt.Errorf("store: embedded schema version: %w", err)
	}
	if current > target {
		return fmt.Errorf("store: schema version %d is newer than this binary's newest embedded migration %d; "+
			"use a matching or newer image, or restore a pre-upgrade backup (docs/restore.md)", current, target)
	}
	return nil
}

// checkSchema permits a fresh database without creating goose's version table. All other read failures stop open
// or migration rather than treating an unreadable version as a fresh database.
func checkSchema(ctx context.Context, db *sql.DB) error {
	var exists bool
	if err := db.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type = 'table' AND name = ?)", MigrationTable).Scan(&exists); err != nil {
		return fmt.Errorf("store: schema version table: %w", err)
	}
	if !exists {
		return nil
	}
	current, err := schemaVersion(ctx, db)
	if err != nil {
		return err
	}
	return CheckSchemaVersion(current)
}

func schemaVersion(ctx context.Context, db *sql.DB) (int64, error) {
	var current int64
	err := db.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version_id), 0) FROM "+MigrationTable+" WHERE is_applied = 1").Scan(&current)
	if err != nil {
		return 0, fmt.Errorf("store: schema version: %w", err)
	}
	return current, nil
}

func (s *Store) provider(readOnly bool) (*goose.Provider, error) {
	sub, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}
	db := s.writer
	if readOnly {
		db = s.reader
	}
	return goose.NewProvider(database.DialectSQLite3, db, sub,
		goose.WithTableName(MigrationTable),
		goose.WithDisableGlobalRegistry(true),
	)
}

// Migrate applies every pending migration on the writer connection and returns what it applied.
func (s *Store) Migrate(ctx context.Context) ([]*goose.MigrationResult, error) {
	if err := checkSchema(ctx, s.writer); err != nil {
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	p, err := s.provider(false)
	if err != nil {
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	res, err := p.Up(ctx)
	if err != nil {
		return res, fmt.Errorf("store: migrate: %w", err)
	}
	return res, nil
}

// MigrationStatus lists every known migration and whether it is applied. It runs on the writer, because goose
// creates its version table on first use.
func (s *Store) MigrationStatus(ctx context.Context) ([]*goose.MigrationStatus, error) {
	p, err := s.provider(false)
	if err != nil {
		return nil, fmt.Errorf("store: migration status: %w", err)
	}
	st, err := p.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: migration status: %w", err)
	}
	return st, nil
}

// SchemaVersion returns the applied version and the newest version this binary knows. It only reads, so it suits
// readiness checks: a database never migrated reports an error.
func (s *Store) SchemaVersion(ctx context.Context) (current, target int64, err error) {
	target, err = latestSchemaVersion()
	if err != nil {
		return 0, 0, err
	}
	current, err = schemaVersion(ctx, s.reader)
	return current, target, err
}
