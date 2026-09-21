// Package server wires the application core to a JSON HTTP API and the
// embedded single-page UI.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"msgcheck/internal/app"
)

// Server holds HTTP handlers.
type Server struct {
	App *app.App
}

// New constructs a server.
func New(a *app.App) *Server { return &Server{App: a} }

// Routes returns the root mux.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.index)
	mux.HandleFunc("GET /static/app.js", s.appJS)
	mux.HandleFunc("GET /static/style.css", s.css)

	mux.HandleFunc("GET /api/state", s.getState)
	mux.HandleFunc("GET /api/uploads", s.getUploads)
	mux.HandleFunc("POST /api/import", s.handleImport)
	mux.HandleFunc("GET /api/uploads/{id}/raw", s.getRaw)
	mux.HandleFunc("GET /api/keys/{key}", s.getKey)

	mux.HandleFunc("GET /api/issues/{lang}", s.getIssues)
	mux.HandleFunc("GET /api/validations/{id}", s.replay)
	mux.HandleFunc("GET /api/renames", s.getRenames)
	mux.HandleFunc("POST /api/renames", s.postRename)
	mux.HandleFunc("GET /api/exemptions", s.getExemptions)
	mux.HandleFunc("POST /api/exemptions", s.postExempt)
	mux.HandleFunc("POST /api/exemptions/revoke", s.postRevoke)

	mux.HandleFunc("POST /api/batch-fixes", s.postBatch)
	mux.HandleFunc("GET /api/batch-fixes/{id}", s.getBatch)
	mux.HandleFunc("GET /api/operations/{id}", s.getOp)

	mux.HandleFunc("POST /api/certs", s.postCert)
	mux.HandleFunc("GET /api/certs", s.getCerts)
	mux.HandleFunc("GET /api/certs/{id}", s.getCert)
	mux.HandleFunc("GET /api/certs/{id}/download", s.downloadCert)

	mux.HandleFunc("POST /api/demo-data", s.demoData)

	return withRecover(mux)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	var ce *app.ConflictError
	if errors.As(err, &ce) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "conflict", "detail": ce.Reason, "current": ce.Current,
		})
		return
	}
	var br *app.BadRequestError
	if errors.As(err, &br) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": br.Reason})
		return
	}
	var nf *app.NotFoundError
	if errors.As(err, &nf) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found", "detail": nf.Reason})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal", "detail": err.Error()})
}

func (s *Server) getState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.App.View())
}

func (s *Server) getUploads(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.App.Uploads())
}

func (s *Server) getRenames(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"renames": s.App.Renames()})
}

func (s *Server) getExemptions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"exemptions": s.App.Exemptions()})
}

func (s *Server) getIssues(w http.ResponseWriter, r *http.Request) {
	lang := r.PathValue("lang")
	rep, err := s.App.Issues(lang)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, rep)
}

func (s *Server) replay(w http.ResponseWriter, r *http.Request) {
	rep, err := s.App.ReplayValidation(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, rep)
}

func (s *Server) getKey(w http.ResponseWriter, r *http.Request) {
	k, err := pathUnescape(r.PathValue("key"))
	if err != nil {
		writeErr(w, &app.BadRequestError{Reason: "bad key"})
		return
	}
	v, err := s.App.Key(k)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func pathUnescape(s string) (string, error) {
	// Go 1.22+ PathValue is already unescaped for standard mux; keep helper.
	return s, nil
}

func (s *Server) postRename(w http.ResponseWriter, r *http.Request) {
	var req app.RenameRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	res, err := s.App.ConfirmRename(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) postExempt(w http.ResponseWriter, r *http.Request) {
	var req app.ExemptRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	ex, pin, err := s.App.GrantExemption(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"exemption": ex, "snapshot": pin})
}

func (s *Server) postRevoke(w http.ResponseWriter, r *http.Request) {
	var req app.RevokeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	pin, err := s.App.RevokeExemption(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"snapshot": pin})
}

func decodeJSON(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		return &app.BadRequestError{Reason: "cannot read body"}
	}
	if err := json.Unmarshal(body, v); err != nil {
		return &app.BadRequestError{Reason: "invalid JSON: " + err.Error()}
	}
	return nil
}

func withRecover(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				writeJSON(w, 500, map[string]string{"error": "internal", "detail": fmt.Sprint(rec)})
			}
		}()
		h.ServeHTTP(w, r)
	})
}

var _ = mime.TypeByExtension
var _ = strings.TrimSpace
