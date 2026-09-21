package store

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"

	"catalogcheck/internal/validate"
)

// Upload is one exact raw file upload, byte-for-byte downloadable.
type Upload struct {
	ID         string    `json:"id"`
	Filename   string    `json:"filename"`
	SHA256     string    `json:"sha256"`
	Size       int       `json:"size"`
	ReceivedAt time.Time `json:"received_at"`
	BlobPath   string    `json:"blob_path"`
	Missing    bool      `json:"missing,omitempty"`
}

// VersionRef is a catalog version identified by content fingerprint.
type VersionRef struct {
	ID             string    `json:"id"`
	Language       string    `json:"language"`
	Role           string    `json:"role"`
	Fingerprint    string    `json:"fingerprint"`
	Keys           int       `json:"keys"`
	CanonicalBlob  string    `json:"canonical_blob"`
	UploadIDs      []string  `json:"upload_ids"`
	CreatedAt      time.Time `json:"created_at"`
	ParserVersion  string    `json:"parser_version"`
	Seq            int       `json:"seq"`
	PreviousBaseID string    `json:"previous_base_id,omitempty"`
	BlobMissing    bool      `json:"blob_missing,omitempty"`
}

// Exemption is a time-limited waiver for one issue fingerprint.
type Exemption struct {
	ID               string     `json:"id"`
	OperationID      string     `json:"operation_id,omitempty"`
	IssueFingerprint string     `json:"issue_fingerprint"`
	Language         string     `json:"language"`
	Key              string     `json:"key"`
	Code             string     `json:"code"`
	Reason           string     `json:"reason"`
	Owner            string     `json:"owner"`
	CreatedAt        time.Time  `json:"created_at"`
	ExpiresAt        time.Time  `json:"expires_at"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
	SnapshotID       string     `json:"snapshot_id"`
}

// Active reports whether the exemption is in force at instant now.
func (e Exemption) Active(now time.Time) bool {
	if e.RevokedAt != nil {
		return false
	}
	return now.Before(e.ExpiresAt)
}

// ResultMeta is state-side metadata for a validation result.
type ResultMeta struct {
	ID              string    `json:"id"`
	Language        string    `json:"language"`
	BaseVersionID   string    `json:"base_version_id"`
	TargetVersionID string    `json:"target_version_id"`
	BaseFP          string    `json:"base_fingerprint"`
	TargetFP        string    `json:"target_fingerprint"`
	MappingHash     string    `json:"mapping_hash"`
	ParserVersion   string    `json:"parser_version"`
	Seq             int       `json:"seq"`
	BlobPath        string    `json:"blob_path"`
	CreatedAt       time.Time `json:"created_at"`
	BlobMissing     bool      `json:"blob_missing,omitempty"`
}

// SubReceipt records one language's outcome inside a batch operation.
type SubReceipt struct {
	Language      string    `json:"language"`
	Status        string    `json:"status"` // success, conflict
	VersionID     string    `json:"version_id,omitempty"`
	Fingerprint   string    `json:"fingerprint,omitempty"`
	ContentSHA    string    `json:"content_sha,omitempty"`
	Keys          int       `json:"keys,omitempty"`
	Reason        string    `json:"reason,omitempty"`
	ExpectedVerID string    `json:"expected_version_id,omitempty"`
	ActualVerID   string    `json:"actual_version_id,omitempty"`
	RecordedAt    time.Time `json:"recorded_at"`
}

// IdemRecord persists the result of a client-supplied operation id.
type IdemRecord struct {
	Key        string                `json:"key"`
	Kind       string                `json:"kind"`
	RequestSHA string                `json:"request_sha"`
	Status     int                   `json:"status"`
	Response   []byte                `json:"response"`
	CreatedAt  time.Time             `json:"created_at"`
	Subs       map[string]SubReceipt `json:"subs,omitempty"`
}

// CertMeta describes a generated release certificate.
type CertMeta struct {
	ID          string    `json:"id"`
	SHA256      string    `json:"sha256"`
	BlobPath    string    `json:"blob_path"`
	Seq         int       `json:"seq"`
	SnapshotID  string    `json:"snapshot_id"`
	BasisHash   string    `json:"basis_hash"`
	CreatedAt   time.Time `json:"created_at"`
	BlobMissing bool      `json:"blob_missing,omitempty"`
}

// State is the single durable state machine document.
type State struct {
	FormatVersion    int                    `json:"format_version"`
	ParserVersion    string                 `json:"parser_version"`
	Seq              int                    `json:"seq"`
	Root             string                 `json:"root"`
	BaselineLanguage string                 `json:"baseline_language,omitempty"`
	BaseChain        []string               `json:"base_chain"`
	LangVersion      map[string]string      `json:"lang_version"`
	Versions         map[string]VersionRef  `json:"versions"`
	Uploads          map[string]Upload      `json:"uploads"`
	Edges            []validate.MappingEdge `json:"edges"`
	Exemptions       []Exemption            `json:"exemptions"`
	Results          map[string]ResultMeta  `json:"results"`
	Idem             map[string]IdemRecord  `json:"idem"`
	Certs            []CertMeta             `json:"certs"`
	LatestCertID     string                 `json:"latest_cert_id,omitempty"`
	Warnings         []string               `json:"startup_warnings,omitempty"`
}

// Snapshot identifies one committed state.
type Snapshot struct {
	Seq  int    `json:"seq"`
	Root string `json:"root"`
	ID   string `json:"id"`
}

func (s *State) snapshot() Snapshot {
	return Snapshot{Seq: s.Seq, Root: s.Root, ID: snapshotID(s.Seq, s.Root)}
}

func snapshotID(seq int, root string) string {
	h := sha256.New()
	b := strconv.AppendInt(nil, int64(seq), 10)
	h.Write(b)
	h.Write([]byte{0})
	h.Write([]byte(root))
	return "snap-" + strconv.Itoa(seq) + "-" + hex.EncodeToString(h.Sum(nil))[:12]
}
