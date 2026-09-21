package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// Issue types.
const (
	IssueMissingKey      = "missing_key"
	IssueExtraKey        = "extra_key"
	IssuePlaceholderSet  = "placeholder_set_mismatch"
	IssuePlaceholderType = "placeholder_type_drift"
	IssuePluralCategory  = "plural_category_missing"
	IssueTagImbalance    = "tag_imbalance"
	IssueTagMismatch     = "tag_set_mismatch"
	IssueParseError      = "parse_error"
)

// Issue is one validation finding.
type Issue struct {
	Language string `json:"language"`
	Key      string `json:"key"`
	Type     string `json:"type"`
	Name     string `json:"name,omitempty"`
	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// Clone for immutability.
func (i Issue) identity() string {
	h := sha256.New()
	write := func(label, val string) {
		h.Write([]byte(label))
		h.Write([]byte{0})
		h.Write([]byte(val))
		h.Write([]byte{0})
	}
	write("lang", i.Language)
	write("key", i.Key)
	write("type", i.Type)
	write("name", i.Name)
	write("expected", i.Expected)
	write("actual", i.Actual)
	write("detail", i.Detail)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// IssueID is the stable identifier of a finding within one validation result.
func (i Issue) IssueID(resultID string) string {
	return resultID + ":" + i.identity()
}

// Edges maps old baseline key -> new baseline key.
type Edges map[string]string

// ResolveChain walks old->new edges starting at key, returning all nodes and
// detecting cycles.
func (e Edges) ResolveChain(start string) ([]string, bool) {
	seen := map[string]bool{start: true}
	chain := []string{start}
	cur := start
	for {
		next, ok := e[cur]
		if !ok {
			return chain, false
		}
		if seen[next] {
			return chain, true
		}
		seen[next] = true
		chain = append(chain, next)
		cur = next
	}
}

// ValidateTarget compares one target language against the baseline.
func ValidateTarget(base *File, target *File, edges Edges) []Issue {
	var issues []Issue

	// Build base parsed cache.
	baseParsed := map[string]Parsed{}
	for k, v := range base.Messages {
		baseParsed[k] = Parse(v)
	}
	targParsed := map[string]Parsed{}
	for k, v := range target.Messages {
		targParsed[k] = Parse(v)
	}

	// Edges are old-name -> current-name. Build:
	//   chains[baselineKey] = all names that ever referred to this key (starting
	//   from the current baseline name and walking reverse edges);
	//   origin[name] = current baseline key.
	origin := map[string]string{}
	baselineKeys := make([]string, 0, len(base.Messages))
	for k := range base.Messages {
		baselineKeys = append(baselineKeys, k)
	}
	sort.Strings(baselineKeys)
	chains := map[string][]string{}
	reverse := map[string][]string{}
	for from, to := range edges {
		reverse[to] = append(reverse[to], from)
	}
	var expand func(string) []string
	seenGlobal := map[string]bool{}
	expand = func(node string) []string {
		out := []string{node}
		for _, prev := range reverse[node] {
			if seenGlobal[node+"->"+prev] {
				continue
			}
			seenGlobal[node+"->"+prev] = true
			out = append(out, expand(prev)...)
		}
		return out
	}
	for _, b := range baselineKeys {
		chain := expand(b)
		chains[b] = chain
		for _, node := range chain {
			if _, dup := origin[node]; !dup {
				origin[node] = b
			}
		}
	}

	// Target key ownership.
	targetKeys := make([]string, 0, len(target.Messages))
	for k := range target.Messages {
		targetKeys = append(targetKeys, k)
	}
	sort.Strings(targetKeys)

	extraSeen := map[string]bool{}
	for _, t := range targetKeys {
		if _, known := origin[t]; !known {
			if !extraSeen[t] {
				issues = append(issues, Issue{Language: target.Language, Key: t, Type: IssueExtraKey,
					Detail: "key not present in baseline or rename mapping"})
				extraSeen[t] = true
			}
		}
	}

	for _, b := range baselineKeys {
		chain := chains[b]
		var matched string
		for _, node := range chain {
			if _, ok := target.Messages[node]; ok {
				matched = node
				break
			}
		}
		if matched == "" {
			issues = append(issues, Issue{Language: target.Language, Key: b, Type: IssueMissingKey,
				Detail: "baseline key missing in target"})
			continue
		}
		bp := baseParsed[b]
		tp := targParsed[matched]

		// Generic parser errors (unmatched quotes/braces) in the target text.
		for _, pe := range tp.Errors {
			if pe == "unbalanced rich-text tags" {
				continue
			}
			issues = append(issues, Issue{Language: target.Language, Key: matched,
				Type: IssueParseError, Detail: pe})
		}

		// Parse / tag imbalance errors.
		if len(tp.Errors) > 0 {
			issues = append(issues, Issue{Language: target.Language, Key: matched, Type: IssueTagImbalance,
				Detail: strings.Join(tp.Errors, "; ")})
		}
		if len(bp.Errors) > 0 && len(tp.Errors) == 0 {
			// baseline itself is broken; not counted against target.
		}

		issues = append(issues, comparePlaceholders(target.Language, matched, bp, tp)...)
		issues = append(issues, comparePlurals(target.Language, matched, bp, tp)...)
		issues = append(issues, compareTags(target.Language, matched, bp, tp)...)
	}

	// Sort for determinism.
	sortIssues(issues)
	return issues
}

func comparePlaceholders(lang, key string, base, targ Parsed) []Issue {
	var issues []Issue
	bByName := map[string]Placeholder{}
	tByName := map[string]Placeholder{}
	for _, p := range base.Placeholders {
		bByName[p.Name] = p
	}
	for _, p := range targ.Placeholders {
		tByName[p.Name] = p
	}
	var missing, extra []string
	for name := range bByName {
		if _, ok := tByName[name]; !ok {
			missing = append(missing, name)
		}
	}
	for name := range tByName {
		if _, ok := bByName[name]; !ok {
			extra = append(extra, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	for _, name := range missing {
		// Same-name placeholders are type drift, not set mismatch.
		issues = append(issues, Issue{Language: lang, Key: key, Type: IssuePlaceholderSet, Name: name,
			Expected: "present", Actual: "missing"})
	}
	for _, name := range extra {
		issues = append(issues, Issue{Language: lang, Key: key, Type: IssuePlaceholderSet, Name: name,
			Expected: "absent", Actual: "present"})
	}
	for name, bp := range bByName {
		if tp, ok := tByName[name]; ok && bp.Type != tp.Type {
			issues = append(issues, Issue{Language: lang, Key: key, Type: IssuePlaceholderType, Name: name,
				Expected: bp.Type, Actual: tp.Type})
		}
	}
	// A baseline plural/selectordinal rendered as a simple target argument
	// necessarily drops every branch: surface the required categories.
	for name, bp := range bByName {
		if bp.Type != "plural" {
			continue
		}
		if tp, ok := tByName[name]; ok && tp.Type != "plural" {
			for _, c := range []string{"one", "other"} {
				issues = append(issues, Issue{Language: lang, Key: key, Type: IssuePluralCategory, Name: name,
					Expected: c, Actual: "missing", Detail: "baseline plural argument downgraded to " + tp.Type})
			}
		}
	}
	return dedupeIssues(issues)
}

func comparePlurals(lang, key string, base, targ Parsed) []Issue {
	var issues []Issue
	baseCases := pluralCases(base.Nodes)
	targCases := pluralCases(targ.Nodes)
	for ph, cases := range baseCases {
		tcases := targCases[ph]
		have := map[string]bool{}
		for _, c := range tcases {
			have[c] = true
		}
		missing := []string{}
		for _, c := range cases {
			if !have[c] {
				missing = append(missing, c)
			}
		}
		if !have["other"] {
			missing = append(missing, "other")
		}
		sort.Strings(missing)
		for _, c := range missing {
			issues = append(issues, Issue{Language: lang, Key: key, Type: IssuePluralCategory, Name: ph,
				Expected: c, Actual: "missing", Detail: "plural branch required by baseline"})
		}
	}
	return dedupeIssues(issues)
}

func pluralCases(nodes []Node) map[string][]string {
	out := map[string][]string{}
	var walk func([]Node)
	walk = func(ns []Node) {
		for _, n := range ns {
			if n.Kind == KindPlural {
				for _, b := range n.Branches {
					out[n.Name] = append(out[n.Name], b.Case)
				}
			}
			for _, b := range n.Branches {
				walk(b.Children)
			}
			walk(n.Children)
		}
	}
	walk(nodes)
	return out
}

func compareTags(lang, key string, base, targ Parsed) []Issue {
	bTags := tagSet(base.Tags)
	tTags := tagSet(targ.Tags)
	var issues []Issue
	for name := range bTags {
		if !tTags[name] {
			issues = append(issues, Issue{Language: lang, Key: key, Type: IssueTagMismatch, Name: name,
				Expected: "present", Actual: "missing"})
		}
	}
	for name := range tTags {
		if !bTags[name] {
			issues = append(issues, Issue{Language: lang, Key: key, Type: IssueTagMismatch, Name: name,
				Expected: "absent", Actual: "present"})
		}
	}
	return issues
}

func tagSet(tags []TagUse) map[string]bool {
	m := map[string]bool{}
	for _, t := range tags {
		m[t.Name] = true
	}
	return m
}

func dedupeIssues(in []Issue) []Issue {
	seen := map[string]bool{}
	out := in[:0]
	for _, i := range in {
		id := i.identity()
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, i)
	}
	return out
}

func sortIssues(issues []Issue) {
	sort.Slice(issues, func(i, j int) bool {
		a, b := issues[i], issues[j]
		if a.Language != b.Language {
			return a.Language < b.Language
		}
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Expected != b.Expected {
			return a.Expected < b.Expected
		}
		return a.Actual < b.Actual
	})
}
