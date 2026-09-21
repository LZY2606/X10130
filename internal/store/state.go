// Package store implements crash-safe local persistence for the workbench.
package store

import (
	"time"

	"messagecatalog/internal/catalog"
)

// VersionRecord is one catalog content version for one language.
type VersionRecord struct {
	ID            string            `json:"id"` // lang + ":" + fingerprint prefix
	Language      string            `json:"language"`
	Role          string            `json:"role"` // baseline | target
	Fingerprint   string            `json:"fingerprint"`
	ParserVersion string            `json:"parserVersion"`
	Messages      []catalog.Message `json:"messages"`
	UploadIDs     []string          `json:"uploadIds"`
	CreatedAt     time.Time         `json:"createdAt"`
}

// UploadRecord keeps a raw upload byte-for-byte retrievable.
type UploadRecord struct {
	ID         string    `json:"id"`
	Language   string    `json:"language"`
	Role       string    `json:"role"`
	Filename   string    `json:"filename"`
	BlobName   string    `json:"blobName"`
	Size       int64     `json:"size"`
	SHA256     string    `json:"sha256"`
	UploadedAt time.Time `json:"uploadedAt"`
	VersionID  string    `json:"versionId"`
}

// RenameEdge is one confirmed baseline rename mapping.
type RenameEdge struct {
	From        string    `json:"from"`
	To          string    `json:"to"`
	ConfirmedAt time.Time `json:"confirmedAt"`
	Version     string    `json:"version"` // baseline fingerprint when confirmed
}

// Exemption excuses one issue until a deadline.
type Exemption struct {
	ID          string    `json:"id"`
	IssueKey    string    `json:"issueKey"` // stable identity incl. versions
	Language    string    `json:"language"`
	MessageKey  string    `json:"messageKey"`
	Type        string    `json:"type"`
	Reason      string    `json:"reason"`
	ExpiresAt   time.Time `json:"expiresAt"`
	CreatedAt   time.Time `json:"createdAt"`
	Revoked     bool      `json:"revoked,omitempty"`
	OperationID string    `json:"operationId,omitempty"`
}

// ValidationRecord is an immutable validation result.
type ValidationRecord struct {
	ID              string        `json:"id"`
	TargetLang      string        `json:"targetLang"`
	BaseVersionID   string        `json:"baseVersionId"`
	TargVersionID   string        `json:"targVersionId"`
	MappingSig      string        `json:"mappingSig"`
	ParserVersion   string        `json:"parserVersion"`
	BaseFingerprint string        `json:"baseFingerprint"`
	TargFingerprint string        `json:"targFingerprint"`
	Issues          []StoredIssue `json:"issues"`
	CreatedAt       time.Time     `json:"createdAt"`
	Superseded      bool          `json:"superseded"`
	Invalidated     bool          `json:"invalidated"`
	InvalidReason   string        `json:"invalidReason,omitempty"`
	SupersededBy    string        `json:"supersededBy,omitempty"`
}

// StoredIssue is one issue persisted inside a validation record.
type StoredIssue struct {
	Language string `json:"language"`
	Key      string `json:"key"`
	Type     string `json:"type"`
	Name     string `json:"name,omitempty"`
	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`
	Detail   string `json:"detail,omitempty"`
	IssueKey string `json:"issueKey"`
}

// ProofRecord references one saved release proof artifact.
type ProofRecord struct {
	ID        string    `json:"id"`
	SHA256    string    `json:"sha256"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"createdAt"`
	Stamp     string    `json:"stamp"`
	BlobName  string    `json:"blobName"`
	Current   bool      `json:"current"`
}

// BatchLangResult is the per-language outcome of a batch fix.
type BatchLangResult struct {
	Language  string    `json:"language"`
	Status    string    `json:"status"` // applied | conflict | skipped
	VersionID string    `json:"versionId,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	AppliedAt time.Time `json:"appliedAt,omitempty"`
}

// BatchRecord stores a batch fix attempt and its per-language results.
type BatchRecord struct {
	ID            string            `json:"id"`
	OperationID   string            `json:"operationId"`
	Reason        string            `json:"reason"`
	BaseVersionID string            `json:"baseVersionId"`
	Results       []BatchLangResult `json:"results"`
	CreatedAt     time.Time         `json:"createdAt"`
}

// OperationRecord makes mutating calls idempotent.
type OperationRecord struct {
	Key         string    `json:"key"`
	RequestHash string    `json:"requestHash"`
	Kind        string    `json:"kind"`
	Status      int       `json:"status"`
	Response    string    `json:"response"`
	RecordedAt  time.Time `json:"recordedAt"`
}

// RecoveryEvent reports a detected storage problem at startup.
type RecoveryEvent struct {
	Time    time.Time `json:"time"`
	Scope   string    `json:"scope"`
	Message string    `json:"message"`
}

// State is the full durable application state.
type State struct {
	Seq            int64              `json:"seq"`
	RoleByLang     map[string]string  `json:"roleByLang"`
	CurrentByLang  map[string]string  `json:"currentByLang"` // lang -> version id
	Versions       []VersionRecord    `json:"versions"`
	Uploads        []UploadRecord     `json:"uploads"`
	RenameEdges    []RenameEdge       `json:"renameEdges"`
	Exemptions     []Exemption        `json:"exemptions"`
	Validations    []ValidationRecord `json:"validations"`
	Proofs         []ProofRecord      `json:"proofs"`
	Batches        []BatchRecord      `json:"batches"`
	Operations     []OperationRecord  `json:"operations"`
	RecoveryEvents []RecoveryEvent    `json:"recoveryEvents"`
}

func newState() *State {
	return &State{
		RoleByLang:    map[string]string{},
		CurrentByLang: map[string]string{},
	}
}
