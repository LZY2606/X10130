// Package web exposes the service over a small JSON HTTP API and a single
// page application with no external assets.
package web

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"messagecatalog/internal/service"
)

// Server bundles dependencies for handlers.
type Server struct {
	svc *service.Service
	mux *http.ServeMux
}

// New wires routes.
func New(svc *service.Service) *Server {
	s := &Server{svc: svc, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("/", s.index)
	s.mux.HandleFunc("/api/state", s.handleState)
	s.mux.HandleFunc("/api/import", s.handleImport)
	s.mux.HandleFunc("/api/rename/confirm", s.handleRename)
	s.mux.HandleFunc("/api/issues", s.handleIssues)
	s.mux.HandleFunc("/api/compare", s.handleCompare)
	s.mux.HandleFunc("/api/exemption", s.handleExemption)
	s.mux.HandleFunc("/api/batch", s.handleBatch)
	s.mux.HandleFunc("/api/proof", s.handleProof)
	s.mux.HandleFunc("/api/replay", s.handleReplay)
	s.mux.HandleFunc("/api/tuples", s.handleTuples)
	s.mux.HandleFunc("/api/raw", s.handleRaw)
	s.mux.HandleFunc("/api/op", s.handleOp)
	s.mux.HandleFunc("/api/clock", s.handleClock)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	var ce *service.ConflictError
	var re *service.RequestError
	switch {
	case errors.As(err, &ce):
		writeJSON(w, http.StatusConflict, map[string]string{"error": ce.Error(), "type": "conflict"})
	case errors.As(err, &re):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": re.Error(), "type": "request"})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error(), "type": "server"})
	}
}

func opID(r *http.Request, body map[string]json.RawMessage) string {
	if v := r.Header.Get("X-Op-Id"); v != "" {
		return v
	}
	if raw, ok := body["opId"]; ok {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	}
	return ""
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(indexHTML))
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.svc.View())
}

type importReq struct {
	Language string `json:"language"`
	Content  string `json:"content"` // base64 raw file
	Filename string `json:"filename"`
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "post only", 405)
		return
	}
	var body map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	var req importReq
	rawBody, _ := json.Marshal(body)
	_ = json.Unmarshal(rawBody, &req)
	raw, err := base64.StdEncoding.DecodeString(req.Content)
	if err != nil {
		// also accept plain JSON text
		raw = []byte(req.Content)
	}
	res, err := s.svc.ImportCatalog(opID(r, body), req.Language, raw)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, res)
}

type renameReq struct {
	BaseVersion string            `json:"baseVersion"`
	Edges       map[string]string `json:"edges"`
}

func (s *Server) handleRename(w http.ResponseWriter, r *http.Request) {
	var body map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	var req renameReq
	rb, _ := json.Marshal(body)
	_ = json.Unmarshal(rb, &req)
	res, err := s.svc.ConfirmRename(opID(r, body), req.BaseVersion, req.Edges)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) handleIssues(w http.ResponseWriter, r *http.Request) {
	lang := r.URL.Query().Get("lang")
	res, err := s.svc.ReportFor(lang)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) handleCompare(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	res, err := s.svc.SideBySideKey(key)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) handleExemption(w http.ResponseWriter, r *http.Request) {
	var body map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	var req service.ExemptionRequest
	rb, _ := json.Marshal(body)
	_ = json.Unmarshal(rb, &req)
	res, err := s.svc.SetExemption(opID(r, body), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, res)
}

type batchReq struct {
	Items []struct {
		Language    string `json:"language"`
		BaseVersion string `json:"baseVersion"`
		Content     string `json:"content"`
	} `json:"items"`
}

func (s *Server) handleBatch(w http.ResponseWriter, r *http.Request) {
	var body map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	var req batchReq
	rb, _ := json.Marshal(body)
	_ = json.Unmarshal(rb, &req)
	svcReq := service.BatchFixRequest{}
	for _, it := range req.Items {
		raw, derr := base64.StdEncoding.DecodeString(it.Content)
		if derr != nil {
			raw = []byte(it.Content)
		}
		svcReq.Items = append(svcReq.Items, service.BatchFixItem{
			Language: it.Language, BaseVersion: it.BaseVersion, Content: raw,
		})
	}
	res, err := s.svc.BatchFix(opID(r, body), svcReq)
	if err != nil {
		writeErr(w, err)
		return
	}
	code := 200
	if len(res.Conflicted) > 0 {
		code = 207 // multi-status: partial success
	}
	writeJSON(w, code, res)
}

func (s *Server) handleProof(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		res, b, err := s.svc.LatestProof()
		if err != nil {
			writeErr(w, err)
			return
		}
		if dl := r.URL.Query().Get("download"); dl == "1" {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Disposition", `attachment; filename="release-proof.json"`)
			_, _ = w.Write(b)
			return
		}
		writeJSON(w, 200, map[string]any{"meta": res, "proof": json.RawMessage(b)})
		return
	}
	var body map[string]json.RawMessage
	_ = json.NewDecoder(r.Body).Decode(&body)
	res, b, err := s.svc.GenerateProof()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"meta": res, "proof": json.RawMessage(b)})
}

func (s *Server) handleReplay(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("tuple")
	res, err := s.svc.ReplayTuple(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) handleTuples(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.svc.StoredTuples())
}

func (s *Server) handleRaw(w http.ResponseWriter, r *http.Request) {
	lang := r.URL.Query().Get("lang")
	b, up, err := s.svc.RawUploadForLang(lang)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition",
		`attachment; filename="`+lang+`-upload-`+up.ID+`.json"`)
	_, _ = w.Write(b)
}

func (s *Server) handleOp(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	op, ok := s.svc.OperationStatus(id)
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "unknown operation id"})
		return
	}
	writeJSON(w, 200, op)
}

func (s *Server) handleClock(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, 200, map[string]string{"now": s.svc.Now().Format(time.RFC3339)})
		return
	}
	var req struct {
		Now string `json:"now"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if strings.TrimSpace(req.Now) == "" {
		s.svc.SetRealClock()
		writeJSON(w, 200, map[string]string{"now": s.svc.Now().Format(time.RFC3339), "mode": "real"})
		return
	}
	t, err := time.Parse(time.RFC3339, req.Now)
	if err != nil {
		writeErr(w, &service.RequestError{Msg: "time must be RFC3339"})
		return
	}
	s.svc.SetFixedTime(t.UTC())
	writeJSON(w, 200, map[string]string{"now": s.svc.Now().Format(time.RFC3339), "mode": "fixed"})
}
