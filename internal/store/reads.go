package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"catalogcheck/internal/catalog"
	"catalogcheck/internal/validate"
)

// Overview is the machine-readable full snapshot view for the UI.
type Overview struct {
	Snapshot          Snapshot               `json:"snapshot"`
	ParserVersion     string                 `json:"parser_version"`
	BaselineLanguage  string                 `json:"baseline_language"`
	BaseVersion       *VersionRef            `json:"base_version,omitempty"`
	Languages         map[string]LangView    `json:"languages"`
	Edges             []validate.MappingEdge `json:"edges"`
	PendingRenames    []PendingTransition    `json:"pending_renames"`
	Exemptions        []ExemptionView        `json:"exemptions"`
	LatestCert        *CertMeta              `json:"latest_cert,omitempty"`
	LatestCertCurrent bool                   `json:"latest_cert_current"`
	Warnings          []string               `json:"startup_warnings,omitempty"`
	ServerNow         time.Time              `json:"server_now"`
}

// LangView is one target language in the overview.
type LangView struct {
	Version       VersionRef  `json:"version"`
	CurrentResult *ResultMeta `json:"current_result"`
	Stale         bool        `json:"stale"`
	StaleReason   string      `json:"stale_reason,omitempty"`
	IssueTotal    int         `json:"issue_total"`
	IssueActive   int         `json:"issue_active"`
	Exempted      int         `json:"exempted"`
	Expired       int         `json:"expired"`
}

// PendingTransition describes added/removed keys between two baseline versions.
type PendingTransition struct {
	FromVersionID string   `json:"from_version_id"`
	ToVersionID   string   `json:"to_version_id"`
	Added         []string `json:"added"`
	Removed       []string `json:"removed"`
}

// ExemptionView attaches expiry state to an exemption.
type ExemptionView struct {
	Exemption Exemption `json:"exemption"`
	Active    bool      `json:"active"`
	Expired   bool      `json:"expired"`
}

// IssueRow is one issue with its exemption state.
type IssueRow struct {
	Issue       validate.Issue `json:"issue"`
	ExemptionID string         `json:"exemption_id,omitempty"`
	Exempted    bool           `json:"exempted"`
	Expired     bool           `json:"expired,omitempty"`
	Exemption   *Exemption     `json:"exemption,omitempty"`
}

// IssuesView binds an issue list to a validation result and snapshot.
type IssuesView struct {
	Snapshot        Snapshot   `json:"snapshot"`
	ResultID        string     `json:"result_id"`
	Language        string     `json:"language"`
	Stale           bool       `json:"stale"`
	StaleReason     string     `json:"stale_reason,omitempty"`
	BaseVersionID   string     `json:"base_version_id"`
	TargetVersionID string     `json:"target_version_id"`
	ParserVersion   string     `json:"parser_version"`
	MappingHash     string     `json:"mapping_hash"`
	Issues          []IssueRow `json:"issues"`
	ActiveCount     int        `json:"active_count"`
}

func (s *Store) currentResultIDLocked(lang string) (string, bool) {
	baseID := ""
	if len(s.st.BaseChain) > 0 {
		baseID = s.st.BaseChain[len(s.st.BaseChain)-1]
	}
	tvID := s.st.LangVersion[lang]
	var candidates []ResultMeta
	for _, r := range s.st.Results {
		if r.Language == lang && r.BaseVersionID == baseID &&
			r.ParserVersion == s.st.ParserVersion {
			candidates = append(candidates, r)
		}
	}
	if len(candidates) == 0 {
		return "", false
	}
	// Only the result whose mapping hash equals the currently relevant edge
	// subset is current; historical attempts remain replayable.
	tv, hasCat := s.st.Versions[tvID]
	var tCat = (*catalog.Catalog)(nil)
	if hasCat {
		tCat, _ = s.loadCatalog(tv)
	}
	var bCat = (*catalog.Catalog)(nil)
	if bv, ok := s.st.Versions[baseID]; ok {
		bCat, _ = s.loadCatalog(bv)
	}
	want := ""
	if tCat != nil && bCat != nil {
		want = validate.MappingHash(relevantEdgesFor(s.st.Edges, bCat, tCat))
	}
	var fresh []ResultMeta
	for _, c := range candidates {
		if want != "" && c.MappingHash == want {
			fresh = append(fresh, c)
		}
	}
	if len(fresh) == 0 {
		return candidates[0].ID, true
	}
	sort.Slice(fresh, func(i, j int) bool {
		if !fresh[i].CreatedAt.Equal(fresh[j].CreatedAt) {
			return fresh[i].CreatedAt.After(fresh[j].CreatedAt)
		}
		return fresh[i].Seq > fresh[j].Seq
	})
	return fresh[0].ID, true
}

