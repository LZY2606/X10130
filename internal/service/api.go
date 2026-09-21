package service

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"messagecatalog/internal/store"
)

// APIError is a JSON error body.
type APIError struct {
	Error    string `json:"error"`
	Code     string `json:"code,omitempty"`
	Conflict bool   `json:"conflict,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	ae := APIError{Error: err.Error()}
	status := http.StatusBadRequest
	var ce *ConflictError
	if errors.As(err, &ce) {
		status = http.StatusConflict
		ae.Conflict = true
		ae.Code = "conflict"
	}
	if errors.Is(err, store.ErrIdempotency) {
		status = http.StatusConflict
		ae.Conflict = true
		ae.Code = "idempotency_conflict"
	}
	if errors.Is(err, store.ErrReadOnly) {
		status = http.StatusServiceUnavailable
		ae.Code = "read_only_recovery"
	}
	if errors.Is(err, errNotFound) {
		status = http.StatusNotFound
	}
	writeJSON(w, status, ae)
}

func opID(r *http.Request) string {
	if v := r.Header.Get("Idempotency-Key"); v != "" {
		return v
	}
	if v := r.URL.Query().Get("opId"); v != "" {
		return v
	}
	return ""
}

func decode(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}

func parseTime(v string) time.Time {
	if v == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func atoiDefault(v string, def int) int {
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
