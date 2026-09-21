package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"msgcheck/internal/catalog"
	"msgcheck/internal/icu"
	"msgcheck/internal/state"
)

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// ValidationInput pins the exact inputs of a validation run.
type ValidationInput struct {
	Parser       string `json:"parser"`
	BaselineV    string `json:"baseline_version"`
	TargetLang   string `json:"target_language"`
	TargetV      string `json:"target_version"`
	MappingsFP   string `json:"mappings_fp"`
}

// ValidationID is deterministic in all inputs, so identical runs replay.
func ValidationID(in ValidationInput) string {
	b := mustJSON(in)
	sum := sha256.Sum256(b)
	return "val_" + hex.EncodeToString(sum[:16])
}

// ValidationStatus is current | invalid_<reason> for a stored result.
type ValidationStatus struct {
	Rec    *state.ValidationRec
	Status string
}

// validateLocked runs validation for one target language and caches the
// result. Results are a pure function of their pinned inputs.
func (a *App) validateLocked(lang string) (*state.ValidationRec, error) {
	if a.data.Baseline == nil {
		return nil, &BadRequestError{Reason: "import a baseline catalog first"}
	}
	ls, ok := a.data.Langs[lang]
	if !ok {
		return nil, &NotFoundError{Reason: "unknown target language " + lang}
	}
	in := ValidationInput{
		Parser:     a.parserVer,
		BaselineV:  a.data.Baseline.Version,
		TargetLang: lang,
		TargetV:    ls.Version,
		MappingsFP: mappingsFP(a.data),
	}
	id := ValidationID(in)
	if rec, ok := a.data.Validations[id]; ok {
		return rec, nil
	}
	base, err := a.loadCatalogLocked(a.data.Baseline.Language, a.data.Baseline.RawBlob)
	if err != nil {
		return nil, err
	}
	tgt, err := a.loadCatalogLocked(lang, ls.RawBlob)
	if err != nil {
		return nil, err
	}
	issues := compareCatalogs(a.data, lang, base, tgt)
	rec := &state.ValidationRec{
		ID: id, Parser: in.Parser, BaselineV: in.BaselineV,
		TargetLang: lang, TargetV: in.TargetV, Mappings: in.MappingsFP,
		CreatedAt: a.now(), Issues: issues,
	}
	a.data.Validations[id] = rec
	return rec, nil
}

// compareCatalogs aligns target keys onto the current baseline via rename
// chains and produces all issues.
func compareCatalogs(d *state.Data, lang string, baseCat, tgtCat *catalog.Catalog) []state.Issue {
	var issues []state.Issue
	next := map[string]string{}
	for _, r := range d.Renames {
		next[r.OldKey] = r.NewKey
	}
	resolve := func(k string) string {
		cur := k
		seen := map[string]bool{cur: true}
		for {
			n, ok := next[cur]
			if !ok || seen[n] {
				return cur
			}
			seen[n] = true
			cur = n
		}
	}

	tgtByCurrent := map[string]string{} // current key -> target key
	for tk := range tgtCat.Messages {
		tgtByCurrent[resolve(tk)] = tk
	}

	// Missing: baseline keys with no aligned target message.
	for _, bk := range baseCat.Keys() {
		if _, ok := tgtByCurrent[bk]; !ok {
			issues = append(issues, state.Issue{
				Code: state.IssueMissing, Language: lang, Key: bk,
				Detail: fmt.Sprintf("target language %q has no message for key %q", lang, bk),
			}.WithID())
		}
	}

	// Extra: target keys whose resolved name is not in the current baseline.
	baseSet := map[string]bool{}
	for _, bk := range baseCat.Keys() {
		baseSet[bk] = true
	}
	for _, tk := range tgtCat.Keys() {
		cur := resolve(tk)
		if !baseSet[cur] {
			issues = append(issues, state.Issue{
				Code: state.IssueExtra, Language: lang, Key: tk,
				Detail: fmt.Sprintf("target language %q has extra key %q not present in baseline", lang, tk),
			}.WithID())
		}
	}

	for _, bk := range baseCat.Keys() {
		tk, ok := tgtByCurrent[bk]
		if !ok {
			continue
		}
		bm := baseCat.Messages[bk]
		tm := tgtCat.Messages[tk]
		issues = append(issues, compareMessages(lang, bk, bm, tm)...)
	}
	sort.SliceStable(issues, func(i, j int) bool {
		if issues[i].Key != issues[j].Key {
			return issues[i].Key < issues[j].Key
		}
		if issues[i].Code != issues[j].Code {
			return issues[i].Code < issues[j].Code
		}
		return issues[i].Placeholder < issues[j].Placeholder
	})
	return issues
}

