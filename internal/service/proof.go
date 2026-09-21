package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"messagecatalog/internal/catalog"
	"messagecatalog/internal/store"
)

// ProofIssueCount groups counts per language.
type ProofIssueCount struct {
	Language      string `json:"language"`
	ValidationID  string `json:"validationId"`
	Open          int    `json:"open"`
	Exempt        int    `json:"exempt"`
	Expired       int    `json:"expired"`
	Invalidated   bool   `json:"invalidated,omitempty"`
	InvalidReason string `json:"invalidReason,omitempty"`
}

// ProofExemption is one exemption included in the proof.
type ProofExemption struct {
	ID         string    `json:"id"`
	Language   string    `json:"language"`
	MessageKey string    `json:"messageKey"`
	Type       string    `json:"type"`
	Reason     string    `json:"reason"`
	ExpiresAt  time.Time `json:"expiresAt"`
	IssueKey   string    `json:"issueKey"`
}

// ProofInputFingerprint captures input identities.
type ProofInputFingerprint struct {
	ParserVersion string            `json:"parserVersion"`
	BaseLanguage  string            `json:"baseLanguage"`
	BaseVersionID string            `json:"baseVersionId"`
	Fingerprint   string            `json:"fingerprint"`
	Targets       map[string]string `json:"targets"`
	MappingSig    string            `json:"mappingSig"`
}

// Proof is the machine-readable release certificate.
type Proof struct {
	Kind             string                `json:"kind"`
	ProofVersion     string                `json:"proofVersion"`
	GeneratedAtState int64                 `json:"generatedAtStateSeq"`
	SnapshotStamp    string                `json:"snapshotStamp"`
	StateStamp       string                `json:"stateStamp"`
	ParserVersion    string                `json:"parserVersion"`
	Inputs           ProofInputFingerprint `json:"inputs"`
	OpenIssueCount   int                   `json:"unexemptedIssueCount"`
	Counts           []ProofIssueCount     `json:"counts"`
	Exemptions       []ProofExemption      `json:"exemptions"`
	Current          bool                  `json:"current"`
}

const proofVersion = "release-proof-1"

