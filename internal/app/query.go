package app

import (
	"sort"
	"time"

	"msgcheck/internal/icu"
	"msgcheck/internal/state"
)

// StateView is the UI's top-level snapshot.
type StateView struct {
	Pin       state.Pin             `json:"pin"`
	Baseline  *BaselineView         `json:"baseline,omitempty"`
	Languages []LanguageView        `json:"languages"`
	Renames   []state.Rename        `json:"renames"`
	Notes     []string              `json:"recovery_notes"`
	Validations []ValidationSummary `json:"validations"`
	Certs     []CertSummary         `json:"certs"`
}

// BaselineView describes the current baseline.
type BaselineView struct {
	Language string    `json:"language"`
	Version  string    `json:"version"`
	Imported time.Time `json:"imported"`
	Filename string    `json:"filename,omitempty"`
}

// LanguageView describes one target language at one snapshot.
type LanguageView struct {
	Language string    `json:"language"`
	Version  string    `json:"version"`
	Imported time.Time `json:"imported"`
	Filename string    `json:"filename,omitempty"`
}

// ValidationSummary describes one cached validation and whether it matches
// the current state.
type ValidationSummary struct {
	ID            string             `json:"id"`
	TargetLang    string             `json:"target_language"`
	TargetVersion string             `json:"target_version"`
	Parser        string             `json:"parser"`
	BaselineVer   string             `json:"baseline_version"`
	MappingsFP    string             `json:"mappings_fp"`
	Current       bool               `json:"current"`
	InvalidReason string             `json:"invalid_reason,omitempty"`
	IssueCount    int                `json:"issue_count"`
	Unexempted    int                `json:"unexempted_count"`
	Exempted      int                `json:"exempted_count"`
}

// View returns a complete state snapshot read.
func (a *App) View() StateView {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.advanceLocked()
	return a.viewLocked()
}

func (a *App) viewLocked() StateView {
	v := StateView{Pin: a.pinLocked()}
	if a.data.Baseline != nil {
		v.Baseline = &BaselineView{
			Language: a.data.Baseline.Language, Version: a.data.Baseline.Version,
			Imported: a.data.Baseline.Imported, Filename: a.data.Baseline.Filename,
		}
	}
	for _, l := range a.data.Langs {
		v.Languages = append(v.Languages, LanguageView{
			Language: l.Language, Version: l.Version, Imported: l.Imported, Filename: l.Filename,
		})
	}
	sort.Slice(v.Languages, func(i, j int) bool { return v.Languages[i].Language < v.Languages[j].Language })
	v.Renames = make([]state.Rename, len(a.data.Renames))
	copy(v.Renames, a.data.Renames)
	for _, n := range a.notes {
		v.Notes = append(v.Notes, "["+n.Level+"] "+n.Detail)
	}
	ids := make([]string, 0, len(a.data.Validations))
	for id := range a.data.Validations {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		rec := a.data.Validations[id]
		sum := ValidationSummary{
			ID: rec.ID, TargetLang: rec.TargetLang, TargetVersion: rec.TargetV,
			Parser: rec.Parser, BaselineVer: rec.BaselineV, MappingsFP: rec.Mappings,
			IssueCount: len(rec.Issues),
		}
		sum.InvalidReason = a.invalidReasonLocked(rec)
		sum.Current = sum.InvalidReason == ""
		sum.Exempted, sum.Unexempted = a.countExemptedLocked(rec.Issues)
		v.Validations = append(v.Validations, sum)
	}
	v.Certs = a.certSummariesLocked()
	return v
}

func (a *App) invalidReasonLocked(rec *state.ValidationRec) string {
	if rec.Parser != a.parserVer {
		return "parser changed (was " + rec.Parser + ", now " + a.parserVer + ")"
	}
	if a.data.Baseline == nil {
		return "baseline missing"
	}
	if rec.BaselineV != a.data.Baseline.Version {
		return "baseline catalog changed"
	}
	if rec.Mappings != mappingsFP(a.data) {
		return "rename mapping changed"
	}
	if cur, ok := a.data.Langs[rec.TargetLang]; !ok {
		return "target language removed"
	} else if cur.Version != rec.TargetV {
		return "target catalog changed"
	}
	return ""
}

func (a *App) activeExemptionLocked(issueKey string, basis time.Time) *state.Exemption {
	for i := range a.data.Exemptions {
		ex := &a.data.Exemptions[i]
		if ex.IssueKey != issueKey {
			continue
		}
		if ex.ExpiresAt != nil && !ex.ExpiresAt.After(basis) {
			return nil
		}
		return ex
	}
	return nil
}

func (a *App) countExemptedLocked(issues []state.Issue) (exempted, unexempted int) {
	basis := a.data.CommittedAt
	for _, is := range issues {
		if a.activeExemptionLocked(is.ID, basis) != nil {
			exempted++
		} else {
			unexempted++
		}
	}
	return
}

// IssueView is one issue plus exemption info.
type IssueView struct {
	state.Issue
	Exempted bool             `json:"exempted"`
	Exemption *state.Exemption `json:"exemption,omitempty"`
}

// IssuesReport is the issue list for one language, bound to a pin.
type IssuesReport struct {
	Pin           state.Pin    `json:"pin"`
	ValidationID  string       `json:"validation_id"`
	Current       bool         `json:"current"`
	InvalidReason string       `json:"invalid_reason,omitempty"`
	Language      string       `json:"language"`
	Issues        []IssueView  `json:"issues"`
	Unexempted    int          `json:"unexempted_count"`
	Exempted      int          `json:"exempted_count"`
}

