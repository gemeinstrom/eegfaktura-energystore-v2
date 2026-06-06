package excelexport

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/xuri/excelize/v2"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/counterpoint"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/queryengine"
)

func TestExportEnergyToExcel_EmptyDataset(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	repo := counterpoint.NewRepository(mock)
	qe := queryengine.New(mock, repo)
	eng := New(qe, repo)

	now := time.Now()
	cpRows := func() *pgxmock.Rows {
		return mock.NewRows([]string{
			"tenant_id", "ec_id", "metering_point", "direction", "source_idx",
			"period_start", "period_end", "payload", "updated_at",
		}).AddRow("vfeeg", "TE100200", "AT_CON",
			int16(counterpoint.DirectionConsumer), 0,
			(*time.Time)(nil), (*time.Time)(nil), []byte(`{}`), now)
	}
	// runner → repo.ListByEC (build CP context) + queryengine → repo.ListByEC
	mock.ExpectQuery(`FROM counterpoint_meta`).WithArgs("vfeeg", "TE100200").WillReturnRows(cpRows())
	mock.ExpectQuery(`FROM counterpoint_meta`).WithArgs("vfeeg", "TE100200").WillReturnRows(cpRows())

	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.Local)
	end := start.AddDate(0, 1, 0)
	mock.ExpectQuery(`FROM energy_data`).
		WithArgs("vfeeg", "TE100200", start, end).
		WillReturnRows(mock.NewRows([]string{"ts", "metering_point", "meter_code", "value", "qov"}))

	cps := &ExportCPs{
		CommunityID: "EC-VFEEG-001",
		Cps: []InvestigatorCP{
			{MeteringPoint: "AT_CON", Direction: "CONSUMPTION", Name: "Anna"},
		},
	}
	buf, err := eng.ExportEnergyToExcel(context.Background(), "vfeeg", "TE100200", start, end, cps)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatalf("empty xlsx buffer")
	}
	// Round-trip: open + verify sheet names.
	f, err := excelize.OpenReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("openreader: %v", err)
	}
	defer f.Close()
	have := map[string]bool{}
	for _, n := range f.GetSheetList() {
		have[n] = true
	}
	if !have["Summary"] || !have["Energiedaten"] {
		t.Fatalf("missing sheets: %v", f.GetSheetList())
	}
	if have["Sheet1"] {
		t.Fatalf("Sheet1 should have been deleted")
	}
}

func TestRoundTo6(t *testing.T) {
	cases := map[float64]float64{
		1.2345678901: 1.234568,
		0.0000005:    0.000001,
		-1.5:         -1.500000,
	}
	for in, want := range cases {
		if got := roundTo6(in); got != want {
			t.Errorf("roundTo6(%v)=%v want %v", in, got, want)
		}
	}
}

func TestReturnFloat_OutOfBounds(t *testing.T) {
	if returnFloat(nil, 0) != 0 {
		t.Error("nil slice")
	}
	if returnFloat([]float64{1, 2}, 5) != 0 {
		t.Error("out of bounds")
	}
	if returnFloat([]float64{1, 2}, 1) != 2 {
		t.Error("in range")
	}
}
