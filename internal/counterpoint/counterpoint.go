// Package counterpoint ports v1's CounterPointMeta layer to the v2
// long-schema. v1 stored the meta in a BadgerDB bucket keyed by
// (tenant, year) — see model/sourcemodel.go CounterPointMeta and
// model/counterpoint.go MeterDirection. v2 keeps an authoritative SQL
// table (counterpoint_meta) declared in migrations/0001_init.sql.
//
// The table layout is intentionally narrower than v1's struct: the
// hot fields (direction, source_idx, period) are first-class columns,
// while extension fields (Name, Count, raw v1 enum strings) live in a
// JSONB payload so future fields don't need migrations.
package counterpoint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/store"
)

// Direction is the v2 numeric encoding of v1's MeterDirection.
//
// v1 strings are mapped as follows:
//
//	"CONSUMPTION" → DirectionConsumer (1)
//	"GENERATION"  → DirectionProducer (2)
type Direction int16

const (
	DirectionConsumer Direction = 1
	DirectionProducer Direction = 2
)

// String returns the v1 enum string ("CONSUMPTION" / "GENERATION") so
// JSON responses match v1.
func (d Direction) String() string {
	switch d {
	case DirectionConsumer:
		return "CONSUMPTION"
	case DirectionProducer:
		return "GENERATION"
	default:
		return ""
	}
}

// ParseDirection accepts v1 strings and lowercase aliases.
func ParseDirection(s string) (Direction, error) {
	switch s {
	case "CONSUMPTION", "consumer", "consumption", "CONSUMER":
		return DirectionConsumer, nil
	case "GENERATION", "producer", "generation", "PRODUCER", "GENERATOR":
		return DirectionProducer, nil
	default:
		return 0, fmt.Errorf("counterpoint: unknown direction %q", s)
	}
}

