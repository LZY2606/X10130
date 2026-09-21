package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"msgcheck/internal/state"
	"msgcheck/internal/store"
)

// Cert is the machine-readable release certificate. JSON field order and
// all nested lists are fixed, so regenerating for the same committed state
// yields byte-identical output.
type Cert struct {
	Schema              string        `json:"schema"`
	CertificateID       string        `json:"certificate_id"`
	Snapshot            string        `json:"snapshot"`
	Sequence            int64         `json:"sequence"`
	ParserVersion       string        `json:"parser_version"`
	BasisTime           time.Time     `json:"basis_time"`
	BaselineLanguage    string        `json:"baseline_language"`
	BaselineVersion     string        `json:"baseline_version"`
	TargetLanguages     []CertLang    `json:"target_languages"`
	Inputs              CertInputs    `json:"inputs"`
	Mappings            CertMappings  `json:"mappings"`
	ValidationResults   []CertVal     `json:"validation_results"`
	UnexemptedIssueCount int          `json:"unexempted_issue_count"`
	ExemptedIssueCount   int          `json:"exempted_issue_count"`
	Exemptions          []CertExempt  `json:"exemptions"`
}

// CertLang is one target language fingerprint row.
type CertLang struct {
	Language string `json:"language"`
	Version  string `json:"version"`
}

// CertInputs pins all raw input fingerprints.
type CertInputs struct {
	BaselineRaw string     `json:"baseline_raw"`
	Targets     []CertLang `json:"targets"`
}

// CertMappings pins the rename chain.
type CertMappings struct {
	Fingerprint string       `json:"fingerprint"`
	Edges       []CertEdge   `json:"edges"`
}

// CertEdge is one confirmed rename.
type CertEdge struct {
	OldKey string `json:"old_key"`
	NewKey string `json:"new_key"`
}

// CertVal binds one validation result to its exact inputs.
type CertVal struct {
	ValidationID    string   `json:"validation_id"`
	Language        string   `json:"language"`
	ParserVersion   string   `json:"parser_version"`
	BaselineVersion string   `json:"baseline_version"`
	TargetVersion   string   `json:"target_version"`
	MappingsFP      string   `json:"mappings_fp"`
	IssueCount      int      `json:"issue_count"`
	Unexempted      int      `json:"unexempted_count"`
	Exempted        int      `json:"exempted_count"`
}

