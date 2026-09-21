package service

import "time"

// State is the single mutable root, persisted atomically by the store.
type State struct {
	Seq int `json:"seq"`

	// Uploads: language -> newest upload id (raw bytes stay in blobs).
	Uploads map[string]*Upload `json:"uploads"`

	// Catalogs: language -> current parsed catalog blob + version info.
	Catalogs map[string]*CatalogVersion `json:"catalogs"`

	// BaselineLanguage is the designated base language.
	BaselineLanguage string `json:"baselineLanguage"`

	// Rename mapping edges: old key -> new key, baseline scoped implicitly.
	Mapping        map[string]string `json:"mapping"`
	MappingVersion int               `json:"mappingVersion"`

	// Pending rename proposals from the latest baseline import.
	PendingRenames *PendingRename `json:"pendingRenames,omitempty"`

	// Exemptions keyed by stable issue id.
	Exemptions map[string]*Exemption `json:"exemptions"`

	// Idempotent operation records keyed by caller-supplied op id.
	Operations map[string]*Operation `json:"operations"`

	// Batch operations (also keyed in Operations, kept typed here).
	Batches map[string]*Batch `json:"batches"`

	// Snapshots, one per committed mutation, newest last.
	Snapshots []*Snapshot `json:"snapshots"`

	// Reports cached under their basis tuples.
	Reports map[string]*ReportRecord `json:"reports"`

	// Proofs ever generated.
	Proofs []*ProofRecord `json:"proofs"`

	// Parser version at last full evaluation.
	ParserVersion string `json:"parserVersion"`
}

// Upload remembers one raw import so it can be downloaded byte-for-byte.
type Upload struct {
	ID         string `json:"id"`
	Language   string `json:"language"`
	RawBlob    string `json:"rawBlob"`
	Size       int    `json:"size"`
	ImportedAt string `json:"importedAt"`
	Seq        int    `json:"seq"`
	SnapshotID string `json:"snapshotId"`
}

// CatalogVersion describes one language's current catalog.
type CatalogVersion struct {
	Language   string `json:"language"`
	Version    string `json:"version"` // content fingerprint (normalized)
	Blob       string `json:"blob"`    // canonical parsed catalog blob
	UploadID   string `json:"uploadId"`
	ImportedAt string `json:"importedAt"`
	Seq        int    `json:"seq"`
}

// PendingRename holds a proposal that a baseline rename must be confirmed
// before target-language inheritance changes.
type PendingRename struct {
	OldVersion string            `json:"oldVersion"`
	NewVersion string            `json:"newVersion"`
	Removed    []string          `json:"removed"`
	Added      []string          `json:"added"`
	Suggested  map[string]string `json:"suggested"` // old -> new guess
	CreatedAt  string            `json:"createdAt"`
}

// Exemption for one issue.
type Exemption struct {
	IssueID   string `json:"issueId"`
	Reason    string `json:"reason"`
	CreatedAt string `json:"createdAt"`
	Deadline  string `json:"deadline,omitempty"` // RFC3339; empty = no expiry
	// Basis at creation so an old exemption cannot silently attach to new text.
	BaselineVersion string `json:"baselineVersion"`
	TargetVersion   string `json:"targetVersion"`
	MappingVersion  int    `json:"mappingVersion"`
	ParserVersion   string `json:"parserVersion"`
}

// Expired reports expiry at time now.
func (e *Exemption) Expired(now time.Time) bool {
	if e.Deadline == "" {
		return false
	}
	d, err := time.Parse(time.RFC3339, e.Deadline)
	if err != nil {
		return false
	}
	return !now.Before(d)
}

// Operation is the idempotency record.
type Operation struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	RequestSHA string `json:"requestSha"`
	Status     string `json:"status"` // applied
	Seq        int    `json:"seq"`
	SnapshotID string `json:"snapshotId"`
	CreatedAt  string `json:"createdAt"`
	ResultJSON string `json:"resultJson"`
}

// Batch tracks per-language progress so a retry applies only unfinished parts.
type Batch struct {
	ID        string                  `json:"id"`
	Items     []*BatchItem            `json:"items"`
	Results   map[string]*BatchResult `json:"results"`
	CreatedAt string                  `json:"createdAt"`
}

// BatchItem is one requested language fix.
type BatchItem struct {
	Language    string `json:"language"`
	BaseVersion string `json:"baseVersion"`
	Raw         []byte `json:"raw"`
	RequestSHA  string `json:"requestSha"`
}

// BatchResult is the permanent per-language receipt.
type BatchResult struct {
	Language   string `json:"language"`
	Status     string `json:"status"` // success, conflict, error
	NewVersion string `json:"newVersion,omitempty"`
	UploadID   string `json:"uploadId,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Seq        int    `json:"seq,omitempty"`
	SnapshotID string `json:"snapshotId,omitempty"`
}

// Snapshot identifies one committed state.
type Snapshot struct {
	ID              string            `json:"id"`
	Seq             int               `json:"seq"`
	CreatedAt       string            `json:"createdAt"`
	BaselineVersion string            `json:"baselineVersion"`
	Languages       map[string]string `json:"languages"` // lang -> content version
	MappingVersion  int               `json:"mappingVersion"`
	ParserVersion   string            `json:"parserVersion"`
	Note            string            `json:"note"`
}

// ReportRecord is a validation output stored under its basis tuple.
type ReportRecord struct {
	TupleSHA        string `json:"tupleSha"`
	ParserVersion   string `json:"parserVersion"`
	BaselineVersion string `json:"baselineVersion"`
	TargetVersion   string `json:"targetVersion"`
	MappingVersion  int    `json:"mappingVersion"`
	Language        string `json:"language"`
	Blob            string `json:"blob"`
	CreatedAt       string `json:"createdAt"`
}

// ProofRecord tracks a generated proof file.
type ProofRecord struct {
	ID         string `json:"id"`
	Blob       string `json:"blob"`
	SnapshotID string `json:"snapshotId"`
	Seq        int    `json:"seq"`
	CreatedAt  string `json:"createdAt"`
	Current    bool   `json:"current"`
}
