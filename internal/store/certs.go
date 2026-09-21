package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"catalogcheck/internal/validate"
)

type catalogLike = struct {
	Entries []struct {
		Key     string `json:"key"`
		Message string `json:"message"`
		Context string `json:"context,omitempty"`
	} `json:"entries"`
	Parsed map[string]struct{} `json:"-"`
}

// CertIssue is one issue in the certificate.
type CertIssue struct {
	Code        string   `json:"code"`
	Language    string   `json:"language"`
	Key         string   `json:"key,omitempty"`
	Detail      string   `json:"detail,omitempty"`
	Expected    []string `json:"expected,omitempty"`
	Actual      []string `json:"actual,omitempty"`
	Fingerprint string   `json:"fingerprint"`
}

// CertExemption records a waiver included in the certificate.
type CertExemption struct {
	ID               string `json:"id"`
	IssueFingerprint string `json:"issue_fingerprint"`
	Language         string `json:"language"`
	Key              string `json:"key,omitempty"`
	Code             string `json:"code,omitempty"`
	Reason           string `json:"reason"`
	Owner            string `json:"owner,omitempty"`
	CreatedAt        string `json:"created_at"`
	ExpiresAt        string `json:"expires_at"`
	Status           string `json:"status"` // active or expired
}

// CertLanguage is one target language section.
type CertLanguage struct {
	Language          string `json:"language"`
	TargetVersionID   string `json:"target_version_id"`
	TargetFingerprint string `json:"target_fingerprint"`
	ResultID          string `json:"result_id"`
	IssueTotal        int    `json:"issue_total"`
	ActiveCount       int    `json:"active_count"`
	ExemptedCount     int    `json:"exempted_count"`
	ExpiredCount      int    `json:"expired_count"`
}

// Cert is the deterministic machine-readable release certificate.
type Cert struct {
	Kind              string                 `json:"kind"`
	CertVersion       int                    `json:"cert_version"`
	ParserVersion     string                 `json:"parser_version"`
	SnapshotID        string                 `json:"snapshot_id"`
	BasisHash         string                 `json:"basis_hash"`
	BaselineLanguage  string                 `json:"baseline_language"`
	BaseVersionID     string                 `json:"base_version_id"`
	BaseFingerprint   string                 `json:"base_fingerprint"`
	Languages         []CertLanguage         `json:"languages"`
	InputFingerprints map[string]string      `json:"input_fingerprints"`
	MappingEdges      []validate.MappingEdge `json:"mapping_edges"`
	ActiveIssueCount  int                    `json:"active_issue_count"`
	Exemptions        []CertExemption        `json:"exemptions"`
	Issues            []CertIssue            `json:"issues"`
	Releasable        bool                   `json:"releasable"`
}

// certBasis is the hash of everything a certificate depends on. It includes
// expiry status (not wall-clock instants), so equal states and equal expiry
// determinations produce equal certificates.
func (s *Store) certBasisLocked(now time.Time) string {
	type exStatus struct {
		FP, Lang, Status, Expires string
	}
	exs := []exStatus{}
	for _, e := range s.st.Exemptions {
		if e.RevokedAt != nil {
			continue
		}
		status := "active"
		expires := e.ExpiresAt.UTC().Format(time.RFC3339)
		if !e.Active(now) {
			status = "expired"
		}
		exs = append(exs, exStatus{e.IssueFingerprint, e.Language, status, expires})
	}
	sort.Slice(exs, func(i, j int) bool {
		if exs[i].Lang != exs[j].Lang {
			return exs[i].Lang < exs[j].Lang
		}
		return exs[i].FP < exs[j].FP
	})
	type doc struct {
		Parser     string                 `json:"parser"`
		Base       string                 `json:"base"`
		LangVer    map[string]string      `json:"langver"`
		Edges      []validate.MappingEdge `json:"edges"`
		Exemptions []exStatus             `json:"exemptions"`
	}
	d := doc{Parser: s.st.ParserVersion, LangVer: map[string]string{}}
	if len(s.st.BaseChain) > 0 {
		id := s.st.BaseChain[len(s.st.BaseChain)-1]
		d.Base = id + ":" + s.st.Versions[id].Fingerprint
	}
	for k, v := range s.st.LangVersion {
		d.LangVer[k] = v + ":" + s.st.Versions[v].Fingerprint
	}
	d.Edges = append([]validate.MappingEdge(nil), s.st.Edges...)
	d.Exemptions = exs
	b, _ := json.Marshal(d)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// CertBasis exposes the basis hash for the UI.
func (s *Store) CertBasis(now time.Time) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.certBasisLocked(now)
}

