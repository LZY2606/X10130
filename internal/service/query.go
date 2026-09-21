package service

import (
	"fmt"
	"sort"
	"time"

	"messagecatalog/internal/catalog"
	"messagecatalog/internal/store"
)

// KeyView is the side-by-side parsed structure for one key across languages.
type KeyView struct {
	Key           string                `json:"key"`
	ResolvedFrom  string                `json:"resolvedFrom,omitempty"`
	Base          *LanguageMessageView  `json:"base,omitempty"`
	Targets       []LanguageMessageView `json:"targets"`
	SnapshotStamp string                `json:"snapshotStamp"`
	StateSeq      int64                 `json:"stateSeq"`
	MappingSig    string                `json:"mappingSig"`
	ParserVersion string                `json:"parserVersion"`
}

// LanguageMessageView holds raw + parsed for one language.
type LanguageMessageView struct {
	Language  string          `json:"language"`
	VersionID string          `json:"versionId"`
	Found     bool            `json:"found"`
	Raw       string          `json:"raw,omitempty"`
	Parsed    *catalog.Parsed `json:"parsed,omitempty"`
}

// CompareKey returns side-by-side structure for one key.
func (s *Service) CompareKey(key string) (*KeyView, error) {
	snap := s.Snapshot()
	st := snap.State
	e := edges(st)
	base := currentBaseVersion(st)
	if base == nil {
		return nil, fmt.Errorf("no baseline catalog imported")
	}
	view := &KeyView{Key: key, SnapshotStamp: snap.Stamp, StateSeq: st.Seq,
		MappingSig: mappingSig(st), ParserVersion: catalog.ParserVersion}

	if bm, ok := findMessage(base, key); ok {
		lmv := &LanguageMessageView{Language: base.Language, VersionID: base.ID, Found: true,
			Raw: bm.Raw, Parsed: &bm.Parsed}
		view.Base = lmv
	}
	langs := make([]string, 0)
	for lang, role := range st.RoleByLang {
		if role == roleTarget {
			langs = append(langs, lang)
		}
	}
	sort.Strings(langs)
	for _, lang := range langs {
		tv := currentVersion(st, lang)
		if tv == nil {
			continue
		}
		lmv := &LanguageMessageView{Language: lang, VersionID: tv.ID}
		// Try direct key first, otherwise follow the baseline chain.
		if m, ok := findMessage(tv, key); ok {
			lmv.Found = true
			lmv.Raw = m.Raw
			p := m.Parsed
			lmv.Parsed = &p
		} else if _, isBase := findMessage(base, key); isBase {
			chain, _ := e.ResolveChain(key)
			for _, node := range chain {
				if m, ok := findMessage(tv, node); ok {
					lmv.Found = true
					lmv.Raw = m.Raw
					p := m.Parsed
					lmv.Parsed = &p
					view.ResolvedFrom = node
				}
			}
		}
		view.Targets = append(view.Targets, *lmv)
	}
	return view, nil
}

func findMessage(v *store.VersionRecord, key string) (*catalog.Message, bool) {
	for i := range v.Messages {
		if v.Messages[i].Key == key {
			return &v.Messages[i], true
		}
	}
	return nil, false
}

// Keys lists current baseline keys.
func (s *Service) Keys() []string {
	snap := s.Snapshot()
	base := currentBaseVersion(snap.State)
	if base == nil {
		return nil
	}
	keys := make([]string, 0, len(base.Messages))
	for _, m := range base.Messages {
		keys = append(keys, m.Key)
	}
	sort.Strings(keys)
	return keys
}

// Languages describes imported languages.
type LanguageInfo struct {
	Language     string `json:"language"`
	Role         string `json:"role"`
	VersionID    string `json:"versionId"`
	Fingerprint  string `json:"fingerprint"`
	MessageCount int    `json:"messageCount"`
}

func (s *Service) Languages() []LanguageInfo {
	snap := s.Snapshot()
	st := snap.State
	langs := make([]LanguageInfo, 0)
	for lang, role := range st.RoleByLang {
		id := st.CurrentByLang[lang]
		info := LanguageInfo{Language: lang, Role: role, VersionID: id}
		if v := currentVersion(st, lang); v != nil {
			info.Fingerprint = v.Fingerprint
			info.MessageCount = len(v.Messages)
		}
		langs = append(langs, info)
	}
	sort.Slice(langs, func(i, j int) bool {
		if langs[i].Role != langs[j].Role {
			return langs[i].Role < langs[j].Role
		}
		return langs[i].Language < langs[j].Language
	})
	return langs
}

// RawUpload returns the exact bytes of an upload.
func (s *Service) RawUpload(uploadID string) (data []byte, filename string, err error) {
	snap := s.Snapshot()
	for _, u := range snap.State.Uploads {
		if u.ID == uploadID {
			data, err := s.Store.ReadRaw(u.BlobName)
			if err != nil {
				return nil, "", err
			}
			return data, u.Filename, nil
		}
	}
	return nil, "", errNotFound
}

// Uploads lists raw uploads.
func (s *Service) Uploads() []store.UploadRecord {
	snap := s.Snapshot()
	out := append([]store.UploadRecord(nil), snap.State.Uploads...)
	sort.Slice(out, func(i, j int) bool { return out[i].UploadedAt.After(out[j].UploadedAt) })
	return out
}

// Proofs lists proof records with freshness relative to the current snapshot.
type ProofInfo struct {
	store.ProofRecord
	MatchesCurrent bool `json:"matchesCurrent"`
}

func (s *Service) Proofs() []ProofInfo {
	snap := s.Snapshot()
	st := snap.State
	stamp := stateStamp(st)
	var out []ProofInfo
	for _, p := range st.Proofs {
		out = append(out, ProofInfo{ProofRecord: p, MatchesCurrent: p.Stamp == stamp && p.Current})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// ReadProof returns proof bytes and whether it matches the current state.
func (s *Service) ReadProof(id string) ([]byte, bool, error) {
	snap := s.Snapshot()
	st := snap.State
	stamp := stateStamp(st)
	for _, p := range st.Proofs {
		if p.ID == id {
			data, err := s.Store.ReadProof(p.BlobName)
			return data, p.Stamp == stamp && p.Current, err
		}
	}
	return nil, false, errNotFound
}

// Batches returns batch records.
func (s *Service) Batches() []store.BatchRecord {
	snap := s.Snapshot()
	out := append([]store.BatchRecord(nil), snap.State.Batches...)
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// SetTime moves the controlled clock (no-op for real clock services).
func (s *Service) SetTime(t time.Time) {
	if c, ok := s.Clock.(*ControlledClock); ok {
		c.Set(t)
	}
}

// AddTime advances the controlled clock.
func (s *Service) AddTime(d time.Duration) {
	if c, ok := s.Clock.(*ControlledClock); ok {
		c.Add(d)
	}
}

// Now returns the service clock time.
func (s *Service) Now() time.Time { return s.now() }
