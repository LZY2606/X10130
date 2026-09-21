package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"messagecatalog/internal/catalog"
	"messagecatalog/internal/store"
)

// ValidateView is a validation result with resolved issue/exemption status.
type ValidateView struct {
	ID              string      `json:"id"`
	TargetLang      string      `json:"targetLang"`
	BaseVersionID   string      `json:"baseVersionId"`
	TargVersionID   string      `json:"targVersionId"`
	BaseFingerprint string      `json:"baseFingerprint"`
	TargFingerprint string      `json:"targFingerprint"`
	ParserVersion   string      `json:"parserVersion"`
	MappingSig      string      `json:"mappingSig"`
	SnapshotStamp   string      `json:"snapshotStamp"`
	StateSeq        int64       `json:"stateSeq"`
	Issues          []IssueView `json:"issues"`
	OpenCount       int         `json:"openCount"`
	ExemptCount     int         `json:"exemptCount"`
	ExpiredCount    int         `json:"expiredCount"`
	Current         bool        `json:"current"`
	Invalidated     bool        `json:"invalidated"`
	InvalidReason   string      `json:"invalidReason,omitempty"`
	CreatedAt       time.Time   `json:"createdAt"`
}

// IssueView adds stable id and exemption state.
type IssueView struct {
	IssueKey  string           `json:"issueKey"`
	Language  string           `json:"language"`
	Key       string           `json:"key"`
	Type      string           `json:"type"`
	Name      string           `json:"name,omitempty"`
	Expected  string           `json:"expected,omitempty"`
	Actual    string           `json:"actual,omitempty"`
	Detail    string           `json:"detail,omitempty"`
	Exemption *store.Exemption `json:"exemption,omitempty"`
	Expired   bool             `json:"expired,omitempty"`
}

func validationID(target string, baseID, targID, mapSig string) string {
	b, _ := json.Marshal(struct {
		P string `json:"p"`
		T string `json:"t"`
		B string `json:"b"`
		G string `json:"g"`
		M string `json:"m"`
	}{catalog.ParserVersion, target, baseID, targID, mapSig})
	sum := sha256.Sum256(b)
	return "val-" + hex.EncodeToString(sum[:])[:16]
}

func currentBaseVersion(st *store.State) *store.VersionRecord {
	// baseline role: there is one logical baseline; find language with role.
	var baseLang string
	for lang, role := range st.RoleByLang {
		if role == roleBaseline {
			baseLang = lang
			break
		}
	}
	if baseLang == "" {
		return nil
	}
	id := st.CurrentByLang[baseLang]
	for i := range st.Versions {
		if st.Versions[i].ID == id {
			return &st.Versions[i]
		}
	}
	return nil
}

func currentVersion(st *store.State, lang string) *store.VersionRecord {
	id, ok := st.CurrentByLang[lang]
	if !ok {
		return nil
	}
	for i := range st.Versions {
		if st.Versions[i].ID == id {
			return &st.Versions[i]
		}
	}
	return nil
}

func versionFile(v *store.VersionRecord) *catalog.File {
	f := &catalog.File{Language: v.Language, Messages: map[string]string{}}
	for _, m := range v.Messages {
		f.Messages[m.Key] = m.Raw
	}
	return f
}

// ValidateTarget computes (or replays) validation for a target language.
func (s *Service) ValidateTarget(lang string) (*ValidateView, error) {
	snap := s.Snapshot()
	st := snap.State
	base := currentBaseVersion(st)
	if base == nil {
		return nil, fmt.Errorf("no baseline catalog imported")
	}
	targ := currentVersion(st, lang)
	if targ == nil {
		return nil, fmt.Errorf("no catalog imported for language %q", lang)
	}
	ms := mappingSig(st)
	id := validationID(lang, base.ID, targ.ID, ms)

	// Existing record (only non-invalidated records count as current).
	var rec *store.ValidationRecord
	for i := range st.Validations {
		if st.Validations[i].ID == id {
			rec = &st.Validations[i]
			break
		}
	}
	if rec == nil {
		// Compute and persist once.
		if err := s.persistValidation(lang, base, targ, ms); err != nil {
			return nil, err
		}
	}
	// Always view the latest committed state so the reported seq matches the
	// stored validation record.
	snap = s.Snapshot()
	st = snap.State
	for i := range st.Validations {
		if st.Validations[i].ID == id {
			rec = &st.Validations[i]
			break
		}
	}
	view := buildView(st, rec, s.now(), snap.Stamp, st.Seq)
	view.Current = !rec.Invalidated && isValidationCurrent(st, rec)
	return view, nil
}

