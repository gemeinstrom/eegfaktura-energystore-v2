package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/store"
)

func newServer(t *testing.T) (*Server, pgxmock.PgxPoolIface) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	return New(store.FromPool(mock)), mock
}

func TestHealthz(t *testing.T) {
	srv, _ := newServer(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestReadyzDBOK(t *testing.T) {
	srv, mock := newServer(t)
	defer mock.Close()
	mock.ExpectPing()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestReadyzDBDown(t *testing.T) {
	srv, mock := newServer(t)
	defer mock.Close()
	mock.ExpectPing().WillReturnError(&pgconn.PgError{Message: "down"})
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestRangeMissingParams(t *testing.T) {
	srv, _ := newServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/energy/vfeeg/TE100200/range", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestRangeOK(t *testing.T) {
	srv, mock := newServer(t)
	defer mock.Close()

	from := time.Date(2026, 6, 6, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	ts := from.Add(time.Hour)

	mock.ExpectQuery(`SELECT tenant_id, ec_id, metering_point, meter_code, ts, value, qov`).
		WithArgs("vfeeg", "TE100200", "AT00100", "G.01", from, to).
		WillReturnRows(mock.NewRows([]string{"tenant_id", "ec_id", "metering_point", "meter_code", "ts", "value", "qov"}).
			AddRow("vfeeg", "TE100200", "AT00100", "G.01", ts, float64(1.5), int16(0)))

	url := "/api/v1/energy/vfeeg/TE100200/range?mp=AT00100&code=G.01&from=" +
		from.Format(time.RFC3339) + "&to=" + to.Format(time.RFC3339)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v", err)
	}
	if body.Count != 1 {
		t.Fatalf("expected count=1, got %d", body.Count)
	}
}

func TestLastRecordDateEmpty(t *testing.T) {
	srv, mock := newServer(t)
	defer mock.Close()

	mock.ExpectQuery(`SELECT MAX\(ts\)`).
		WithArgs("vfeeg", "TE100200", "AT00100", "G.01").
		WillReturnRows(mock.NewRows([]string{"max"}).AddRow(nil))

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/energy/vfeeg/TE100200/last-record-date?mp=AT00100&code=G.01", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := rec.Body.String(); !contains(got, `"lastRecordDate":null`) {
		t.Fatalf("expected null lastRecordDate, got %s", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
