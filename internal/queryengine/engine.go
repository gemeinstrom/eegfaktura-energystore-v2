package queryengine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/counterpoint"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/store"
)

// EngineContext mirrors v1 store.EngineContext: it's the per-query bag
// passed to every QueryFunction implementation. Most of the fields are
// derived from the counterpoint topology + the requested [start, end].
type EngineContext struct {
	Start, End      time.Time
	Meta            []counterpoint.CounterPoint // consumers, then producers
	MetaMap         map[string]*counterpoint.CounterPoint
	Info            *CounterPointMetaInfo
	CountCons       int
	CountProd       int
	PeriodsConsumer map[int]periodRange
	PeriodsProducer map[int]periodRange
}

type periodRange struct {
	start time.Time
	end   time.Time
}

// QueryFunction is the v1 EnergyConsumer interface: receive Start, then
// HandleLine per timestamp, then End.
type QueryFunction interface {
	HandleStart(ctx *EngineContext) error
	HandleLine(ctx *EngineContext, line *RawSourceLine) error
	HandleEnd(ctx *EngineContext) error
}

// Engine is the public entry point. It owns the loader + counterpoint
// repository so callers only pass (tenant, ec, range).
type Engine struct {
	pool store.PgxPool
	repo *counterpoint.Repository
}

// New constructs an Engine over the same pool used for energy_data.
func New(pool store.PgxPool, repo *counterpoint.Repository) *Engine {
	return &Engine{pool: pool, repo: repo}
}

// ErrNoRows is returned by Query when no slots exist in range. Mirrors v1
// "no Rows found".
var ErrNoRows = errors.New("queryengine: no rows found in range")

// Query runs the consumer against all slots in [start, end] for the
// given (tenant, ec).
func (e *Engine) Query(ctx context.Context, tenant, ec string, start, end time.Time,
	consumer QueryFunction) error {
	loader, err := newDataLoader(e.pool, e.repo, tenant, ec, start, end)
	if err != nil {
		return err
	}

	// v1 (store/engine.go) peeks the first row to learn the effective
	// data start, then builds the EngineContext with that as `start` so
	// the bucket cursor (cacheTime) is synchronised with the first real
	// slot. Without this the year-spanning report endpoints (intraday,
	// loadcurve) end up with `cacheTime` lagging months behind `ts`,
	// flushing one slot per bucket and smearing all data across every
	// hour-of-day / day-of-year label.
	var (
		engCtx      *EngineContext
		started     bool
		prev        *time.Time
		seen        bool
		ensureStart func(firstTS time.Time) error
	)

	ensureStart = func(firstTS time.Time) error {
		if started {
			return nil
		}
		engCtx = buildEngineCtx(loader, firstTS, end)
		started = true
		return consumer.HandleStart(engCtx)
	}

	const slotDur = 15 * time.Minute

	err = loader.emit(ctx, func(line *RawSourceLine) error {
		seen = true
		ts, perr := parseRowIDTS(line.ID)
		if perr != nil {
			return perr
		}

		if err := ensureStart(ts); err != nil {
			return err
		}

		// gap fill — v1 fills missing 15-min slots with zero lines.
		if prev != nil {
			diff := int(ts.Sub(*prev) / slotDur)
			for i := 1; i < diff; i++ {
				gapTS := prev.Add(time.Duration(i) * slotDur)
				gap := loader.lineFor(rowIDForTS(gapTS))
				if err := consumer.HandleLine(engCtx, gap); err != nil {
					return err
				}
			}
		}
		copyTS := ts
		prev = &copyTS

		return consumer.HandleLine(engCtx, line)
	})
	if err != nil {
		return err
	}
	if !seen {
		return ErrNoRows
	}
	return consumer.HandleEnd(engCtx)
}

