package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"msgcheck/internal/app"
)

func formVal(r *http.Request, key string) string {
	return strings.TrimSpace(r.FormValue(key))
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeErr(w, &app.BadRequestError{Reason: "expected multipart form with file"})
		return
	}
	f, fh, err := r.FormFile("file")
	if err != nil {
		writeErr(w, &app.BadRequestError{Reason: "missing file field"})
		return
	}
	defer f.Close()
	raw, err := readAllLimit(f, 64<<20)
	if err != nil {
		writeErr(w, err)
		return
	}
	role := formVal(r, "role")
	language := formVal(r, "language")
	filename := fh.Filename
	res, err := s.App.Import(role, language, filename, raw, formVal(r, "expected_snapshot"), formVal(r, "op_id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, res)
}

func readAllLimit(f interface{ Read([]byte) (int, error) }, limit int64) ([]byte, error) {
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	var total int64
	for {
		n, err := f.Read(tmp)
		if n > 0 {
			total += int64(n)
			if total > limit {
				return nil, &app.BadRequestError{Reason: "file too large"}
			}
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			if err == io.EOF {
				return buf, nil
			}
			return buf, err
		}
	}
}

func (s *Server) getRaw(w http.ResponseWriter, r *http.Request) {
	b, filename, lang, err := s.App.RawUpload(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if filename == "" {
		filename = lang + ".json"
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", contentDisposition(filename))
	w.WriteHeader(200)
	_, _ = w.Write(b)
}

func contentDisposition(filename string) string {
	return "attachment; filename=\"" + strconv.Quote(filename) + "\""
}

type batchItemJSON struct {
	Language    string `json:"language"`
	Filename    string `json:"filename,omitempty"`
	BaseVersion string `json:"base_version"`
	Content     string `json:"content"`
}

type batchReqJSON struct {
	Items            []batchItemJSON `json:"items"`
	ExpectedSnapshot string          `json:"expected_snapshot,omitempty"`
	OpID             string          `json:"op_id,omitempty"`
}

func (s *Server) postBatch(w http.ResponseWriter, r *http.Request) {
	var in batchReqJSON
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	req := app.BatchRequest{ExpectedSnapshot: in.ExpectedSnapshot, OpID: in.OpID}
	for _, it := range in.Items {
		req.Items = append(req.Items, app.BatchItemRequest{
			Language: it.Language, Filename: it.Filename,
			BaseVersion: it.BaseVersion, Content: []byte(it.Content),
		})
	}
	resp, err := s.App.BatchFix(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, resp)
}

func (s *Server) getBatch(w http.ResponseWriter, r *http.Request) {
	b, ok := s.App.Batch(r.PathValue("id"))
	if !ok {
		writeErr(w, &app.NotFoundError{Reason: "unknown batch operation"})
		return
	}
	writeJSON(w, 200, b)
}

func (s *Server) getOp(w http.ResponseWriter, r *http.Request) {
	op, ok := s.App.Operation(r.PathValue("id"))
	if !ok {
		writeErr(w, &app.NotFoundError{Reason: "unknown operation"})
		return
	}
	writeJSON(w, 200, op)
}

func (s *Server) postCert(w http.ResponseWriter, r *http.Request) {
	res, err := s.App.GenerateCert()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"cert":     res.Cert,
		"replayed": res.Replayed,
		"snapshot": res.Snapshot,
		"bytes":    json.RawMessage(res.Bytes),
	})
}

func (s *Server) getCerts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"certs": s.App.Certs()})
}

func (s *Server) getCert(w http.ResponseWriter, r *http.Request) {
	b, meta, err := s.App.CertBytes(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	cur := s.App.Pin().Snapshot
	writeJSON(w, 200, map[string]any{
		"current": meta.Snapshot == cur,
		"snapshot": meta.Snapshot,
		"created_at": meta.CreatedAt.Format(time.RFC3339),
		"cert":    json.RawMessage(b),
	})
}

func (s *Server) downloadCert(w http.ResponseWriter, r *http.Request) {
	b, _, err := s.App.CertBytes(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="release-certificate.json"`)
	w.WriteHeader(200)
	_, _ = w.Write(b)
}
