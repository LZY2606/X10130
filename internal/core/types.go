// Package core holds the domain model and business logic of the message
// catalog validation workbench.
package core

import (
	"time"

	"msgcat/internal/icu"
)

// MessageFile is the upload format for a message catalog.
type MessageFile struct {
	Language string        `json:"language"`
	Messages []FileMessage `json:"messages"`
}

// FileMessage is one message as uploaded.
type FileMessage struct {
	Key     string `json:"key"`
	Text    string `json:"text"`
	Context string `json:"context,omitempty"`
}

// Message is a stored message: raw text plus parsed structure.
type Message struct {
	Key          string              `json:"key"`
	Text         string              `json:"text"`
	Context      string              `json:"context,omitempty"`
	ParseError   string              `json:"parseError,omitempty"`
	ParseKind    string              `json:"parseKind,omitempty"` // "unbalanced-tags" | "syntax-error"
	AST          *icu.Node           `json:"ast,omitempty"`
	Placeholders map[string]string   `json:"placeholders,omitempty"`
	Plurals      map[string][]string `json:"plurals,omitempty"`
}

// Version is an immutable catalog version identified by content fingerprint.
type Version struct {
	ID       string              `json:"id"` // content fingerprint
	Language string              `json:"language"`
	Messages map[string]*Message `json:"messages"`
}

// Import records one uploaded raw file. Every import keeps its exact bytes
// downloadable even when its content fingerprint matches an existing version.
type Import struct {
	ID        string `json:"id"`
	Language  string `json:"language"`
	VersionID string `json:"versionId"`
	File      string `json:"file"`
	Size      int64  `json:"size"`
	Time      string `json:"time"`
}

// Exemption waives one issue until a deadline.
type Exemption struct {
	IssueID   string `json:"issueId"`
	Language  string `json:"language"`
	Key       string `json:"key"`
	Reason    string `json:"reason"`
	ExpiresAt string `json:"expiresAt"` // RFC3339
	CreatedAt string `json:"createdAt"`
}

// Issue is one validation finding.
type Issue struct {
	ID        string `json:"id"`
	Language  string `json:"language"`
	Key       string `json:"key"`
	Kind      string `json:"kind"`
	Detail    string `json:"detail"`
	Exempted  bool   `json:"exempted"`
	Reason    string `json:"reason,omitempty"`
	ExpiresAt string `json:"expiresAt,omitempty"`
}

// Result is a validation outcome bound to parser version, catalog versions,
// mapping state and active exemptions. Old results stay replayable.
type Result struct {
	ID            string   `json:"id"` // dependency fingerprint
	Language      string   `json:"language"`
	ParserVersion string   `json:"parserVersion"`
	BaseVersion   string   `json:"baseVersion"`
	TargetVersion string   `json:"targetVersion"`
	MappingHash   string   `json:"mappingHash"`
	Snapshot      int64    `json:"snapshot"`
	Issues        []*Issue `json:"issues"`
}

// BatchItem is one per-language fix inside a batch.
type BatchItem struct {
	Language        string `json:"language"`
	ExpectedVersion string `json:"expectedVersion"`
	Content         string `json:"content"`
	Status          string `json:"status"` // pending | success | conflict | error
	VersionID       string `json:"versionId,omitempty"`
	ImportID        string `json:"importId,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

// Batch is a multi-language fix request with per-language receipts.
type Batch struct {
	ID          string       `json:"id"`
	RequestHash string       `json:"requestHash"`
	BaseVersion string       `json:"baseVersion"`
	MappingHash string       `json:"mappingHash"`
	Items       []*BatchItem `json:"items"`
	Done        bool         `json:"done"`
}

// Operation records an idempotency-keyed mutation and its response.
type Operation struct {
	RequestHash string `json:"requestHash"`
	Kind        string `json:"kind"`
	Response    []byte `json:"response"`
}

// ProofMeta describes a stored release proof.
type ProofMeta struct {
	ID        string `json:"id"` // sha256 of proof bytes
	Snapshot  int64  `json:"snapshot"`
	StateHash string `json:"stateHash"`
	Time      string `json:"time"`
}

// State is the full persistent state.
type State struct {
	Seq          int64                 `json:"seq"`
	BaseLanguage string                `json:"baseLanguage"`
	Current      map[string]string     `json:"current"` // language -> version ID
	PreviousBase string                `json:"previousBase,omitempty"`
	Versions     map[string]*Version   `json:"versions"`
	Imports      map[string]*Import    `json:"imports"`
	Mappings     map[string]string     `json:"mappings"` // old key -> new key
	MappingSeq   int64                 `json:"mappingSeq"`
	Exemptions   map[string]*Exemption `json:"exemptions"` // issue ID -> exemption
	Results      map[string]*Result    `json:"results"`
	Batches      map[string]*Batch     `json:"batches"`
	Operations   map[string]*Operation `json:"operations"`
	Proofs       map[string]*ProofMeta `json:"proofs"`
	LatestProof  string                `json:"latestProof,omitempty"`
}

// NewState returns an empty initialized state.
func NewState() *State {
	return &State{
		Current:    map[string]string{},
		Versions:   map[string]*Version{},
		Imports:    map[string]*Import{},
		Mappings:   map[string]string{},
		Exemptions: map[string]*Exemption{},
		Results:    map[string]*Result{},
		Batches:    map[string]*Batch{},
		Operations: map[string]*Operation{},
		Proofs:     map[string]*ProofMeta{},
	}
}

// ConflictError reports an optimistic-concurrency or idempotency conflict.
type ConflictError struct{ Reason string }

func (e *ConflictError) Error() string { return e.Reason }

// NotFoundError reports a missing entity.
type NotFoundError struct{ What string }

func (e *NotFoundError) Error() string { return "not found: " + e.What }

// BadRequestError reports invalid input.
type BadRequestError struct{ Reason string }

func (e *BadRequestError) Error() string { return e.Reason }

// Persister abstracts local durable storage.
type Persister interface {
	Commit(st *State) error
	WriteRaw(id string, b []byte) (string, error)
	ReadRaw(path string) ([]byte, error)
	WriteProof(id string, b []byte) error
	ReadProof(id string) ([]byte, error)
	Anomalies() []string
}

// nowTime is a small helper for consistent timestamps.
func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