// Issues validates (or replays) one target language.
func (a *App) Issues(lang string) (*IssuesReport, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.advanceLocked()
	rec, err := a.validateLocked(lang)
	if err != nil {
		return nil, err
	}
	return a.issuesFromRecLocked(rec), nil
}

func (a *App) issuesFromRecLocked(rec *state.ValidationRec) *IssuesReport {
	r := &IssuesReport{
		Pin: a.pinLocked(), ValidationID: rec.ID, Language: rec.TargetLang,
		InvalidReason: a.invalidReasonLocked(rec),
	}
	r.Current = r.InvalidReason == ""
	basis := a.data.CommittedAt
	for _, is := range rec.Issues {
		iv := IssueView{Issue: is}
		if ex := a.activeExemptionLocked(is.ID, basis); ex != nil {
			iv.Exempted = true
			iv.Exemption = ex
			r.Exempted++
		} else {
			r.Unexempted++
		}
		r.Issues = append(r.Issues, iv)
	}
	return r
}

// ReplayValidation returns a stored historical result by id, clearly
// identifying the inputs it was based on.
func (a *App) ReplayValidation(id string) (*IssuesReport, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.advanceLocked()
	rec, ok := a.data.Validations[id]
	if !ok {
		return nil, &NotFoundError{Reason: "no validation result " + id}
	}
	return a.issuesFromRecLocked(rec), nil
}

// KeyView is the side-by-side structural view of one key.
type KeyView struct {
	Pin        state.Pin      `json:"pin"`
	Key        string         `json:"key"`
	Baseline   *MessageView   `json:"baseline,omitempty"`
	Targets    map[string]*MessageView `json:"targets"`
	RenamedFrom []string      `json:"renamed_from,omitempty"`
}

// MessageView shows raw text plus parsed structure.
type MessageView struct {
	Language string        `json:"language"`
	Version  string        `json:"version"`
	Raw      string        `json:"raw"`
	Context  string        `json:"context,omitempty"`
	Parsed   *icu.Message  `json:"parsed"`
}

// Key returns the side-by-side view of a key across languages.
func (a *App) Key(key string) (*KeyView, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.advanceLocked()
	if a.data.Baseline == nil {
		return nil, &BadRequestError{Reason: "no baseline imported"}
	}
	v := &KeyView{Pin: a.pinLocked(), Key: key, Targets: map[string]*MessageView{}}
	base, err := a.loadCatalogLocked(a.data.Baseline.Language, a.data.Baseline.RawBlob)
	if err != nil {
		return nil, err
	}
	// If key is a historical name, resolve through the chain.
	cur, path := resolveChain(a.data, key)
	if cur != key {
		v.Key = cur
		if len(path) > 1 {
			v.RenamedFrom = path[:len(path)-1]
		}
	}
	if e, ok := base.Entries[cur]; ok {
		v.Baseline = &MessageView{
			Language: a.data.Baseline.Language, Version: a.data.Baseline.Version,
			Raw: e.Pattern, Context: e.Context, Parsed: base.Messages[cur],
		}
	}
	langs := make([]string, 0, len(a.data.Langs))
	for l := range a.data.Langs {
		langs = append(langs, l)
	}
	sort.Strings(langs)
	for _, l := range langs {
		ls := a.data.Langs[l]
		c, err := a.loadCatalogLocked(l, ls.RawBlob)
		if err != nil {
			continue
		}
		// Find target key that resolves to cur, prefer exact name.
		tk := ""
		if _, ok := c.Entries[cur]; ok {
			tk = cur
		} else {
			for k := range c.Entries {
				got, _ := resolveChain(a.data, k)
				if got == cur {
					tk = k
					break
				}
			}
		}
		if tk == "" {
			continue
		}
		e := c.Entries[tk]
		v.Targets[l] = &MessageView{
			Language: l, Version: ls.Version, Raw: e.Pattern,
			Context: e.Context, Parsed: c.Messages[tk],
		}
	}
	return v, nil
}

// UploadsView lists every retained raw upload.
type UploadsView struct {
	Pin     state.Pin      `json:"pin"`
	Uploads []state.Upload `json:"uploads"`
}

// Uploads returns the raw-upload index.
func (a *App) Uploads() UploadsView {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.advanceLocked()
	ups := make([]state.Upload, len(a.data.Uploads))
	copy(ups, a.data.Uploads)
	sort.Slice(ups, func(i, j int) bool { return ups[i].At.Before(ups[j].At) })
	return UploadsView{Pin: a.pinLocked(), Uploads: ups}
}

// RawUpload returns the original bytes of one upload.
func (a *App) RawUpload(id string) ([]byte, string, string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, u := range a.data.Uploads {
		if u.ID == id {
			b, err := a.st.ReadBlob(u.Blob)
			return b, u.Filename, u.Language, err
		}
	}
	return nil, "", "", &NotFoundError{Reason: "unknown upload " + id}
}

// Exemptions returns active exemptions.
func (a *App) Exemptions() []state.Exemption {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.advanceLocked()
	out := make([]state.Exemption, len(a.data.Exemptions))
	copy(out, a.data.Exemptions)
	return out
}
