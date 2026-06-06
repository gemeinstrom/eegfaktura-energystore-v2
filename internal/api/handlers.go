// Package api wires the HTTP/REST endpoints for energystore-v2.
//
// Scope of this iteration:
//   - /healthz, /readyz                                    process probes
//   - GET /api/v1/energy/{tenant}/{ec}/range               raw slot range
//   - GET /api/v1/energy/{tenant}/{ec}/last-record-date    max(ts) per MP+code
//
// Aggregation / chart / billing endpoints from v1 (combined-report,
// load-curve, intra-day, etc.) are deliberately deferred — they consume
// energy_data and belong to a later iteration once UpsertSlots is producing
// real data.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/store"
)

type Server struct {
	store *store.Store
	mux   *http.ServeMux
}

func New(s *store.Store) *Server {
	srv := &Server{store: s, mux: http.NewServeMux()}
	srv.routes()
	return srv
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.mux.HandleFunc("GET /readyz", s.handleReady)
	s.mux.HandleFunc("GET /api/v1/energy/{tenant}/{ec}/range", s.handleRange)
	s.mux.HandleFunc("GET /api/v1/energy/{tenant}/{ec}/last-record-date", s.handleLastRecordDate)
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
