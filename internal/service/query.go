package service

import (
	"sort"

	"messagecatalog/internal/icu"
)

// LangView is the API view of one imported language.
type LangView struct {
	Language       string `json:"language"`
	ContentVersion string `json:"contentVersion"`
	IsBaseline     bool   `json:"isBaseline"`
	UploadID       string `json:"uploadId"`
	ImportedAt     string `json:"importedAt"`
	Seq            int    `json:"seq"`
}

// StateView is the top-level status payload carrying stable snapshot ids.
type StateView struct {
	SnapshotID       string         `json:"snapshotId"`
	Seq              int            `json:"seq"`
	BaselineLanguage string         `json:"baselineLanguage"`
	MappingVersion   int            `json:"mappingVersion"`
	ParserVersion    string         `json:"parserVersion"`
	Languages        []*LangView    `json:"languages"`
	PendingRename    *PendingRename `json:"pendingRename,omitempty"`
	Degraded         bool           `json:"degraded"`
	IntegrityNote    string         `json:"integrityNote,omitempty"`
	Now              string         `json:"now"`
}

// View returns a consistent snapshot of service status.
func (s *Service) View() *StateView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v := &StateView{
		Seq:              s.state.Seq,
		BaselineLanguage: s.state.BaselineLanguage,
		MappingVersion:   s.state.MappingVersion,
		ParserVersion:    icu.Version,
		PendingRename:    s.state.PendingRenames,
		Now:              s.nowRFC(),
	}
	if len(s.state.Snapshots) > 0 {
		v.SnapshotID = s.state.Snapshots[len(s.state.Snapshots)-1].ID
	}
	for lang, c := range s.state.Catalogs {
		v.Languages = append(v.Languages, &LangView{
			Language: lang, ContentVersion: c.Version,
			IsBaseline: lang == s.state.BaselineLanguage,
			UploadID:   c.UploadID, ImportedAt: c.ImportedAt, Seq: c.Seq,
		})
	}
	sort.Slice(v.Languages, func(i, j int) bool { return v.Languages[i].Language < v.Languages[j].Language })
	v.Degraded, v.IntegrityNote = s.degraded, s.integrityMsg
	return v
}

// RawUpload returns exact bytes for an upload id (used for downloads).
func (s *Service) RawUpload(uploadID string) ([]byte, string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, u := range s.state.Uploads {
		if u.ID == uploadID {
			b, err := s.st.GetBlob(u.RawBlob)
			return b, u.Language, err
		}
	}
	return nil, "", &RequestError{Msg: "upload not found: " + uploadID}
}

// RawUploadForLang returns the latest raw upload for a language.
func (s *Service) RawUploadForLang(lang string) ([]byte, *Upload, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.state.Uploads[lang]
	if !ok {
		return nil, nil, &RequestError{Msg: "no upload for " + lang}
	}
	b, err := s.st.GetBlob(u.RawBlob)
	return b, u, err
}

// EntryView is one message in the side-by-side structural view.
type EntryView struct {
	Key      string `json:"key"`
	Text     string `json:"text"`
	Context  string `json:"context,omitempty"`
	Parsed   any    `json:"parsed,omitempty"`
	ParseErr string `json:"parseError,omitempty"`
}

// SideBySide compares one key across all languages on one snapshot.
type SideBySide struct {
	SnapshotID     string                `json:"snapshotId"`
	Key            string                `json:"key"`
	ResolvedRoot   string                `json:"resolvedRoot"`
	MappingVersion int                   `json:"mappingVersion"`
	Languages      map[string]*EntryView `json:"languages"`
}

