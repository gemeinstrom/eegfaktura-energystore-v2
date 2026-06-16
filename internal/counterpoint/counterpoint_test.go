package counterpoint

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
)

// TestMarshalJSON_V1WireCompat checks the wire shape consumed by the
// customer-web SPA: snake_case period_start/period_end with v1's
// "DD.MM.YYYY HH:mm:ss" format and dir string. Mismatching this breaks
// FilterHelper.filterActiveMeter → empty cps → empty Excel-Export.
func TestMarshalJSON_V1WireCompat(t *testing.T) {
	ps := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	pe := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	cp := &CounterPoint{
		TenantID:      "TE100200",
		ECID:          "TE100200",
		MeteringPoint: "AT0010000000000000000010000000001",
		Direction:     DirectionConsumer,
		SourceIdx:     0,
		PeriodStart:   &ps,
		PeriodEnd:     &pe,
		Name:          "Anna Berger",
		UpdatedAt:     time.Date(2026, 6, 6, 19, 0, 53, 0, time.UTC),
	}
	b, err := json.Marshal(cp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, want := range []struct{ key, value string }{
		{"period_start", "01.01.2026 00:00:00"},
		{"period_end", "01.01.2030 00:00:00"},
		{"dir", "CONSUMPTION"},
		// v1-semantic: "name" is the metering point identifier, not a
		// human label. The customer-web SPA's metaAdapter keys on this
		// and downstream filters do meta[meterId] lookups.
		{"name", "AT0010000000000000000010000000001"},
		{"displayName", "Anna Berger"},
		{"id", "cpmeta/2026"},
	} {
		if v, _ := got[want.key].(string); v != want.value {
			t.Errorf("%s: want %q, got %q (full %s)", want.key, want.value, v, b)
		}
	}
	if !strings.Contains(string(b), "\"sourceIdx\":0") {
		t.Errorf("sourceIdx missing: %s", b)
	}
	// Ensure the v2-native shape didn't accidentally leak through.
	if strings.Contains(string(b), "\"periodStart\"") || strings.Contains(string(b), "\"direction\"") {
		t.Errorf("v2 camelCase leaked to wire: %s", b)
	}
}

func newRepo(t *testing.T) (*Repository, pgxmock.PgxPoolIface) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	return NewRepository(mock), mock
}

func TestParseDirection(t *testing.T) {
	cases := map[string]Direction{
		"CONSUMPTION": DirectionConsumer,
		"GENERATION":  DirectionProducer,
		"consumer":    DirectionConsumer,
		"producer":    DirectionProducer,
		"GENERATOR":   DirectionProducer,
	}
	for in, want := range cases {
		got, err := ParseDirection(in)
		if err != nil || got != want {
			t.Errorf("ParseDirection(%q) = %v %v, want %v", in, got, err, want)
		}
	}
	if _, err := ParseDirection("nope"); err == nil {
		t.Fatal("expected error for unknown direction")
	}
}

func TestDirectionString(t *testing.T) {
	if DirectionConsumer.String() != "CONSUMPTION" {
		t.Fatalf("consumer string: %q", DirectionConsumer.String())
	}
	if DirectionProducer.String() != "GENERATION" {
		t.Fatalf("producer string: %q", DirectionProducer.String())
	}
}

func TestUpsertRequiresFields(t *testing.T) {
	r, _ := newRepo(t)
	if err := r.Upsert(context.Background(), CounterPoint{}); err == nil {
		t.Fatal("expected error for empty cp")
	}
}

func TestUpsertRequiresDirection(t *testing.T) {
	r, _ := newRepo(t)
	err := r.Upsert(context.Background(), CounterPoint{
		TenantID: "t", ECID: "e", MeteringPoint: "m",
	})
	if err == nil {
		t.Fatal("expected error for missing direction")
	}
}

