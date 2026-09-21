package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"msgcatalog/internal/store"
)

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(indexHTML))
}

func (s *Server) getState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.st.Summary())
}

func (s *Server) importCatalog(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid multipart form: " + err.Error()})
		return
	}
	lang := r.FormValue("language")
	asBaseline := r.FormValue("baseline") == "true" || r.FormValue("baseline") == "1"
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "file field is required"})
		return
	}
	defer file.Close()
	raw, err := io.ReadAll(file)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	res, err := s.st.Import(opKey(r), store.ImportRequest{
		Language: lang, Raw: raw, AsBaseline: asBaseline,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	res.Version = nil // avoid dumping every message twice; keep meta
	_ = hdr
	writeJSON(w, 200, res)
}

func (s *Server) rawFile(w http.ResponseWriter, r *http.Request) {
	sha := r.URL.Query().Get("sha")
	if sha == "" {
		writeJSON(w, 400, map[string]string{"error": "sha is required"})
		return
	}
	data, err := s.st.RawBytes(sha)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Raw-SHA256", sha)
	w.Write(data)
}

func (s *Server) issues(w http.ResponseWriter, r *http.Request) {
	seq := querySeq(r)
	view, err := s.st.View(seq)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, view)
}

func (s *Server) keyView(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" {
		writeJSON(w, 400, map[string]string{"error": "key is required"})
		return
	}
	kv, err := s.st.KeyView(querySeq(r), key)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, kv)
}

func (s *Server) rename(w http.ResponseWriter, r *http.Request) {
	var req store.RenameRequest
	if err := decode(r, &req); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	res, err := s.st.ConfirmRename(opKey(r), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) exemption(w http.ResponseWriter, r *http.Request) {
	var req store.ExemptionRequest
	if err := decode(r, &req); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	res, err := s.st.SetExemption(opKey(r), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) batch(w http.ResponseWriter, r *http.Request) {
	var req store.BatchRequest
	if err := decode(r, &req); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	res, err := s.st.ApplyBatch(opKey(r), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	// New version message bodies are large and already available via
	// /api/key; keep the receipt focused on per-language outcomes.
	if res.Batch != nil {
		res.Batch.NewVersions = nil
	}
	writeJSON(w, 200, res)
}

func (s *Server) proof(w http.ResponseWriter, r *http.Request) {
	var req store.ProofRequest
	if err := decode(r, &req); err != nil {
		// body may be empty
		req = store.ProofRequest{}
	}
	out, err := s.st.GenerateProof(opKey(r), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"record":            out.Record,
		"bytes":             out.Bytes,
		"idempotent_replay": out.Idempotent,
	})
}

func (s *Server) getProof(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	data, err := s.st.ProofBytes(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	var doc map[string]any
	_ = json.Unmarshal(data, &doc)
	writeJSON(w, 200, doc)
}

func (s *Server) proofFile(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	data, err := s.st.ProofBytes(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="release-proof.json"`)
	w.Write(data)
}

func (s *Server) runs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"runs": s.st.Runs()})
}

func (s *Server) snapshots(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"seqs": s.st.SnapshotSeqList()})
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	defer r.Body.Close()
	return dec.Decode(v)
}

func querySeq(r *http.Request) int64 {
	q := r.URL.Query().Get("seq")
	if q == "" {
		return 0
	}
	n, _ := strconv.ParseInt(q, 10, 64)
	return n
}