func compareMessages(lang, key string, bm, tm *icu.Message) []state.Issue {
	var issues []state.Issue
	bp := bm.PlaceholderMap()
	tp := tm.PlaceholderMap()

	var onlyB, onlyT []string
	for name := range bp {
		if _, ok := tp[name]; !ok {
			onlyB = append(onlyB, name)
		}
	}
	for name := range tp {
		if _, ok := bp[name]; !ok {
			onlyT = append(onlyT, name)
		}
	}
	sort.Strings(onlyB)
	sort.Strings(onlyT)
	if len(onlyB) > 0 || len(onlyT) > 0 {
		issues = append(issues, state.Issue{
			Code: state.IssuePlaceholderMissing, Language: lang, Key: key,
			Detail:  "placeholder sets differ",
			Expected: onlyB, Actual: onlyT,
		}.WithID())
	}

	for _, name := range sortedKeys(bp) {
		bph := bp[name]
		tph, ok := tp[name]
		if !ok {
			continue
		}
		if bph.Type != tph.Type {
			issues = append(issues, state.Issue{
				Code: state.IssueTypeDrift, Language: lang, Key: key, Placeholder: name,
				Detail:   fmt.Sprintf("placeholder %q type drifted from %q to %q", name, typeName(bph.Type), typeName(tph.Type)),
				Expected: []string{typeName(bph.Type)}, Actual: []string{typeName(tph.Type)},
			}.WithID())
		}
		if bph.Type == "plural" || bph.Type == "selectordinal" || tph.Type == "plural" || tph.Type == "selectordinal" {
			bc := bph.CategoryKeys()
			tc := tph.CategoryKeys()
			if !equalSlices(bc, tc) {
				issues = append(issues, state.Issue{
					Code: state.IssuePlural, Language: lang, Key: key, Placeholder: name,
					Detail:   fmt.Sprintf("%s categories differ for placeholder %q", typeName(bph.Type), name),
					Expected: bc, Actual: tc,
				}.WithID())
			}
		}
	}

	if bm.HasUnbalancedTags() {
		issues = append(issues, state.Issue{
			Code: state.IssueTags, Language: lang, Key: key, Side: "baseline",
			Detail: "rich-text tags are unbalanced in the baseline message",
		}.WithID())
	}
	if tm.HasUnbalancedTags() {
		issues = append(issues, state.Issue{
			Code: state.IssueTags, Language: lang, Key: key, Side: "target",
			Detail: "rich-text tags are unbalanced in the target message",
		}.WithID())
	}
	syntaxErrs := func(side string, m *icu.Message) {
		var uniq []string
		seen := map[string]bool{}
		var walk func(nm *icu.Message)
		walk = func(nm *icu.Message) {
			for _, e := range nm.ParseErrors {
				if !tagError(e) && !seen[e] {
					seen[e] = true
					uniq = append(uniq, e)
				}
			}
			for _, ph := range nm.Placeholders {
				for _, sub := range ph.Cases {
					walk(sub)
				}
			}
		}
		walk(m)
		for _, e := range uniq {
			issues = append(issues, state.Issue{
				Code: "icu_syntax", Language: lang, Key: key, Side: side,
				Detail: e,
			}.WithID())
		}
	}
	syntaxErrs("baseline", bm)
	syntaxErrs("target", tm)
	return issues
}

func tagError(e string) bool {
	return containsAny(e, "tag")
}

func containsAny(s string, sub ...string) bool {
	for _, x := range sub {
		if indexOf(s, x) >= 0 {
			return true
		}
	}
	return false
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func sortedKeys(m map[string]*icu.Placeholder) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func typeName(t string) string {
	if t == "" {
		return "(none)"
	}
	return t
}
