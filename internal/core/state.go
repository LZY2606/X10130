// Package core holds the domain state, persistence and business logic of
// the message catalog validation workbench.
package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"msgcat/internal/icu"
)

// Message is one catalog entry: raw text plus its parsed structure.
type Message struct {
	Key       string        `json:"key"`
	Text      string        `json:"text"`
	Context   string        `json:"context,omitempty"`
	Structure icu.Structure `json:"structure"`
}

// Version is an immutable, content-fingerprinted catalog snapshot for one
// language. Identical content (ignoring key order and newline style)
// yields the same version ID.
type Version struct {
	ID          string              `json:"id"`
	Fingerprint string              `json:"fingerprint"`
	Lang        string              `json:"lang"`
	Entries     map[string]*Message `json:"entries"`
	CreatedAt   time.Time           `json:"createdAt"`
}

// Import records one uploaded file. The raw bytes are stored separately
// and are downloadable byte-identical.
type Import struct {
	ID        string    `json:"id"`
	Seq       int64     `json:"seq"`
	Lang      string    `json:"lang"`
	Role      string    `json:"role"` // "base" or "target"
	VersionID string    `json:"versionId"`
	RawPath   string    `json:"rawPath"`
	RawSHA256 string    `json:"rawSha256"`
	Filename  string    `json:"filename"`
	CreatedAt time.Time `json:"createdAt"`
}

// RenameMapping is a user-confirmed rename from an old base key to a new
// base key. Target-language entries for the old key then count as
// translations of the new key.
type RenameMapping struct {
	ID            string    `json:"id"`
	From          string    `json:"from"`
	To            string    `json:"to"`
	BaseVersionID string    `json:"baseVersionId"`
	CreatedAt     time.Time `json:"createdAt"`
}

// Exemption waives one issue fingerprint until a deadline.
type Exemption struct {
	ID               string    `json:"id"`
	IssueFingerprint string    `json:"issueFingerprint"`
	Reason           string    `json:"reason"`
	ExpiresAt        time.Time `json:"expiresAt"`
	CreatedAt        time.Time `json:"createdAt"`
}

// Issue is one validation finding.
type Issue struct {
	Fingerprint string `json:"fingerprint"`
	Lang        string `json:"lang"`
	Key         string `json:"key"`
	Type        string `json:"type"`
	Detail      string `json:"detail"`
}

// Validation is a stored, replayable validation result bound to the
// catalog versions, parser version and mapping state it was computed
// from.
type Validation struct {
	ID                   string            `json:"id"`
	BaseVersionID        string            `json:"baseVersionId"`
	TargetVersionIDs     map[string]string `json:"targetVersionIds"`
	ParserVersion        string            `json:"parserVersion"`
	MappingsHash         string            `json:"mappingsHash"`
	StateSeq             int64             `json:"stateSeq"`
	Issues               []Issue           `json:"issues"`
	InvalidatedLanguages map[string]string `json:"invalidatedLanguages,omitempty"`
	CreatedAt            time.Time         `json:"createdAt"`
}

// Operation is a persisted idempotency record for a mutating call.
type Operation struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	RequestHash string          `json:"requestHash"`
	Status      string          `json:"status"` // "completed" or "partial"
	Result      json.RawMessage `json:"result"`
	CreatedAt   time.Time       `json:"createdAt"`
}

// Proof records one generated release proof on disk.
type Proof struct {
	ID        string    `json:"id"`
	StateID   string    `json:"stateId"`
	StateSeq  int64     `json:"stateSeq"`
	Path      string    `json:"path"`
	SHA256    string    `json:"sha256"`
	CreatedAt time.Time `json:"createdAt"`
}

// State is the full persistent state.
type State struct {
	Seq            int64                     `json:"seq"`
	ImportSeq      int64                     `json:"importSeq"`
	ParserVersion  string                    `json:"parserVersion"`
	Versions       map[string]*Version       `json:"versions"`
	Imports        map[string]*Import        `json:"imports"`
	CurrentVersion map[string]string         `json:"currentVersion"` // lang -> versionID
	BaseLang       string                    `json:"baseLang"`
	Mappings       map[string]*RenameMapping `json:"mappings"`
	Exemptions     map[string]*Exemption     `json:"exemptions"`
	Validations    map[string]*Validation    `json:"validations"`
	Operations     map[string]*Operation     `json:"operations"`
	Proofs         map[string]*Proof         `json:"proofs"`
}

// NewState returns an empty state.
func NewState() *State {
	return &State{
		ParserVersion:  icu.ParserVersion,
		Versions:       map[string]*Version{},
		Imports:        map[string]*Import{},
		CurrentVersion: map[string]string{},
		Mappings:       map[string]*RenameMapping{},
		Exemptions:     map[string]*Exemption{},
		Validations:    map[string]*Validation{},
		Operations:     map[string]*Operation{},
		Proofs:         map[string]*Proof{},
	}
}

// NormalizeNewlines converts CRLF and CR line endings to LF so that
// newline style never affects content fingerprints.
func NormalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// Fingerprint computes the canonical content fingerprint of catalog
// entries: independent of key order and newline style.
func Fingerprint(entries map[string]*Message) string {
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		m := entries[k]
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write([]byte(NormalizeNewlines(m.Text)))
		h.Write([]byte{0})
		h.Write([]byte(NormalizeNewlines(m.Context)))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// VersionID derives a version ID from a fingerprint.
func VersionID(fp string) string { return "v-" + fp[:16] }

// MappingsHash hashes the current mapping set for binding validations.
func (s *State) MappingsHash() string {
	ms := make([]*RenameMapping, 0, len(s.Mappings))
	for _, m := range s.Mappings {
		ms = append(ms, m)
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].From < ms[j].From })
	h := sha256.New()
	for _, m := range ms {
		h.Write([]byte(m.From + "->" + m.To))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// StateID is a stable identifier of the committed snapshot, used to
// correlate issues views, proofs and optimistic-concurrency checks.
func (s *State) StateID() string {
	type snap struct {
		Seq            int64             `json:"seq"`
		ParserVersion  string            `json:"parserVersion"`
		CurrentVersion map[string]string `json:"currentVersion"`
		BaseLang       string            `json:"baseLang"`
		MappingsHash   string            `json:"mappingsHash"`
		Exemptions     []string          `json:"exemptions"`
	}
	ex := make([]string, 0, len(s.Exemptions))
	for _, e := range s.Exemptions {
		ex = append(ex, e.ID+":"+e.IssueFingerprint+":"+e.ExpiresAt.UTC().Format(time.RFC3339Nano))
	}
	sort.Strings(ex)
	b, _ := json.Marshal(snap{
		Seq:            s.Seq,
		ParserVersion:  s.ParserVersion,
		CurrentVersion: s.CurrentVersion,
		BaseLang:       s.BaseLang,
		MappingsHash:   s.MappingsHash(),
		Exemptions:     ex,
	})
	sum := sha256.Sum256(b)
	return fmt.Sprintf("s%d-%s", s.Seq, hex.EncodeToString(sum[:])[:12])
}

// IssueFingerprint builds a stable fingerprint for an issue.
func IssueFingerprint(lang, key, typ, detail string) string {
	sum := sha256.Sum256([]byte(lang + "|" + key + "|" + typ + "|" + detail))
	return hex.EncodeToString(sum[:])[:16]
}

// shortHash hashes arbitrary JSON-able content for deterministic IDs.
func shortHash(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}