// CounterPoint is the v2 representation of a metering point's meta.
type CounterPoint struct {
	TenantID      string     `json:"tenantId"`
	ECID          string     `json:"ecId"`
	MeteringPoint string     `json:"meteringPoint"`
	Direction     Direction  `json:"direction"`
	SourceIdx     int        `json:"sourceIdx"`
	PeriodStart   *time.Time `json:"periodStart,omitempty"`
	PeriodEnd     *time.Time `json:"periodEnd,omitempty"`
	Name          string     `json:"name,omitempty"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

// metaTimeLayout matches v1's CounterPointMeta period_start/period_end
// string format ("DD.MM.YYYY HH:mm:ss"). The customer-web SPA parses
// against exactly this pattern in src/util/FilterHelper.unit.ts; any
// other format makes the meter filter fall through to false and
// downstream features (Excel-Export, period validation) see an empty
// cps list.
const metaTimeLayout = "02.01.2006 15:04:05"

// MarshalJSON emits the v1-compat wire shape used by report.meta:
//
//	"name":          string  — v1 stored the metering-point ID here
//	                          (NOT a human name); customer-web's
//	                          metaAdapter is keyed by name and
//	                          downstream filters look it up via
//	                          meta[meterId]. So we MUST emit
//	                          c.MeteringPoint, not c.Name.
//	"displayName":   string  — v2-addition, carries the cp_meta.payload
//	                          human-readable name if present.
//	"sourceIdx":     int
//	"dir":           "CONSUMPTION" | "GENERATION"
//	"period_start":  "DD.MM.YYYY HH:mm:ss"
//	"period_end":    "DD.MM.YYYY HH:mm:ss"
//	"id":            "cpmeta/<year>"   — v1 BadgerDB bucket key
//	"count":         int               — v1 carried this; v2 omits with 0
//
// The v2-native fields (tenantId, ecId, meteringPoint, updatedAt) are
// kept side-by-side so v2-aware clients still get them. v1 ignored
// unknown keys.
func (c *CounterPoint) MarshalJSON() ([]byte, error) {
	out := struct {
		ID            string `json:"id,omitempty"`
		Name          string `json:"name"`
		DisplayName   string `json:"displayName,omitempty"`
		SourceIdx     int    `json:"sourceIdx"`
		Dir           string `json:"dir"`
		Count         uint16 `json:"count"`
		PeriodStart   string `json:"period_start"`
		PeriodEnd     string `json:"period_end"`
		TenantID      string `json:"tenantId,omitempty"`
		ECID          string `json:"ecId,omitempty"`
		MeteringPoint string `json:"meteringPoint,omitempty"`
		UpdatedAt     string `json:"updatedAt,omitempty"`
	}{
		Name:          c.MeteringPoint,
		DisplayName:   c.Name,
		SourceIdx:     c.SourceIdx,
		Dir:           c.Direction.String(),
		TenantID:      c.TenantID,
		ECID:          c.ECID,
		MeteringPoint: c.MeteringPoint,
	}
	if c.PeriodStart != nil {
		out.PeriodStart = c.PeriodStart.Format(metaTimeLayout)
		out.ID = fmt.Sprintf("cpmeta/%d", c.PeriodStart.Year())
	}
	if c.PeriodEnd != nil {
		out.PeriodEnd = c.PeriodEnd.Format(metaTimeLayout)
	}
	if !c.UpdatedAt.IsZero() {
		out.UpdatedAt = c.UpdatedAt.Format(time.RFC3339Nano)
	}
	return json.Marshal(out)
}

// payload is the JSONB blob stored in counterpoint_meta.payload. Carries
// fields beyond the first-class columns so we don't need a migration each
// time v1 grows a new attribute.
type payload struct {
	Name string `json:"name,omitempty"`
}

// Repository wraps a store.PgxPool to provide CRUD for counterpoint_meta.
type Repository struct {
	pool store.PgxPool
}

// NewRepository builds a Repository against an existing pool. The Store
// type exposes its pool via FromPool — production wiring passes the same
// pgxpool used for energy_data, since both live in postgres-energy.
func NewRepository(pool store.PgxPool) *Repository {
	return &Repository{pool: pool}
}

const upsertSQL = `
INSERT INTO counterpoint_meta
    (tenant_id, ec_id, metering_point, direction, source_idx,
     period_start, period_end, payload, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
ON CONFLICT (tenant_id, ec_id, metering_point)
DO UPDATE SET
    direction    = EXCLUDED.direction,
    source_idx   = EXCLUDED.source_idx,
    period_start = EXCLUDED.period_start,
    period_end   = EXCLUDED.period_end,
    payload      = EXCLUDED.payload,
    updated_at   = now()`

// Upsert writes (or updates) a CounterPoint row. UpdatedAt is server-set.
func (r *Repository) Upsert(ctx context.Context, cp CounterPoint) error {
	if cp.TenantID == "" || cp.ECID == "" || cp.MeteringPoint == "" {
		return errors.New("counterpoint: tenant, ec and meteringPoint are required")
	}
	if cp.Direction != DirectionConsumer && cp.Direction != DirectionProducer {
		return fmt.Errorf("counterpoint: invalid direction %d", cp.Direction)
	}
	pj, err := json.Marshal(payload{Name: cp.Name})
	if err != nil {
		return fmt.Errorf("counterpoint: marshal payload: %w", err)
	}
	if _, err := r.pool.Exec(ctx, upsertSQL,
		cp.TenantID, cp.ECID, cp.MeteringPoint, int16(cp.Direction), cp.SourceIdx,
		cp.PeriodStart, cp.PeriodEnd, pj); err != nil {
		return fmt.Errorf("counterpoint: upsert: %w", err)
	}
	return nil
}

const getSQL = `
SELECT tenant_id, ec_id, metering_point, direction, source_idx,
       period_start, period_end, payload, updated_at
FROM counterpoint_meta
WHERE tenant_id = $1 AND ec_id = $2 AND metering_point = $3`

// Get returns the meta for one metering point, or ok=false.
func (r *Repository) Get(ctx context.Context, tenant, ec, mp string) (CounterPoint, bool, error) {
	row := r.pool.QueryRow(ctx, getSQL, tenant, ec, mp)
	cp, err := scanCP(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return CounterPoint{}, false, nil
	}
	if err != nil {
		return CounterPoint{}, false, fmt.Errorf("counterpoint: get: %w", err)
	}
	return cp, true, nil
}

const listByECSQL = `
SELECT tenant_id, ec_id, metering_point, direction, source_idx,
       period_start, period_end, payload, updated_at
FROM counterpoint_meta
WHERE tenant_id = $1 AND ec_id = $2
ORDER BY direction ASC, source_idx ASC`

// ListByEC returns all counterpoints for one EC, sorted (consumer first,
// producer second) by source_idx. This is the access path the
// calculation layer relies on — order matters because v1's allocation
// algorithms expect consumer/producer arrays in source_idx order.
func (r *Repository) ListByEC(ctx context.Context, tenant, ec string) ([]CounterPoint, error) {
	rows, err := r.pool.Query(ctx, listByECSQL, tenant, ec)
	if err != nil {
		return nil, fmt.Errorf("counterpoint: list: %w", err)
	}
	defer rows.Close()
	out := make([]CounterPoint, 0, 32)
	for rows.Next() {
		cp, err := scanCP(rows)
		if err != nil {
			return nil, fmt.Errorf("counterpoint: scan: %w", err)
		}
		out = append(out, cp)
	}
	return out, rows.Err()
}

const deleteSQL = `
DELETE FROM counterpoint_meta
WHERE tenant_id = $1 AND ec_id = $2 AND metering_point = $3`

// Delete removes one metering point's meta. No error if it doesn't exist.
func (r *Repository) Delete(ctx context.Context, tenant, ec, mp string) error {
	if _, err := r.pool.Exec(ctx, deleteSQL, tenant, ec, mp); err != nil {
		return fmt.Errorf("counterpoint: delete: %w", err)
	}
	return nil
}

// scannable abstracts pgx.Row / pgx.Rows so scanCP works for both.
type scannable interface {
	Scan(dest ...any) error
}

func scanCP(s scannable) (CounterPoint, error) {
	var (
		cp        CounterPoint
		dirRaw    int16
		payloadBs []byte
	)
	if err := s.Scan(&cp.TenantID, &cp.ECID, &cp.MeteringPoint, &dirRaw, &cp.SourceIdx,
		&cp.PeriodStart, &cp.PeriodEnd, &payloadBs, &cp.UpdatedAt); err != nil {
		return CounterPoint{}, err
	}
	cp.Direction = Direction(dirRaw)
	if len(payloadBs) > 0 {
		var p payload
		if err := json.Unmarshal(payloadBs, &p); err != nil {
			return CounterPoint{}, fmt.Errorf("payload unmarshal: %w", err)
		}
		cp.Name = p.Name
	}
	return cp, nil
}

// Partition splits a list of counterpoints into consumers + producers,
// preserving source_idx ordering inside each slice. Mirrors v1's
// expected layout for RawSourceLine.Consumers / .Producers.
func Partition(cps []CounterPoint) (consumers, producers []CounterPoint) {
	for _, cp := range cps {
		switch cp.Direction {
		case DirectionConsumer:
			consumers = append(consumers, cp)
		case DirectionProducer:
			producers = append(producers, cp)
		}
	}
	return consumers, producers
}
