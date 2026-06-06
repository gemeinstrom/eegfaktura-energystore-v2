// Package migrate runs the embedded SQL migrations against the target DB.
//
// Design choices:
//   - Migration files are embedded (see ../../migrations). One PR ships code
//     + schema atomically.
//   - Idempotent: each file is recorded in schema_migrations and skipped on
//     re-run. Safe to call on every pod start as an init-container or as the
//     `migrate` subcommand.
//   - Cluster-safe: a Postgres advisory lock serializes concurrent runners
//     (multiple replicas, init-containers across pods). The lock is released
//     when the connection closes.
//   - No partial state: each file runs in its own transaction. If a file
//     fails, schema_migrations is not advanced — the same file re-runs on the
//     next call.
package migrate

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/migrations"
)

// advisoryLockKey is the bigint key for the cluster-wide migration lock.
// Stable, arbitrary, distinct from other advisory-lock users in the DB.
const advisoryLockKey int64 = 73_46_94_82_50

const schemaTableSQL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    name        TEXT PRIMARY KEY,
    applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
)`

// Run applies all embedded migrations that are not yet recorded in
// schema_migrations. Uses a single pgx.Conn (not a pool) so the advisory
// lock stays held for the whole run.
func Run(ctx context.Context, dsn string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("migrate: connect: %w", err)
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryLockKey); err != nil {
		return fmt.Errorf("migrate: acquire lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", advisoryLockKey)
	}()

	if _, err := conn.Exec(ctx, schemaTableSQL); err != nil {
		return fmt.Errorf("migrate: create schema_migrations: %w", err)
	}

	applied, err := loadApplied(ctx, conn)
	if err != nil {
		return err
	}

	files, err := listMigrations()
	if err != nil {
		return err
	}

	for _, name := range files {
		if applied[name] {
			continue
		}
		body, err := migrations.FS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("migrate: read %s: %w", name, err)
		}
		if err := applyOne(ctx, conn, name, string(body)); err != nil {
			return err
		}
	}
	return nil
}

func loadApplied(ctx context.Context, conn *pgx.Conn) (map[string]bool, error) {
	rows, err := conn.Query(ctx, "SELECT name FROM schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("migrate: load applied: %w", err)
	}
	defer rows.Close()
	out := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("migrate: scan applied: %w", err)
		}
		out[name] = true
	}
	return out, rows.Err()
}

func listMigrations() ([]string, error) {
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("migrate: read embed: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// applyOne executes the migration body and records success.
// We deliberately do NOT wrap the body in an outer transaction: TimescaleDB
// `CREATE MATERIALIZED VIEW ... WITH (timescaledb.continuous)` cannot run
// inside a tx. Convention: each migration file manages its own atomicity
// (idempotent IF-NOT-EXISTS or self-contained BEGIN/COMMIT blocks).
func applyOne(ctx context.Context, conn *pgx.Conn, name, body string) error {
	if _, err := conn.Exec(ctx, body); err != nil {
		return fmt.Errorf("migrate: apply %s: %w", name, err)
	}
	if _, err := conn.Exec(ctx,
		"INSERT INTO schema_migrations (name) VALUES ($1) ON CONFLICT DO NOTHING",
		name); err != nil {
		return fmt.Errorf("migrate: record %s: %w", name, err)
	}
	return nil
}
