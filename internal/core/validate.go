package core

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"msgcat/internal/icu"
)

// languagesLocked returns base + target languages, sorted.
func (s *Service) languagesLocked() []string {
	set := map[string]bool{}
	if s.st.BaseLanguage != "" {
		set[s.st.BaseLanguage] = true
	}
	for lang := range s.st.Current {
		set[lang] = true
	}
	out := make([]string, 0, len(set))
	for lang := range set {
		out = append(out, lang)
	}
	sort.Strings(out)
	return out
}

// depHashLocked computes the dependency fingerprint for one language's
// validation result: parser version, base + target versions, the mappings
// relevant to the involved keys, and the active exemptions for the language.
// Changes that do not affect a language leave its fingerprint — and thus its
// existing result — untouched (selective invalidation).
func (s *Service) depHashLocked(lang string, now time.Time) string {
	st := s.st
	baseID := st.Current[st.BaseLanguage]
	targetID := st.Current[lang]
	keys := map[string]bool{}
	if base := st.Versions[baseID]; base != nil {
		for k := range base.Messages {
			keys[k] = true
		}
	}
	if target := st.Versions[targetID]; target != nil {
		for k := range target.Messages {
			keys[k] = true
		}
	}
	var maps []string
	for old, nw := range st.Mappings {
		if keys[old] || keys[nw] {
			maps = append(maps, old+"->"+nw)
		}
	}
	sort.Strings(maps)
	var exs []string
	for _, ex := range st.Exemptions {
		if ex.Language != lang {
			continue
		}
		exp, err := time.Parse(time.RFC3339Nano, ex.ExpiresAt)
		if err != nil || !now.Before(exp) {
			continue // expired exemptions do not shape results
		}
		exs = append(exs, ex.IssueID+"|"+ex.Reason+"|"+ex.ExpiresAt)
	}
	sort.Strings(exs)
	parts := []string{"parser=" + icu.ParserVersion, "base=" + baseID, "target=" + targetID}
	parts = append(parts, maps...)
	parts = append(parts, exs...)
	return "r" + hashParts(strings.Join(parts, "\n"))[:40]
}

// resultForLocked returns the cached or freshly computed validation result
// for a language. The second return value reports whether a new result was
// created (caller should commit).
func (s *Service) resultForLocked(lang string, now time.Time) (*Result, bool) {
	id := s.depHashLocked(lang, now)
	if r, ok := s.st.Results[id]; ok {
		return r, false
	}
	r := &Result{
		ID:            id,
		Language:      lang,
		ParserVersion: icu.ParserVersion,
		BaseVersion:   s.st.Current[s.st.BaseLanguage],
		TargetVersion: s.st.Current[lang],
		MappingHash:   s.mappingHashLocked(),
		Snapshot:      s.st.Seq,
		Issues:        s.computeIssuesLocked(lang, now),
	}
	s.st.Results[id] = r
	return r, true
}

