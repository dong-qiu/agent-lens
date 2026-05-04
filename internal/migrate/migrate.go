// Package migrate embeds the SQL migrations under internal/migrate/sql/
// and applies them on agent-lens startup. v0.1 personal-mode users do
// not need a separate `golang-migrate` CLI install (issue #12 — compose
// auto-migrate). Set AGENT_LENS_SKIP_MIGRATE=1 to opt out.
package migrate

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	_ "github.com/lib/pq"
)

func openDB(dsn string) (*sql.DB, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

//go:embed sql/*.sql
var migrationsFS embed.FS

// Up applies any pending migrations against the given Postgres DSN. Returns
// nil on success or when the schema is already current. Wraps
// migrate.ErrNoChange so callers can treat "no change" as success.
func Up(dsn string) error {
	src, err := iofs.New(migrationsFS, "sql")
	if err != nil {
		return fmt.Errorf("migrate: open embedded source: %w", err)
	}

	db, err := openDB(dsn)
	if err != nil {
		return fmt.Errorf("migrate: open db: %w", err)
	}
	defer func() { _ = db.Close() }()

	driver, err := postgres.WithInstance(db, &postgres.Config{})
	if err != nil {
		return fmt.Errorf("migrate: postgres driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "postgres", driver)
	if err != nil {
		return fmt.Errorf("migrate: new instance: %w", err)
	}

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate: up: %w", err)
	}
	return nil
}