func TestUpsertOK(t *testing.T) {
	r, mock := newRepo(t)
	defer mock.Close()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(1, 0, 0)
	mock.ExpectExec(`INSERT INTO counterpoint_meta`).
		WithArgs("vfeeg", "TE100200", "AT00100",
			int16(DirectionConsumer), 3,
			&start, &end,
			[]byte(`{"name":"Anna's house"}`),
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	cp := CounterPoint{
		TenantID: "vfeeg", ECID: "TE100200", MeteringPoint: "AT00100",
		Direction: DirectionConsumer, SourceIdx: 3,
		PeriodStart: &start, PeriodEnd: &end, Name: "Anna's house",
	}
	if err := r.Upsert(context.Background(), cp); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestEnsureForMeter_RequiresFields(t *testing.T) {
	r, mock := newRepo(t)
	defer mock.Close()
	if err := r.EnsureForMeter(context.Background(), "", "ec", "mp", DirectionConsumer); err == nil {
		t.Error("expected error for empty tenant")
	}
	if err := r.EnsureForMeter(context.Background(), "t", "", "mp", DirectionConsumer); err == nil {
		t.Error("expected error for empty ec")
	}
	if err := r.EnsureForMeter(context.Background(), "t", "ec", "", DirectionConsumer); err == nil {
		t.Error("expected error for empty meteringPoint")
	}
	if err := r.EnsureForMeter(context.Background(), "t", "ec", "mp", 0); err == nil {
		t.Error("expected error for invalid direction")
	}
}

func TestEnsureForMeter_OK(t *testing.T) {
	r, mock := newRepo(t)
	defer mock.Close()
	// Direction-keyed source_idx auto-pick + ON CONFLICT DO NOTHING.
	// We don't pin the SQL text — just verify args + that no error is
	// thrown when the row is new.
	mock.ExpectExec(`INSERT INTO counterpoint_meta`).
		WithArgs("vfeeg", "TE100200", "AT00100",
			int16(DirectionConsumer),
			[]byte(`{"name":"AT00100"}`),
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	if err := r.EnsureForMeter(context.Background(),
		"vfeeg", "TE100200", "AT00100", DirectionConsumer); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestGetNotFound(t *testing.T) {
	r, mock := newRepo(t)
	defer mock.Close()
	mock.ExpectQuery(`FROM counterpoint_meta`).
		WithArgs("vfeeg", "TE100200", "AT00100").
		WillReturnError(pgx.ErrNoRows)
	_, ok, err := r.Get(context.Background(), "vfeeg", "TE100200", "AT00100")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false")
	}
}

func TestListByEC_OrderingAndPartition(t *testing.T) {
	r, mock := newRepo(t)
	defer mock.Close()
	now := time.Now().UTC()
	cols := []string{
		"tenant_id", "ec_id", "metering_point", "direction", "source_idx",
		"period_start", "period_end", "payload", "updated_at",
	}
	mock.ExpectQuery(`FROM counterpoint_meta`).
		WithArgs("vfeeg", "TE100200").
		WillReturnRows(mock.NewRows(cols).
			AddRow("vfeeg", "TE100200", "AT0001",
				int16(DirectionConsumer), 0,
				(*time.Time)(nil), (*time.Time)(nil),
				[]byte(`{"name":"c0"}`), now).
			AddRow("vfeeg", "TE100200", "AT0002",
				int16(DirectionConsumer), 1,
				(*time.Time)(nil), (*time.Time)(nil),
				[]byte(`{"name":"c1"}`), now).
			AddRow("vfeeg", "TE100200", "AT9001",
				int16(DirectionProducer), 0,
				(*time.Time)(nil), (*time.Time)(nil),
				[]byte(`{"name":"p0"}`), now),
		)
	got, err := r.ListByEC(context.Background(), "vfeeg", "TE100200")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 cps, got %d", len(got))
	}
	cons, prods := Partition(got)
	if len(cons) != 2 || len(prods) != 1 {
		t.Fatalf("partition: cons=%d prods=%d", len(cons), len(prods))
	}
	if cons[0].MeteringPoint != "AT0001" || cons[1].MeteringPoint != "AT0002" {
		t.Fatalf("consumer ordering broken: %+v", cons)
	}
	if cons[0].Name != "c0" {
		t.Fatalf("payload name not decoded: %q", cons[0].Name)
	}
}

func TestDelete(t *testing.T) {
	r, mock := newRepo(t)
	defer mock.Close()
	mock.ExpectExec(`DELETE FROM counterpoint_meta`).
		WithArgs("vfeeg", "TE100200", "AT00100").
		WillReturnResult(pgxmock.NewResult("DELETE", 1))
	if err := r.Delete(context.Background(), "vfeeg", "TE100200", "AT00100"); err != nil {
		t.Fatalf("delete: %v", err)
	}
}