func isValidationCurrent(st *store.State, rec *store.ValidationRecord) bool {
	base := currentBaseVersion(st)
	targ := currentVersion(st, rec.TargetLang)
	if base == nil || targ == nil {
		return false
	}
	return base.ID == rec.BaseVersionID && targ.ID == rec.TargVersionID &&
		mappingSig(st) == rec.MappingSig && rec.ParserVersion == catalog.ParserVersion &&
		!rec.Invalidated
}

func (s *Service) persistValidation(lang string, base, targ *store.VersionRecord, ms string) error {
	return s.Store.Commit(func(st *store.State) error {
		id := validationID(lang, base.ID, targ.ID, ms)
		for _, r := range st.Validations {
			if r.ID == id {
				return nil
			}
		}
		issues := catalog.ValidateTarget(versionFile(base), versionFile(targ), edges(st))
		rec := store.ValidationRecord{
			ID: id, TargetLang: lang, BaseVersionID: base.ID, TargVersionID: targ.ID,
			MappingSig: ms, ParserVersion: catalog.ParserVersion,
			BaseFingerprint: base.Fingerprint, TargFingerprint: targ.Fingerprint,
			CreatedAt: s.now(),
		}
		for _, is := range issues {
			ik := is.IssueID(id)
			rec.Issues = append(rec.Issues, store.StoredIssue{
				Language: is.Language, Key: is.Key, Type: is.Type, Name: is.Name,
				Expected: is.Expected, Actual: is.Actual, Detail: is.Detail, IssueKey: ik,
			})
		}
		st.Validations = append(st.Validations, rec)
		return nil
	})
}

func buildView(st *store.State, rec *store.ValidationRecord, now time.Time, stamp string, seq int64) *ValidateView {
	v := &ValidateView{
		ID: rec.ID, TargetLang: rec.TargetLang, BaseVersionID: rec.BaseVersionID,
		TargVersionID: rec.TargVersionID, BaseFingerprint: rec.BaseFingerprint,
		TargFingerprint: rec.TargFingerprint, ParserVersion: rec.ParserVersion,
		MappingSig: rec.MappingSig, SnapshotStamp: stamp, StateSeq: seq,
		Invalidated: rec.Invalidated, InvalidReason: rec.InvalidReason,
		CreatedAt: rec.CreatedAt,
	}
	exByKey := map[string]*store.Exemption{}
	for i := range st.Exemptions {
		e := &st.Exemptions[i]
		exByKey[e.IssueKey] = e
	}
	for _, is := range rec.Issues {
		iv := IssueView{
			IssueKey: is.IssueKey, Language: is.Language, Key: is.Key, Type: is.Type,
			Name: is.Name, Expected: is.Expected, Actual: is.Actual, Detail: is.Detail,
		}
		if e := exByKey[is.IssueKey]; e != nil {
			expired := !e.ExpiresAt.After(now)
			if !e.Revoked {
				iv.Exemption = e
				iv.Expired = expired
				if expired {
					v.ExpiredCount++
					v.OpenCount++
				} else {
					v.ExemptCount++
				}
			} else {
				v.OpenCount++
			}
		} else {
			v.OpenCount++
		}
		v.Issues = append(v.Issues, iv)
	}
	return v
}

// Replay returns a fixed old validation result by id.
func (s *Service) Replay(id string) (*ValidateView, error) {
	snap := s.Snapshot()
	st := snap.State
	for i := range st.Validations {
		if st.Validations[i].ID == id {
			v := buildView(st, &st.Validations[i], s.now(), snap.Stamp, st.Seq)
			v.Current = !st.Validations[i].Invalidated && isValidationCurrent(st, &st.Validations[i])
			return v, nil
		}
	}
	return nil, errNotFound
}

// CurrentValidations returns the current validation view for every target.
func (s *Service) CurrentValidations() ([]*ValidateView, error) {
	snap := s.Snapshot()
	st := snap.State
	if currentBaseVersion(st) == nil {
		return nil, nil
	}
	langs := make([]string, 0)
	for lang, role := range st.RoleByLang {
		if role == roleTarget {
			langs = append(langs, lang)
		}
	}
	sort.Strings(langs)
	var out []*ValidateView
	for _, l := range langs {
		v, err := s.ValidateTarget(l)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}
