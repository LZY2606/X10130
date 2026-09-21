package store

import "msgcatalog/internal/icu"

// VersionEntry is one immutable catalog version identified by content
// fingerprint. The parsed structures are stored alongside the texts.
type VersionEntry struct {
	Fingerprint   string             `json:"fingerprint"`
	ParserVersion string             `json:"parser_version"`
	Messages      []catalogMessage   `json:"messages"`
	CreatedAt     int64              `json:"created_at"`
}

type catalogMessage struct {
	Key     string    `json:"key"`
	Text    string    `json:"text"`
	Context string    `json:"context,omitempty"`
	AST     *icu.AST  `json:"ast"`
}

// RawRef points at a byte-identical uploaded file, stored content-addressed.
type RawRef struct {
	BlobSHA string `json:"blob_sha"`
	Size    int    `json:"size"`
	SavedAt int64  `json:"saved_at"`
}

// LangState is one language's current pointer plus its full import history.
type LangState struct {
	Name       string   `json:"name"`
	CurrentFP  string   `json:"current_fp"`
	Imports    []Import `json:"imports"`
}

type Import struct {
	Fingerprint string `json:"fingerprint"`
	Raw         RawRef `json:"raw"`
	At          int64  `json:"at"`
	AsBaseline  bool   `json:"as_baseline"`
}

// Edge is one confirmed rename oldKey -> newKey.
type Edge struct {
	OldKey string `json:"old_key"`
	NewKey string `json:"new_key"`
}

// RenameProposal is produced when a baseline re-import changes the key set.
type RenameProposal struct {
	ID            string   `json:"id"`
	BeforeFP      string   `json:"before_fp"`
	AfterFP       string   `json:"after_fp"`
	OldOnly       []string `json:"old_only"`
	NewOnly       []string `json:"new_only"`
	Common        []string `json:"common"`
	At            int64    `json:"at"`
	Resolved      bool     `json:"resolved"`
}

type Exemption struct {
	IssueID  string `json:"issue_id"`
	Reason   string `json:"reason"`
	Deadline int64  `json:"deadline"`
	Created  int64  `json:"created"`
	OpKey    string `json:"op_key"`
	Revoked  bool   `json:"revoked"`
}

// LangFix is the per-language result of a batch fix.
type LangFix struct {
	Language       string `json:"language"`
	ExpectedFP     string `json:"expected_fp"`
	AppliedFP      string `json:"applied_fp,omitempty"`
	Status         string `json:"status"` // applied | already-applied | conflict
	Reason         string `json:"reason,omitempty"`
	At             int64  `json:"at"`
}

// BatchFix groups the per-language results of one request.
type BatchFix struct {
	OpKey      string                  `json:"op_key"`
	ReqHash    string                  `json:"req_hash"`
	Langs      []LangFix               `json:"langs"`
	NewVersions map[string]*VersionEntry `json:"new_versions,omitempty"`
	At         int64                   `json:"at"`
}

// Run is a validation result bound to parser version and catalog versions.
type Run struct {
	ID              string  `json:"id"`
	Seq             int64   `json:"seq"`
	ParserVersion   string  `json:"parser_version"`
	BaselineFP      string  `json:"baseline_fp"`
	BaselineName    string  `json:"baseline_name"`
	MappingSig      string  `json:"mapping_sig"`
	LangFPs         map[string]string `json:"lang_fps"`
	IssueCount      int     `json:"issue_count"`
	Status          string  `json:"status"` // current | invalid
	InvalidReason   string  `json:"invalid_reason,omitempty"`
	Created         int64   `json:"created"`
}

type ProofRecord struct {
	ID        string         `json:"id"`
	Seq       int64          `json:"seq"`
	AsOf      int64          `json:"as_of"`
	Current   bool           `json:"current"`
	Document  ProofDocument  `json:"document"`
	SavedAt   int64          `json:"saved_at"`
}

// ProofDocument is the deterministic, machine-readable release proof.
type ProofDocument struct {
	Kind           string             `json:"kind"`
	ProofID        string             `json:"proof_id"`
	ParserVersion  string             `json:"parser_version"`
	SnapshotID     string             `json:"snapshot_id"`
	Seq            int64              `json:"seq"`
	AsOf           int64              `json:"as_of"`
	InputFingerprints map[string]string `json:"input_fingerprints"`
	Baseline       BaselineRef        `json:"baseline"`
	MappingSig     string             `json:"mapping_sig"`
	UnexemptedCount int               `json:"unexempted_issue_count"`
	IssueCount     int                `json:"issue_count"`
	Exemptions     []ProofExemption   `json:"exemptions"`
}

type BaselineRef struct {
	Language    string `json:"language"`
	Fingerprint string `json:"fingerprint"`
}

type ProofExemption struct {
	IssueID  string `json:"issue_id"`
	Reason   string `json:"reason"`
	Deadline int64  `json:"deadline"`
}

// IdemRecord persists the outcome of an idempotent operation.
type IdemRecord struct {
	OpKey    string `json:"op_key"`
	ReqHash  string `json:"req_hash"`
	Kind     string `json:"kind"`
	Result   string `json:"result"`
	Seq      int64  `json:"seq"`
	At       int64  `json:"at"`
}

// Snapshot captures one committed state for stable, replayable reads.
type Snapshot struct {
	Seq           int64                       `json:"seq"`
	ID            string                      `json:"id"`
	At            int64                        `json:"at"`
	Baseline      string                      `json:"baseline"`
	LangFPs       map[string]string           `json:"lang_fps"`
	MappingSig    string                      `json:"mapping_sig"`
	Edges         []Edge                      `json:"edges"`
	Proposals     map[string]RenameProposal   `json:"proposals"`
	Exemptions    map[string]Exemption        `json:"exemptions"`
	Issues        []Issue                     `json:"issues"`
	ParserVersion string                      `json:"parser_version"`
	RunID         string                      `json:"run_id"`
}

type State struct {
	Seq         int64
	Baseline    string
	Langs       map[string]*LangState
	Versions    map[string]*VersionEntry
	Edges       []Edge
	Proposals   map[string]RenameProposal
	Exemptions  map[string]Exemption
	Batches     map[string]*BatchFix
	Runs        map[string]*Run
	Idem        map[string]IdemRecord
	Proofs      map[string]ProofRecord
	ProofOps    map[string]string
	Snapshots   map[int64]*Snapshot
	LastProofID string
}