// GenerateCert builds, persists and returns the current certificate.
func (s *Store) GenerateCert(pinnedSnapshot string, now time.Time) (*Cert, []byte, *CertMeta, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pinnedSnapshot != "" && pinnedSnapshot != s.st.snapshot().ID {
		return nil, nil, nil, false, fmt.Errorf("%w: 快照 %s 已变化，不能为旧快照生成最新证明", ErrConflict, pinnedSnapshot)
	}
	if len(s.st.BaseChain) == 0 {
		return nil, nil, nil, false, errors.New("尚无基准目录")
	}
	// Deterministic fast path: an existing complete certificate for the same
	// basis is byte-identical and creates no new version or commit.
	if s.st.LatestCertID != "" {
		for _, c := range s.st.Certs {
			if c.ID == s.st.LatestCertID && !c.BlobMissing && c.BasisHash == s.certBasisLocked(now) {
				b, err := s.readBlob(c.BlobPath)
				if err == nil {
					var existing Cert
					if json.Unmarshal(b, &existing) == nil {
						return &existing, b, &c, true, nil
					}
				}
			}
		}
	}
	baseVer := s.st.Versions[s.st.BaseChain[len(s.st.BaseChain)-1]]
	cert := &Cert{
		Kind: "catalog-release-proof", CertVersion: 1,
		ParserVersion:    s.st.ParserVersion,
		BaselineLanguage: s.st.BaselineLanguage,
		BaseVersionID:    baseVer.ID, BaseFingerprint: baseVer.Fingerprint,
		InputFingerprints: map[string]string{s.st.BaselineLanguage: baseVer.Fingerprint},
		MappingEdges:      append([]validate.MappingEdge(nil), s.st.Edges...),
	}
	exSeen := map[string]bool{}
	langList := sortedLangKeys(s.st.LangVersion)
	for _, lang := range langList {
		rid, ok := s.currentResultIDLocked(lang)
		if !ok {
			return nil, nil, nil, false, fmt.Errorf("语言 %s 缺少校验结果", lang)
		}
		meta := s.st.Results[rid]
		stale, reason := s.resultStaleLocked(meta)
		if stale {
			return nil, nil, nil, false, fmt.Errorf("语言 %s 的校验结果已失效（%s），不能生成看似最新的证明", lang, reason)
		}
		r, err := s.loadResult(rid)
		if err != nil {
			return nil, nil, nil, false, err
		}
		cl := CertLanguage{
			Language: lang, TargetVersionID: meta.TargetVersionID,
			TargetFingerprint: meta.TargetFP, ResultID: rid, IssueTotal: len(r.Issues),
		}
		cert.InputFingerprints[lang] = meta.TargetFP
		for _, is := range r.Issues {
			cert.Issues = append(cert.Issues, CertIssue{
				Code: is.Code, Language: is.Language, Key: is.Key, Detail: is.Detail,
				Expected: is.Expected, Actual: is.Actual, Fingerprint: is.Fingerprint,
			})
			if e := s.exemptionFor(is.Fingerprint, lang, now); e != nil {
				if e.Active(now) {
					cl.ExemptedCount++
				} else if e.RevokedAt == nil {
					cl.ExpiredCount++
					cl.ActiveCount++
					cert.ActiveIssueCount++
				} else {
					cl.ActiveCount++
					cert.ActiveIssueCount++
				}
				ck := e.ID
				if !exSeen[ck] {
					exSeen[ck] = true
					status := "active"
					if !e.Active(now) && e.RevokedAt == nil {
						status = "expired"
					}
					cert.Exemptions = append(cert.Exemptions, CertExemption{
						ID: e.ID, IssueFingerprint: e.IssueFingerprint, Language: e.Language,
						Key: e.Key, Code: e.Code, Reason: e.Reason, Owner: e.Owner,
						CreatedAt: e.CreatedAt.UTC().Format(time.RFC3339),
						ExpiresAt: e.ExpiresAt.UTC().Format(time.RFC3339), Status: status,
					})
				}
			} else {
				cl.ActiveCount++
				cert.ActiveIssueCount++
			}
		}
		cert.Languages = append(cert.Languages, cl)
	}
	sort.Slice(cert.Issues, func(i, j int) bool {
		if cert.Issues[i].Language != cert.Issues[j].Language {
			return cert.Issues[i].Language < cert.Issues[j].Language
		}
		if cert.Issues[i].Code != cert.Issues[j].Code {
			return cert.Issues[i].Code < cert.Issues[j].Code
		}
		return cert.Issues[i].Fingerprint < cert.Issues[j].Fingerprint
	})
	sort.Slice(cert.Exemptions, func(i, j int) bool {
		if cert.Exemptions[i].Language != cert.Exemptions[j].Language {
			return cert.Exemptions[i].Language < cert.Exemptions[j].Language
		}
		return cert.Exemptions[i].IssueFingerprint < cert.Exemptions[j].IssueFingerprint
	})
	cert.Releasable = cert.ActiveIssueCount == 0
	cert.BasisHash = s.certBasisLocked(now)
	body, err := marshalCert(cert)
	if err != nil {
		return nil, nil, nil, false, err
	}
	sum := sha256.Sum256(body)
	sha := hex.EncodeToString(sum[:])
	certID := "cert-" + cert.BasisHash[:12]
	cert.SnapshotID = s.st.snapshot().ID
	// snapshot id must be part of deterministic bytes; re-marshal after set.
	body, err = marshalCert(cert)
	if err != nil {
		return nil, nil, nil, false, err
	}
	sum = sha256.Sum256(body)
	sha = hex.EncodeToString(sum[:])
	rel, _, err := s.writeBlob("certs", body)
	if err != nil {
		return nil, nil, nil, false, err
	}
	// Drop any earlier meta records for the same basis; cert content is
	// byte-identical, so no new version is produced.
	kept := s.st.Certs[:0]
	for _, c := range s.st.Certs {
		if c.BasisHash != cert.BasisHash {
			kept = append(kept, c)
		}
	}
	s.st.Certs = kept
	meta := CertMeta{ID: certID, SHA256: sha, BlobPath: rel, Seq: len(s.st.Certs) + 1,
		SnapshotID: cert.SnapshotID, BasisHash: cert.BasisHash, CreatedAt: now}
	s.st.Certs = append(s.st.Certs, meta)
	s.st.LatestCertID = certID
	if err := s.commitLocked(); err != nil {
		return nil, nil, nil, false, err
	}
	current := meta.BasisHash == s.certBasisLocked(now)
	return cert, body, &meta, current, nil
}

