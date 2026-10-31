// Package store owns the connection pool and the migration runner.
package store

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/joelshore/ticketing/api/migrations"
)

func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	cfg.MaxConns = 10
	cfg.ConnConfig.Tracer = NewSlowQueryTracer(SlowQueryThreshold(), slog.Default())
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// Migrate applies every migration not yet recorded, in filename order.
//
// The ledger is `schema_migrations`, but a database created by the Rust version has sqlx's
// `_sqlx_migrations` instead and its tables already exist. Adopting those versions rather than
// replaying them is what makes the Go binary a drop-in on an existing database — otherwise the
// first boot would try to CREATE TABLE over a live schema and fail.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (
		   version     bigint      PRIMARY KEY,
		   name        text        NOT NULL,
		   applied_at  timestamptz NOT NULL DEFAULT now()
		 )`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO schema_migrations (version, name, applied_at)
		 SELECT version, description, installed_on FROM _sqlx_migrations WHERE success
		 ON CONFLICT (version) DO NOTHING`); err != nil {
		// No _sqlx_migrations means a fresh database, which is the normal case.
		if !strings.Contains(err.Error(), "_sqlx_migrations") {
			return fmt.Errorf("adopt sqlx migrations: %w", err)
		}
	}

	applied := map[int64]bool{}
	rows, err := pool.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("read schema_migrations: %w", err)
	}
	for rows.Next() {
		var version int64
		if err := rows.Scan(&version); err != nil {
			rows.Close()
			return err
		}
		applied[version] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// The same list /api/version reports, so what it names is what this ran.
	names, err := migrations.Names()
	if err != nil {
		return err
	}

	for _, name := range names {
		version, label, err := parseMigrationName(name)
		if err != nil {
			return err
		}
		if applied[version] {
			continue
		}
		body, err := migrations.FS.ReadFile(name)
		if err != nil {
			return err
		}
		// One transaction per migration, so a failure leaves nothing half-applied.
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`, version, label); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

// parseMigrationName reads "0001_init.sql" as version 1, named "init" — the same shape sqlx used,
// so adopted rows line up with the files they came from.
func parseMigrationName(name string) (int64, string, error) {
	base := strings.TrimSuffix(name, ".sql")
	digits, label, found := strings.Cut(base, "_")
	if !found {
		return 0, "", fmt.Errorf("migration %q must be named <version>_<name>.sql", name)
	}
	version, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, "", fmt.Errorf("migration %q has a non-numeric version: %w", name, err)
	}
	return version, label, nil
}
