package core

import (
	"fmt"
	"sort"
	"strings"

	"msgcat/internal/icu"
)

// Issue types.
const (
	IssueMissing               = "missing"
	IssueExtra                 = "extra"
	IssuePlaceholderMismatch   = "placeholder-mismatch"
	IssuePlaceholderTypeDrift  = "placeholder-type-drift"
	IssuePluralCategoryMissing = "plural-category-missing"
	IssueTagUnbalanced         = "tag-unbalanced"
	IssueParseError            = "parse-error"
)

// BaseLocale returns the locale marked as base, or "".
func BaseLocale(st *State) string {
	for _, l := range st.Locales {
		if l.Role == "base" {
			return l.Locale
		}
	}
	return ""
}

// ValidateLocale computes the validation result for one locale against the
// current state. The result ID is a pure function of its basis and issues,
// so unchanged locales keep their existing result (selective invalidation).
func ValidateLocale(st *State, locale string) *Result {
	ls := st.Locales[locale]
	ver := st.Versions[ls.CurrentVersion]
	chain := Chain(st.Mappings)
	mhash := MappingsHash(st.Mappings)
	baseLocale := BaseLocale(st)
	var base *Version
	if baseLocale != "" {
		base = st.Versions[st.Locales[baseLocale].CurrentVersion]
	}
	r := &Result{
		Locale:        locale,
		LocaleVersion: ver.ID,
		ParserVersion: icu.ParserVersion,
		MappingsHash:  mhash,
	}
	if base != nil {
		r.BaseVersion = base.ID
	}
	var issues []Issue
	add := func(key, typ, detail string) {
		issues = append(issues, Issue{
			ID:     "is_" + ShortHash(locale, key, typ, detail),
			Locale: locale,
			Key:    key,
			Type:   typ,
			Detail: detail,
		})
	}
	// Self checks: parse errors and tag balance.
	for _, k := range SortedKeys(ver.Messages) {
		m := ver.Messages[k]
		if m.ParseError != "" {
			add(k, IssueParseError, m.ParseError)
			continue
		}
		if err := icu.CheckTags(m.AST); err != nil {
			add(k, IssueTagUnbalanced, err.Error())
		}
	}
	// Cross checks against the base catalog.
	if base != nil && locale != baseLocale {
		eff := map[string]string{} // resolved key -> original target key
		for k := range ver.Messages {
			eff[Resolve(chain, k)] = k
		}
		for _, bk := range SortedKeys(base.Messages) {
			orig, ok := eff[bk]
			if !ok {
				add(bk, IssueMissing, "目标语言缺少该 key")
				continue
			}
			for _, is := range compareMessages(locale, bk, base.Messages[bk], ver.Messages[orig]) {
				issues = append(issues, is)
			}
		}
		for _, tk := range SortedKeys(ver.Messages) {
			rk := Resolve(chain, tk)
			if _, ok := base.Messages[rk]; !ok {
				add(tk, IssueExtra, "基准语言中不存在该 key")
			}
		}
	}
	sort.Slice(issues, func(i, j int) bool { return issues[i].ID < issues[j].ID })
	r.Issues = issues
	r.ID = ResultID(r)
	return r
}

