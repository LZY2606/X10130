package service

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"messagecatalog/internal/catalog"
	"messagecatalog/internal/icu"
)

// Issue codes.
const (
	CodeMissingKey         = "missing_key"
	CodeExtraKey           = "extra_key"
	CodePlaceholderMissing = "placeholder_set_mismatch"
	CodePlaceholderDrift   = "placeholder_type_drift"
	CodePluralCategory     = "plural_category_missing"
	CodeTagUnbalanced      = "richtag_unbalanced"
	CodeParseError         = "parse_error"
)

// Severity levels.
const (
	SevError = "error"
	SevWarn  = "warning"
)

// Issue is one detected problem.
type Issue struct {
	ID          string `json:"id"`
	Code        string `json:"code"`
	Severity    string `json:"severity"`
	Language    string `json:"language"`
	Key         string `json:"key"`
	Placeholder string `json:"placeholder,omitempty"`
	Message     string `json:"message"`
	Expected    string `json:"expected,omitempty"`
	Actual      string `json:"actual,omitempty"`
}

// ParsedEntry carries the parse result.
type ParsedEntry struct {
	Entry catalog.Entry
	Msg   *icu.Message
	Err   string
}

// Report is the full validation result for one target language.
type Report struct {
	Language        string   `json:"language"`
	ParserVersion   string   `json:"parserVersion"`
	BaselineVersion string   `json:"baselineVersion"`
	TargetVersion   string   `json:"targetVersion"`
	MappingVersion  int      `json:"mappingVersion"`
	GeneratedAt     string   `json:"generatedAt"`
	Issues          []*Issue `json:"issues"`
}

// parsedCatalog indexes a catalog with parse results.
type parsedCatalog struct {
	cat     *catalog.Catalog
	entries map[string]*ParsedEntry
}

func parseCatalog(c *catalog.Catalog) *parsedCatalog {
	pc := &parsedCatalog{cat: c, entries: map[string]*ParsedEntry{}}
	for _, e := range c.Entries {
		pe := &ParsedEntry{Entry: e}
		m, err := icu.Parse(catalog.NormalizeText(e.Text))
		if err != nil {
			pe.Err = err.Error()
		} else {
			pe.Msg = m
		}
		pc.entries[e.Key] = pe
	}
	return pc
}

// resolveRoot follows mapping edges old->new to the final key.
func resolveRoot(mapping map[string]string, key string) string {
	seen := map[string]bool{key: true}
	for {
		nxt, ok := mapping[key]
		if !ok {
			return key
		}
		if seen[nxt] {
			return key // defensive: cycles are rejected on insert
		}
		seen[nxt] = true
		key = nxt
	}
}

// rootAliases returns all keys that resolve to the given root.
func rootAliases(mapping map[string]string) map[string][]string {
	out := map[string][]string{}
	// include every key: both edges' sources and targets, plus catalog keys.
	add := func(k string) string {
		r := resolveRoot(mapping, k)
		out[r] = append(out[r], k)
		return r
	}
	for old, new := range mapping {
		add(old)
		add(new)
	}
	return out
}