// GenerateProof builds a proof from one committed snapshot and atomically saves it.
func (s *Service) GenerateProof(opID string, expectedSeq int64) (*Proof, *store.ProofRecord, bool, error) {
	req := struct {
		Seq int64 `json:"expectedSeq"`
	}{expectedSeq}
	_, raw, replay, err := s.withIdempotency(opID, "proof", req, func(st *store.State, now time.Time) (int, string, error) {
		if expectedSeq != 0 && expectedSeq != st.Seq {
			return 0, "", &ConflictError{Msg: fmt.Sprintf("state changed while generating proof: expected seq %d, current %d", expectedSeq, st.Seq)}
		}
		proof, err := buildProof(st, now)
		if err != nil {
			return 0, "", err
		}
		data, err := deterministicProof(proof)
		if err != nil {
			return 0, "", err
		}
		blobName, sum, size, err := s.Store.WriteProof(data)
		if err != nil {
			return 0, "", fmt.Errorf("proof persistence failed: %w", err)
		}
		rec := store.ProofRecord{
			ID: "proof-" + randID(), SHA256: sum, Size: size,
			CreatedAt: now, Stamp: proof.StateStamp, BlobName: blobName, Current: true,
		}
		// Only this new proof describes current state; older ones stay readable
		// as historical proofs.
		for i := range st.Proofs {
			st.Proofs[i].Current = false
		}
		st.Proofs = append(st.Proofs, rec)
		out := struct {
			Proof  Proof             `json:"proof"`
			Record store.ProofRecord `json:"record"`
		}{*proof, rec}
		b, _ := json.Marshal(out)
		return 200, string(b), nil
	})
	if err != nil {
		return nil, nil, false, err
	}
	var out struct {
		Proof  Proof             `json:"proof"`
		Record store.ProofRecord `json:"record"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, nil, replay, err
	}
	return &out.Proof, &out.Record, replay, nil
}

func buildProof(st *store.State, now time.Time) (*Proof, error) {
	base := currentBaseVersion(st)
	if base == nil {
		return nil, fmt.Errorf("no baseline catalog imported")
	}
	// current validation record per target
	currentByLang := map[string]*store.ValidationRecord{}
	invalidated := false
	for i := range st.Validations {
		v := &st.Validations[i]
		if !isValidationCurrent(st, v) {
			continue
		}
		currentByLang[v.TargetLang] = v
	}
	// Every target must have a current validation result.
	for lang, role := range st.RoleByLang {
		if role != roleTarget {
			continue
		}
		v := currentByLang[lang]
		if v == nil || v.Invalidated {
			invalidated = true
		}
	}
	targets := map[string]string{}
	counts := []ProofIssueCount{}
	totalOpen := 0
	langList := make([]string, 0)
	for lang := range st.RoleByLang {
		if st.RoleByLang[lang] == roleTarget {
			langList = append(langList, lang)
		}
	}
	sort.Strings(langList)
	exemptByIssue := map[string]store.Exemption{}
	for _, e := range st.Exemptions {
		if !e.Revoked && e.ExpiresAt.After(now) {
			exemptByIssue[e.IssueKey] = e
		}
	}
	exList := []ProofExemption{}
	for _, lang := range langList {
		tid := st.CurrentByLang[lang]
		targets[lang] = tid
		v := currentByLang[lang]
		c := ProofIssueCount{Language: lang, ValidationID: func() string {
			if v != nil {
				return v.ID
			}
			return ""
		}()}
		if v == nil {
			c.Invalidated = true
			c.InvalidReason = "no current validation result"
			invalidated = true
		} else {
			c.Invalidated = v.Invalidated
			c.InvalidReason = v.InvalidReason
			for _, is := range v.Issues {
				if e, ok := exemptByIssue[is.IssueKey]; ok {
					c.Exempt++
					exList = append(exList, ProofExemption{
						ID: e.ID, Language: e.Language, MessageKey: e.MessageKey,
						Type: e.Type, Reason: e.Reason, ExpiresAt: e.ExpiresAt.UTC(),
						IssueKey: e.IssueKey,
					})
				} else {
					c.Open++
				}
			}
		}
		totalOpen += c.Open
		counts = append(counts, c)
	}
	sort.Slice(exList, func(i, j int) bool {
		if exList[i].IssueKey != exList[j].IssueKey {
			return exList[i].IssueKey < exList[j].IssueKey
		}
		return exList[i].ID < exList[j].ID
	})
	stamp := stateStamp(st)
	p := &Proof{
		Kind: proofKind(), ProofVersion: proofVersion, GeneratedAtState: st.Seq,
		SnapshotStamp: stamp, StateStamp: stamp, ParserVersion: catalog.ParserVersion,
		Inputs: ProofInputFingerprint{
			ParserVersion: catalog.ParserVersion, BaseLanguage: base.Language,
			BaseVersionID: base.ID, Fingerprint: base.Fingerprint, Targets: targets,
			MappingSig: mappingSig(st),
		},
		OpenIssueCount: totalOpen, Counts: counts, Exemptions: exList,
		Current: !invalidated,
	}
	if invalidated {
		return nil, &ConflictError{Msg: "one or more target validation results are invalidated or missing; revalidate before generating a release proof"}
	}
	return p, nil
}

func proofKind() string { return "message-catalog-release-proof" }

func deterministicProof(p *Proof) ([]byte, error) {
	// The persisted artifact must be byte-identical for identical logical state.
	// Seq is a change counter advanced even by saving the proof itself, so it is
	// omitted from the artifact (the snapshot stamp binds the content).
	contentOnly := *p
	contentOnly.GeneratedAtState = 0
	b, err := json.Marshal(&contentOnly)
	if err != nil {
		return nil, err
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

var _ = sha256.New
var _ = hex.EncodeToString
