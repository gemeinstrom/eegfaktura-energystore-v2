package calc

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/queryengine"
)

// legacyReportConsumer drives the v1 /eeg/report path: collects per-CP
// intermediate (Monthly/Annual/...) and summary EnergyReport objects.
// Implements queryengine.QueryFunction.
type legacyReportConsumer struct {
	info              *queryengine.CounterPointMetaInfo
	alloc             AllocationHandlerV2
	reportID          string
	switchIntermediate func(rowID string) bool
	intermediateID    func(rowID string) string

	results      *calcResults
	intermediate *calcResults
	reports      []*EnergyReport
	lastID       string
}

func (l *legacyReportConsumer) HandleStart(ctx *queryengine.EngineContext) error {
	l.info = ctx.Info
	l.results = newCalcResult(ctx.Info)
	l.intermediate = newCalcResult(ctx.Info)
	return nil
}

func (l *legacyReportConsumer) HandleLine(_ *queryengine.EngineContext, line *queryengine.RawSourceLine) error {
	if l.switchIntermediate(line.ID) {
		l.reports = append(l.reports, l.flushIntermediate())
		if err := sumIntermediate(*l.intermediate, l.results); err != nil {
			return err
		}
		l.intermediate = newCalcResult(l.info)
	}
	if err := appendResults(line, l.alloc, l.intermediate); err != nil {
		return err
	}
	l.lastID = line.ID
	return nil
}

func (l *legacyReportConsumer) HandleEnd(_ *queryengine.EngineContext) error {
	l.reports = append(l.reports, l.flushIntermediate())
	return sumIntermediate(*l.intermediate, l.results)
}

func (l *legacyReportConsumer) flushIntermediate() *EnergyReport {
	return &EnergyReport{
		ID:            l.intermediateID(l.lastID),
		Consumed:      l.intermediate.rCons.RoundToFixed(6).Elements,
		Allocated:     l.intermediate.rAlloc.RoundToFixed(6).Elements,
		Produced:      l.intermediate.rProd.RoundToFixed(6).Elements,
		Distributed:   l.intermediate.rDist.RoundToFixed(6).Elements,
		Shared:        l.intermediate.rShar.RoundToFixed(6).Elements,
		TotalProduced: l.intermediate.pSum,
	}
}

// EnergyReport (legacy /eeg/report path) — mirror v1 calculation/energy.go
// EnergyReport. The (year, segment, periodCode) tuple selects monthly /
// half / quarter / annual aggregation.
func (e *Engine) EnergyReport(ctx context.Context, tenant string, year, segment int, periodCode string) (*EegEnergy, error) {
	code := strings.ToUpper(periodCode)
	if len(code) < 2 {
		code += "X"
	}

	var (
		reportID, switchID string
		switchFn           func(line *queryengine.RawSourceLine) bool
		intermediateFn     func(line *queryengine.RawSourceLine) string
		startTime, endTime time.Time
	)
	_ = switchID

	switch code[1] {
	case 'M':
		reportID = fmt.Sprintf("YM/%d/%.2d", year, segment)
		cDay := 0
		switchFn = func(line *queryengine.RawSourceLine) bool {
			ts, _ := rowIDTime(line.ID)
			if cDay == 0 {
				cDay = ts.Day()
				return false
			}
			should := ts.Day() > cDay
			cDay = ts.Day()
			return should
		}
		intermediateFn = func(line *queryengine.RawSourceLine) string {
			ts, _ := rowIDTime(line.ID)
			return fmt.Sprintf("IRP/%d/%.2d/%.2d", year, segment, ts.Day())
		}
		startTime = time.Date(year, time.Month(segment), 1, 0, 0, 0, 0, time.Local)
		endTime = startTime.AddDate(0, 1, 0)
	case 'H':
		reportID = fmt.Sprintf("YH/%d/%d", year, segment)
		cMonth := 0
		switchFn = func(line *queryengine.RawSourceLine) bool {
			ts, _ := rowIDTime(line.ID)
			if cMonth == 0 {
				cMonth = int(ts.Month())
				return false
			}
			should := int(ts.Month()) > cMonth
			if should {
				cMonth++
			}
			return should
		}
		intermediateFn = func(line *queryengine.RawSourceLine) string {
			ts, _ := rowIDTime(line.ID)
			return fmt.Sprintf("IRP/%d/%d/%.2d", year, segment, ts.Month())
		}
		startTime = time.Date(year, time.Month(((segment-1)*6)+1), 1, 0, 0, 0, 0, time.Local)
		endTime = startTime.AddDate(0, 6, 0)
	case 'Q':
		reportID = fmt.Sprintf("YQ/%d/%d", year, segment)
		cMonth := 0
		switchFn = func(line *queryengine.RawSourceLine) bool {
			ts, _ := rowIDTime(line.ID)
			if cMonth == 0 {
				cMonth = int(ts.Month())
				return false
			}
			should := int(ts.Month()) > cMonth
			if should {
				cMonth++
			}
			return should
		}
		intermediateFn = func(line *queryengine.RawSourceLine) string {
			ts, _ := rowIDTime(line.ID)
			return fmt.Sprintf("IRP/%d/%d/%.2d", year, segment, ts.Month())
		}
		startTime = time.Date(year, time.Month(((segment-1)*3)+1), 1, 0, 0, 0, 0, time.Local)
		endTime = startTime.AddDate(0, 3, 0)
	default:
		reportID = fmt.Sprintf("Y/%d/0", year)
		cMonth := 0
		switchFn = func(line *queryengine.RawSourceLine) bool {
			ts, _ := rowIDTime(line.ID)
			if cMonth == 0 {
				cMonth = int(ts.Month())
				return false
			}
			should := int(ts.Month()) > cMonth
			cMonth = int(ts.Month())
			return should
		}
		intermediateFn = func(line *queryengine.RawSourceLine) string {
			ts, _ := rowIDTime(line.ID)
			return fmt.Sprintf("IRP/%d/0/%.2d/", year, ts.Month())
		}
		startTime = time.Date(year, time.January, 1, 0, 0, 0, 0, time.Local)
		endTime = time.Date(year, time.December, 31, 23, 45, 0, 0, time.Local)
	}

	cons := &legacyReportConsumer{
		alloc:    AllocDynamicV2,
		reportID: reportID,
		switchIntermediate: func(id string) bool {
			line := &queryengine.RawSourceLine{ID: id}
			return switchFn(line)
		},
		intermediateID: func(id string) string {
			line := &queryengine.RawSourceLine{ID: id}
			return intermediateFn(line)
		},
	}
	if err := e.qe.Query(ctx, tenant, "", startTime, endTime, cons); err != nil {
		return nil, err
	}

	summary := &EnergyReport{
		ID:            reportID,
		Consumed:      ensureMatrix(cons.results.rCons, cons.info.ConsumerCount).RoundToFixed(6).Elements,
		Allocated:     ensureMatrix(cons.results.rAlloc, cons.info.ConsumerCount).RoundToFixed(6).Elements,
		Produced:      ensureMatrix(cons.results.rProd, cons.info.ProducerCount).RoundToFixed(6).Elements,
		Distributed:   ensureMatrix(cons.results.rDist, cons.info.ProducerCount).RoundToFixed(6).Elements,
		Shared:        ensureMatrix(cons.results.rShar, cons.info.ConsumerCount).RoundToFixed(6).Elements,
		TotalProduced: cons.results.pSum,
	}
	out := &EegEnergy{Report: summary, Results: cons.reports}
	return out, nil
}
