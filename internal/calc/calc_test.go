package calc

import (
	"context"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/counterpoint"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/queryengine"
)

func TestMatrixMultiplyAdd(t *testing.T) {
	a := MakeMatrix([]float64{1, 2, 3, 4}, 2, 2)
	b := MakeMatrix([]float64{5, 6, 7, 8}, 2, 2)
	c := Multiply(a, b)
	if c.GetElm(0, 0) != 19 || c.GetElm(0, 1) != 22 ||
		c.GetElm(1, 0) != 43 || c.GetElm(1, 1) != 50 {
		t.Fatalf("multiply broken: %v", c.Elements)
	}
	d := MakeMatrix([]float64{1, 1, 1, 1}, 2, 2)
	_ = c.Add(d)
	if c.GetElm(0, 0) != 20 || c.GetElm(1, 1) != 51 {
		t.Fatalf("add broken: %v", c.Elements)
	}
}

func TestAllocDynamicV2_FullCover(t *testing.T) {
	// 1 consumer (Consumed=2 Allocated=1 Distributed=0.5),
	// 1 producer (Produced=3 Distributed=2.5)
	cons := MakeMatrix([]float64{2, 1, 0.5}, 1, 3)
	prod := MakeMatrix([]float64{3, 2.5}, 1, 2)
	alloc, share, p := AllocDynamicV2(cons, prod)
	if alloc.GetElm(0, 0) != 0.5 {
		t.Errorf("alloc[0]=%v want 0.5 (consumer slot 2)", alloc.GetElm(0, 0))
	}
	if share.GetElm(0, 0) != 1 {
		t.Errorf("share[0]=%v want 1 (consumer slot 1)", share.GetElm(0, 0))
	}
	if p.GetElm(0, 0) != 2.5 {
		t.Errorf("prod[0]=%v want 2.5 (producer slot 1)", p.GetElm(0, 0))
	}
}

func TestRecortRoundToFixed(t *testing.T) {
	r := &Recort{Consumption: 1.234567, Allocation: 0.987654}
	r.RoundToFixed(2)
	if r.Consumption != 1.23 || r.Allocation != 0.99 {
		t.Fatalf("round: %+v", r)
	}
}

func TestEnsureFloatSliceGrow(t *testing.T) {
	a := []float64{1, 2}
	a = ensureFloatSlice(a, 5)
	if len(a) != 5 || a[0] != 1 || a[1] != 2 {
		t.Fatalf("grow: %v", a)
	}
}

func TestPeriodToStartEndTime(t *testing.T) {
	s, e, err := PeriodToStartEndTime(2026, 6, "YM")
	if err != nil {
		t.Fatal(err)
	}
	if s.Month() != time.June || e.Month() != time.June {
		t.Errorf("YM: s=%v e=%v", s, e)
	}
	if _, _, err := PeriodToStartEndTime(2026, 0, "YM"); err == nil {
		t.Error("expected error for invalid segment")
	}
}

func TestRowIDTime(t *testing.T) {
	ts, err := rowIDTime("CP/2026/06/06/12/00")
	if err != nil {
		t.Fatal(err)
	}
	if ts.Year() != 2026 || ts.Month() != time.June || ts.Day() != 6 {
		t.Errorf("got %v", ts)
	}
}

// TestEnergySummary drives Engine.EnergySummary end-to-end through
// queryengine.QuerySummaryReport with pgxmock.
func TestEnergySummary(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	repo := counterpoint.NewRepository(mock)
	eng := New(mock, repo)

	now := time.Now()
	mock.ExpectQuery(`FROM counterpoint_meta`).
		WithArgs("vfeeg", "TE100200").
		WillReturnRows(mock.NewRows([]string{
			"tenant_id", "ec_id", "metering_point", "direction", "source_idx",
			"period_start", "period_end", "payload", "updated_at",
		}).AddRow("vfeeg", "TE100200", "AT_CON",
			int16(counterpoint.DirectionConsumer), 0,
			(*time.Time)(nil), (*time.Time)(nil), []byte(`{}`), now))

	start, end, _ := PeriodToStartEndTime(2026, 6, "YM")
	mock.ExpectQuery(`FROM energy_data`).
		WithArgs("vfeeg", "TE100200", start, end).
		WillReturnRows(mock.NewRows([]string{"ts", "metering_point", "meter_code", "value", "qov"}))

	_, err = eng.EnergySummary(context.Background(), "vfeeg", "TE100200", 2026, 6, "YM")
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
}

