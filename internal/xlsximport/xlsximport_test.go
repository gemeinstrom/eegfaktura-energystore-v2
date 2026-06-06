package xlsximport

import (
	"bytes"
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/xuri/excelize/v2"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/counterpoint"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/store"
)

// makeFixtureXLSX builds an Excel file matching the netzbetreiber export
// layout: a header block (MeteringpointID, Energy direction, Period
// start/end, Metercode) and one data row keyed by a German-format date.
func makeFixtureXLSX(t *testing.T) []byte {
	t.Helper()
	f := excelize.NewFile()
	const sheet = "Energiedaten"
	idx, err := f.NewSheet(sheet)
	if err != nil {
		t.Fatalf("new sheet: %v", err)
	}
	f.SetActiveSheet(idx)
	_ = f.DeleteSheet("Sheet1")

	// 3 MPs: one consumer (cons1) with G.01+G.02+G.03; one producer
	// (prod1) with G.01 (Total) + Profit (Profit→G.02 wide-array slot, but
	// for producers Profit maps to share slot which our codes table maps
	// to P.01). Cells (1-based row, 1-based col).
	rows := [][]string{
		{"MeteringpointID",
			"AT00100000000000000000000001CONS", "AT00100000000000000000000001CONS", "AT00100000000000000000000001CONS",
			"AT00100000000000000000000001PROD", "AT00100000000000000000000001PROD"},
		{"Energy direction",
			"CONSUMPTION", "CONSUMPTION", "CONSUMPTION",
			"GENERATION", "GENERATION"},
		{"Period start",
			"01.01.2026 00:00:00", "01.01.2026 00:00:00", "01.01.2026 00:00:00",
			"01.01.2026 00:00:00", "01.01.2026 00:00:00"},
		{"Period end",
			"31.12.2026 23:45:00", "31.12.2026 23:45:00", "31.12.2026 23:45:00",
			"31.12.2026 23:45:00", "31.12.2026 23:45:00"},
		{"Metercode",
			"GESAMTVERBRAUCH LT. MESSUNG (BEI TEILNAHME GEM. ERZEUGUNG) [KWH]",
			"ANTEIL GEMEINSCHAFTLICHE ERZEUGUNG [KWH]",
			"EIGENDECKUNG GEMEINSCHAFTLICHE ERZEUGUNG [KWH]",
			"GESAMTE GEMEINSCHAFTLICHE ERZEUGUNG [KWH]",
			"GESAMT/ÜBERSCHUSSERZEUGUNG, GEMEINSCHAFTSÜBERSCHUSS [KWH]"},
		{"06.06.2026 12:00:00", "2.0", "0.5", "0.3", "3.0", "2.7"},
	}
	for r, row := range rows {
		for c, v := range row {
			cell, _ := excelize.CoordinatesToCellName(c+1, r+1)
			f.SetCellStr(sheet, cell, v)
		}
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatalf("write: %v", err)
	}
	return buf.Bytes()
}

func TestImporter_ParsesAndUpserts(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	repo := counterpoint.NewRepository(mock)
	st := store.FromPool(mock)

	// 2 CP upserts (consumer + producer)
	mock.ExpectExec(`INSERT INTO counterpoint_meta`).
		WithArgs("vfeeg", "TE100200", "AT00100000000000000000000001CONS",
			int16(counterpoint.DirectionConsumer), 0,
			pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg()).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	mock.ExpectExec(`INSERT INTO counterpoint_meta`).
		WithArgs("vfeeg", "TE100200", "AT00100000000000000000000001PROD",
			int16(counterpoint.DirectionProducer), 0,
			pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg()).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	// 5 slot upserts in one batch: cons G.01/G.02/G.03 + prod G.01/P.01
	batch := mock.ExpectBatch()
	batch.ExpectExec(`INSERT INTO energy_data`).
		WithArgs("vfeeg", "TE100200", "AT00100000000000000000000001CONS", "1-1:1.9.0 G.01",
			pgxmock.AnyArg(), float64(2.0), int16(1)).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	batch.ExpectExec(`INSERT INTO energy_data`).
		WithArgs("vfeeg", "TE100200", "AT00100000000000000000000001CONS", "1-1:2.9.0 G.02",
			pgxmock.AnyArg(), float64(0.5), int16(1)).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	batch.ExpectExec(`INSERT INTO energy_data`).
		WithArgs("vfeeg", "TE100200", "AT00100000000000000000000001CONS", "1-1:2.9.0 G.03",
			pgxmock.AnyArg(), float64(0.3), int16(1)).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	batch.ExpectExec(`INSERT INTO energy_data`).
		WithArgs("vfeeg", "TE100200", "AT00100000000000000000000001PROD", "1-1:2.9.0 G.01",
			pgxmock.AnyArg(), float64(3.0), int16(1)).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	batch.ExpectExec(`INSERT INTO energy_data`).
		WithArgs("vfeeg", "TE100200", "AT00100000000000000000000001PROD", "1-1:2.9.0 P.01",
			pgxmock.AnyArg(), float64(2.7), int16(1)).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	im := &Importer{
		Tenant:     "vfeeg",
		ECID:       "TE100200",
		SheetName:  "Energiedaten",
		Repository: repo,
		Store:      st,
	}

	cps, slots, err := im.ImportReader(context.Background(), bytes.NewReader(makeFixtureXLSX(t)))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if cps != 2 {
		t.Errorf("expected 2 cps, got %d", cps)
	}
	if slots != 5 {
		t.Errorf("expected 5 slots, got %d", slots)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestClassifyMeterCode(t *testing.T) {
	cases := map[string]meterCodeKind{
		"GESAMTVERBRAUCH LT. MESSUNG [KWH]":  codeTotal,
		"GESAMTE GEMEINSCHAFTLICHE ERZEUGUNG": codeTotalProd,
		"ANTEIL GEMEINSCHAFTLICHE":           codeShare,
		"EIGENDECKUNG GEMEINSCHAFTLICHE":     codeCoverage,
		"ÜBERSCHUSSERZEUGUNG":                codeProfit,
		"UEBERSCHUSSERZEUGUNG":               codeProfit,
		"something else":                     codeBad,
	}
	for in, want := range cases {
		if got := classifyMeterCode(in); got != want {
			t.Errorf("classifyMeterCode(%q) = %v want %v", in, got, want)
		}
	}
}

func TestParseDateCell(t *testing.T) {
	if _, err := parseDateCell("06.06.2026 12:00:00"); err != nil {
		t.Errorf("german format: %v", err)
	}
	if _, err := parseDateCell("44561.5"); err != nil {
		t.Errorf("excel numeric: %v", err)
	}
	if _, err := parseDateCell(""); err == nil {
		t.Errorf("empty cell should error")
	}
}