// SideBySideKey builds the structural comparison for one key.
func (s *Service) SideBySideKey(key string) (*SideBySide, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := &SideBySide{Key: key, Languages: map[string]*EntryView{},
		MappingVersion: s.state.MappingVersion,
		ResolvedRoot:   resolveRoot(s.state.Mapping, key)}
	if len(s.state.Snapshots) > 0 {
		out.SnapshotID = s.state.Snapshots[len(s.state.Snapshots)-1].ID
	}
	langs := mapKeys(s.state.Catalogs)
	sort.Strings(langs)
	for _, lang := range langs {
		cv := s.state.Catalogs[lang]
		c, err := s.loadCatalogBlob(cv.Blob)
		if err != nil {
			return nil, err
		}
		idx := c.Index()
		lookup := key
		if _, ok := idx[lookup]; !ok {
			lookup = resolveRoot(s.state.Mapping, key)
		}
		e, ok := idx[lookup]
		if !ok {
			// for languages, try reverse alias
			for _, alias := range rootAliasesFor(s.state.Mapping, key) {
				if e2, ok2 := idx[alias]; ok2 {
					e = e2
					ok = true
					break
				}
			}
		}
		if !ok {
			out.Languages[lang] = &EntryView{ParseErr: "key missing in this language"}
			continue
		}
		ev := &EntryView{Key: e.Key, Text: e.Text, Context: e.Context}
		m, perr := icuParse(e.Text)
		if perr != nil {
			ev.ParseErr = perr.Error()
		} else {
			ev.Parsed = structureOf(m)
		}
		out.Languages[lang] = ev
	}
	return out, nil
}

// IssueView augments an issue with exemption status.
type IssueView struct {
	Issue
	Exempted          bool   `json:"exempted"`
	ExemptionActive   bool   `json:"exemptionActive"`
	ExemptionReason   string `json:"exemptionReason,omitempty"`
	ExemptionDeadline string `json:"exemptionDeadline,omitempty"`
}

// LanguageReportView is the issue list for one language on one snapshot.
type LanguageReportView struct {
	SnapshotID      string       `json:"snapshotId"`
	TupleSHA        string       `json:"tupleSha"`
	Language        string       `json:"language"`
	ParserVersion   string       `json:"parserVersion"`
	BaselineVersion string       `json:"baselineVersion"`
	TargetVersion   string       `json:"targetVersion"`
	MappingVersion  int          `json:"mappingVersion"`
	GeneratedAt     string       `json:"generatedAt"`
	Issues          []*IssueView `json:"issues"`
	UnexemptedCount int          `json:"unexemptedCount"`
	ExpiredCount    int          `json:"expiredCount"`
	// Validity against the current state.
	Current       bool   `json:"current"`
	InvalidReason string `json:"invalidReason,omitempty"`
}

// ReportFor returns the current issues for a language.
func (s *Service) ReportFor(language string) (*LanguageReportView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, tuple, err := s.ensureReportLocked(language)
	if err != nil {
		return nil, err
	}
	return s.decorateLocked(r, tuple), nil
}

func (s *Service) decorateLocked(r *Report, tuple string) *LanguageReportView {
	now := s.Now()
	v := &LanguageReportView{
		TupleSHA: tuple, Language: r.Language, ParserVersion: r.ParserVersion,
		BaselineVersion: r.BaselineVersion, TargetVersion: r.TargetVersion,
		MappingVersion: r.MappingVersion, GeneratedAt: r.GeneratedAt, Current: true,
	}
	if len(s.state.Snapshots) > 0 {
		v.SnapshotID = s.state.Snapshots[len(s.state.Snapshots)-1].ID
	}
	for _, is := range r.Issues {
		iv := &IssueView{Issue: *is}
		if ex, ok := s.state.Exemptions[is.ID]; ok {
			iv.Exempted = true
			if ex.Expired(now) {
				v.ExpiredCount++
				iv.ExemptionDeadline = ex.Deadline
				iv.ExemptionReason = ex.Reason
			} else {
				iv.ExemptionActive = true
				iv.ExemptionReason = ex.Reason
				iv.ExemptionDeadline = ex.Deadline
				v.UnexemptedCount-- // net effect handled below
			}
		}
		v.Issues = append(v.Issues, iv)
	}
	total := len(r.Issues)
	activeEx := 0
	for _, is := range r.Issues {
		if ex, ok := s.state.Exemptions[is.ID]; ok && !ex.Expired(now) {
			activeEx++
		}
	}
	v.UnexemptedCount = total - activeEx
	return v
}