// TestEnergyReportV2_NoRows: when energy_data has no rows, the report's
// participant meters remain untouched (no panic, no totals).
func TestEnergyReportV2_NoRows(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	repo := counterpoint.NewRepository(mock)
	eng := New(mock, repo)

	now := time.Now()
	cpRows := mock.NewRows([]string{
		"tenant_id", "ec_id", "metering_point", "direction", "source_idx",
		"period_start", "period_end", "payload", "updated_at",
	}).AddRow("vfeeg", "TE100200", "AT_CON",
		int16(counterpoint.DirectionConsumer), 0,
		(*time.Time)(nil), (*time.Time)(nil), []byte(`{}`), now)

	// repo.ListByEC is called twice in EnergyReportV2: once at the top
	// for cpMap, once inside queryengine. The second one happens when
	// the Engine.Query invokes newDataLoader → repo.ListByEC.
	mock.ExpectQuery(`FROM counterpoint_meta`).WithArgs("vfeeg", "TE100200").WillReturnRows(cpRows)
	mock.ExpectQuery(`FROM counterpoint_meta`).WithArgs("vfeeg", "TE100200").WillReturnRows(
		mock.NewRows([]string{
			"tenant_id", "ec_id", "metering_point", "direction", "source_idx",
			"period_start", "period_end", "payload", "updated_at",
		}).AddRow("vfeeg", "TE100200", "AT_CON",
			int16(counterpoint.DirectionConsumer), 0,
			(*time.Time)(nil), (*time.Time)(nil), []byte(`{}`), now))

	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.Local)
	end := start.AddDate(0, 1, 0)
	mock.ExpectQuery(`FROM energy_data`).
		WithArgs("vfeeg", "TE100200", start, end).
		WillReturnRows(mock.NewRows([]string{"ts", "metering_point", "meter_code", "value", "qov"}))

	report := &ReportResponse{
		ParticipantReports: []ParticipantReport{
			{ParticipantID: "p1", Meters: []*MeterReport{
				{MeterID: "AT_CON", From: start.UnixMilli(), Until: end.UnixMilli()},
			}},
		},
	}
	if err := eng.EnergyReportV2(context.Background(), "vfeeg", "TE100200", 2026, 6, "YM", report); err != nil {
		t.Fatalf("report v2: %v", err)
	}
	// On no rows we silently return; report.ID is still set.
	if report.ID == "" {
		t.Error("expected ID to be set even on no rows")
	}
	// Every meter must have a non-nil Report (v1-parity / prod-compat).
	// Without ensureMeterReports the SPA crashes at m.report.summary
	// because the Go-zero `*Report = nil` marshals to JSON `null`.
	for prIdx, pr := range report.ParticipantReports {
		for mIdx, m := range pr.Meters {
			if m.Report == nil {
				t.Fatalf("participantReports[%d].meters[%d].Report is nil — expected zero-Report",
					prIdx, mIdx)
			}
			if m.Report.Intermediate.Consumption == nil ||
				m.Report.Intermediate.Utilization == nil ||
				m.Report.Intermediate.Allocation == nil ||
				m.Report.Intermediate.Production == nil {
				t.Errorf("participantReports[%d].meters[%d]: intermediate slices must be initialised to []",
					prIdx, mIdx)
			}
		}
	}
}

// Compile-time check that queryengine.QueryFunction is satisfied.
var _ queryengine.QueryFunction = (*participantConsumer)(nil)
var _ queryengine.QueryFunction = (*legacyReportConsumer)(nil)
