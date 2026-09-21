package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"messagecatalog/internal/icu"
)

// ProofInputFingerprint hashes the exact inputs for one language.
type ProofInputFingerprint struct {
	Language       string `json:"language"`
	ContentVersion string `json:"contentVersion"`
	RawUploadSHA   string `json:"rawUploadSha"`
}

// ProofExemption is one exemption line in the proof.
type ProofExemption struct {
	IssueID         string `json:"issueId"`
	Language        string `json:"language"`
	Code            string `json:"code"`
	Key             string `json:"key"`
	Status          string `json:"status"` // active, expired, missing
	Reason          string `json:"reason"`
	Deadline        string `json:"deadline,omitempty"`
	BaselineVersion string `json:"baselineVersion"`
	TargetVersion   string `json:"targetVersion"`
	MappingVersion  int    `json:"mappingVersion"`
	ParserVersion   string `json:"parserVersion"`
}

// Proof is the machine-readable release certificate.
type Proof struct {
	ProofVersion      string                  `json:"proofVersion"`
	SnapshotID        string                  `json:"snapshotId"`
	StateSeq          int                     `json:"stateSeq"`
	ParserVersion     string                  `json:"parserVersion"`
	BaselineLanguage  string                  `json:"baselineLanguage"`
	BaselineVersion   string                  `json:"baselineVersion"`
	MappingVersion    int                     `json:"mappingVersion"`
	GeneratedAt       string                  `json:"generatedAt"`
	InputFingerprints []ProofInputFingerprint `json:"inputFingerprints"`
	LanguageCounts    map[string]*ProofCounts `json:"languageCounts"`
	UnexemptedTotal   int                     `json:"unexemptedTotal"`
	Exemptions        []ProofExemption        `json:"exemptions"`
}

// ProofCounts summarizes one language.
type ProofCounts struct {
	Total      int `json:"total"`
	Exempted   int `json:"exemptedActive"`
	Expired    int `json:"exemptedExpired"`
	Unexempted int `json:"unexempted"`
}

// ProofResult is returned to callers.
type ProofResult struct {
	ProofID    string `json:"proofId"`
	SnapshotID string `json:"snapshotId"`
	StateSeq   int    `json:"stateSeq"`
	Current    bool   `json:"current"`
	ByteCount  int    `json:"byteCount"`
}

// GenerateProof assembles a certificate for one fully-committed snapshot.
// The whole read happens under lock so imports/renames/expiry during assembly
// cannot mix states; identical state + exemption status => identical bytes.
func (s *Service) GenerateProof() (*ProofResult, []byte, error) {
	if err := s.checkWritable(); err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	base := s.state.Catalogs[s.state.BaselineLanguage]
	if base == nil {
		return nil, nil, &RequestError{Msg: "no baseline catalog; cannot issue a release proof"}
	}

	p := &Proof{
		ProofVersion:     "release-proof/1.0",
		StateSeq:         s.state.Seq,
		ParserVersion:    icu.Version,
		BaselineLanguage: s.state.BaselineLanguage,
		BaselineVersion:  base.Version,
		MappingVersion:   s.state.MappingVersion,
		GeneratedAt:      s.nowRFC(),
		LanguageCounts:   map[string]*ProofCounts{},
	}
	if len(s.state.Snapshots) > 0 {
		p.SnapshotID = s.state.Snapshots[len(s.state.Snapshots)-1].ID
	}

	// Ordered language list.
	langs := mapKeys(s.state.Catalogs)
	sort.Strings(langs)
	now := s.Now()

	// issue id -> view for exemption decoration, language order stable
	issueByID := map[string]*Issue{}
	for _, lang := range langs {
		up := s.state.Uploads[lang]
		rawSHA := ""
		if up != nil {
			rawSHA = up.RawBlob
		}
		p.InputFingerprints = append(p.InputFingerprints, ProofInputFingerprint{
			Language: lang, ContentVersion: s.state.Catalogs[lang].Version,
			RawUploadSHA: rawSHA,
		})
		r, _, err := s.ensureReportLocked(lang)
		if err != nil {
			return nil, nil, err
		}
		cnt := &ProofCounts{}
		for _, is := range r.Issues {
			cnt.Total++
			issueByID[is.ID] = is
			if ex, ok := s.state.Exemptions[is.ID]; ok {
				if ex.Expired(now) {
					cnt.Expired++
				} else {
					cnt.Exempted++
				}
			}
		}
		cnt.Unexempted = cnt.Total - cnt.Exempted
		p.UnexemptedTotal += cnt.Unexempted
		p.LanguageCounts[lang] = cnt
	}

	// Exemptions sorted by issue id; include expired and even missing ones
	// (issue no longer present) so reviewers see the full decision trail.
	exIDs := mapKeys(s.state.Exemptions)
	sort.Strings(exIDs)
	for _, id := range exIDs {
		ex := s.state.Exemptions[id]
		pe := ProofExemption{
			IssueID: id, Status: "active", Reason: ex.Reason,
			Deadline: ex.Deadline, BaselineVersion: ex.BaselineVersion,
			TargetVersion: ex.TargetVersion, MappingVersion: ex.MappingVersion,
			ParserVersion: ex.ParserVersion,
		}
		if ex.Expired(now) {
			pe.Status = "expired"
		}
		if is, ok := issueByID[id]; ok {
			pe.Language = is.Language
			pe.Code = is.Code
			pe.Key = is.Key
		} else {
			pe.Status = "missing"
		}
		p.Exemptions = append(p.Exemptions, pe)
	}

	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	raw = append(raw, '\n')
	sum := sha256.Sum256(raw)
	blobID := hex.EncodeToString(sum[:])

	// Dedup: same snapshot already produced identical bytes.
	var rec *ProofRecord
	for _, x := range s.state.Proofs {
		if x.Seq == p.StateSeq && x.Blob == blobID {
			rec = x
			break
		}
	}
	if rec == nil {
		if _, err := s.st.PutBlob(raw); err != nil {
			return nil, nil, err
		}
		// Mark older proofs as historical.
		for _, x := range s.state.Proofs {
			x.Current = false
		}
		rec = &ProofRecord{
			ID: "proof-" + blobID[:12], Blob: blobID,
			SnapshotID: p.SnapshotID, Seq: p.StateSeq,
			CreatedAt: s.nowRFC(), Current: true,
		}
		s.state.Proofs = append(s.state.Proofs, rec)
		// Proof record is metadata appended without changing state sequence.
		newVer := s.version + 1
		if err := s.st.Commit(s.state, s.version, newVer); err != nil {
			s.state.Proofs = s.state.Proofs[:len(s.state.Proofs)-1]
			return nil, nil, err
		}
		s.version = newVer
	}
	return &ProofResult{
		ProofID: rec.ID, SnapshotID: rec.SnapshotID, StateSeq: rec.Seq,
		Current: rec.Seq == s.state.Seq, ByteCount: len(raw),
	}, raw, nil
}

// LatestProof returns the newest proof and whether it matches current state.
func (s *Service) LatestProof() (*ProofResult, []byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.state.Proofs) == 0 {
		return nil, nil, &RequestError{Msg: "no proof generated yet"}
	}
	rec := s.state.Proofs[len(s.state.Proofs)-1]
	b, err := s.st.GetBlob(rec.Blob)
	if err != nil {
		return nil, nil, fmt.Errorf("proof unreadable: %w", err)
	}
	current := rec.Seq == s.state.Seq
	return &ProofResult{
		ProofID: rec.ID, SnapshotID: rec.SnapshotID, StateSeq: rec.Seq,
		Current: current, ByteCount: len(b),
	}, b, nil
}
