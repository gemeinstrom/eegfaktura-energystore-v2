// Package api wires the HTTP/REST endpoints for energystore-v2.
package api

import (
	"encoding/json"
	"net/http"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/store"
)

// Server exposes the HTTP handlers.
type Server struct {
	store *store.Store
	mux   *http.ServeMux
}

// New constructs the Server and registers routes.
func New(s *store.Store) *Server {
	srv := &Server{store: s, mux: http.NewServeMux()}
	srv.routes()
	return srv
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.mux.HandleFunc("GET /readyz", s.handleReady)
	// TODO: parity endpoints for v1's /api/v1/... surface.
}

// Handler returns the configured *http.ServeMux for embedding into an
// *http.Server.
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReady(w http.ResponseWriter, _ *http.Request) {
	// TODO: ping DB pool, check MQTT connection.
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
