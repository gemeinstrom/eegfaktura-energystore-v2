package queryengine

import (
	"context"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/counterpoint"
)

func newMockEngine(t *testing.T) (*Engine, pgxmock.PgxPoolIface) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	repo := counterpoint.NewRepository(mock)
	return New(mock, repo), mock
}

func metaRows(mock pgxmock.PgxPoolIface) *pgxmock.Rows {
	return mock.NewRows([]string{
		"tenant_id", "ec_id", "metering_point", "direction", "source_idx",
		"period_start", "period_end", "payload", "updated_at",
	})
}

func slotRows(mock pgxmock.PgxPoolIface) *pgxmock.Rows {
	return mock.NewRows([]string{"ts", "metering_point", "meter_code", "value", "qov"})
}

func expectMeta(mock pgxmock.PgxPoolIface, rows *pgxmock.Rows) {
	mock.ExpectQuery(`FROM counterpoint_meta`).
		WithArgs("vfeeg", "TE100200").
		WillReturnRows(rows)
}

func expectSlots(mock pgxmock.PgxPoolIface, start, end time.Time, rows *pgxmock.Rows) {
	mock.ExpectQuery(`FROM energy_data`).
		WithArgs("vfeeg", "TE100200", start, end).
		WillReturnRows(rows)
}

