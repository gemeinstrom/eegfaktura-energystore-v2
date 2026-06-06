// Package store provides the TimescaleDB-backed persistence layer for
// energystore-v2. Replaces the BadgerDB embedded store of v1.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Slot is a single quarter-hour measurement of one metering point + OBIS code.
// Corresponds to one row in the energy_data hypertable.
type Slot struct {
	TenantID       string
	ECID           string
	MeteringPoint  string
	MeterCode      string
	Timestamp      time.Time
	Value          float64
	QoV            int16
}

// Store wraps the pgxpool for TimescaleDB access.
type Store struct {
	pool *pgxpool.Pool
}

// New constructs a Store from a DSN. Callers must Close it.
func New(ctx context.Context, dsn string, minConns, maxConns int32, appName string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("store: parse dsn: %w", err)
	}
	cfg.MinConns = minConns
	cfg.MaxConns = maxConns
	cfg.ConnConfig.RuntimeParams["application_name"] = appName

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("store: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close releases the connection pool.
func (s *Store) Close() {
	s.pool.Close()
}

// UpsertSlots writes a batch of slots with INSERT ... ON CONFLICT DO UPDATE.
// This is the core hot path that replaces v1's Full-Range Read-Modify-Write.
//
// TODO: implement via pgx.Batch or COPY-with-conflict pattern depending on
// benchmark results. Skeleton signature for now.
func (s *Store) UpsertSlots(ctx context.Context, slots []Slot) error {
	if len(slots) == 0 {
		return nil
	}
	return fmt.Errorf("store: UpsertSlots not yet implemented")
}

// QueryRange returns slots in [from, to] for the given tenant/ec/mp/code.
// TODO: implement.
func (s *Store) QueryRange(ctx context.Context, tenant, ec, mp, code string, from, to time.Time) ([]Slot, error) {
	return nil, fmt.Errorf("store: QueryRange not yet implemented")
}
