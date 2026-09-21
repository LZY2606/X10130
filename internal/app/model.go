// Package app holds the catalogue domain state: content-addressed versions,
// rename mappings, exemptions, idempotent operation records, validation runs
// and release proofs. All state is persisted locally as one atomic JSON
// document plus raw import blobs.
package app

import (
	"encoding/json"
	"time"

	"messagebench/internal/icu"
)

// Message is one catalogue entry: original text, optional context and the
// parsed ICU structure.
type Message struct {
	Text    string       `json:"text"`
	Context string       `json:"context,omitempty"`
	Parsed  *icu.Message `json:"parsed"`
}

// Version is an immutable, content-addressed catalogue snapshot for one
// language. Reordering keys or changing newline style yields the same ID.
type Version struct {
	ID          string              `json:"id"`
	Lang        string              `json:"lang"`
	Fingerprint string              `json:"fingerprint"`
	Messages    map[string]*Message `json:"messages"`
	CreatedSeq  int64               `json:"createdSeq"`
}

// Import records one upload. The raw bytes are stored separately so every
// import can be downloaded byte-identically even when it maps to an existing
// version.
type Import struct {
	ID        string `json:"id"`
	Lang      string `json:"lang"`
	Role      string `json:"role"` // "base" | "target"
	VersionID string `json:"versionID"`
	RawFile   string `json:"rawFile"`
	RawSHA256 string `json:"rawSHA256"`
	Size      int    `json:"size"`
	Seq       int64  `json:"seq"`
}

// Mapping is a confirmed rename from an old base key to a new base key.
type Mapping struct {
	From string `json:"from"`
	To   string `json:"to"`
	Seq  int64  `json:"seq"`
}

// Exemption waives one issue until Deadline (inclusive of nothing past it).
type Exemption struct {
	IssueID  string    `json:"issueID"`
	Reason   string    `json:"reason"`
	Deadline time.Time `json:"deadline"`
	Seq      int64     `json:"seq"`
}

// OpRecord persists the result of a mutating operation so a retry with the
// same operation ID and same content returns the recorded result.
type OpRecord struct {
	Kind        string          `json:"kind"`
	RequestHash string          `json:"requestHash"`
	Response    json.RawMessage `json:"response"`
	Seq         int64           `json:"seq"`
}

// Issue is one validation finding. ID is a stable hash of its content so
// exemptions survive across catalogue versions.
type Issue struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Lang   string `json:"lang"`
	Key    string `json:"key"`
	Detail string `json:"detail"`
}

// Run is a validation result bound to parser version, catalogue versions and
// mapping state. Old runs stay replayable.
type Run struct {
	ID            string            `json:"id"`
	ParserVersion string            `json:"parserVersion"`
	BaseVersion   string            `json:"baseVersion"`
	Targets       map[string]string `json:"targets"` // lang -> versionID
	MappingHash   string            `json:"mappingHash"`
	Issues        []Issue           `json:"issues"`
	Seq           int64             `json:"seq"`
}

// ProofRecord points at an atomically written proof file.
type ProofRecord struct {
	ID          string    `json:"id"`
	Snapshot    int64     `json:"snapshot"`
	GeneratedAt time.Time `json:"generatedAt"`
	File        string    `json:"file"`
}

// State is the whole persistent document.
type State struct {
	Seq        int64                  `json:"seq"`
	BaseLang   string                 `json:"baseLang"`
	Heads      map[string]string      `json:"heads"` // lang -> versionID
	Versions   map[string]*Version    `json:"versions"`
	Imports    map[string]*Import     `json:"imports"`
	Mappings   []*Mapping             `json:"mappings"`
	Exemptions map[string]*Exemption  `json:"exemptions"`
	Ops        map[string]*OpRecord   `json:"ops"`
	Runs       []*Run                 `json:"runs"`
	Proofs     []*ProofRecord         `json:"proofs"`
}

func newState() *State {
	return &State{
		Heads:      map[string]string{},
		Versions:   map[string]*Version{},
		Imports:    map[string]*Import{},
		Exemptions: map[string]*Exemption{},
		Ops:        map[string]*OpRecord{},
	}
}