// CertExempt is one exemption included in the proof.
type CertExempt struct {
	IssueKey  string     `json:"issue_key"`
	Reason    string     `json:"reason"`
	GrantedAt time.Time  `json:"granted_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// CertSummary describes a stored certificate.
type CertSummary struct {
	ID        string    `json:"id"`
	Snapshot  string    `json:"snapshot"`
	CreatedAt time.Time `json:"created_at"`
	Current   bool      `json:"current"`
}

// CertResult is returned from GenerateCert.
type CertResult struct {
	Cert      Cert       `json:"cert"`
	Bytes     []byte     `json:"-"`
	Replayed  bool       `json:"replayed"`
	Snapshot  state.Pin  `json:"snapshot"`
}

func certBlobID(content []byte) string {
	sum := sha256.Sum256(content)
	return "cert_" + hex.EncodeToString(sum[:20])
}

// GenerateCert produces a deterministic certificate for one committed
// snapshot. If a certificate for that snapshot exists, it is replayed
// byte-for-byte. It never mixes states: assembly, blob write and index
// update happen under one lock, and a crash while writing leaves no
// partially-indexed proof.
func (a *App) GenerateCert() (*CertResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := a.advanceLocked(); err != nil {
		return nil, err
	}
	if a.data.Baseline == nil {
		return nil, &BadRequestError{Reason: "no baseline catalog; cannot certify"}
	}
	pin := a.pinLocked()

	// Validate every language so the proof covers the full snapshot; any
	// stale validation is rebuilt against current inputs under this same
	// lock-held state.
	var valIDs []string
	for lang := range a.data.Langs {
		rec, err := a.validateLocked(lang)
		if err != nil {
			return nil, err
		}
		valIDs = append(valIDs, rec.ID)
	}
	sort.Strings(valIDs)

	c := Cert{
		Schema:           "message-catalog-release-cert/v1",
		Snapshot:         pin.Snapshot,
		Sequence:         pin.Seq,
		ParserVersion:    pin.Parser,
		BasisTime:        a.data.CommittedAt,
		BaselineLanguage: a.data.Baseline.Language,
		BaselineVersion:  a.data.Baseline.Version,
		Inputs: CertInputs{BaselineRaw: a.data.Baseline.RawBlob},
		Mappings: CertMappings{Fingerprint: pin.MappingsFP},
	}
	langs := make([]string, 0, len(a.data.Langs))
	for l := range a.data.Langs {
		langs = append(langs, l)
	}
	sort.Strings(langs)
	for _, l := range langs {
		row := CertLang{Language: l, Version: a.data.Langs[l].Version}
		c.TargetLanguages = append(c.TargetLanguages, row)
		c.Inputs.Targets = append(c.Inputs.Targets, row)
	}
	for _, r := range a.data.Renames {
		c.Mappings.Edges = append(c.Mappings.Edges, CertEdge{OldKey: r.OldKey, NewKey: r.NewKey})
	}
	sort.Slice(c.Mappings.Edges, func(i, j int) bool {
		if c.Mappings.Edges[i].OldKey != c.Mappings.Edges[j].OldKey {
			return c.Mappings.Edges[i].OldKey < c.Mappings.Edges[j].OldKey
		}
		return c.Mappings.Edges[i].NewKey < c.Mappings.Edges[j].NewKey
	})
	for _, id := range valIDs {
		rec := a.data.Validations[id]
		if a.invalidReasonLocked(rec) != "" {
			// A current-state cert cannot be assembled while an input is
			// stale for a language. validateLocked above guarantees current
			// results, so this is defensive.
			return nil, &ConflictError{Reason: "validation for " + rec.TargetLang + " is not based on current inputs", Current: pin}
		}
		ex, unex := a.countExemptedLocked(rec.Issues)
		c.ValidationResults = append(c.ValidationResults, CertVal{
			ValidationID: rec.ID, Language: rec.TargetLang, ParserVersion: rec.Parser,
			BaselineVersion: rec.BaselineV, TargetVersion: rec.TargetV, MappingsFP: rec.Mappings,
			IssueCount: len(rec.Issues), Unexempted: unex, Exempted: ex,
		})
		c.ExemptedIssueCount += ex
		c.UnexemptedIssueCount += unex
	}
	// Exemptions section: every active exemption, sorted by issue key.
	seenEx := map[string]bool{}
	for _, ex := range a.data.Exemptions {
		if seenEx[ex.IssueKey] {
			continue
		}
		seenEx[ex.IssueKey] = true
		c.Exemptions = append(c.Exemptions, CertExempt{
			IssueKey: ex.IssueKey, Reason: ex.Reason,
			GrantedAt: ex.GrantedAt, ExpiresAt: ex.ExpiresAt,
		})
	}
	sort.Slice(c.Exemptions, func(i, j int) bool { return c.Exemptions[i].IssueKey < c.Exemptions[j].IssueKey })

	// Deterministic serialization with the ID field present but empty; the
	// ID is then derived from those bytes and filled in, so the same state
	// always produces the same ID and bytes.
	c.CertificateID = ""
	body, err := json.MarshalIndent(&c, "", "  ")
	if err != nil {
		return nil, err
	}
	id := certBlobID(body)
	// Replay exact prior bytes if this snapshot+content was already proven.
	for _, cm := range a.data.Certs {
		if cm.Snapshot == pin.Snapshot && cm.ID == id {
			if existing, rerr := a.st.ReadBlob(cm.Blob); rerr == nil {
				var old Cert
				_ = json.Unmarshal(existing, &old)
				return &CertResult{Cert: old, Bytes: existing, Replayed: true, Snapshot: pin}, nil
			}
		}
	}
	// Cert id is derived from the body; emit final form.
	c.CertificateID = id
	body, err = json.MarshalIndent(&c, "", "  ")
	if err != nil {
		return nil, err
	}
	blob := certBlobID(body)

	// Fault boundary: write blob via the store; injected crashes leave no
	// index entry and no truncated reference.
	if a.CrashCert == "tmp" {
		path := a.st.BlobPath(blob)
		tmp := path + ".tmp"
		if werr := os.WriteFile(tmp, body, 0o644); werr != nil {
			return nil, werr
		}
		return nil, store.ErrCrashSimulation
	}
	if err := a.st.WriteBlob(blob, body); err != nil {
		if errors.Is(err, store.ErrCrashSimulation) {
			return nil, err
		}
		return nil, err
	}
	if a.CrashCert == "index" {
		return nil, store.ErrCrashSimulation
	}
	a.data.Certs = append(a.data.Certs, state.CertMeta{
		ID: id, Snapshot: pin.Snapshot, Blob: blob, CreatedAt: a.now(),
	})
	if err := a.commitLocked(a.now()); err != nil {
		return nil, err
	}
	return &CertResult{Cert: c, Bytes: body, Snapshot: a.pinLocked()}, nil
}

func (a *App) certSummariesLocked() []CertSummary {
	out := make([]CertSummary, 0, len(a.data.Certs))
	cur := a.pinLocked().Snapshot
	for _, cm := range a.data.Certs {
		out = append(out, CertSummary{
			ID: cm.ID, Snapshot: cm.Snapshot, CreatedAt: cm.CreatedAt, Current: cm.Snapshot == cur,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// Certs lists known certificates with current/historical status.
func (a *App) Certs() []CertSummary {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.advanceLocked()
	return a.certSummariesLocked()
}

// CertBytes returns the raw bytes of a stored certificate.
func (a *App) CertBytes(id string) ([]byte, *state.CertMeta, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.advanceLocked()
	for i := range a.data.Certs {
		cm := &a.data.Certs[i]
		if cm.ID == id {
			b, err := a.st.ReadBlob(cm.Blob)
			if err != nil {
				return nil, nil, fmt.Errorf("certificate blob missing: %w", err)
			}
			return b, cm, nil
		}
	}
	return nil, nil, &NotFoundError{Reason: "unknown certificate " + id}
}
