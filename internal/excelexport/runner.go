package excelexport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/counterpoint"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/queryengine"
)

// Sheet is what every sheet implementation satisfies. Mirrors v1.
type Sheet interface {
	initSheet(ctx *runnerContext) error
	handleLine(ctx *runnerContext, line *queryengine.RawSourceLine) error
	closeSheet(ctx *runnerContext) error
}

// Engine wraps a queryengine.Engine + counterpoint.Repository so callers
// only need to inject one thing.
type Engine struct {
	qe   *queryengine.Engine
	repo *counterpoint.Repository
}

// New constructs an excel-export Engine.
func New(qe *queryengine.Engine, repo *counterpoint.Repository) *Engine {
	return &Engine{qe: qe, repo: repo}
}

// ExportEnergyToExcel renders the standard 2-sheet (Summary + Energiedaten)
// xlsx for the given (tenant, ec, start, end, cps). Returns the file
// buffer ready to be written to an HTTP response.
//
// v1 also wrote a QoV Log sheet if any line failed the QoV check; we keep
// the same behaviour.
func (e *Engine) ExportEnergyToExcel(ctx context.Context, tenant, ecid string,
	start, end time.Time, cps *ExportCPs) (*bytes.Buffer, error) {
	if cps == nil {
		return nil, errors.New("excelexport: ExportCPs required")
	}
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	runner := &runner{sheets: []Sheet{
		&SummarySheet{name: "Summary", excel: f},
		&EnergySheet{name: "Energiedaten", excel: f},
	}}

	return runner.run(ctx, e.qe, e.repo, f, tenant, ecid, start, end, cps)
}

// ExportEnergyForMonth is the v1 ExportExcel convenience wrapper that
// maps (year, month) to a calendar-month range.
func (e *Engine) ExportEnergyForMonth(ctx context.Context, tenant, ecid string,
	year, month int, cps *ExportCPs) (*bytes.Buffer, error) {
	if cps == nil {
		// v1 path used the default community ID for the operator email
		// path. We require an explicit value to avoid surprises.
		return nil, errors.New("excelexport: ExportCPs required (community ID + cps list)")
	}
	start := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.Local)
	end := time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.Local)
	return e.ExportEnergyToExcel(ctx, tenant, ecid, start, end, cps)
}

// runner walks RawSourceLines through every Sheet.
type runner struct {
	sheets []Sheet
}

func (r *runner) run(ctx context.Context, qe *queryengine.Engine,
	repo *counterpoint.Repository, f *excelize.File,
	tenant, ecid string, start, end time.Time, cps *ExportCPs) (*bytes.Buffer, error) {
	// Build runnerContext from counterpoint topology + requested CPs.
	allCps, err := repo.ListByEC(ctx, tenant, ecid)
	if err != nil {
		return nil, fmt.Errorf("excelexport: list cps: %w", err)
	}
	rCtx := buildRunnerContext(start, end, cps, allCps)

	for _, s := range r.sheets {
		if err := s.initSheet(rCtx); err != nil {
			return nil, err
		}
	}

	// queryengine.Engine.Query drives the line iteration with gap-fill.
	// We adapt the call into a Sheet-dispatching QueryFunction.
	cf := &consumerFunc{rCtx: rCtx, sheets: r.sheets}
	if err := qe.Query(ctx, tenant, ecid, start, end, cf); err != nil {
		if !errors.Is(err, queryengine.ErrNoRows) {
			return nil, err
		}
		// no rows → still flush the sheets so the file is valid.
	}

	for _, s := range r.sheets {
		if err := s.closeSheet(rCtx); err != nil {
			return nil, err
		}
	}

	// v1 generates a QoV log sheet when any line failed the check.
	if len(rCtx.qovLogArray) > 0 {
		_ = generateQoVLogSheet(rCtx, f)
	}

	_ = f.DeleteSheet("Sheet1")
	return f.WriteToBuffer()
}

type consumerFunc struct {
	rCtx   *runnerContext
	sheets []Sheet
}

func (c *consumerFunc) HandleStart(_ *queryengine.EngineContext) error { return nil }

func (c *consumerFunc) HandleLine(_ *queryengine.EngineContext, line *queryengine.RawSourceLine) error {
	for _, s := range c.sheets {
		if err := s.handleLine(c.rCtx, line); err != nil {
			return err
		}
	}
	return nil
}

func (c *consumerFunc) HandleEnd(_ *queryengine.EngineContext) error { return nil }

func buildRunnerContext(start, end time.Time, cps *ExportCPs,
	allCps []counterpoint.CounterPoint) *runnerContext {

	periodsCon := map[int]periodRange{}
	periodsPro := map[int]periodRange{}
	metaMap := make(map[string]*counterpoint.CounterPoint, len(allCps))
	for i := range allCps {
		cp := allCps[i]
		metaMap[cp.MeteringPoint] = &cp
		pr := periodRange{}
		if cp.PeriodStart != nil {
			pr.start = *cp.PeriodStart
		}
		if cp.PeriodEnd != nil {
			pr.end = *cp.PeriodEnd
		}
		if cp.Direction == counterpoint.DirectionConsumer {
			periodsCon[cp.SourceIdx] = pr
		} else {
			periodsPro[cp.SourceIdx] = pr
		}
	}

	// Build `meta` from the requested cps order (consumers, then producers).
	var consMeta, prodMeta []counterpoint.CounterPoint
	for _, k := range cps.Cps {
		v, ok := metaMap[k.MeteringPoint]
		if !ok {
			continue
		}
		if v.Direction == counterpoint.DirectionConsumer {
			consMeta = append(consMeta, *v)
		} else {
			prodMeta = append(prodMeta, *v)
		}
	}
	meta := append(consMeta, prodMeta...)
	countCons := len(consMeta)
	countProd := len(prodMeta)

	info := &queryengine.CounterPointMetaInfo{
		ConsumerCount:  countCons,
		ProducerCount:  countProd,
		MaxConsumerIdx: -1,
		MaxProducerIdx: -1,
	}
	for _, cp := range allCps {
		if cp.Direction == counterpoint.DirectionConsumer && cp.SourceIdx > info.MaxConsumerIdx {
			info.MaxConsumerIdx = cp.SourceIdx
		}
		if cp.Direction == counterpoint.DirectionProducer && cp.SourceIdx > info.MaxProducerIdx {
			info.MaxProducerIdx = cp.SourceIdx
		}
	}

	return &runnerContext{
		start:           start,
		end:             end,
		cps:             cps,
		metaMap:         metaMap,
		meta:            meta,
		info:            info,
		countCons:       countCons,
		countProd:       countProd,
		periodsConsumer: periodsCon,
		periodsProducer: periodsPro,
		checkBegin: func(lineDate, mDate time.Time) bool {
			return lineDate.Before(mDate)
		},
	}
}

// dateToString formats DD.MM.YYYY HH:MM:SS local. Mirrors v1
// utils.DateToString.
func dateToString(t time.Time) string {
	t = t.In(time.Local)
	return fmt.Sprintf("%.2d.%.2d.%.4d %.2d:%.2d:%.2d",
		t.Day(), int(t.Month()), t.Year(), t.Hour(), t.Minute(), t.Second())
}