// marshalCert sorts all map keys and omits no fields, giving stable bytes.
func marshalCert(c *Cert) ([]byte, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, b, "", "  "); err != nil {
		return nil, err
	}
	pretty.WriteByte('\n')
	return pretty.Bytes(), nil
}

// CertBytes returns the bytes and metadata of a stored certificate.
func (s *Store) CertBytes(certID string) ([]byte, CertMeta, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.st.Certs {
		if c.ID == certID {
			if c.BlobMissing {
				return nil, c, false, errors.New("证明文件完整性校验失败，已标记为不可用")
			}
			b, err := s.readBlob(c.BlobPath)
			return b, c, true, err
		}
	}
	return nil, CertMeta{}, false, errors.New("证明不存在")
}

// CertMetaList lists generated certificates with current/historical flag.
func (s *Store) CertMetaList(now time.Time) []struct {
	Meta    CertMeta `json:"meta"`
	Current bool     `json:"current"`
} {
	s.mu.Lock()
	defer s.mu.Unlock()
	basis := s.certBasisLocked(now)
	out := make([]struct {
		Meta    CertMeta `json:"meta"`
		Current bool     `json:"current"`
	}, 0, len(s.st.Certs))
	for _, c := range s.st.Certs {
		out = append(out, struct {
			Meta    CertMeta `json:"meta"`
			Current bool     `json:"current"`
		}{c, !c.BlobMissing && c.BasisHash == basis})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Meta.Seq > out[j].Meta.Seq })
	return out
}
