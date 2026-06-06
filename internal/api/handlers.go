// Package api wires the HTTP/REST endpoints for energystore-v2.
//
// Scope:
//   - /healthz, /readyz, /metrics — process probes + Prometheus
//   - /api/v1/energy/{tenant}/{ec}/range, .../last-record-date
//     thin v2-native endpoints (slot range, last record date)
//   - /eeg/* and /eeg/v2/{ecid}/* — v1-parity REST surface (Workstream G)
//   - /query/* — Basic-Auth-protected query API (v1 parity)
//
// Excel endpoints (POST /eeg/{ecid}/excel/...) return 501 until
// Workstream H lands; the route is in place so external configs don't
// have to change.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/auth"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/calc"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/metrics"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/queryengine"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/store"
)

// HealthProvider exposes runtime readiness signals beyond the DB pool.
type HealthProvider interface {
	Connected() bool
}

// Options groups the optional dependencies; nil-safe defaults apply.
type Options struct {
	Logger      *slog.Logger
	Metrics     *metrics.Metrics
	MQTT        HealthProvider
	Auth        *auth.Middleware
	QueryEngine *queryengine.Engine
	Calc        *calc.Engine
}

type Server struct {
	store   *store.Store
	mux     *http.ServeMux
	logger  *slog.Logger
	metrics *metrics.Metrics
	mqtt    HealthProvider
	auth    *auth.Middleware
	qe      *queryengine.Engine
	calc    *calc.Engine
}

// New keeps the bare-minimum constructor for tests that don't need
// observability or the calc/queryengine stack.
func New(s *store.Store) *Server {
	return NewWithOptions(s, Options{})
}

// NewWithOptions wires everything into the server.
func NewWithOptions(s *store.Store, opts Options) *Server {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	srv := &Server{
		store:   s,
		mux:     http.NewServeMux(),
		logger:  logger.With("component", "api"),
		metrics: opts.Metrics,
		mqtt:    opts.MQTT,
		auth:    opts.Auth,
		qe:      opts.QueryEngine,
		calc:    opts.Calc,
	}
	srv.routes()
	return srv
}

func (s *Server) routes() {
	s.handle("GET /healthz", s.handleHealth)
	s.handle("GET /readyz", s.handleReady)
	s.handle("GET /api/v1/energy/{tenant}/{ec}/range", s.handleRange)
	s.handle("GET /api/v1/energy/{tenant}/{ec}/last-record-date", s.handleLastRecordDate)
	if s.metrics != nil {
		s.mux.Handle("GET /metrics", s.metrics.Handler())
	}
	s.eegRoutes()
}

func (s *Server) handle(pattern string, h http.HandlerFunc) {
	if s.metrics != nil {
		s.mux.Handle(pattern, s.metrics.Instrument(pattern, h))
		return
	}
	s.mux.HandleFunc(pattern, h)
}

// protect wraps a v1-style JWT-aware handler with auth middleware when
// auth is enabled. When auth is nil (dev / tests), the inner handler
// runs with the tenant taken from the X-Tenant header verbatim and a
// nil claims pointer.
func (s *Server) protect(h auth.HandlerFunc) http.HandlerFunc {
	if s.auth != nil {
		return s.auth.ProtectApp(h)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		h(w, r, nil, r.Header.Get("X-Tenant"))
	}
}

// protectAPI is the same idea for Basic-Auth endpoints.
func (s *Server) protectAPI(h auth.HandlerFunc) http.HandlerFunc {
	if s.auth != nil {
		return s.auth.ProtectAPI(h)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		h(w, r, nil, r.Header.Get("X-Tenant"))
	}
}

func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "db-unreachable"})
		return
	}
	if s.mqtt != nil && !s.mqtt.Connected() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "mqtt-disconnected"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) handleRange(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")
	ec := r.PathValue("ec")
	mp := r.URL.Query().Get("mp")
	code := r.URL.Query().Get("code")
	if mp == "" || code == "" {
		writeError(w, http.StatusBadRequest, "missing required query params: mp, code")
		return
	}
	from, to, err := parseRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	slots, err := s.store.QueryRange(r.Context(), tenant, ec, mp, code, from, to)
	if err != nil {
		s.logger.Error("range query", "err", err, "tenant", tenant, "ec", ec)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"slots": slots, "count": len(slots)})
}

func (s *Server) handleLastRecordDate(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")
	ec := r.PathValue("ec")
	mp := r.URL.Query().Get("mp")
	code := r.URL.Query().Get("code")
	if mp == "" || code == "" {
		writeError(w, http.StatusBadRequest, "missing required query params: mp, code")
		return
	}
	ts, ok, err := s.store.LastRecordDate(r.Context(), tenant, ec, mp, code)
	if err != nil {
		s.logger.Error("last record date", "err", err, "tenant", tenant, "ec", ec)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"lastRecordDate": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lastRecordDate": ts.UTC().Format(time.RFC3339)})
}

func parseRange(r *http.Request) (time.Time, time.Time, error) {
	fromStr := r.URL.Query().Get("from")
	toStr := r.URL.Query().Get("to")
	if fromStr == "" || toStr == "" {
		return time.Time{}, time.Time{}, errors.New("missing required query params: from, to (RFC3339)")
	}
	from, err := time.Parse(time.RFC3339, fromStr)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid from: %w", err)
	}
	to, err := time.Parse(time.RFC3339, toStr)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid to: %w", err)
	}
	return from, to, nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