// loadResult reads a result blob.
func (s *Store) loadResult(id string) (*validate.ResultData, error) {
	meta, ok := s.st.Results[id]
	if !ok {
		return nil, fmt.Errorf("校验结果 %s 不存在", id)
	}
	b, err := s.readBlob(meta.BlobPath)
	if err != nil {
		return nil, err
	}
	var r validate.ResultData
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Store) resultStaleLocked(m ResultMeta) (bool, string) {
	baseID := ""
	if len(s.st.BaseChain) > 0 {
		baseID = s.st.BaseChain[len(s.st.BaseChain)-1]
	}
	if m.ParserVersion != s.st.ParserVersion {
		return true, "解析器版本已升级"
	}
	if m.BaseVersionID != baseID {
		return true, "基准目录已更新"
	}
	if m.TargetVersionID != s.st.LangVersion[m.Language] {
		return true, "目标目录已更新"
	}
	if m.BlobMissing {
		return true, "结果数据损坏"
	}
	// Mapping relevance is computed per target catalog: verify that the
	// result's mapping hash still matches the relevant edge subset.
	tv, ok := s.st.Versions[s.st.LangVersion[m.Language]]
	if !ok {
		return true, "目标版本缺失"
	}
	tCat, err := s.loadCatalog(tv)
	if err != nil {
		return true, "目标结构不可读"
	}
	bv, ok := s.st.Versions[baseID]
	if !ok {
		return true, "基准版本缺失"
	}
	bCat, err := s.loadCatalog(bv)
	if err != nil {
		return true, "基准结构不可读"
	}
	want := validate.MappingHash(relevantEdgesFor(s.st.Edges, bCat, tCat))
	if m.MappingHash != want {
		return true, "改名映射已变化"
	}
	return false, ""
}

func (s *Store) exemptionFor(issueFingerprint, lang string, now time.Time) *Exemption {
	var best *Exemption
	for i := range s.st.Exemptions {
		e := &s.st.Exemptions[i]
		if e.IssueFingerprint == issueFingerprint && e.Language == lang && e.RevokedAt == nil {
			if best == nil || e.CreatedAt.After(best.CreatedAt) {
				best = e
			}
		}
	}
	return best
}

// Overview returns the full current-state view.
func (s *Store) Overview(now time.Time) *Overview {
	s.mu.Lock()
	defer s.mu.Unlock()
	ov := &Overview{
		Snapshot:         s.st.snapshot(),
		ParserVersion:    s.st.ParserVersion,
		BaselineLanguage: s.st.BaselineLanguage,
		Languages:        map[string]LangView{},
		Edges:            append([]validate.MappingEdge(nil), s.st.Edges...),
		ServerNow:        now.UTC(),
		Warnings:         append([]string(nil), s.st.Warnings...),
	}
	if len(s.st.BaseChain) > 0 {
		bid := s.st.BaseChain[len(s.st.BaseChain)-1]
		bv := s.st.Versions[bid]
		ov.BaseVersion = &bv
		if len(s.st.BaseChain) >= 2 {
			pid := s.st.BaseChain[len(s.st.BaseChain)-2]
			pv := s.st.Versions[pid]
			pc, _ := s.loadCatalog(pv)
			cc, _ := s.loadCatalog(bv)
			if pc != nil && cc != nil {
				ov.PendingRenames = append(ov.PendingRenames, pendingBetween(pv, bv, pc, cc))
			}
		}
	}
	for _, lang := range sortedLangKeys(s.st.LangVersion) {
		v := s.st.Versions[s.st.LangVersion[lang]]
		lv := LangView{Version: v}
		if rid, ok := s.currentResultIDLocked(lang); ok {
			meta := s.st.Results[rid]
			lv.CurrentResult = &meta
			stale, reason := s.resultStaleLocked(meta)
			lv.Stale = stale
			lv.StaleReason = reason
			if r, err := s.loadResult(rid); err == nil {
				for _, is := range r.Issues {
					lv.IssueTotal++
					if e := s.exemptionFor(is.Fingerprint, lang, now); e != nil {
						lv.Exempted++
						if !e.Active(now) {
							lv.Expired++
							lv.IssueActive++
						}
					} else {
						lv.IssueActive++
					}
				}
			}
		}
		ov.Languages[lang] = lv
	}
	for i := range s.st.Exemptions {
		e := s.st.Exemptions[i]
		ov.Exemptions = append(ov.Exemptions, ExemptionView{Exemption: e, Active: e.Active(now), Expired: !e.Active(now) && e.RevokedAt == nil})
	}
	if s.st.LatestCertID != "" {
		for i := range s.st.Certs {
			if s.st.Certs[i].ID == s.st.LatestCertID {
				ov.LatestCert = &s.st.Certs[i]
				ov.LatestCertCurrent = !ov.LatestCert.BlobMissing && ov.LatestCert.BasisHash == s.certBasisLocked(now)
			}
		}
	}
	return ov
}

