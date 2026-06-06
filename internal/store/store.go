// Package store provides the TimescaleDB-backed persistence layer for
// energystore-v2. Replaces the BadgerDB embedded store of v1.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Slot is a single quarter-hour measurement of one metering point + OBIS code.
// Corresponds to one row in the energy_data hypertable.
type Slot struct {
	TenantID      string
	ECID          string
	MeteringPoint string
	MeterCode     string
	Timestamp     time.Time
	Value         float64
	QoV           int16
}

// PgxPool is the subset of *pgxpool.Pool used by Store. Defined so tests can
// substitute pgxmock.PgxPoolIface.
type PgxPool interface {
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Ping(ctx context.Context) error
	Close()
}

// Store wraps the pgxpool for TimescaleDB access.
type Store struct {
	pool PgxPool
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

// FromPool builds a Store around an existing PgxPool. Test helper.
func FromPool(p PgxPool) *Store { return &Store{pool: p} }

// RawPool exposes the underlying PgxPool so sibling packages
// (counterpoint, queryengine, calc) can share the same pool.
func RawPool(s *Store) PgxPool { return s.pool }

// Close releases the connection pool.
func (s *Store) Close() {
	s.pool.Close()
}

// Ping checks the connection.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

const upsertSlotSQL = `
INSERT INTO energy_data
    (tenant_id, ec_id, metering_point, meter_code, ts, value, qov)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (tenant_id, ec_id, metering_point, meter_code, ts)
DO UPDATE SET value = EXCLUDED.value, qov = EXCLUDED.qov`

// UpsertSlots writes a batch of slots with INSERT ... ON CONFLICT DO UPDATE.
// This is the core hot path that replaces v1's Full-Range Read-Modify-Write:
// only the cells that the broker actually delivered are touched.
//
// All slots in one call are sent as a single pgx.Batch (= one round-trip,
// pipelined). For batches larger than batchChunkSize, the slice is split.
func (s *Store) UpsertSlots(ctx context.Context, slots []Slot) error {
	const batchChunkSize = 1000
	if len(slots) == 0 {
		return nil
	}
	for offset := 0; offset < len(slots); offset += batchChunkSize {
		end := offset + batchChunkSize
		if end > len(slots) {
			end = len(slots)
		}
		if err := s.upsertChunk(ctx, slots[offset:end]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) upsertChunk(ctx context.Context, slots []Slot) error {
	batch := &pgx.Batch{}
	for i := range slots {
		sl := &slots[i]
		batch.Queue(upsertSlotSQL,
			sl.TenantID, sl.ECID, sl.MeteringPoint, sl.MeterCode,
			sl.Timestamp, sl.Value, sl.QoV)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	for i := 0; i < len(slots); i++ {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("store: upsert slot %d: %w", i, err)
		}
	}
	return nil
}

const queryRangeSQL = `
SELECT tenant_id, ec_id, metering_point, meter_code, ts, value, qov
FROM energy_data
WHERE tenant_id = $1
  AND ec_id = $2
  AND metering_point = $3
  AND meter_code = $4
  AND ts >= $5
  AND ts <= $6
ORDER BY ts ASC`

// QueryRange returns slots in [from, to] for the given tenant/ec/mp/code.
func (s *Store) QueryRange(ctx context.Context, tenant, ec, mp, code string, from, to time.Time) ([]Slot, error) {
	if from.After(to) {
		return nil, errors.New("store: QueryRange: from after to")
	}
	rows, err := s.pool.Query(ctx, queryRangeSQL, tenant, ec, mp, code, from, to)
	if err != nil {
		return nil, fmt.Errorf("store: query range: %w", err)
	}
	defer rows.Close()
	out := make([]Slot, 0, 96)
	for rows.Next() {
		var sl Slot
		if err := rows.Scan(&sl.TenantID, &sl.ECID, &sl.MeteringPoint, &sl.MeterCode,
			&sl.Timestamp, &sl.Value, &sl.QoV); err != nil {
			return nil, fmt.Errorf("store: scan: %w", err)
		}
		out = append(out, sl)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: rows: %w", err)
	}
	return out, nil
}

const lastRecordDateSQL = `
SELECT MAX(ts)
FROM energy_data
WHERE tenant_id = $1
  AND ec_id = $2
  AND metering_point = $3
  AND meter_code = $4`

// LastRecordDate returns the most recent slot timestamp for the given
// tenant/ec/mp/code, or ok=false when no rows exist.
func (s *Store) LastRecordDate(ctx context.Context, tenant, ec, mp, code string) (time.Time, bool, error) {
	row := s.pool.QueryRow(ctx, lastRecordDateSQL, tenant, ec, mp, code)
	var ts *time.Time
	if err := row.Scan(&ts); err != nil {
		return time.Time{}, false, fmt.Errorf("store: last record date: %w", err)
	}
	if ts == nil {
		return time.Time{}, false, nil
	}
	return *ts, true, nil
}

const writeDLQSQL = `
INSERT INTO mqtt_dlq (topic, failure, error, payload)
VALUES ($1, $2, $3, $4)`

// WriteDLQ records a failed MQTT message for later replay.
// failure should be "decode" or "upsert".
func (s *Store) WriteDLQ(ctx context.Context, topic, failure, errMsg string, payload []byte) error {
	if _, err := s.pool.Exec(ctx, writeDLQSQL, topic, failure, errMsg, payload); err != nil {
		return fmt.Errorf("store: write DLQ: %w", err)
	}
	return nil
}
