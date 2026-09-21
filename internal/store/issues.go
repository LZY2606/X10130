package store

import (
	"fmt"
	"sort"

	"msgcatalog/internal/icu"
)

// Issue describes one finding for one language/key.
type Issue struct {
	ID       string `json:"id"`
	Code     string `json:"code"`
	Language string `json:"language"`
	Key      string `json:"key"`
	Detail   string `json:"detail,omitempty"`
}

const (
	CodeParseError       = "parse-error"
	CodeMissing          = "missing-key"
	CodeExtra            = "extra-key"
	CodePlaceholderSet   = "placeholder-mismatch"
	CodePlaceholderType  = "placeholder-type-drift"
	CodePluralMissing    = "plural-category-missing"
	CodeTagUnbalanced    = "tag-unbalanced"
)

// closure computes the effective newKey for each confirmed old key by
// following chains. Cycles are treated as identity (they are rejected on
// write, so this only guards stale data).
func closure(edges []Edge) map[string]string {
	byOld := map[string]string{}
	for _, e := range edges {
		byOld[e.OldKey] = e.NewKey
	}
	out := map[string]string{}
	for old := range byOld {
		seen := map[string]bool{old: true}
		cur := old
		for {
			nxt, ok := byOld[cur]
			if !ok {
				break
			}
			if seen[nxt] {
				cur = old
				break
			}
			seen[nxt] = true
			cur = nxt
		}
		out[old] = cur
	}
	return out
}

// newToOldKeys maps every effective current baseline key back to the set of
// historical keys that flow into it (must be unique on write).
func newToOld(edges []Edge) map[string]string {
	c := closure(edges)
	out := map[string]string{}
	for old, newK := range c {
		if _, dup := out[newK]; dup && old != newK {
			// merge guard; writes prevent this
		}
		out[newK] = old
	}
	return out
}

type langView struct {
	msgs map[string]*catalogMessage
	keys []string
}

func viewOf(v *VersionEntry) langView {
	m := map[string]*catalogMessage{}
	keys := make([]string, 0, len(v.Messages))
	for i := range v.Messages {
		mm := v.Messages[i]
		m[mm.Key] = &mm
		keys = append(keys, mm.Key)
	}
	return langView{msgs: m, keys: keys}
}

// Validate computes all issues for a state at time now. It is a pure
// function of parser version, versions, edges and exemption timing is not
// applied here (exemptions are layered on at read/proof time).
func Validate(st *State, now int64) []Issue {
	if st.Baseline == "" || st.Versions[st.Langs[st.Baseline].CurrentFP] == nil {
		return nil
	}
	baseVer := st.Versions[st.Langs[st.Baseline].CurrentFP]
	base := viewOf(baseVer)
	cl := closure(st.Edges)
	// effective baseline key -> historical key (for issue identity)
	// Build current-key set of baseline.
	var issues []Issue

	// Baseline structural errors.
	for _, k := range base.keys {
		m := base.msgs[k]
		if m.AST != nil && m.AST.Error != "" {
			issues = append(issues, Issue{
				ID: issueID(CodeParseError, st.Baseline, k),
				Code: CodeParseError, Language: st.Baseline, Key: k,
				Detail: m.AST.Error,
			})
		}
		if m.AST != nil && len(m.AST.Unbalanced) > 0 {
			issues = append(issues, Issue{
				ID: issueID(CodeTagUnbalanced, st.Baseline, k),
				Code: CodeTagUnbalanced, Language: st.Baseline, Key: k,
				Detail: describeTags(m.AST.Unbalanced),
			})
		}
	}

	langNames := make([]string, 0, len(st.Langs))
	for name := range st.Langs {
		if name == st.Baseline {
			continue
		}
		langNames = append(langNames, name)
	}
	sort.Strings(langNames)

	// baseline current keys, with historical aliases resolved via edges.
	baseCurrent := map[string]bool{}
	for _, k := range base.keys {
		baseCurrent[k] = true
	}

	for _, lang := range langNames {
		ls := st.Langs[lang]
		tv := st.Versions[ls.CurrentFP]
		if tv == nil {
			continue
		}
		tgt := viewOf(tv)
		tgtKeys := map[string]bool{}
		for _, k := range tgt.keys {
			tgtKeys[k] = true
		}

		// Missing: for each baseline current key, is there a target message
		// under that key OR any historical alias present in target.
		aliasToCurrent := map[string]string{}
		for old, cur := range cl {
			aliasToCurrent[old] = cur
		}
		for _, bk := range base.keys {
			bm := base.msgs[bk]
			var tm *catalogMessage
			var matchedKey string
			if tgt.msgs[bk] != nil {
				tm = tgt.msgs[bk]
				matchedKey = bk
			} else {
				// search aliases that land on bk
				var aliases []string
				for old, cur := range aliasToCurrent {
					if cur == bk && old != bk {
						aliases = append(aliases, old)
					}
				}
				sort.Strings(aliases)
				for _, a := range aliases {
					if tgt.msgs[a] != nil {
						tm = tgt.msgs[a]
						matchedKey = a
						break
					}
				}
			}
			if tm == nil {
				issues = append(issues, Issue{
					ID: issueID(CodeMissing, lang, bk), Code: CodeMissing,
					Language: lang, Key: bk,
				})
				continue
			}
			issues = append(issues, compareMessage(lang, bk, matchedKey, bm, tm)...)
		}

		// Extra: target keys that match neither baseline current keys nor
		// any known historical alias.
		for _, tk := range tgt.keys {
			if baseCurrent[tk] {
				continue
			}
			if cur, ok := aliasToCurrent[tk]; ok && baseCurrent[cur] {
				continue
			}
			issues = append(issues, Issue{
				ID: issueID(CodeExtra, lang, tk), Code: CodeExtra,
				Language: lang, Key: tk,
			})
		}
	}
	sortIssues(issues)
	return issues
}

