// Package app implements the message-catalog validation workbench domain:
// content-fingerprinted catalog versions, rename mappings, exemptions with
// deadlines, idempotent operations, validation runs bound to parser and
// catalog versions, and deterministic release attestations.
package app

import (
	"encoding/json"
	"time"

	"catalogcheck/internal/icu"
)

// ParserVersion is the parser semantics version all results are bound to.
const ParserVersion = icu.ParserVersion

const (
	RoleBase   = "base"
	RoleTarget = "target"
)

// Message is one catalog entry: raw text plus its parsed structure.
type Message struct {
	Raw     string         `json:"raw"`
	Context string         `json:"context,omitempty"`
	Parsed  *icu.Structure `json:"parsed"`
}

// Catalog is one language's current catalog at a content-fingerprinted
// version.
type Catalog struct {
	Language  string              `json:"language"`
	Role      string              `json:"role"`
	VersionID string              `json:"version_id"`
	Messages  map[string]*Message `json:"messages"`
}

// Import records one upload. Every import keeps its own raw blob so the
// exact uploaded bytes remain downloadable even when the content version is
// unchanged.
type Import struct {
	ID        string    `json:"id"`
	Language  string    `json:"language"`
	Role      string    `json:"role"`
	VersionID string    `json:"version_id"`
	BlobHash  string    `json:"blob_hash"`
	Time      time.Time `json:"time"`
	OpID      string    `json:"op_id,omitempty"`
}

// Mapping is a confirmed key rename in the base catalog.
type Mapping struct {
	ID          string    `json:"id"`
	Old         string    `json:"old"`
	New         string    `json:"new"`
	BaseVersion string    `json:"base_version"`
	Time        time.Time `json:"time"`
	OpID        string    `json:"op_id,omitempty"`
}

// Exemption waives one issue fingerprint until a deadline.
type Exemption struct {
	ID        string    `json:"id"`
	IssueFP   string    `json:"issue_fp"`
	Reason    string    `json:"reason"`
	ExpiresAt time.Time `json:"expires_at"`
	Time      time.Time `json:"time"`
	OpID      string    `json:"op_id,omitempty"`
}

// FixResult is the per-language outcome of a batch fix.
type FixResult struct {
	Status    string `json:"status"` // success | failed
	VersionID string `json:"version_id,omitempty"`
	ImportID  string `json:"import_id,omitempty"`
	Receipt   string `json:"receipt,omitempty"`
	Error     string `json:"error,omitempty"`
}

// BatchState tracks per-language results so a retry only processes
// languages that have not yet succeeded.
type BatchState struct {
	Fixes map[string]*FixResult `json:"fixes"`
}

// OpRecord persists an idempotent operation and its committed result.
type OpRecord struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	ReqHash string          `json:"req_hash"`
	Result  json.RawMessage `json:"result,omitempty"`
	Batch   *BatchState     `json:"batch,omitempty"`
	Time    time.Time       `json:"time"`
}

// Basis identifies the exact inputs a validation run was computed from.
type Basis struct {
	ParserVersion string            `json:"parser_version"`
	BaseVersion   string            `json:"base_version"`
	MappingHash   string            `json:"mapping_hash"`
	Targets       map[string]string `json:"targets"`
}

// Issue is one detected catalog problem.
type Issue struct {
	FP          string `json:"fp"`
	Type        string `json:"type"`
	Lang        string `json:"lang"`
	Key         string `json:"key"`
	Placeholder string `json:"placeholder,omitempty"`
	Detail      string `json:"detail"`
}

// Run is a persisted validation result bound to its basis.
type Run struct {
	ID     string    `json:"id"`
	Time   time.Time `json:"time"`
	Basis  Basis     `json:"basis"`
	Issues []Issue   `json:"issues"`
}

// AttestationMeta registers a committed attestation document.
type AttestationMeta struct {
	ID      string    `json:"id"`
	StateID string    `json:"state_id"`
	Time    time.Time `json:"time"`
}

// PendingRename lists removed/added base keys awaiting user confirmation.
type PendingRename struct {
	BaseVersion string   `json:"base_version"`
	Removed     []string `json:"removed"`
	Added       []string `json:"added"`
}

// State is the complete persisted application state.
type State struct {
	Seq          int                         `json:"seq"`
	BaseLang     string                      `json:"base_lang"`
	Catalogs     map[string]*Catalog         `json:"catalogs"`
	Imports      []*Import                   `json:"imports"`
	Mappings     []*Mapping                  `json:"mappings"`
	Exemptions   map[string]*Exemption       `json:"exemptions"`
	Ops          map[string]*OpRecord        `json:"ops"`
	Runs         []*Run                      `json:"runs"`
	Attestations map[string]*AttestationMeta `json:"attestations"`
	Pending      *PendingRename              `json:"pending,omitempty"`
}

func emptyState() *State {
	return &State{
		Catalogs:     map[string]*Catalog{},
		Exemptions:   map[string]*Exemption{},
		Ops:          map[string]*OpRecord{},
		Attestations: map[string]*AttestationMeta{},
	}
}

func (s *State) initNil() {
	if s.Catalogs == nil {
		s.Catalogs = map[string]*Catalog{}
	}
	if s.Exemptions == nil {
		s.Exemptions = map[string]*Exemption{}
	}
	if s.Ops == nil {
		s.Ops = map[string]*OpRecord{}
	}
	if s.Attestations == nil {
		s.Attestations = map[string]*AttestationMeta{}
	}
}

// ConflictError reports a precondition or idempotency conflict (HTTP 409).
type ConflictError struct{ Msg string }

func (e *ConflictError) Error() string { return e.Msg }

// BadRequestError reports invalid input (HTTP 400).
type BadRequestError struct{ Msg string }

func (e *BadRequestError) Error() string { return e.Msg }

// NotFoundError reports a missing entity (HTTP 404).
type NotFoundError struct{ Msg string }

func (e *NotFoundError) Error() string { return e.Msg }
