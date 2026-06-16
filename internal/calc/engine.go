package calc

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/counterpoint"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/queryengine"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/store"
)

// Engine bundles the queryengine + counterpoint repository so callers
// only need to inject one thing.
type Engine struct {
	qe   *queryengine.Engine
	repo *counterpoint.Repository
}

// New constructs a calc.Engine over a shared pool.
func New(pool store.PgxPool, repo *counterpoint.Repository) *Engine {
	return &Engine{
		qe:   queryengine.New(pool, repo),
		repo: repo,
	}
}

// EnergySummary mirrors v1 calculation/energy.go EnergySummary. "no rows"
// returns an empty ReportData (not an error).
func (e *Engine) EnergySummary(ctx context.Context, tenant, ec string, year, segment int,
	periodCode string) (*queryengine.ReportData, error) {
	start, end, err := PeriodToStartEndTime(year, segment, periodCode)
	if err != nil {
		return nil, err
	}
	return e.qe.QuerySummaryReport(ctx, tenant, ec, start, end)
}

// EnergyReportV2 mirrors v1 calculation/energy.go EnergyReportV2. The
// caller hands in `report` carrying ParticipantReports + meter
// from/until windows; we drive per-day allocation and accumulate into
// each participant.
func (e *Engine) EnergyReportV2(ctx context.Context, tenant, ec string,
	year, segment int, periodCode string,
	report *ReportResponse) error {
	code := strings.ToUpper(periodCode)
	if len(code) < 2 {
		code += "X"
	}

	var (
		startDate    time.Time
		endDate      time.Time
		switchPeriod func(currentDate time.Time) int
	)
	switch code[1] {
	case 'M':
		startDate = time.Date(year, time.Month(segment), 1, 0, 0, 0, 0, time.Local)
		endDate = startDate.AddDate(0, 1, 0)
		switchPeriod = func(d time.Time) int { return d.Day() }
	case 'H':
		startDate = time.Date(year, time.Month(((segment-1)*6)+1), 1, 0, 0, 0, 0, time.Local)
		endDate = startDate.AddDate(0, 6, 0)
		_, startWeek := startDate.ISOWeek()
		switchPeriod = func(d time.Time) int {
			_, w := d.ISOWeek()
			a := w - startWeek
			b := 53
			return int(math.Max(float64((a%b+b)%b), 1))
		}
	case 'Q':
		startDate = time.Date(year, time.Month(((segment-1)*3)+1), 1, 0, 0, 0, 0, time.Local)
		endDate = startDate.AddDate(0, 3, 0)
		_, startWeek := startDate.ISOWeek()
		switchPeriod = func(d time.Time) int {
			_, w := d.ISOWeek()
			a := w - startWeek
			b := 52
			return int(math.Max(float64((a%b+b)%b), 1))
		}
	default:
		startDate = time.Date(year, time.January, 1, 0, 0, 0, 0, time.Local)
		endDate = time.Date(year, time.December, 31, 23, 45, 0, 0, time.Local)
		startMonth := startDate.Month()
		switchPeriod = func(d time.Time) int {
			a := int(d.Month() - startMonth)
			b := 12
			return int(math.Max(float64((a%b+b)%b)+1, 1))
		}
	}

	report.ID = fmt.Sprintf("%s/%.4d/%.2d", code, year, segment)

	cps, err := e.repo.ListByEC(ctx, tenant, ec)
	if err != nil {
		return err
	}
	cpMap := make(map[string]*counterpoint.CounterPoint, len(cps))
	for i := range cps {
		cp := cps[i]
		cpMap[cp.MeteringPoint] = &cp
		report.Meta = append(report.Meta, &cp)
	}

	// Pre-initialise every meter's Report to an empty struct (zero-
	// summary + empty intermediate slices). This matches prod-v1's
	// /eeg/report wire shape — v1 emitted `{summary:{0,0,0,0},
	// intermediate:{[],[],[],[]}}` for meters without data in the
	// query window, the fork v2 was emitting `null` which crashed the
	// customer-web SPA at `m.report.summary.consumption`.
	//
	// `participantConsumer.flushDay` only initialises `m.Report` when
	// it has data to add; meters outside the window or without a
	// matching counterpoint stayed at nil. Doing it here covers all
	// three paths: query-error / ErrNoRows / per-meter-no-data.
	ensureMeterReports(report)

	cons := &participantConsumer{
		alloc:     AllocDynamicV2,
		report:    report,
		cpMap:     cpMap,
		startDate: startDate,
		switchIdx: switchPeriod,
	}
	if err := e.qe.Query(ctx, tenant, ec, startDate, endDate, cons); err != nil {
		if errors.Is(err, queryengine.ErrNoRows) {
			return nil
		}
		return err
	}
	return nil
}

// ensureMeterReports walks the response and sets `m.Report` to an
// empty Report struct for any meter that still has nil. v1-parity
// (matches prod's /eeg/report response shape).
func ensureMeterReports(report *ReportResponse) {
	for prIdx := range report.ParticipantReports {
		pr := &report.ParticipantReports[prIdx]
		for _, m := range pr.Meters {
			if m.Report == nil {
				m.SetReport(&Report{
					Intermediate: IntermediateRecord{
						Consumption: []float64{},
						Utilization: []float64{},
						Allocation:  []float64{},
						Production:  []float64{},
					},
				})
			}
		}
	}
}

// rowIDTime parses queryengine's "CP/Y/M/D/h/m" row-ID format.
func rowIDTime(id string) (time.Time, error) {
	var prefix string
	var yr, mo, dy, hr, mn int
	n, err := fmt.Sscanf(id, "%2s/%04d/%02d/%02d/%02d/%02d",
		&prefix, &yr, &mo, &dy, &hr, &mn)
	if err != nil || n != 6 || prefix != "CP" {
		return time.Time{}, fmt.Errorf("calc: bad row id %q", id)
	}
	return time.Date(yr, time.Month(mo), dy, hr, mn, 0, 0, time.Local), nil
}
