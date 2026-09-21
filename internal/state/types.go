// Package state defines the durable application state: catalog versions,
// rename mappings, exemptions, batch-fix receipts and idempotent operations.
package state

import (
	"encoding/json"
	"time"
)

// BaselineRec records one imported version of the baseline language.
type BaselineRec struct {
	Language string `json:"language"`
	Version  string `json:"version"` // fingerprint
	RawBlob  string `json:"raw_blob"`
	Filename string `json:"filename,omitempty"`
	Imported time.Time `json:"imported"`
}

// LangState is the current state of one target language.
type LangState struct {
	Language string `json:"language"`
	Version  string `json:"version"`
	RawBlob  string `json:"raw_blob"`
	Filename string `json:"filename,omitempty"`
	Imported time.Time `json:"imported"`
}

// Upload is one historical raw-file upload, kept so every import can be
// re-downloaded byte-for-byte even when its content version already existed.
type Upload struct {
	ID       string    `json:"id"`
	Role     string    `json:"role"` // baseline | target
	Language string    `json:"language"`
	Version  string    `json:"version"`
	Blob     string    `json:"blob"`
	Filename string    `json:"filename,omitempty"`
	At       time.Time `json:"at"`
}

// Rename is one confirmed edge oldKey -> newKey in the baseline lineage.
type Rename struct {
	OldKey     string    `json:"old_key"`
	NewKey     string    `json:"new_key"`
	Language   string    `json:"language"` // baseline language at confirmation time
	ConfirmedAt time.Time `json:"confirmed_at"`
	FromVersion string   `json:"from_version"`
	ToVersion   string   `json:"to_version"`
}

// Exemption silences one identified issue until an optional deadline.
type Exemption struct {
	ID        string    `json:"id"`
	IssueKey  string    `json:"issue_key"`
	Reason    string    `json:"reason"`
	GrantedAt time.Time `json:"granted_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// BatchItem is the per-language outcome of a batch fix.
type BatchItem struct {
	Language   string     `json:"language"`
	Status     string     `json:"status"` // success | conflict
	OldVersion string     `json:"old_version"`
	NewVersion string     `json:"new_version,omitempty"`
	Blob       string     `json:"blob,omitempty"`
	Reason     string     `json:"reason,omitempty"`
	AppliedAt  *time.Time `json:"applied_at,omitempty"`
}

// Batch is one idempotent batch-fix operation.
type Batch struct {
	OpID      string              `json:"op_id"`
	Items     []BatchItem         `json:"items"`
	CreatedAt time.Time           `json:"created_at"`
	UpdatedAt time.Time           `json:"updated_at"`
}

// Operation records the idempotent processing result of a mutating request.
type Operation struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Request   string          `json:"request"` // canonical hash of request content
	At        time.Time       `json:"at"`
	Response  json.RawMessage `json:"response"`
	Snapshot  string          `json:"snapshot"` // state hash after applying
}

// CertMeta records a generated release certificate.
type CertMeta struct {
	ID        string    `json:"id"`
	Snapshot  string    `json:"snapshot"`
	Blob      string    `json:"blob"`
	CreatedAt time.Time `json:"created_at"`
}

// ValidationRec caches a validation run (deterministic function of inputs).
type ValidationRec struct {
	ID         string    `json:"id"`
	Parser     string    `json:"parser"`
	BaselineV  string    `json:"baseline_version"`
	TargetLang string    `json:"target_language"`
	TargetV    string    `json:"target_version"`
	Mappings   string    `json:"mappings_fp"`
	CreatedAt  time.Time `json:"created_at"`
	Issues     []Issue   `json:"issues"`
}

// Pin identifies one committed state for consistent reads and conflicts.
type Pin struct {
	Snapshot      string `json:"snapshot"`
	Seq           int64  `json:"seq"`
	Parser        string `json:"parser"`
	BaselineVer   string `json:"baseline_version"`
	MappingsFP    string `json:"mappings_fp"`
	BaselineLang  string `json:"baseline_language"`
}

// Data is the full durable state. Meta fields are bookkeeping excluded from
// the content hash that drives snapshot identity.
type Data struct {
	Seq         int64                    `json:"seq"`
	Baseline    *BaselineRec             `json:"baseline,omitempty"`
	Langs       map[string]*LangState    `json:"langs"`
	Uploads     []Upload                 `json:"uploads"`
	Renames     []Rename                 `json:"renames"`
	Exemptions  []Exemption              `json:"exemptions"`
	Batches     map[string]*Batch        `json:"batches"`
	CommittedAt time.Time                `json:"committed_at"`

	Ops        map[string]*Operation  `json:"ops,omitempty"`
	Certs      []CertMeta             `json:"certs,omitempty"`
	Validations map[string]*ValidationRec `json:"validations,omitempty"`
}

// NewData returns an empty initialized state.
func NewData() *Data {
	return &Data{
		Langs:       map[string]*LangState{},
		Batches:     map[string]*Batch{},
		Ops:         map[string]*Operation{},
		Validations: map[string]*ValidationRec{},
	}
}

// Clone returns a deep copy of the state so callers can never mutate the
// committed snapshot through a returned pointer.
func (d *Data) Clone() *Data {
	if d == nil {
		return nil
	}
	b, _ := json.Marshal(d)
	var out Data
	_ = json.Unmarshal(b, &out)
	return &out
}