func pendingBetween(from, to VersionRef, fromCat, toCat *catalog.Catalog) PendingTransition {
	tr := PendingTransition{FromVersionID: from.ID, ToVersionID: to.ID}
	oldSet := map[string]bool{}
	newSet := map[string]bool{}
	for _, e := range fromCat.Entries {
		oldSet[e.Key] = true
	}
	for _, e := range toCat.Entries {
		newSet[e.Key] = true
	}
	for k := range oldSet {
		if !newSet[k] {
			tr.Removed = append(tr.Removed, k)
		}
	}
	for k := range newSet {
		if !oldSet[k] {
			tr.Added = append(tr.Added, k)
		}
	}
	sort.Strings(tr.Added)
	sort.Strings(tr.Removed)
	return tr
}

// Issues returns the issue list for one language, either the current result or
// a historical result id.
func (s *Store) Issues(language, resultID string, pinnedSnapshot string, now time.Time) (*IssuesView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pinnedSnapshot != "" && pinnedSnapshot != s.st.snapshot().ID && resultID == "" {
		return nil, fmt.Errorf("%w: 快照 %s 已不是当前状态，请通过旧结果 id 查看固定结果", ErrConflict, pinnedSnapshot)
	}
	if resultID == "" {
		rid, ok := s.currentResultIDLocked(language)
		if !ok {
			return nil, fmt.Errorf("语言 %s 尚无校验结果", language)
		}
		resultID = rid
	}
	r, err := s.loadResult(resultID)
	if err != nil {
		return nil, err
	}
	meta := s.st.Results[resultID]
	view := &IssuesView{
		ResultID: resultID, Language: r.Language,
		BaseVersionID: r.BaseVersionID, TargetVersionID: r.TargetVersionID,
		ParserVersion: r.ParserVersion, MappingHash: r.MappingHash,
	}
	if meta, ok := s.st.Results[resultID]; ok {
		view.Stale, view.StaleReason = s.resultStaleLocked(meta)
	}
	for _, is := range r.Issues {
		row := IssueRow{Issue: is}
		if e := s.exemptionFor(is.Fingerprint, r.Language, now); e != nil {
			row.ExemptionID = e.ID
			row.Exemption = e
			if e.Active(now) {
				row.Exempted = true
			} else if e.RevokedAt == nil {
				row.Expired = true
				view.ActiveCount++
			} else {
				view.ActiveCount++
			}
		} else {
			view.ActiveCount++
		}
		view.Issues = append(view.Issues, row)
	}
	view.Snapshot = s.st.snapshot()
	_ = meta
	return view, nil
}

// Result returns a replayable historical validation result.
func (s *Store) Result(resultID string) (*validate.ResultData, *ResultMeta, bool, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	meta, ok := s.st.Results[resultID]
	if !ok {
		return nil, nil, false, "", errors.New("结果不存在")
	}
	r, err := s.loadResult(resultID)
	if err != nil {
		return nil, nil, false, "", err
	}
	stale, reason := s.resultStaleLocked(meta)
	return r, &meta, stale, reason, nil
}

// RawUpload returns exact upload bytes.
func (s *Store) RawUpload(uploadID string) (data []byte, up Upload, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	up, ok := s.st.Uploads[uploadID]
	if !ok {
		return nil, up, errors.New("原始文件不存在")
	}
	if up.Missing {
		return nil, up, errors.New("该原始文件的完整性校验失败，已被隔离，无法下载")
	}
	b, err := s.readBlob(up.BlobPath)
	return b, up, err
}

// ExemptionByID fetches one exemption.
func (s *Store) ExemptionByID(id string) (Exemption, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.st.Exemptions {
		if e.ID == id {
			return e, true
		}
	}
	return Exemption{}, false
}