func sortedKeysOf[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func issueID(lang, key, kind, detail string) string {
	return hashParts(lang, key, kind, detail)[:16]
}

// computeIssuesLocked runs all checks for one language against the base.
func (s *Service) computeIssuesLocked(lang string, now time.Time) []*Issue {
	st := s.st
	var issues []*Issue
	add := func(key, kind, detail string) {
		issues = append(issues, &Issue{
			ID:       issueID(lang, key, kind, detail),
			Language: lang,
			Key:      key,
			Kind:     kind,
			Detail:   detail,
		})
	}
	base := st.Versions[st.Current[st.BaseLanguage]]
	target := st.Versions[st.Current[lang]]
	if target == nil {
		return issues
	}
	// Parse errors (including unbalanced rich-text tags) are always reported.
	for _, k := range sortedKeysOf(target.Messages) {
		m := target.Messages[k]
		if m.ParseError != "" {
			kind := m.ParseKind
			if kind == "" {
				kind = "syntax-error"
			}
			add(k, kind, m.ParseError)
		}
	}
	if lang == st.BaseLanguage || base == nil {
		return s.applyExemptionsLocked(lang, issues, now)
	}
	// Key matching through confirmed rename mappings.
	matched := map[string]*Message{} // base key -> target message
	for _, tk := range sortedKeysOf(target.Messages) {
		resolved := s.Resolve(tk)
		if _, ok := base.Messages[resolved]; ok {
			if _, dup := matched[resolved]; dup {
				add(tk, "extra-key", fmt.Sprintf("key %q and another key both resolve to base key %q", tk, resolved))
				continue
			}
			matched[resolved] = target.Messages[tk]
		} else {
			add(tk, "extra-key", fmt.Sprintf("key %q has no counterpart in the base catalog (no confirmed mapping)", tk))
		}
	}
	for _, bk := range sortedKeysOf(base.Messages) {
		bm := base.Messages[bk]
		tm, ok := matched[bk]
		if !ok {
			add(bk, "missing-key", fmt.Sprintf("key %q is present in the base catalog but missing in %s", bk, lang))
			continue
		}
		if tm.ParseError != "" || bm.ParseError != "" {
			continue // structural comparison impossible
		}
		// Placeholder set mismatch.
		var missing, extra []string
		for name := range bm.Placeholders {
			if _, ok := tm.Placeholders[name]; !ok {
				missing = append(missing, name)
			}
		}
		for name := range tm.Placeholders {
			if _, ok := bm.Placeholders[name]; !ok {
				extra = append(extra, name)
			}
		}
		if len(missing) > 0 || len(extra) > 0 {
			sort.Strings(missing)
			sort.Strings(extra)
			var parts []string
			if len(missing) > 0 {
				parts = append(parts, "missing placeholders: "+strings.Join(missing, ", "))
			}
			if len(extra) > 0 {
				parts = append(parts, "extra placeholders: "+strings.Join(extra, ", "))
			}
			add(bk, "placeholder-mismatch", strings.Join(parts, "; "))
		}
		// Placeholder type drift.
		var drifts []string
		for _, name := range sortedKeysOf(bm.Placeholders) {
			bt := bm.Placeholders[name]
			tt, ok := tm.Placeholders[name]
			if ok && bt != tt {
				drifts = append(drifts, fmt.Sprintf("%s: base=%s target=%s", name, bt, tt))
			}
		}
		if len(drifts) > 0 {
			add(bk, "placeholder-type-drift", strings.Join(drifts, "; "))
		}
		// Missing plural categories.
		for _, name := range sortedKeysOf(bm.Plurals) {
			bcats := bm.Plurals[name]
			tcats, ok := tm.Plurals[name]
			if !ok {
				continue // covered by placeholder-mismatch
			}
			tset := map[string]bool{}
			for _, c := range tcats {
				tset[c] = true
			}
			var miss []string
			for _, c := range bcats {
				if !tset[c] {
					miss = append(miss, c)
				}
			}
			if len(miss) > 0 {
				sort.Strings(miss)
				add(bk, "plural-category-missing", fmt.Sprintf("placeholder {%s}: missing plural categories: %s", name, strings.Join(miss, ", ")))
			}
		}
	}
	return s.applyExemptionsLocked(lang, issues, now)
}

func (s *Service) applyExemptionsLocked(lang string, issues []*Issue, now time.Time) []*Issue {
	for _, iss := range issues {
		ex, ok := s.st.Exemptions[iss.ID]
		if !ok {
			continue
		}
		exp, err := time.Parse(time.RFC3339Nano, ex.ExpiresAt)
		if err != nil || !now.Before(exp) {
			continue // expired: no longer waives
		}
		iss.Exempted = true
		iss.Reason = ex.Reason
		iss.ExpiresAt = ex.ExpiresAt
	}
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Key != issues[j].Key {
			return issues[i].Key < issues[j].Key
		}
		return issues[i].ID < issues[j].ID
	})
	return issues
}
