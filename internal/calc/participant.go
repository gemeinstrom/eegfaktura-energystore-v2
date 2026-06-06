package calc

import (
	"time"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/counterpoint"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/queryengine"
)

// participantConsumer implements queryengine.QueryFunction to drive
// EnergyReportV2: it groups RawSourceLines by calendar day, runs
// AllocDynamicV2 per-day, and appends the resulting allocation +
// share + production values to each ParticipantReport whose meter
// from/until window overlaps the current day.
type participantConsumer struct {
	alloc     AllocationHandlerV2
	report    *ReportResponse
	cpMap     map[string]*counterpoint.CounterPoint
	startDate time.Time
	switchIdx func(currentDate time.Time) int

	info       *queryengine.CounterPointMetaInfo
	daySummary *calcResults
	currentDay time.Time
	dayInit    bool
}

func (p *participantConsumer) HandleStart(ctx *queryengine.EngineContext) error {
	p.info = ctx.Info
	p.daySummary = newCalcResult(ctx.Info)
	p.currentDay = p.startDate
	return nil
}

func (p *participantConsumer) HandleLine(_ *queryengine.EngineContext, line *queryengine.RawSourceLine) error {
	ts, err := rowIDTime(line.ID)
	if err != nil {
		return nil // skip — mirror v1 silent-skip of bad row IDs
	}

	if !p.dayInit {
		p.currentDay = ts
		p.dayInit = true
	}

	if ts.YearDay() != p.currentDay.YearDay() {
		if err := p.flushDay(p.currentDay); err != nil {
			return err
		}
		p.daySummary = newCalcResult(p.info)
		p.currentDay = ts
	}
	return appendResults(line, p.alloc, p.daySummary)
}

func (p *participantConsumer) HandleEnd(_ *queryengine.EngineContext) error {
	if err := p.flushDay(p.currentDay); err != nil {
		return err
	}
	// Final rounding pass (v1 RoundToFixed-loop at end of
	// calcParticipantReport).
	for _, pr := range p.report.ParticipantReports {
		for _, m := range pr.Meters {
			if m.Report != nil {
				m.Report.RoundToFixed(6)
			}
		}
	}
	return nil
}

func (p *participantConsumer) flushDay(day time.Time) error {
	for meterID, cp := range p.cpMap {
		for prIdx := range p.report.ParticipantReports {
			pr := &p.report.ParticipantReports[prIdx]
			for _, m := range pr.Meters {
				if m.MeterID != meterID {
					continue
				}
				from := TruncateToDay(time.UnixMilli(m.From))
				until := TruncateToDay(time.UnixMilli(m.Until))
				if !(from.Unix() <= day.Unix() && day.Unix() <= until.Unix()) {
					continue
				}
				if m.Report == nil {
					m.SetReport(&Report{})
				}
				switch cp.Direction {
				case counterpoint.DirectionConsumer:
					values := []float64{
						p.daySummary.rCons.RoundToFixed(6).GetElm(cp.SourceIdx, 0),
						p.daySummary.rShar.RoundToFixed(6).GetElm(cp.SourceIdx, 0),
						p.daySummary.rAlloc.RoundToFixed(6).GetElm(cp.SourceIdx, 0),
					}
					m.Report.Summary.Consumption += values[0]
					m.Report.Summary.Allocation += values[1]
					m.Report.Summary.Utilization += values[2]
					p.report.TotalConsumption += values[0]
					p.appendIntermediate(m, values, cp.Direction, day)
				case counterpoint.DirectionProducer:
					values := []float64{
						p.daySummary.rProd.GetElm(cp.SourceIdx, 0),
						p.daySummary.rDist.GetElm(cp.SourceIdx, 0),
					}
					m.Report.Summary.Production += values[0]
					m.Report.Summary.Allocation += values[1]
					p.report.TotalProduction += values[0]
					p.appendIntermediate(m, values, cp.Direction, day)
				}
			}
		}
	}
	return nil
}

func (p *participantConsumer) appendIntermediate(m *MeterReport, values []float64,
	dir counterpoint.Direction, day time.Time) {
	idx := p.switchIdx(day)
	m.Report.Intermediate.ID = "IRP/2023/01"

	switch dir {
	case counterpoint.DirectionConsumer:
		m.Report.Intermediate.Consumption = ensureFloatSlice(m.Report.Intermediate.Consumption, idx)
		m.Report.Intermediate.Allocation = ensureFloatSlice(m.Report.Intermediate.Allocation, idx)
		m.Report.Intermediate.Utilization = ensureFloatSlice(m.Report.Intermediate.Utilization, idx)
		m.Report.Intermediate.Consumption[idx-1] = RoundFixed(m.Report.Intermediate.Consumption[idx-1]+values[0], 6)
		m.Report.Intermediate.Allocation[idx-1] = RoundFixed(m.Report.Intermediate.Allocation[idx-1]+values[1], 6)
		m.Report.Intermediate.Utilization[idx-1] = RoundFixed(m.Report.Intermediate.Utilization[idx-1]+values[2], 6)
	case counterpoint.DirectionProducer:
		m.Report.Intermediate.Production = ensureFloatSlice(m.Report.Intermediate.Production, idx)
		m.Report.Intermediate.Allocation = ensureFloatSlice(m.Report.Intermediate.Allocation, idx)
		m.Report.Intermediate.Production[idx-1] += values[0]
		m.Report.Intermediate.Allocation[idx-1] += values[1]
	}
}
