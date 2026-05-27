package sqlitedb

import (
	"database/sql"
	"embed"
	"fmt"
	"path/filepath"

	"github.com/golang-migrate/migrate/v4"
	migsqlite "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "modernc.org/sqlite"
)

//go:embed migration/*.sql
var migrations embed.FS

// OpenDB opens (or creates) the SQLite database at datadir/solverd.db and
// runs all pending migrations.
func OpenDB(datadir string) (*sql.DB, error) {
	dbPath := filepath.Join(datadir, "solverd.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Enable WAL mode for better concurrent read/write performance.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		// nolint:errcheck
		db.Close()
		return nil, fmt.Errorf("failed to enable WAL mode: %w", err)
	}

	if err := runMigrations(db); err != nil {
		// nolint:errcheck
		db.Close()
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	return db, nil
}

func runMigrations(db *sql.DB) error {
	source, err := iofs.New(migrations, "migration")
	if err != nil {
		return fmt.Errorf("failed to create migration source: %w", err)
	}

	driver, err := migsqlite.WithInstance(db, &migsqlite.Config{})
	if err != nil {
		return fmt.Errorf("failed to create migration driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", source, "sqlite", driver)
	if err != nil {
		return fmt.Errorf("failed to create migrate instance: %w", err)
	}

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("migration failed: %w", err)
	}

	return nil
}
