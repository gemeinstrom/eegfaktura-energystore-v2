package graphqlapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/auth"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/calc"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/counterpoint"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/queryengine"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/store"
)

func newGqlEngine(t *testing.T) (*Engine, pgxmock.PgxPoolIface) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	st := store.FromPool(mock)
	repo := counterpoint.NewRepository(mock)
	qe := queryengine.New(mock, repo)
	cEng := calc.New(mock, repo)
	gql, err := New(st, cEng, qe, repo, Options{})
	if err != nil {
		t.Fatalf("graphql: %v", err)
	}
	return gql, mock
}

func TestGraphQL_RejectsGET(t *testing.T) {
	gql, _ := newGqlEngine(t)
	req := httptest.NewRequest(http.MethodGet, "/query", nil)
	rec := httptest.NewRecorder()
	gql.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestGraphQL_LastEnergyDate_NoMeta(t *testing.T) {
	gql, mock := newGqlEngine(t)
	defer mock.Close()
	mock.ExpectQuery(`FROM counterpoint_meta`).
		WithArgs("vfeeg", "TE100200").
		WillReturnRows(mock.NewRows([]string{
			"tenant_id", "ec_id", "metering_point", "direction", "source_idx",
			"period_start", "period_end", "payload", "updated_at",
		}))
	q := `{"query":"query Q($tenant: String!, $ecId: String!) { lastEnergyDate(tenant: $tenant, ecId: $ecId) }","variables":{"tenant":"vfeeg","ecId":"TE100200"}}`
	req := httptest.NewRequest(http.MethodPost, "/query", strings.NewReader(q))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	gql.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			LastEnergyDate string `json:"lastEnergyDate"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.LastEnergyDate != "" {
		t.Fatalf("expected empty date, got %q", resp.Data.LastEnergyDate)
	}
}

func TestGraphQL_LastEnergyDate_WithMeta(t *testing.T) {
	gql, mock := newGqlEngine(t)
	defer mock.Close()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 12, 31, 23, 45, 0, 0, time.UTC)
	mock.ExpectQuery(`FROM counterpoint_meta`).
		WithArgs("vfeeg", "TE100200").
		WillReturnRows(mock.NewRows([]string{
			"tenant_id", "ec_id", "metering_point", "direction", "source_idx",
			"period_start", "period_end", "payload", "updated_at",
		}).AddRow("vfeeg", "TE100200", "AT_CON",
			int16(counterpoint.DirectionConsumer), 0,
			&start, &end, []byte(`{}`), time.Now()))
	q := `{"query":"{ lastEnergyDate(tenant: \"vfeeg\", ecId: \"TE100200\") }"}`
	req := httptest.NewRequest(http.MethodPost, "/query", strings.NewReader(q))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	gql.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"lastEnergyDate"`) {
		t.Fatalf("expected lastEnergyDate field, got %s", rec.Body.String())
	}
}

func TestGraphQL_TenantArgMismatchRejected(t *testing.T) {
	// A verified tenant on the context (set by auth.GQL) must win over
	// the client-supplied tenant arg: naming a foreign tenant is an error,
	// and no DB query may run.
	gql, mock := newGqlEngine(t)
	defer mock.Close()
	q := `{"query":"{ lastEnergyDate(tenant: \"OTHER\", ecId: \"TE100200\") }"}`
	req := httptest.NewRequest(http.MethodPost, "/query", strings.NewReader(q))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithTenant(req.Context(), "VFEEG"))
	rec := httptest.NewRecorder()
	gql.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (GraphQL error in payload), got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "tenant mismatch") {
		t.Fatalf("expected tenant mismatch error, got %s", rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("no DB query expected: %v", err)
	}
}

func TestGraphQL_VerifiedTenantOverridesArg(t *testing.T) {
	// With a verified tenant on the context, the resolver must query with
	// the verified value (uppercased by the middleware), not the raw arg.
	gql, mock := newGqlEngine(t)
	defer mock.Close()
	mock.ExpectQuery(`FROM counterpoint_meta`).
		WithArgs("VFEEG", "TE100200").
		WillReturnRows(mock.NewRows([]string{
			"tenant_id", "ec_id", "metering_point", "direction", "source_idx",
			"period_start", "period_end", "payload", "updated_at",
		}))
	q := `{"query":"{ lastEnergyDate(tenant: \"vfeeg\", ecId: \"TE100200\") }"}`
	req := httptest.NewRequest(http.MethodPost, "/query", strings.NewReader(q))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithTenant(req.Context(), "VFEEG"))
	rec := httptest.NewRecorder()
	gql.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "tenant mismatch") {
		t.Fatalf("unexpected mismatch error: %s", rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestGraphQL_MultipartUpload(t *testing.T) {
	gql, _ := newGqlEngine(t)
	// Build a multipart body: operations + map + file `0`.
	body := &bytes.Buffer{}
	w := newWriter(body)
	_ = w.writeField("operations",
		`{"query":"mutation($file: Upload!) { singleUpload(tenant: \"vfeeg\", ecId: \"TE100200\", sheet: \"S\", file: $file) }","variables":{"file":null}}`)
	_ = w.writeField("map", `{"0":["variables.file"]}`)
	_ = w.writeFile("0", "test.xlsx", []byte("not really an xlsx"))
	_ = w.Close()

	req := httptest.NewRequest(http.MethodPost, "/query", body)
	req.Header.Set("Content-Type", w.contentType())
	rec := httptest.NewRecorder()
	gql.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (errors in payload), got %d body=%s", rec.Code, rec.Body.String())
	}
	// Body should be a GraphQL response — singleUpload will fail
	// because the bytes aren't a real xlsx, but the multipart parsing
	// itself must succeed.
	if !strings.Contains(rec.Body.String(), `"data"`) && !strings.Contains(rec.Body.String(), `"errors"`) {
		t.Fatalf("expected GraphQL JSON shape, got %s", rec.Body.String())
	}
}

// minimal multipart helpers
type writer struct {
	buf      *bytes.Buffer
	boundary string
}

func newWriter(buf *bytes.Buffer) *writer { return &writer{buf: buf, boundary: "boundary123"} }

func (w *writer) contentType() string {
	return "multipart/form-data; boundary=" + w.boundary
}

func (w *writer) writeField(name, value string) error {
	w.buf.WriteString("--" + w.boundary + "\r\n")
	w.buf.WriteString("Content-Disposition: form-data; name=\"" + name + "\"\r\n\r\n")
	w.buf.WriteString(value)
	w.buf.WriteString("\r\n")
	return nil
}

func (w *writer) writeFile(name, filename string, data []byte) error {
	w.buf.WriteString("--" + w.boundary + "\r\n")
	w.buf.WriteString("Content-Disposition: form-data; name=\"" + name + "\"; filename=\"" + filename + "\"\r\n")
	w.buf.WriteString("Content-Type: application/octet-stream\r\n\r\n")
	w.buf.Write(data)
	w.buf.WriteString("\r\n")
	return nil
}

func (w *writer) Close() error {
	w.buf.WriteString("--" + w.boundary + "--\r\n")
	return nil
}

// silence unused-import on context.Context in some toolchains.
var _ = context.Background
