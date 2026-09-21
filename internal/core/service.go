package core

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"msgcat/internal/icu"
)

// Service implements the catalog workbench business logic. All methods that
// touch state hold the same mutex, so every read observes one committed
// snapshot and every mutation is committed atomically through the Persister.
type Service struct {
	mu  sync.Mutex
	p   Persister
	st  *State
	now func() time.Time
}

// NewService creates a Service over an already-loaded state.
func NewService(p Persister, st *State, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{p: p, st: st, now: now}
}

// State exposes the underlying state (read-only use by callers that hold no
// locks is unsafe; used by tests and startup reconciliation).
func (s *Service) State() *State { return s.st }

func hashParts(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func hashJSON(v any) string {
	b, _ := json.Marshal(v)
	return hashParts(string(b))
}

func randID(prefix string) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

// ---------- catalog building ----------

func parseMessage(fm FileMessage) *Message {
	m := &Message{Key: fm.Key, Text: fm.Text, Context: fm.Context}
	ast, err := icu.Parse(fm.Text)
	if err != nil {
		m.ParseError = err.Error()
		if te, ok := err.(*icu.Error); ok && te.Tag {
			m.ParseKind = "unbalanced-tags"
		} else {
			m.ParseKind = "syntax-error"
		}
		return m
	}
	m.AST = ast
	a := icu.Analyze(ast)
	m.Placeholders = a.Placeholders
	m.Plurals = a.Plurals
	return m
}

// buildVersion parses a message file and computes its content fingerprint.
// Key order and newline style do not affect the fingerprint.
func buildVersion(language string, content string) (*Version, error) {
	var mf MessageFile
	if err := json.Unmarshal([]byte(content), &mf); err != nil {
		return nil, &BadRequestError{Reason: "invalid message file JSON: " + err.Error()}
	}
	v := &Version{Language: language, Messages: map[string]*Message{}}
	for _, fm := range mf.Messages {
		if fm.Key == "" {
			return nil, &BadRequestError{Reason: "message with empty key"}
		}
		if _, dup := v.Messages[fm.Key]; dup {
			return nil, &BadRequestError{Reason: "duplicate key " + fm.Key}
		}
		v.Messages[fm.Key] = parseMessage(fm)
	}
	keys := make([]string, 0, len(v.Messages))
	for k := range v.Messages {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		m := v.Messages[k]
		b.WriteString(k)
		b.WriteString("\x00")
		b.WriteString(m.Context)
		b.WriteString("\x00")
		if m.AST != nil {
			b.WriteString(icu.Canonical(m.AST))
		} else {
			b.WriteString("RAW:")
			b.WriteString(icu.NormalizeNewlines(m.Text))
		}
		b.WriteString("\x00")
	}
	v.ID = "v" + hashParts(b.String())[:40]
	return v, nil
}

// ---------- idempotency ----------

func (s *Service) replayOp(opID, reqHash string) ([]byte, bool, error) {
	if opID == "" {
		return nil, false, nil
	}
	op, ok := s.st.Operations[opID]
	if !ok {
		return nil, false, nil
	}
	if op.RequestHash != reqHash {
		return nil, false, &ConflictError{Reason: fmt.Sprintf("operation %q was already committed with different content; refusing to guess", opID)}
	}
	return op.Response, true, nil
}

func (s *Service) recordOp(opID, kind, reqHash string, resp any) ([]byte, error) {
	b, err := json.Marshal(resp)
	if err != nil {
		return nil, err
	}
	if opID != "" {
		s.st.Operations[opID] = &Operation{RequestHash: reqHash, Kind: kind, Response: b}
	}
	return b, nil
}

// ---------- import ----------

// ImportRequest uploads one message catalog file.
type ImportRequest struct {
	OpID     string `json:"opId"`
	Language string `json:"language"`
	Base     bool   `json:"base"`
	Content  string `json:"content"`
}

// ImportResponse reports the outcome of an import.
type ImportResponse struct {
	OpID       string `json:"opId"`
	ImportID   string `json:"importId"`
	VersionID  string `json:"versionId"`
	Language   string `json:"language"`
	NewVersion bool   `json:"newVersion"`
	Snapshot   int64  `json:"snapshot"`
}

// Import stores a raw file and its parsed catalog version atomically.
func (s *Service) Import(req ImportRequest) (*ImportResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.OpID == "" {
		req.OpID = randID("op-")
	}
	reqHash := hashJSON(req)
	if b, ok, err := s.replayOp(req.OpID, reqHash); err != nil {
		return nil, err
	} else if ok {
		var resp ImportResponse
		if err := json.Unmarshal(b, &resp); err != nil {
			return nil, err
		}
		return &resp, nil
	}
	lang := req.Language
	var mf MessageFile
	if err := json.Unmarshal([]byte(req.Content), &mf); err != nil {
		return nil, &BadRequestError{Reason: "invalid message file JSON: " + err.Error()}
	}
	if lang == "" {
		lang = mf.Language
	}
	if lang == "" {
		return nil, &BadRequestError{Reason: "language is required"}
	}
	if req.Base {
		if s.st.BaseLanguage != "" && s.st.BaseLanguage != lang {
			return nil, &BadRequestError{Reason: "base language is already " + s.st.BaseLanguage}
		}
	}
	version, err := buildVersion(lang, req.Content)
	if err != nil {
		return nil, err
	}
	// Persist the raw bytes first; a crash here leaves an orphan file that
	// startup reconciliation removes, never a half-visible state.
	importID := randID("imp-")
	path, err := s.p.WriteRaw(importID, []byte(req.Content))
	if err != nil {
		return nil, err
	}
	_, existed := s.st.Versions[version.ID]
	if !existed {
		s.st.Versions[version.ID] = version
	}
	s.st.Imports[importID] = &Import{
		ID:        importID,
		Language:  lang,
		VersionID: version.ID,
		File:      path,
		Size:      int64(len(req.Content)),
		Time:      formatTime(s.now()),
	}
	if req.Base {
		if s.st.BaseLanguage == "" {
			s.st.BaseLanguage = lang
		}
		if cur := s.st.Current[lang]; cur != "" && cur != version.ID {
			s.st.PreviousBase = cur
		}
	}
	s.st.Current[lang] = version.ID
	s.st.Seq++
	resp := &ImportResponse{
		OpID:       req.OpID,
		ImportID:   importID,
		VersionID:  version.ID,
		Language:   lang,
		NewVersion: !existed,
		Snapshot:   s.st.Seq,
	}
	if _, err := s.recordOp(req.OpID, "import", reqHash, resp); err != nil {
		return nil, err
	}
	if err := s.p.Commit(s.st); err != nil {
		return nil, err
	}
	return resp, nil
}

// ---------- rename mappings ----------

// MappingRequest confirms a base-language key rename.
type MappingRequest struct {
	OpID             string `json:"opId"`
	ExpectedSnapshot int64  `json:"expectedSnapshot"`
	OldKey           string `json:"oldKey"`
	NewKey           string `json:"newKey"`
}

// MappingResponse reports a confirmed mapping.
type MappingResponse struct {
	OpID       string `json:"opId"`
	OldKey     string `json:"oldKey"`
	NewKey     string `json:"newKey"`
	MappingSeq int64  `json:"mappingSeq"`
	Snapshot   int64  `json:"snapshot"`
}

// Resolve follows the mapping chain for a key (cycle-safe).
func (s *Service) Resolve(key string) string {
	seen := map[string]bool{}
	cur := key
	for {
		nxt, ok := s.st.Mappings[cur]
		if !ok || seen[nxt] {
			return cur
		}
		seen[nxt] = true
		cur = nxt
	}
}

func (s *Service) mappingHashLocked() string {
	pairs := make([]string, 0, len(s.st.Mappings))
	for old, nw := range s.st.Mappings {
		pairs = append(pairs, old+"->"+nw)
	}
	sort.Strings(pairs)
	return hashParts(strings.Join(pairs, "\n"))
}

// ConfirmMapping establishes old->new after validating snapshot, chain
// acyclicity and the no-silent-merge rule.
func (s *Service) ConfirmMapping(req MappingRequest) (*MappingResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.OpID == "" {
		req.OpID = randID("op-")
	}
	reqHash := hashJSON(req)
	if b, ok, err := s.replayOp(req.OpID, reqHash); err != nil {
		return nil, err
	} else if ok {
		var resp MappingResponse
		if err := json.Unmarshal(b, &resp); err != nil {
			return nil, err
		}
		return &resp, nil
	}
	if req.ExpectedSnapshot != s.st.Seq {
		return nil, &ConflictError{Reason: fmt.Sprintf("catalog state changed: expected snapshot %d, current %d; re-review before confirming", req.ExpectedSnapshot, s.st.Seq)}
	}
	if req.OldKey == "" || req.NewKey == "" {
		return nil, &BadRequestError{Reason: "oldKey and newKey are required"}
	}
	if req.OldKey == req.NewKey {
		return nil, &BadRequestError{Reason: "oldKey and newKey are identical"}
	}
	base := s.st.Versions[s.st.Current[s.st.BaseLanguage]]
	if base == nil {
		return nil, &BadRequestError{Reason: "no base catalog imported"}
	}
	if _, ok := base.Messages[req.NewKey]; !ok {
		return nil, &ConflictError{Reason: fmt.Sprintf("new key %q is not present in the current base catalog", req.NewKey)}
	}
	if _, ok := base.Messages[req.OldKey]; ok {
		return nil, &ConflictError{Reason: fmt.Sprintf("old key %q still exists in the current base catalog; not a rename", req.OldKey)}
	}
	if cur, ok := s.st.Mappings[req.OldKey]; ok {
		if cur == req.NewKey {
			resp := &MappingResponse{OpID: req.OpID, OldKey: req.OldKey, NewKey: req.NewKey, MappingSeq: s.st.MappingSeq, Snapshot: s.st.Seq}
			if _, err := s.recordOp(req.OpID, "mapping", reqHash, resp); err != nil {
				return nil, err
			}
			if err := s.p.Commit(s.st); err != nil {
				return nil, err
			}
			return resp, nil
		}
		return nil, &ConflictError{Reason: fmt.Sprintf("key %q is already mapped to %q", req.OldKey, cur)}
	}
	for old, nw := range s.st.Mappings {
		if nw == req.NewKey {
			return nil, &ConflictError{Reason: fmt.Sprintf("new key %q is already the rename target of %q; refusing to merge two old keys silently", req.NewKey, old)}
		}
	}
	// Cycle check: following the chain from newKey must never reach oldKey.
	for cur, depth := req.NewKey, 0; ; depth++ {
		nxt, ok := s.st.Mappings[cur]
		if !ok {
			break
		}
		if nxt == req.OldKey {
			return nil, &ConflictError{Reason: fmt.Sprintf("mapping %q -> %q would create a rename cycle", req.OldKey, req.NewKey)}
		}
		if depth > len(s.st.Mappings)+1 {
			return nil, &ConflictError{Reason: "existing mapping chain is inconsistent"}
		}
		cur = nxt
	}
	s.st.Mappings[req.OldKey] = req.NewKey
	s.st.MappingSeq++
	s.st.Seq++
	resp := &MappingResponse{OpID: req.OpID, OldKey: req.OldKey, NewKey: req.NewKey, MappingSeq: s.st.MappingSeq, Snapshot: s.st.Seq}
	if _, err := s.recordOp(req.OpID, "mapping", reqHash, resp); err != nil {
		return nil, err
	}
	if err := s.p.Commit(s.st); err != nil {
		return nil, err
	}
	return resp, nil
}

// ---------- exemptions ----------

// ExemptionRequest creates or replaces an exemption for an issue.
type ExemptionRequest struct {
	OpID             string `json:"opId"`
	ExpectedSnapshot int64  `json:"expectedSnapshot"`
	IssueID          string `json:"issueId"`
	Reason           string `json:"reason"`
	ExpiresAt        string `json:"expiresAt"` // RFC3339
}

// ExemptionResponse reports a stored exemption.
type ExemptionResponse struct {
	OpID      string     `json:"opId"`
	Exemption *Exemption `json:"exemption"`
	Snapshot  int64      `json:"snapshot"`
}

// SetExemption waives an issue until a deadline, checked against the
// snapshot the user reviewed.
func (s *Service) SetExemption(req ExemptionRequest) (*ExemptionResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.OpID == "" {
		req.OpID = randID("op-")
	}
	reqHash := hashJSON(req)
	if b, ok, err := s.replayOp(req.OpID, reqHash); err != nil {
		return nil, err
	} else if ok {
		var resp ExemptionResponse
		if err := json.Unmarshal(b, &resp); err != nil {
			return nil, err
		}
		return &resp, nil
	}
	if req.ExpectedSnapshot != s.st.Seq {
		return nil, &ConflictError{Reason: fmt.Sprintf("catalog state changed: expected snapshot %d, current %d; re-review before exempting", req.ExpectedSnapshot, s.st.Seq)}
	}
	if req.Reason == "" {
		return nil, &BadRequestError{Reason: "reason is required"}
	}
	exp, err := time.Parse(time.RFC3339, req.ExpiresAt)
	if err != nil {
		return nil, &BadRequestError{Reason: "expiresAt must be RFC3339: " + err.Error()}
	}
	issue := s.findIssueLocked(req.IssueID, s.now())
	if issue == nil {
		return nil, &NotFoundError{What: "issue " + req.IssueID}
	}
	ex := &Exemption{
		IssueID:   req.IssueID,
		Language:  issue.Language,
		Key:       issue.Key,
		Reason:    req.Reason,
		ExpiresAt: formatTime(exp),
		CreatedAt: formatTime(s.now()),
	}
	s.st.Exemptions[req.IssueID] = ex
	s.st.Seq++
	resp := &ExemptionResponse{OpID: req.OpID, Exemption: ex, Snapshot: s.st.Seq}
	if _, err := s.recordOp(req.OpID, "exemption", reqHash, resp); err != nil {
		return nil, err
	}
	if err := s.p.Commit(s.st); err != nil {
		return nil, err
	}
	return resp, nil
}

func (s *Service) findIssueLocked(issueID string, now time.Time) *Issue {
	for _, lang := range s.languagesLocked() {
		r, _ := s.resultForLocked(lang, now)
		if r == nil {
			continue
		}
		for _, iss := range r.Issues {
			if iss.ID == issueID {
				return iss
			}
		}
	}
	return nil
}
