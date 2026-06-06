package queryengine

import (
	"context"
	"fmt"
	"time"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/counterpoint"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/store"
)

// loadAllSlotsSQL pulls every energy_data row for (tenant, ec) in
// [start, end] sorted by timestamp + metering_point + meter_code. The
// per-timestamp iteration in Engine.Query relies on `ts ASC` to assemble
// RawSourceLines without holding the whole result-set in memory beyond
// the current bucket.
const loadAllSlotsSQL = `
SELECT ts, metering_point, meter_code, value, qov
FROM energy_data
WHERE tenant_id = $1
  AND ec_id = $2
  AND ts >= $3
  AND ts <= $4
ORDER BY ts ASC, metering_point ASC, meter_code ASC`

// dataLoader fans rows out into per-timestamp RawSourceLines. It owns the
// counterpoint topology + indexing so callers don't have to.
type dataLoader struct {
	pool       store.PgxPool
	tenant, ec string
	start, end time.Time

	cps           []counterpoint.CounterPoint // consumers first, then producers
	consumerByMP  map[string]int              // metering point → source_idx
	producerByMP  map[string]int              // metering point → source_idx
	mpDirection   map[string]counterpoint.Direction
	consumerCount int
	producerCount int
}

func newDataLoader(pool store.PgxPool, repo *counterpoint.Repository,
	tenant, ec string, start, end time.Time) (*dataLoader, error) {
	cps, err := repo.ListByEC(context.Background(), tenant, ec)
	if err != nil {
		return nil, err
	}
	l := &dataLoader{
		pool:         pool,
		tenant:       tenant,
		ec:           ec,
		start:        start,
		end:          end,
		cps:          cps,
		consumerByMP: make(map[string]int),
		producerByMP: make(map[string]int),
		mpDirection:  make(map[string]counterpoint.Direction),
	}
	// v1 used the consumer/producer source_idx as the slot offset
	// directly; v2's counterpoint table keeps the same convention
	// (source_idx scoped per direction).
	for _, cp := range cps {
		l.mpDirection[cp.MeteringPoint] = cp.Direction
		switch cp.Direction {
		case counterpoint.DirectionConsumer:
			l.consumerByMP[cp.MeteringPoint] = cp.SourceIdx
			if cp.SourceIdx+1 > l.consumerCount {
				l.consumerCount = cp.SourceIdx + 1
			}
		case counterpoint.DirectionProducer:
			l.producerByMP[cp.MeteringPoint] = cp.SourceIdx
			if cp.SourceIdx+1 > l.producerCount {
				l.producerCount = cp.SourceIdx + 1
			}
		}
	}
	return l, nil
}

// info returns the v1-shape CounterPointMetaInfo for the engine context.
func (l *dataLoader) info() *CounterPointMetaInfo {
	info := &CounterPointMetaInfo{
		ConsumerCount:  l.consumerCount,
		ProducerCount:  l.producerCount,
		MaxConsumerIdx: -1,
		MaxProducerIdx: -1,
	}
	for _, cp := range l.cps {
		if cp.Direction == counterpoint.DirectionConsumer && cp.SourceIdx > info.MaxConsumerIdx {
			info.MaxConsumerIdx = cp.SourceIdx
		}
		if cp.Direction == counterpoint.DirectionProducer && cp.SourceIdx > info.MaxProducerIdx {
			info.MaxProducerIdx = cp.SourceIdx
		}
	}
	return info
}

// lineFor allocates a new zero-line of the right width for the loaded
// (consumerCount, producerCount) topology.
func (l *dataLoader) lineFor(id string) *RawSourceLine {
	out := makeRawSourceLine(id, l.consumerCount*3, l.producerCount*2)
	// v1 initialises QoV slices to 1 ("substituted") and lets calcQoV
	// promote later. We do the same.
	out.QoVConsumers = initIntSlice(1, out.QoVConsumers)
	out.QoVProducers = initIntSlice(1, out.QoVProducers)
	return out
}

// emit reads all slots in [start, end] and invokes `handle` once per
// distinct timestamp with the assembled RawSourceLine. Missing slots
// stay at zero. The slot row's qov overwrites the per-cell qov when
// present; off-band codes (T-variants) are silently dropped.
func (l *dataLoader) emit(ctx context.Context, handle func(*RawSourceLine) error) error {
	rows, err := l.pool.Query(ctx, loadAllSlotsSQL, l.tenant, l.ec, l.start, l.end)
	if err != nil {
		return fmt.Errorf("queryengine: load slots: %w", err)
	}
	defer rows.Close()

	var (
		current   *RawSourceLine
		currentTS time.Time
	)
	flush := func() error {
		if current == nil {
			return nil
		}
		return handle(current)
	}

	for rows.Next() {
		var (
			ts    time.Time
			mp    string
			code  string
			value float64
			qov   int16
		)
		if err := rows.Scan(&ts, &mp, &code, &value, &qov); err != nil {
			return fmt.Errorf("queryengine: scan: %w", err)
		}
		if current == nil || !ts.Equal(currentTS) {
			if err := flush(); err != nil {
				return err
			}
			current = l.lineFor(rowIDForTS(ts))
			currentTS = ts
		}
		l.placeCell(current, mp, code, value, int(qov))
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("queryengine: rows: %w", err)
	}
	return flush()
}

// placeCell writes one slot into the matching wide-array slot. Direction
// is taken from the counterpoint topology; OBIS code maps to the offset
// within the per-CP block.
func (l *dataLoader) placeCell(line *RawSourceLine, mp, code string, value float64, qov int) {
	dir, ok := l.mpDirection[mp]
	if !ok {
		// Slot without a registered counterpoint — silently drop. v1
		// likewise has no slot to write to.
		return
	}
	switch dir {
	case counterpoint.DirectionConsumer:
		slot, ok := consumerSlotForCode(code)
		if !ok {
			return
		}
		idx := l.consumerByMP[mp]*3 + slot
		if idx < len(line.Consumers) {
			line.Consumers[idx] = value
			line.QoVConsumers[idx] = qov
		}
	case counterpoint.DirectionProducer:
		slot, ok := producerSlotForCode(code)
		if !ok {
			return
		}
		idx := l.producerByMP[mp]*2 + slot
		if idx < len(line.Producers) {
			line.Producers[idx] = value
			line.QoVProducers[idx] = qov
		}
	}
}

// rowIDForTS formats a v1-style row ID so existing parsers in the
// function implementations can still ConvertRowIdToTime on it.
//
// v1 keys look like "CP/2026/06/06/12/00". We keep the same format so
// downstream code doesn't have to special-case v2.
func rowIDForTS(ts time.Time) string {
	t := ts.In(time.Local)
	return fmt.Sprintf("CP/%04d/%02d/%02d/%02d/%02d",
		t.Year(), int(t.Month()), t.Day(), t.Hour(), t.Minute())
}

// parseRowIDTS undoes rowIDForTS. Used by the report functions.
func parseRowIDTS(id string) (time.Time, error) {
	var (
		prefix         string
		yr, mo, dy     int
		hour, min      int
	)
	n, err := fmt.Sscanf(id, "%2s/%04d/%02d/%02d/%02d/%02d",
		&prefix, &yr, &mo, &dy, &hour, &min)
	if err != nil || n != 6 || prefix != "CP" {
		return time.Time{}, fmt.Errorf("queryengine: invalid row id %q", id)
	}
	return time.Date(yr, time.Month(mo), dy, hour, min, 0, 0, time.Local), nil
}
