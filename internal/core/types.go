package core

import (
	"encoding/json"

	"msgcat/internal/icu"
)

type Entry struct {
	Key         string       `json:"key"`
	Text        string       `json:"text"` // newline-normalized
	Context     string       `json:"context,omitempty"`
	AST         *icu.Message `json:"ast"`
	ParseErrors []string     `json:"parseErrors,omitempty"`
}

type Version struct {
	ID          string            `json:"id"` // v-<content hash>
	Language    string            `json:"language"`
	Role        string            `json:"role"` // base | target
	Fingerprint string            `json:"fingerprint"`
	Entries     map[string]*Entry `json:"entries"`
	Seq         int64             `json:"seq"`
}

type Import struct {
	ID        string `json:"id"`
	Language  string `json:"language"`
	Role      string `json:"role"`
	Filename  string `json:"filename"`
	VersionID string `json:"versionID"`
	BlobHash  string `json:"blobHash"`
	Seq       int64  `json:"seq"`
}

type Mapping struct {
	ID            string `json:"id"`
	From          string `json:"from"`
	To            string `json:"to"`
	BaseVersionID string `json:"baseVersionID"`
	Seq           int64  `json:"seq"`
}

type Waiver struct {
	ID        string `json:"id"`
	IssueID   string `json:"issueID"`
	Reason    string `json:"reason"`
	ExpiresAt int64  `json:"expiresAt"` // unix seconds
	Seq       int64  `json:"seq"`
}

type Issue struct {
	ID       string `json:"id"`
	Language string `json:"language"`
	Key      string `json:"key"`
	Type     string `json:"type"` // missing, extra, placeholder_mismatch, placeholder_type_drift, plural_category_missing, unbalanced_tags, parse_error
	Detail   string `json:"detail"`
}

type Run struct {
	ID              string   `json:"id"`
	Language        string   `json:"language"`
	BaseVersionID   string   `json:"baseVersionID"`
	TargetVersionID string   `json:"targetVersionID"`
	ParserVersion   string   `json:"parserVersion"`
	MappingHash     string   `json:"mappingHash"`
	MappingIDs      []string `json:"mappingIDs"`
	Issues          []*Issue `json:"issues"`
	Seq             int64    `json:"seq"`
}

type OpRecord struct {
	OpID        string          `json:"opID"`
	Kind        string          `json:"kind"`
	RequestHash string          `json:"requestHash"`
	Status      int             `json:"status"`
	Response    json.RawMessage `json:"response"`
	Batch       *BatchState     `json:"batch,omitempty"`
}

type LangReceipt struct {
	Language        string `json:"language"`
	Status          string `json:"status"` // applied | conflict | error
	VersionID       string `json:"versionID,omitempty"`
	PreviousVersion string `json:"previousVersionID,omitempty"`
	Receipt         string `json:"receipt,omitempty"`
	Error           string `json:"error,omitempty"`
}

type BatchState struct {
	Receipts []*LangReceipt `json:"receipts"`
}

type ProofMeta struct {
	ID         string `json:"id"`
	SnapshotID string `json:"snapshotID"`
	Hash       string `json:"hash"`
	Seq        int64  `json:"seq"`
}

type State struct {
	Seq          int64                 `json:"seq"`
	BaseLanguage string                `json:"baseLanguage"`
	Versions     map[string]*Version   `json:"versions"`
	Current      map[string]string     `json:"current"` // language -> version ID
	Imports      map[string]*Import    `json:"imports"`
	Mappings     map[string]*Mapping   `json:"mappings"`
	Waivers      map[string]*Waiver    `json:"waivers"`
	Runs         map[string]*Run       `json:"runs"`
	Ops          map[string]*OpRecord  `json:"ops"`
	Proofs       map[string]*ProofMeta `json:"proofs"`
	LatestProof  string                `json:"latestProof"`
}

func NewState() *State {
	st := &State{}
	st.ensure()
	return st
}

func (st *State) ensure() {
	if st.Versions == nil {
		st.Versions = map[string]*Version{}
	}
	if st.Current == nil {
		st.Current = map[string]string{}
	}
	if st.Imports == nil {
		st.Imports = map[string]*Import{}
	}
	if st.Mappings == nil {
		st.Mappings = map[string]*Mapping{}
	}
	if st.Waivers == nil {
		st.Waivers = map[string]*Waiver{}
	}
	if st.Runs == nil {
		st.Runs = map[string]*Run{}
	}
	if st.Ops == nil {
		st.Ops = map[string]*OpRecord{}
	}
	if st.Proofs == nil {
		st.Proofs = map[string]*ProofMeta{}
	}
}

type Snapshot struct {
	ID            string            `json:"id"`
	Seq           int64             `json:"seq"`
	BaseLanguage  string            `json:"baseLanguage"`
	Versions      map[string]string `json:"versions"`
	MappingHash   string            `json:"mappingHash"`
	WaiverHash    string            `json:"waiverHash"`
	ParserVersion string            `json:"parserVersion"`
}