func TestQueryEngine_NoRows(t *testing.T) {
	eng, mock := newMockEngine(t)
	defer mock.Close()
	start := time.Date(2026, 6, 6, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	expectMeta(mock, metaRows(mock).AddRow("vfeeg", "TE100200", "AT1",
		int16(1), 0, (*time.Time)(nil), (*time.Time)(nil), []byte(`{}`), time.Now()))
	expectSlots(mock, start, end, slotRows(mock))

	_, err := eng.QuerySummaryReport(context.Background(), "vfeeg", "TE100200", start, end)
	if err != nil {
		t.Fatalf("summary on empty: %v", err)
	}
}

func TestQueryEngine_SummaryAccumulates(t *testing.T) {
	eng, mock := newMockEngine(t)
	defer mock.Close()
	start := time.Date(2026, 6, 6, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)

	now := time.Now()
	expectMeta(mock, metaRows(mock).
		AddRow("vfeeg", "TE100200", "AT_CON1",
			int16(counterpoint.DirectionConsumer), 0,
			(*time.Time)(nil), (*time.Time)(nil), []byte(`{}`), now).
		AddRow("vfeeg", "TE100200", "AT_PROD1",
			int16(counterpoint.DirectionProducer), 0,
			(*time.Time)(nil), (*time.Time)(nil), []byte(`{}`), now))

	ts := start.Add(15 * time.Minute)
	expectSlots(mock, start, end, slotRows(mock).
		AddRow(ts, "AT_CON1", "1-1:1.9.0 G.01", float64(2.0), int16(0)).
		AddRow(ts, "AT_CON1", "1-1:2.9.0 G.02", float64(1.0), int16(0)).
		AddRow(ts, "AT_CON1", "1-1:2.9.0 G.03", float64(0.5), int16(0)).
		AddRow(ts, "AT_PROD1", "1-1:2.9.0 G.01", float64(3.0), int16(0)).
		AddRow(ts, "AT_PROD1", "1-1:2.9.0 P.01", float64(2.5), int16(0)))

	r, err := eng.QuerySummaryReport(context.Background(), "vfeeg", "TE100200", start, end)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if r.Consumed != 2.0 {
		t.Errorf("Consumed: got %v want 2.0", r.Consumed)
	}
	if r.Allocated != 1.0 {
		t.Errorf("Allocated: got %v want 1.0", r.Allocated)
	}
	if r.Distributed != 0.5 {
		t.Errorf("Distributed: got %v want 0.5", r.Distributed)
	}
	if r.Produced != 3.0 {
		t.Errorf("Produced: got %v want 3.0", r.Produced)
	}
}

func TestQueryEngine_RawDefault(t *testing.T) {
	eng, mock := newMockEngine(t)
	defer mock.Close()
	start := time.Date(2026, 6, 6, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)

	now := time.Now()
	expectMeta(mock, metaRows(mock).
		AddRow("vfeeg", "TE100200", "AT_CON1",
			int16(counterpoint.DirectionConsumer), 0,
			(*time.Time)(nil), (*time.Time)(nil), []byte(`{}`), now))

	ts := start.Add(15 * time.Minute)
	expectSlots(mock, start, end, slotRows(mock).
		AddRow(ts, "AT_CON1", "1-1:1.9.0 G.01", float64(2.0), int16(0)))

	got, err := eng.QueryRawData(context.Background(), "vfeeg", "TE100200", start, end,
		[]TargetMP{{MeteringPoint: "AT_CON1"}}, nil)
	if err != nil {
		t.Fatalf("raw: %v", err)
	}
	res, ok := got["AT_CON1"]
	if !ok {
		t.Fatalf("expected key AT_CON1")
	}
	if res.Direction != counterpoint.DirectionConsumer {
		t.Fatalf("direction: %v", res.Direction)
	}
	if len(res.Data) != 1 || res.Data[0].Value[0] != 2.0 {
		t.Fatalf("unexpected data: %+v", res.Data)
	}
}

func TestQueryEngine_IntraDay24Buckets(t *testing.T) {
	eng, mock := newMockEngine(t)
	defer mock.Close()
	start := time.Date(2026, 6, 6, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	now := time.Now()
	expectMeta(mock, metaRows(mock).
		AddRow("vfeeg", "TE100200", "AT_CON1",
			int16(counterpoint.DirectionConsumer), 0,
			(*time.Time)(nil), (*time.Time)(nil), []byte(`{}`), now))
	expectSlots(mock, start, end, slotRows(mock))

	res, err := eng.QueryIntraDayReport(context.Background(), "vfeeg", "TE100200", start, end)
	if err != nil {
		t.Fatalf("intraday: %v", err)
	}
	if len(res) != 24 {
		t.Fatalf("expected 24 buckets, got %d", len(res))
	}
}

func TestQueryEngine_LoadCurveEmpty(t *testing.T) {
	eng, mock := newMockEngine(t)
	defer mock.Close()
	start := time.Date(2026, 6, 6, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 3)

	now := time.Now()
	expectMeta(mock, metaRows(mock).
		AddRow("vfeeg", "TE100200", "AT_CON1",
			int16(counterpoint.DirectionConsumer), 0,
			(*time.Time)(nil), (*time.Time)(nil), []byte(`{}`), now))
	expectSlots(mock, start, end, slotRows(mock))

	res, err := eng.QueryLoadCurveReport(context.Background(), "vfeeg", "TE100200", start, end)
	if err != nil {
		t.Fatalf("loadcurve: %v", err)
	}
	if len(res) != 0 {
		t.Fatalf("expected empty, got %d", len(res))
	}
}

func TestParseRawFunction(t *testing.T) {
	cases := []struct {
		in   string
		fn   string
		args []string
	}{
		{"aggregate(1h)", "AGGREGATE", []string{"1h"}},
		{"AGGREGATE(7d)", "AGGREGATE", []string{"7d"}},
		{"foo()", "FOO", []string{}},
		{"foo(a, b)", "FOO", []string{"a", "b"}},
	}
	for _, c := range cases {
		fn, args, err := ParseRawFunction(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if fn != c.fn {
			t.Errorf("%q: fn=%q want %q", c.in, fn, c.fn)
		}
		if len(args) != len(c.args) {
			t.Errorf("%q: args=%v want %v", c.in, args, c.args)
		}
	}
	if _, _, err := ParseRawFunction("no-parens"); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestCalcQoV(t *testing.T) {
	if got := calcQoV(1, 0); got != 0 {
		t.Errorf("calcQoV(1,0)=%d want 0", got)
	}
	if got := calcQoV(0, 1); got != 0 {
		t.Errorf("calcQoV(0,1)=%d want 0 (current=0 not 1, returns current)", got)
	}
	if got := calcQoV(0, 2); got != 2 {
		t.Errorf("calcQoV(0,2)=%d want 2", got)
	}
}

func TestRowIDRoundTrip(t *testing.T) {
	ts := time.Date(2026, 6, 6, 12, 30, 0, 0, time.Local)
	id := rowIDForTS(ts)
	back, err := parseRowIDTS(id)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !back.Equal(ts) {
		t.Fatalf("round trip: %v != %v", back, ts)
	}
}