func buildEngineCtx(l *dataLoader, start, end time.Time) *EngineContext {
	periodsConsumer := map[int]periodRange{}
	periodsProducer := map[int]periodRange{}
	for _, cp := range l.cps {
		pr := periodRange{}
		if cp.PeriodStart != nil {
			pr.start = *cp.PeriodStart
		}
		if cp.PeriodEnd != nil {
			pr.end = *cp.PeriodEnd
		}
		if cp.Direction == counterpoint.DirectionConsumer {
			periodsConsumer[cp.SourceIdx] = pr
		} else {
			periodsProducer[cp.SourceIdx] = pr
		}
	}

	metaMap := make(map[string]*counterpoint.CounterPoint, len(l.cps))
	for i := range l.cps {
		cp := l.cps[i]
		metaMap[cp.MeteringPoint] = &cp
	}

	// v1 widens [start,end] to local-midnight..23:45.
	s := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.Local)
	e := time.Date(end.Year(), end.Month(), end.Day(), 23, 45, 0, 0, time.Local)

	return &EngineContext{
		Start:           s,
		End:             e,
		Meta:            l.cps,
		MetaMap:         metaMap,
		Info:            l.info(),
		CountCons:       l.consumerCount,
		CountProd:       l.producerCount,
		PeriodsConsumer: periodsConsumer,
		PeriodsProducer: periodsProducer,
	}
}

// cache wraps the bucketing helper v1's report functions reuse. cacheTs
// is the bucket width (1h for IntraDay, 24h for LoadCurve, configurable
// for Aggregate).
type cache struct {
	cacheTs   time.Duration
	cache     RawSourceLine
	cacheTime time.Time
}

func (c *cache) init(ctx *EngineContext) error {
	c.cacheTime = ctx.Start.Add(c.cacheTs)
	c.cache = *makeRawSourceLine("",
		ctx.CountCons*3, ctx.CountProd*2)
	c.cache.QoVConsumers = initIntSlice(1, c.cache.QoVConsumers)
	c.cache.QoVProducers = initIntSlice(1, c.cache.QoVProducers)
	return nil
}

// cacheLine accumulates `line` into the current bucket. When the line
// crosses the bucket boundary it flushes via `flush` and starts a new
// bucket.
//
// Gap handling: if `ts` is many bucket widths past `cacheTime` (e.g. the
// query window starts on Jan 1 but the first real slot is in April), we
// MUST jump `cacheTime` forward by enough whole bucket widths so the
// next bucket actually contains `ts`. Without that catch-up,
// `cacheTime` only advances by one cacheTs per flushed slot — every
// 15-min slot ends up in a different bucket label, smearing the
// dataset across all hour-of-day / day-of-year buckets. The same bug
// applies symmetrically to LoadCurve (cacheTs=24h): one slot per
// calendar day until cacheTime catches up.
func (c *cache) cacheLine(ctx *EngineContext, ts time.Time, line *RawSourceLine,
	flush func(*EngineContext, time.Time, *RawSourceLine) error) error {
	if ts.Before(c.cacheTime) {
		return c.addToCache(line)
	}
	if err := flush(ctx, c.cacheTime, &c.cache); err != nil {
		return err
	}
	c.cacheTime = c.cacheTime.Add(c.cacheTs)
	if !ts.Before(c.cacheTime) {
		gap := ts.Sub(c.cacheTime)
		steps := int64(gap/c.cacheTs) + 1
		c.cacheTime = c.cacheTime.Add(time.Duration(steps) * c.cacheTs)
	}
	c.cache = line.DeepCopy(ctx.CountCons, ctx.CountProd)
	return nil
}

func (c *cache) addToCache(line *RawSourceLine) error {
	c.cache.ID = line.ID
	for i := range line.Consumers {
		if i >= len(c.cache.Consumers) {
			break
		}
		c.cache.Consumers[i] += line.Consumers[i]
		qov := 0
		if i < len(line.QoVConsumers) {
			qov = line.QoVConsumers[i]
		}
		c.cache.QoVConsumers[i] = calcQoV(c.cache.QoVConsumers[i], qov)
	}
	for i := range line.Producers {
		if i >= len(c.cache.Producers) {
			break
		}
		c.cache.Producers[i] += line.Producers[i]
		qov := 0
		if i < len(line.QoVProducers) {
			qov = line.QoVProducers[i]
		}
		c.cache.QoVProducers[i] = calcQoV(c.cache.QoVProducers[i], qov)
	}
	return nil
}

// rowIDToTime is exposed for function implementations that want the
// timestamp from a RawSourceLine.ID.
func rowIDToTime(id string) (time.Time, error) {
	ts, err := parseRowIDTS(id)
	if err != nil {
		return time.Time{}, fmt.Errorf("rowID: %w", err)
	}
	return ts, nil
}