func compareMessages(locale, key string, base, target Message) []Issue {
	var out []Issue
	add := func(typ, detail string) {
		out = append(out, Issue{
			ID:     "is_" + ShortHash(locale, key, typ, detail),
			Locale: locale,
			Key:    key,
			Type:   typ,
			Detail: detail,
		})
	}
	if base.AST == nil || target.AST == nil {
		return out
	}
	ba := argMap(icu.CollectArgs(base.AST))
	ta := argMap(icu.CollectArgs(target.AST))
	var missing, extra []string
	for name := range ba {
		if _, ok := ta[name]; !ok {
			missing = append(missing, name)
		}
	}
	for name := range ta {
		if _, ok := ba[name]; !ok {
			extra = append(extra, name)
		}
	}
	if len(missing) > 0 || len(extra) > 0 {
		sort.Strings(missing)
		sort.Strings(extra)
		add(IssuePlaceholderMismatch,
			fmt.Sprintf("缺少占位符: [%s], 多出占位符: [%s]",
				strings.Join(missing, ","), strings.Join(extra, ",")))
	}
	for _, name := range SortedKeys(ba) {
		t, ok := ta[name]
		if !ok {
			continue
		}
		b := ba[name]
		if b.Type != t.Type {
			add(IssuePlaceholderTypeDrift,
				fmt.Sprintf("占位符 %s 类型漂移: 基准 %q vs 目标 %q", name, b.Type, t.Type))
			continue
		}
		if len(b.Cats) > 0 {
			tset := map[string]bool{}
			for _, c := range t.Cats {
				tset[c] = true
			}
			var miss []string
			for _, c := range b.Cats {
				if !tset[c] {
					miss = append(miss, c)
				}
			}
			if len(miss) > 0 {
				add(IssuePluralCategoryMissing,
					fmt.Sprintf("占位符 %s 缺少复数类别: [%s]", name, strings.Join(miss, ",")))
			}
		}
	}
	return out
}

func argMap(infos []icu.ArgInfo) map[string]icu.ArgInfo {
	out := map[string]icu.ArgInfo{}
	for _, i := range infos {
		out[i.Name] = i
	}
	return out
}

// ResultID derives a deterministic ID from the result basis and issues.
func ResultID(r *Result) string {
	parts := []string{r.Locale, r.BaseVersion, r.LocaleVersion, r.ParserVersion, r.MappingsHash}
	for _, is := range r.Issues {
		parts = append(parts, is.ID)
	}
	return "r_" + ShortHash(parts...)
}

// RecomputeResults validates every locale and stores new results. Results
// whose basis is unchanged keep their existing ID and record.
func RecomputeResults(st *State) {
	for _, locale := range SortedKeys(st.Locales) {
		r := ValidateLocale(st, locale)
		if _, ok := st.Results[r.ID]; !ok {
			r.Seq = st.Seq
			st.Results[r.ID] = r
		}
	}
}

// CurrentResultIDs returns the result IDs matching the current state.
func CurrentResultIDs(st *State) map[string]string {
	out := map[string]string{}
	for _, locale := range SortedKeys(st.Locales) {
		out[locale] = ValidateLocale(st, locale).ID
	}
	return out
}

// PendingRenames computes unconfirmed rename candidates from the last two
// base versions: removed keys not yet mapped away and added keys not yet
// mapped to.
func PendingRenames(st *State) (removed, added []string) {
	baseLocale := BaseLocale(st)
	if baseLocale == "" {
		return nil, nil
	}
	var versions []*Version
	for _, v := range st.Versions {
		if v.Locale == baseLocale {
			versions = append(versions, v)
		}
	}
	if len(versions) < 2 {
		return nil, nil
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].Seq < versions[j].Seq })
	prev := versions[len(versions)-2]
	cur := versions[len(versions)-1]
	mappedFrom := map[string]bool{}
	mappedTo := map[string]bool{}
	for _, m := range st.Mappings {
		mappedFrom[m.From] = true
		mappedTo[m.To] = true
	}
	for k := range prev.Messages {
		if _, ok := cur.Messages[k]; !ok && !mappedFrom[k] {
			removed = append(removed, k)
		}
	}
	for k := range cur.Messages {
		if _, ok := prev.Messages[k]; !ok && !mappedTo[k] {
			added = append(added, k)
		}
	}
	sort.Strings(removed)
	sort.Strings(added)
	return removed, added
}

// ExemptionActive reports whether an exemption is active at the given time.
func ExemptionActive(e *Exemption, nowUnix int64) bool {
	return nowUnix < e.Deadline
}
