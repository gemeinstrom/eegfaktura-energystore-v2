package store

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
)

func newMockStore(t *testing.T) (*Store, pgxmock.PgxPoolIface) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock new pool: %v", err)
	}
	return FromPool(mock), mock
}

func TestUpsertSlotsEmpty(t *testing.T) {
	s, mock := newMockStore(t)
	defer mock.Close()
	if err := s.UpsertSlots(context.Background(), nil); err != nil {
		t.Fatalf("expected nil err for empty slice: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected calls: %v", err)
	}
}

func TestWriteDLQ(t *testing.T) {
	s, mock := newMockStore(t)
	defer mock.Close()
	mock.ExpectExec(`INSERT INTO mqtt_dlq`).
		WithArgs("eegfaktura/vfeeg/energy/TE100200", "decode", "bad json", []byte("{garbage")).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	if err := s.WriteDLQ(context.Background(),
		"eegfaktura/vfeeg/energy/TE100200", "decode", "bad json", []byte("{garbage")); err != nil {
		t.Fatalf("dlq: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestUpsertSlotsBatchSent(t *testing.T) {
	s, mock := newMockStore(t)
	defer mock.Close()

	ts := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	slots := []Slot{
		{TenantID: "vfeeg", ECID: "TE100200", MeteringPoint: "AT00100", MeterCode: "G.01", Timestamp: ts, Value: 1.5, QoV: 0},
		{TenantID: "vfeeg", ECID: "TE100200", MeteringPoint: "AT00100", MeterCode: "G.02", Timestamp: ts, Value: 0.5, QoV: 0},
	}

	exp := mock.ExpectBatch()
	for _, sl := range slots {
		exp.ExpectExec(`INSERT INTO energy_data`).
			WithArgs(sl.TenantID, sl.ECID, sl.MeteringPoint, sl.MeterCode, sl.Timestamp, sl.Value, sl.QoV).
			WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	}

	if err := s.UpsertSlots(context.Background(), slots); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestQueryRangeFromAfterTo(t *testing.T) {
	s, mock := newMockStore(t)
	defer mock.Close()
	from := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	to := from.Add(-1 * time.Hour)
	if _, err := s.QueryRange(context.Background(), "t", "e", "m", "c", from, to); err == nil {
		t.Fatal("expected error when from after to")
	}
}

func TestLastRecordDateEmpty(t *testing.T) {
	s, mock := newMockStore(t)
	defer mock.Close()
	mock.ExpectQuery(`SELECT MAX\(ts\)`).
		WithArgs("vfeeg", "TE100200", "AT00100", "G.01").
		WillReturnRows(mock.NewRows([]string{"max"}).AddRow(nil))
	_, ok, err := s.LastRecordDate(context.Background(), "vfeeg", "TE100200", "AT00100", "G.01")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false on empty result")
	}
}

func TestLastRecordDateFound(t *testing.T) {
	s, mock := newMockStore(t)
	defer mock.Close()
	ts := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT MAX\(ts\)`).
		WithArgs("vfeeg", "TE100200", "AT00100", "G.01").
		WillReturnRows(mock.NewRows([]string{"max"}).AddRow(&ts))
	got, ok, err := s.LastRecordDate(context.Background(), "vfeeg", "TE100200", "AT00100", "G.01")
	if err != nil || !ok {
		t.Fatalf("err=%v ok=%v", err, ok)
	}
	if !got.Equal(ts) {
		t.Fatalf("ts mismatch: %v vs %v", got, ts)
	}
}

func TestQueryRangeOK(t *testing.T) {
	s, mock := newMockStore(t)
	defer mock.Close()

	from := time.Date(2026, 6, 6, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	ts := from.Add(time.Hour)

	rows := mock.NewRows([]string{"tenant_id", "ec_id", "metering_point", "meter_code", "ts", "value", "qov"}).
		AddRow("vfeeg", "TE100200", "AT00100", "G.01", ts, float64(2.5), int16(0))
	mock.ExpectQuery(`SELECT tenant_id, ec_id, metering_point, meter_code, ts, value, qov`).
		WithArgs("vfeeg", "TE100200", "AT00100", "G.01", from, to).
		WillReturnRows(rows)

	got, err := s.QueryRange(context.Background(), "vfeeg", "TE100200", "AT00100", "G.01", from, to)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 slot, got %d", len(got))
	}
	if got[0].Value != 2.5 {
		t.Fatalf("expected Value=2.5, got %v", got[0].Value)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
