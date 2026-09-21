// Package server exposes the catalog workbench over HTTP with local storage.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"catalogcheck/internal/store"
)

// Server wires the store to HTTP handlers.
type Server struct {
	Store *store.Store
}

// New constructs a server.
func New(st *store.Store) *Server { return &Server{Store: st} }

// Mux builds the HTTP route mux.
func (s *Server) Mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.index)
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/import", s.handleImport)
	mux.HandleFunc("/api/raw/", s.handleRaw)
	mux.HandleFunc("/api/issues", s.handleIssues)
	mux.HandleFunc("/api/results/", s.handleResult)
	mux.HandleFunc("/api/rename", s.handleRename)
	mux.HandleFunc("/api/exemption", s.handleExemption)
	mux.HandleFunc("/api/batch", s.handleBatch)
	mux.HandleFunc("/api/certs", s.handleCerts)
	mux.HandleFunc("/api/certs/", s.handleCert)
	return mux
}

func (s *Server) now(r *http.Request) time.Time {
	if v := r.Header.Get("X-Debug-Now"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t
		}
	}
	return time.Now()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	kind := "error"
	switch {
	case errors.Is(err, store.ErrConflict):
		status = http.StatusConflict
		kind = "conflict"
	case errors.Is(err, store.ErrIdemMismatch):
		status = http.StatusUnprocessableEntity
		kind = "idempotency_conflict"
	}
	writeJSON(w, status, map[string]string{"error": err.Error(), "kind": kind})
}

// replay writes a stored idempotent response.
func replay(w http.ResponseWriter, b []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Idempotent-Replay", "true")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, indexHTML)
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.Store.Overview(s.now(r)))
}

type importForm struct {
	role, language, filename, opID, snap string
	content                              []byte
}

func parseImport(r *http.Request) (importForm, error) {
	var f importForm
	if err := r.ParseMultipartForm(32 << 20); err == nil {
		get := func(k string) string {
			if vs := r.MultipartForm.Value[k]; len(vs) > 0 {
				return vs[0]
			}
			return ""
		}
		f.role = get("role")
		f.language = get("language")
		f.filename = get("filename")
		f.opID = get("operation_id")
		f.snap = get("snapshot_id")
		file, fh, err := r.FormFile("file")
		if err == nil {
			defer file.Close()
			b, err := io.ReadAll(file)
			if err != nil {
				return f, err
			}
			f.content = b
			if f.filename == "" {
				f.filename = fh.Filename
			}
		}
		return f, nil
	}
	var body struct {
		Role        string `json:"role"`
		Language    string `json:"language"`
		Filename    string `json:"filename"`
		OperationID string `json:"operation_id"`
		SnapshotID  string `json:"snapshot_id"`
		Content     []byte `json:"content"`
		ContentJSON string `json:"content_json"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return f, err
	}
	f.role, f.language, f.filename = body.Role, body.Language, body.Filename
	f.opID, f.snap, f.content = body.OperationID, body.SnapshotID, body.Content
	if len(f.content) == 0 && body.ContentJSON != "" {
		f.content = []byte(body.ContentJSON)
	}
	return f, nil
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	f, err := parseImport(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if f.filename == "" {
		f.filename = f.language + ".json"
	}
	out, rep, err := s.Store.Import(store.ImportRequest{
		Role: f.role, Language: f.language, Filename: f.filename,
		Content: f.content, OperationID: f.opID, SnapshotID: f.snap,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	if rep != nil {
		replay(w, rep)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleRaw(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Path[len("/api/raw/"):]
	b, up, err := s.Store.RawUpload(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", up.Filename))
	w.Header().Set("X-Upload-SHA256", up.SHA256)
	_, _ = w.Write(b)
}

func (s *Server) handleIssues(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v, err := s.Store.Issues(q.Get("language"), q.Get("result_id"), q.Get("snapshot_id"), s.now(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) handleResult(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Path[len("/api/results/"):]
	data, meta, stale, reason, err := s.Store.Result(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"snapshot": s.Store.Snapshot(),
		"result":   data,
		"meta":     meta,
		"stale":    stale,
		"reason":   reason,
		"now":      s.now(r).UTC(),
	})
}

func (s *Server) handleRename(w http.ResponseWriter, r *http.Request) {
	var req store.RenameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, err)
		return
	}
	out, rep, err := s.Store.ConfirmRename(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	if rep != nil {
		replay(w, rep)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleExemption(w http.ResponseWriter, r *http.Request) {
	var req store.ExemptionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, err)
		return
	}
	out, rep, err := s.Store.SetExemption(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	if rep != nil {
		replay(w, rep)
		return
	}
	writeJSON(w, 200, out)
}

func decodeBatch(r *http.Request) (store.BatchRequest, error) {
	var req struct {
		OperationID     string `json:"operation_id"`
		SnapshotID      string `json:"snapshot_id"`
		ExpectedBaseVer string `json:"expected_base_version_id"`
		Items           []struct {
			Language              string `json:"language"`
			Filename              string `json:"filename"`
			ContentB64            string `json:"content_b64"`
			ContentJSON           string `json:"content_json"`
			ExpectedTargetVersion string `json:"expected_target_version_id"`
		} `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return store.BatchRequest{}, err
	}
	out := store.BatchRequest{
		OperationID: req.OperationID, SnapshotID: req.SnapshotID,
		ExpectedBaseVer: req.ExpectedBaseVer,
	}
	for _, it := range req.Items {
		content := []byte(it.ContentJSON)
		if it.ContentB64 != "" {
			b, err := decodeB64(it.ContentB64)
			if err != nil {
				return out, err
			}
			content = b
		}
		fname := it.Filename
		if fname == "" {
			fname = it.Language + ".json"
		}
		out.Items = append(out.Items, store.BatchLanguageItem{
			Language: it.Language, Filename: fname, Content: content,
			ExpectedTargetVersion: it.ExpectedTargetVersion,
		})
	}
	return out, nil
}

func (s *Server) handleBatch(w http.ResponseWriter, r *http.Request) {
	req, err := decodeBatch(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, rep, err := s.Store.ApplyBatch(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	if rep != nil {
		replay(w, rep)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleCerts(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, 200, map[string]any{
			"snapshot": s.Store.Snapshot(),
			"certs":    s.Store.CertMetaList(s.now(r)),
			"basis":    s.Store.CertBasis(s.now(r)),
		})
		return
	}
	var body struct {
		SnapshotID string `json:"snapshot_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	cert, raw, meta, current, err := s.Store.GenerateCert(body.SnapshotID, s.now(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"cert": cert, "bytes": string(raw), "meta": meta, "current": current,
	})
}

func (s *Server) handleCert(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Path[len("/api/certs/"):]
	b, meta, current, err := s.Store.CertBytes(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	_ = meta
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s.json", id))
		_, _ = w.Write(b)
		return
	}
	var c map[string]any
	_ = json.Unmarshal(b, &c)
	writeJSON(w, 200, map[string]any{"cert": c, "meta": meta, "current": current})
}

var _ = strconv.Itoa