// validateLanguage compares one target catalog against the baseline.
func validateLanguage(base, target *catalog.Catalog, mapping map[string]string, mappingVersion int, at string) *Report {
	pb := parseCatalog(base)
	pt := parseCatalog(target)

	r := &Report{
		Language:        target.Language,
		ParserVersion:   icu.Version,
		BaselineVersion: base.Fingerprint(),
		TargetVersion:   target.Fingerprint(),
		MappingVersion:  mappingVersion,
		GeneratedAt:     at,
	}

	// root -> baseline key to compare against (a root with two live baseline
	// keys would be a silent merge; that is prevented at confirm time).
	rootOfBase := map[string]string{}
	for k := range pb.entries {
		root := resolveRoot(mapping, k)
		rootOfBase[root] = k
	}

	targetRootUsed := map[string]string{}

	addIssue := func(code, sev, key, ph, msg, exp, act string) {
		root := resolveRoot(mapping, key)
		id := issueID(code, target.Language, root, ph)
		r.Issues = append(r.Issues, &Issue{
			ID: id, Code: code, Severity: sev, Language: target.Language,
			Key: key, Placeholder: ph, Message: msg, Expected: exp, Actual: act,
		})
	}

	// Per-target-key checks.
	for key, te := range pt.entries {
		root := resolveRoot(mapping, key)
		bk, ok := rootOfBase[root]
		if !ok {
			addIssue(CodeExtraKey, SevError, key, "", "key does not exist in baseline catalog", "", "")
			continue
		}
		targetRootUsed[root] = key
		be := pb.entries[bk]

		if te.Err != "" {
			addIssue(CodeParseError, SevError, key, "", "target message parse failed: "+te.Err, "", "")
			continue
		}
		if be.Err != "" {
			continue // baseline parse error is reported against baseline
		}

		// Placeholder sets.
		bNames := be.Msg.PlaceholderNames()
		tNames := te.Msg.PlaceholderNames()
		var missing, extra []string
		for n := range bNames {
			if !tNames[n] {
				missing = append(missing, n)
			}
		}
		for n := range tNames {
			if !bNames[n] {
				extra = append(extra, n)
			}
		}
		sort.Strings(missing)
		sort.Strings(extra)
		for _, n := range missing {
			addIssue(CodePlaceholderMissing, SevError, key, n,
				"placeholder present in baseline but missing in translation", "{"+n+"}", "")
		}
		for _, n := range extra {
			addIssue(CodePlaceholderMissing, SevError, key, n,
				"placeholder present in translation but missing in baseline", "", "{"+n+"}")
		}

		// Type drift.
		bTypes := be.Msg.TypeMap()
		tTypes := te.Msg.TypeMap()
		for n := range bNames {
			if bt, ok := bTypes[n]; ok {
				if tt, ok2 := tTypes[n]; ok2 && bt != tt {
					addIssue(CodePlaceholderDrift, SevError, key, n,
						"placeholder type differs from baseline", bt, tt)
				}
			}
		}

		// Plural categories: each baseline plural must cover the same keys.
		bPlural := be.Msg.PluralCategories()
		tPlural := te.Msg.PluralCategories()
		for n, bKeys := range bPlural {
			tKeys, ok := tPlural[n]
			want := keySet(bKeys)
			if !ok {
				addIssue(CodePluralCategory, SevError, key, n,
					"plural placeholder missing in translation", strings.Join(sorted(want), ","), "")
				continue
			}
			got := keySet(tKeys)
			var absent []string
			for k := range want {
				if !got[k] {
					absent = append(absent, k)
				}
			}
			sort.Strings(absent)
			if len(absent) > 0 {
				addIssue(CodePluralCategory, SevError, key, n,
					"plural categories missing: "+strings.Join(absent, ","),
					strings.Join(sorted(want), ","), strings.Join(sorted(got), ","))
			}
		}

		// Rich text tag balance.
		if !icu.TagsBalanced(te.Msg.AllTags()) {
			addIssue(CodeTagUnbalanced, SevError, key, "",
				"rich text tags are unbalanced in translation", "", "")
		}
	}

	// Baseline keys never matched by the target are missing.
	for root, bk := range rootOfBase {
		if _, ok := targetRootUsed[root]; ok {
			continue
		}
		addIssue(CodeMissingKey, SevError, bk, "", "key missing from translation", bk, "")
	}

	// Baseline-level parse errors and tag problems (reported once, language
	// tagged as the baseline so they surface in its own view).
	if base.Language == target.Language {
		for key, be := range pb.entries {
			if be.Err != "" {
				addIssue(CodeParseError, SevError, key, "", "baseline message parse failed: "+be.Err, "", "")
			} else if !icu.TagsBalanced(be.Msg.AllTags()) {
				addIssue(CodeTagUnbalanced, SevError, key, "",
					"rich text tags are unbalanced in baseline", "", "")
			}
		}
	}

	sort.SliceStable(r.Issues, func(i, j int) bool {
		if r.Issues[i].Key != r.Issues[j].Key {
			return r.Issues[i].Key < r.Issues[j].Key
		}
		return r.Issues[i].ID < r.Issues[j].ID
	})
	return r
}

func keySet(keys []string) map[string]bool {
	m := map[string]bool{}
	for _, k := range keys {
		m[k] = true
	}
	return m
}

func sorted(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// issueID is stable across renames because it uses the mapping root.
func issueID(code, language, key, placeholder string) string {
	h := sha256.Sum256([]byte(code + "\x00" + language + "\x00" + key + "\x00" + placeholder))
	return hex.EncodeToString(h[:16])
}