func compareMessage(lang, baseKey, tgtKey string, bm, tm *catalogMessage) []Issue {
	var out []Issue
	if tm.AST == nil || bm.AST == nil {
		return out
	}
	if tm.AST.Error != "" {
		out = append(out, Issue{ID: issueID(CodeParseError, lang, baseKey), Code: CodeParseError,
			Language: lang, Key: baseKey, Detail: tm.AST.Error})
		return out
	}
	bp := paramMap(bm.AST)
	tp := paramMap(tm.AST)
	var missing, extra []string
	for n := range bp {
		if _, ok := tp[n]; !ok {
			missing = append(missing, n)
		}
	}
	for n := range tp {
		if _, ok := bp[n]; !ok {
			extra = append(extra, n)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 || len(extra) > 0 {
		out = append(out, Issue{ID: issueID(CodePlaceholderSet, lang, baseKey),
			Code: CodePlaceholderSet, Language: lang, Key: baseKey,
			Detail: fmt.Sprintf("missing=%v extra=%v", missing, extra)})
	}
	var drift []string
	for n, bt := range bp {
		if tt, ok := tp[n]; ok && typeIncompatible(bt, tt) {
			drift = append(drift, fmt.Sprintf("%s:%s->%s", n, bt, tt))
		}
	}
	sort.Strings(drift)
	if len(drift) > 0 {
		out = append(out, Issue{ID: issueID(CodePlaceholderType, lang, baseKey),
			Code: CodePlaceholderType, Language: lang, Key: baseKey,
			Detail: fmt.Sprintf("drift=%v", drift)})
	}
	// Plural categories.
	bPlural := map[string][]string{}
	for _, pl := range bm.AST.Plurals {
		bPlural[pl.Name] = pl.Categories
	}
	var missingCats []string
	for _, pl := range tm.AST.Plurals {
		want, ok := bPlural[pl.Name]
		if !ok {
			continue
		}
		have := map[string]bool{}
		for _, c := range pl.Categories {
			have[c] = true
		}
		for _, c := range want {
			if !have[c] {
				missingCats = append(missingCats, pl.Name+":"+c)
			}
		}
	}
	sort.Strings(missingCats)
	if len(missingCats) > 0 {
		out = append(out, Issue{ID: issueID(CodePluralMissing, lang, baseKey),
			Code: CodePluralMissing, Language: lang, Key: baseKey,
			Detail: fmt.Sprintf("missing=%v", missingCats)})
	}
	// Tags.
	if len(tm.AST.Unbalanced) > 0 {
		out = append(out, Issue{ID: issueID(CodeTagUnbalanced, lang, baseKey),
			Code: CodeTagUnbalanced, Language: lang, Key: baseKey,
			Detail: describeTags(tm.AST.Unbalanced)})
	}
	if !sameStringSet(bm.AST.Tags, tm.AST.Tags) && len(tm.AST.Unbalanced) == 0 {
		out = append(out, Issue{ID: issueID(CodeTagUnbalanced, lang, baseKey),
			Code: CodeTagUnbalanced, Language: lang, Key: baseKey,
			Detail: fmt.Sprintf("tags baseline=%v target=%v", bm.AST.Tags, tm.AST.Tags)})
	}
	return out
}

func paramMap(a *icu.AST) map[string]string {
	m := map[string]string{}
	for _, p := range a.Params {
		m[p.Name] = p.Type
	}
	return m
}

func typeIncompatible(a, b string) bool {
	if a == b || a == "any" || b == "any" {
		return false
	}
	// number-like family is compatible with plural numeric placeholders.
	numeric := map[string]bool{"number": true, "plural": true, "selectordinal": true}
	if numeric[a] && numeric[b] {
		return false
	}
	return true
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]bool{}
	for _, s := range a {
		m[s] = true
	}
	for _, s := range b {
		if !m[s] {
			return false
		}
	}
	return true
}

func describeTags(ps []icu.TagProblem) string {
	out := ""
	for i, p := range ps {
		if i > 0 {
			out += "; "
		}
		out += p.Kind + ":" + p.Tag
	}
	return out
}

func issueID(code, lang, key string) string {
	return code + "|" + lang + "|" + key
}

func sortIssues(is []Issue) {
	sort.SliceStable(is, func(a, b int) bool {
		if is[a].Language != is[b].Language {
			return is[a].Language < is[b].Language
		}
		if is[a].Key != is[b].Key {
			return is[a].Key < is[b].Key
		}
		return is[a].Code < is[b].Code
	})
}
