// Package server exposes the catalog workbench over HTTP using only the
// standard library.
package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"msgcatalog/internal/store"
)

type Server struct {
	st    *store.Store
	now   func() time.Time
}

func New(st *store.Store, now func() time.Time) *Server {
	if now == nil {
		now = time.Now
	}
	return &Server{st: st, now: now}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.index)
	mux.HandleFunc("GET /api/state", s.getState)
	mux.HandleFunc("POST /api/import", s.importCatalog)
	mux.HandleFunc("GET /api/raw", s.rawFile)
	mux.HandleFunc("GET /api/issues", s.issues)
	mux.HandleFunc("GET /api/key", s.keyView)
	mux.HandleFunc("POST /api/rename", s.rename)
	mux.HandleFunc("POST /api/exemption", s.exemption)
	mux.HandleFunc("POST /api/batch", s.batch)
	mux.HandleFunc("POST /api/proof", s.proof)
	mux.HandleFunc("GET /api/proof", s.getProof)
	mux.HandleFunc("GET /api/proof/file", s.proofFile)
	mux.HandleFunc("GET /api/runs", s.runs)
	mux.HandleFunc("GET /api/snapshots", s.snapshots)
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrIdemConflict):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, store.ErrBadRequest):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, store.ErrReadOnly):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
}

func opKey(r *http.Request) string {
	if k := r.Header.Get("Idempotency-Key"); k != "" {
		return k
	}
	return r.URL.Query().Get("op")
}
