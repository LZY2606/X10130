package service

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

//go:generate echo assets are embedded from web

// Handlers builds the HTTP mux.
func (s *Service) Handlers(assets fs.FS) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{
			"ok": true, "parserVersion": ParserVersionName(), "now": s.now(),
			"degraded": s.Store.Degraded(), "readOnly": s.Store.ReadOnly(),
			"recoveryEvents": s.Store.RecoveryEvents(), "seq": s.Store.Seq(),
		})
	})

	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		snap := s.Snapshot()
		writeJSON(w, 200, map[string]any{
			"seq": snap.Seq, "snapshotStamp": snap.Stamp,
			"parserVersion":  snap.ParserVersion,
			"languages":      s.Languages(),
			"keys":           s.Keys(),
			"recoveryEvents": s.Store.RecoveryEvents(),
			"degraded":       s.Store.Degraded(),
			"readOnly":       s.Store.ReadOnly(),
			"now":            s.now(),
		})
	})

	mux.HandleFunc("POST /api/import", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(16 << 20); err != nil {
			writeErr(w, fmt.Errorf("invalid multipart form: %w", err))
			return
		}
		lang := r.FormValue("language")
		role := r.FormValue("role")
		file, header, ferr := r.FormFile("file")
		if ferr != nil {
			writeErr(w, fmt.Errorf("file is required: %w", ferr))
			return
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, 16<<20))
		if err != nil {
			writeErr(w, err)
			return
		}
		filename := header.Filename
		res, replay, err := s.Import(opID(r), ImportRequest{
			Language: lang, Role: role, Filename: filename, Content: data,
		})
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"result": res, "replayed": replay})
	})

	// JSON import alternative (used by tests/demo seed).
	mux.HandleFunc("POST /api/import-json", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Language string            `json:"language"`
			Role     string            `json:"role"`
			Filename string            `json:"filename"`
			Messages map[string]string `json:"messages"`
		}
		if err := decode(r, &body); err != nil {
			writeErr(w, err)
			return
		}
		data, err := json.Marshal(body.Messages)
		if err != nil {
			writeErr(w, err)
			return
		}
		fn := body.Filename
		if fn == "" {
			fn = body.Language + ".json"
		}
		res, replay, err := s.Import(opID(r), ImportRequest{
			Language: body.Language, Role: body.Role, Filename: fn, Content: data,
		})
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"result": res, "replayed": replay})
	})

	mux.HandleFunc("POST /api/demo/seed", func(w http.ResponseWriter, r *http.Request) {
		d, err := s.SeedDemo(opID(r))
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"seeded": true, "data": d})
	})

	mux.HandleFunc("GET /api/uploads", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"uploads": s.Uploads()})
	})

	mux.HandleFunc("GET /api/uploads/{id}/raw", func(w http.ResponseWriter, r *http.Request) {
		data, filename, err := s.RawUpload(r.PathValue("id"))
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
		_, _ = w.Write(data)
	})

	mux.HandleFunc("GET /api/versions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"versions": s.Store.SortedVersions()})
	})

	mux.HandleFunc("GET /api/compare", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		if key == "" {
			writeErr(w, fmt.Errorf("key is required"))
			return
		}
		v, err := s.CompareKey(key)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, v)
	})

	mux.HandleFunc("GET /api/rename-candidates", func(w http.ResponseWriter, r *http.Request) {
		cs, err := s.RenameCandidates()
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"candidates": cs, "snapshotStamp": s.Snapshot().Stamp})
	})

	mux.HandleFunc("POST /api/renames/confirm", func(w http.ResponseWriter, r *http.Request) {
		var req ConfirmRenameRequest
		if err := decode(r, &req); err != nil {
			writeErr(w, err)
			return
		}
		res, replay, err := s.ConfirmRename(opID(r), req)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"result": res, "replayed": replay})
	})

	mux.HandleFunc("GET /api/validations", func(w http.ResponseWriter, r *http.Request) {
		vs, err := s.CurrentValidations()
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"validations": vs, "snapshotStamp": s.Snapshot().Stamp, "seq": s.Snapshot().Seq})
	})

	mux.HandleFunc("GET /api/validations/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Replay(r.PathValue("id"))
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, v)
	})

	mux.HandleFunc("POST /api/exemptions", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ValidationID     string `json:"validationId"`
			IssueKey         string `json:"issueKey"`
			Reason           string `json:"reason"`
			ExpiresAt        string `json:"expiresAt"`
			ExpectedStateSeq int64  `json:"expectedStateSeq"`
		}
		if err := decode(r, &body); err != nil {
			writeErr(w, err)
			return
		}
		res, replay, err := s.AddExemption(opID(r), ExemptionRequest{
			ValidationID: body.ValidationID, IssueKey: body.IssueKey, Reason: body.Reason,
			ExpiresAt: parseTime(body.ExpiresAt), ExpectedStateSeq: body.ExpectedStateSeq,
		})
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"result": res, "replayed": replay})
	})

	mux.HandleFunc("GET /api/exemptions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"exemptions": s.ListExemptions(), "now": s.now()})
	})

	mux.HandleFunc("POST /api/exemptions/{id}/revoke", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ExpectedStateSeq int64 `json:"expectedStateSeq"`
		}
		_ = decode(r, &body)
		ex, replay, err := s.RevokeExemption(opID(r), r.PathValue("id"), body.ExpectedStateSeq)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"exemption": ex, "replayed": replay})
	})

	mux.HandleFunc("POST /api/batch-fix", func(w http.ResponseWriter, r *http.Request) {
		var req BatchFixRequest
		if err := decode(r, &req); err != nil {
			writeErr(w, err)
			return
		}
		res, replay, err := s.BatchFix(opID(r), req)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"result": res, "replayed": replay})
	})

	mux.HandleFunc("GET /api/batches", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"batches": s.Batches()})
	})

	mux.HandleFunc("POST /api/proofs", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ExpectedStateSeq int64 `json:"expectedStateSeq"`
		}
		_ = decode(r, &body)
		p, rec, replay, err := s.GenerateProof(opID(r), body.ExpectedStateSeq)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"proof": p, "record": rec, "replayed": replay})
	})

	mux.HandleFunc("GET /api/proofs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"proofs": s.Proofs(), "snapshotStamp": s.Snapshot().Stamp})
	})

	mux.HandleFunc("GET /api/proofs/{id}/raw", func(w http.ResponseWriter, r *http.Request) {
		data, current, err := s.ReadProof(r.PathValue("id"))
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("X-Proof-Current", boolHeader(current))
		_, _ = w.Write(data)
	})

	mux.HandleFunc("POST /api/admin/clock", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Set string `json:"set"`
			Add string `json:"add"`
		}
		if err := decode(r, &body); err != nil {
			writeErr(w, err)
			return
		}
		if body.Set != "" {
			t, err := time.Parse(time.RFC3339, body.Set)
			if err != nil {
				writeErr(w, err)
				return
			}
			s.SetTime(t)
		}
		if body.Add != "" {
			d, err := time.ParseDuration(body.Add)
			if err != nil {
				writeErr(w, err)
				return
			}
			s.AddTime(d)
		}
		writeJSON(w, 200, map[string]any{"now": s.now()})
	})

	// Static UI.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		data, err := fs.ReadFile(assets, "web/index.html")
		if err != nil {
			http.Error(w, "ui missing", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	})

	return logging(mux)
}

func boolHeader(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func logging(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			// tiny access log
			fmt.Printf("%s %s\n", r.Method, r.URL.Path)
		}
		h.ServeHTTP(w, r)
	})
}

// ParserVersionName exposes the parser version.
func ParserVersionName() string { return parserVersion() }
