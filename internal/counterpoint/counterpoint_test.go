package counterpoint

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
)

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
