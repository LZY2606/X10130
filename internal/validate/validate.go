// Package validate compares target catalogs against the baseline and detects
// message-level problems.
package validate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"

	"catalogcheck/internal/catalog"
	"catalogcheck/internal/icu"
)

// MappingEdge is one confirmed rename between two baseline versions.
type MappingEdge struct {
	FromVersionID string `json:"from_version_id"`
	FromKey       string `json:"from_key"`
	ToVersionID   string `json:"to_version_id"`
	ToKey         string `json:"to_key"`
}

// Issue describes one detected problem.
type Issue struct {
	Code        string   `json:"code"`
	Language    string   `json:"language"`
	Key         string   `json:"key,omitempty"`
	Detail      string   `json:"detail,omitempty"`
	Expected    []string `json:"expected,omitempty"`
	Actual      []string `json:"actual,omitempty"`
	Fingerprint string   `json:"fingerprint"`
}

// EntryView keeps raw text and parsed structure for side-by-side replay.
type EntryView struct {
	Key     string       `json:"key"`
	Message string       `json:"message"`
	Context string       `json:"context,omitempty"`
	Parsed  *icu.Message `json:"parsed"`
}

// ResultData is a persisted, replayable validation result.
type ResultData struct {
	ID                string      `json:"id"`
	ParserVersion     string      `json:"parser_version"`
	Language          string      `json:"language"`
	BaseVersionID     string      `json:"base_version_id"`
	TargetVersionID   string      `json:"target_version_id"`
	BaseFingerprint   string      `json:"base_fingerprint"`
	TargetFingerprint string      `json:"target_fingerprint"`
	MappingHash       string      `json:"mapping_hash"`
	Issues            []Issue     `json:"issues"`
	BaseEntries       []EntryView `json:"base_entries"`
	TargetEntries     []EntryView `json:"target_entries"`
	Matches           []Match     `json:"matches"`
	CreatedAt         time.Time   `json:"created_at"`
}

// Match links a current baseline key to the corresponding target key.
type Match struct {
	BaseKey   string `json:"base_key"`
	TargetKey string `json:"target_key"`
	Renamed   bool   `json:"renamed"`
}

func issueFingerprint(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// MappingHash hashes the confirmed rename edges in a stable order.
func MappingHash(edges []MappingEdge) string {
	cp := append([]MappingEdge(nil), edges...)
	sort.Slice(cp, func(i, j int) bool {
		if cp[i].FromVersionID != cp[j].FromVersionID {
			return cp[i].FromVersionID < cp[j].FromVersionID
		}
		if cp[i].FromKey != cp[j].FromKey {
			return cp[i].FromKey < cp[j].FromKey
		}
		if cp[i].ToVersionID != cp[j].ToVersionID {
			return cp[i].ToVersionID < cp[j].ToVersionID
		}
		return cp[i].ToKey < cp[j].ToKey
	})
	b, _ := json.Marshal(cp)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}

// forward follows rename edges starting at key k.
func forward(k string, edges []MappingEdge) string {
	cur := k
	for {
		next := ""
		for _, e := range edges {
			if e.FromKey == cur {
				next = e.ToKey
				break
			}
		}
		if next == "" {
			return cur
		}
		cur = next
	}
}

func views(c *catalog.Catalog) []EntryView {
	vs := make([]EntryView, 0, len(c.Entries))
	for _, e := range c.Entries {
		vs = append(vs, EntryView{Key: e.Key, Message: e.Message, Context: e.Context, Parsed: c.Parsed[e.Key]})
	}
	return vs
}

// Validate runs all checks for one target language against the baseline.
func Validate(language, baseVersionID, targetVersionID string, base, target *catalog.Catalog, edges []MappingEdge, parserVersion string, now time.Time) *ResultData {
	r := &ResultData{
		ParserVersion:     parserVersion,
		Language:          language,
		BaseVersionID:     baseVersionID,
		TargetVersionID:   targetVersionID,
		BaseFingerprint:   base.Fingerprint().SHA256,
		TargetFingerprint: target.Fingerprint().SHA256,
		MappingHash:       MappingHash(edges),
		BaseEntries:       views(base),
		TargetEntries:     views(target),
		CreatedAt:         now.UTC(),
	}

	matched := map[string]string{} // current base key -> target key
	consumed := map[string]bool{}
	baseSet := map[string]bool{}
	for _, e := range base.Entries {
		baseSet[e.Key] = true
	}
	for _, te := range target.Entries {
		end := forward(te.Key, edges)
		if baseSet[end] {
			if existing, ok := matched[end]; !ok || existing == end {
				matched[end] = te.Key
			}
			consumed[te.Key] = true
		}
	}
	keys := make([]string, 0, len(matched))
	for bk := range matched {
		keys = append(keys, bk)
	}
	sort.Strings(keys)
	for _, bk := range keys {
		tk := matched[bk]
		r.Matches = append(r.Matches, Match{BaseKey: bk, TargetKey: tk, Renamed: tk != bk})
	}

	for _, be := range base.Entries {
		tk, ok := matched[be.Key]
		if !ok {
			r.Issues = append(r.Issues, Issue{
				Code: "missing_key", Language: language, Key: be.Key,
				Detail:      "目标语言缺少基准消息",
				Fingerprint: issueFingerprint("missing_key", language, be.Key),
			})
			continue
		}
		r.comparePair(be.Key, tk, base, target)
	}
	for _, te := range target.Entries {
		if !consumed[te.Key] {
			r.Issues = append(r.Issues, Issue{
				Code: "extra_key", Language: language, Key: te.Key,
				Detail:      "目标语言存在基准中没有的消息",
				Fingerprint: issueFingerprint("extra_key", language, te.Key),
			})
		}
	}
	sort.SliceStable(r.Issues, func(i, j int) bool {
		if r.Issues[i].Code != r.Issues[j].Code {
			return r.Issues[i].Code < r.Issues[j].Code
		}
		if r.Issues[i].Key != r.Issues[j].Key {
			return r.Issues[i].Key < r.Issues[j].Key
		}
		return r.Issues[i].Detail < r.Issues[j].Detail
	})
	r.ID = resultID(r)
	return r
}

func resultID(r *ResultData) string {
	h := sha256.New()
	write := func(s string) { h.Write([]byte(s)); h.Write([]byte{0}) }
	write(r.ParserVersion)
	write(r.Language)
	write(r.BaseVersionID)
	write(r.TargetVersionID)
	write(r.BaseFingerprint)
	write(r.TargetFingerprint)
	write(r.MappingHash)
	return hex.EncodeToString(h.Sum(nil))[:20]
}

func (r *ResultData) comparePair(baseKey, targetKey string, base, target *catalog.Catalog) {
	bm := base.Parsed[baseKey]
	tm := target.Parsed[targetKey]
	if bm == nil || tm == nil {
		return
	}
	if bm.ParseError != "" {
		r.Issues = append(r.Issues, Issue{
			Code: "parse_error", Language: r.Language, Key: baseKey, Detail: "基准: " + bm.ParseError,
			Fingerprint: issueFingerprint("parse_error", r.Language, baseKey, "base", bm.ParseError),
		})
	}
	if tm.ParseError != "" {
		r.Issues = append(r.Issues, Issue{
			Code: "parse_error", Language: r.Language, Key: baseKey, Detail: "目标: " + tm.ParseError,
			Fingerprint: issueFingerprint("parse_error", r.Language, baseKey, "target", tm.ParseError),
		})
	}
	r.comparePlaceholders(baseKey, bm, tm)
	r.compareTags(baseKey, bm, tm)
}

func argMap(m *icu.Message) map[string]icu.Placeholder {
	out := map[string]icu.Placeholder{}
	var walk func(toks []icu.Token)
	walk = func(toks []icu.Token) {
		for _, t := range toks {
			if t.Kind == "arg" {
				if _, ok := out[t.Arg.Name]; !ok {
					out[t.Arg.Name] = *t.Arg
				}
				for _, b := range t.Arg.Branches {
					if pm, err := icu.Parse(b.Message); err == nil {
						walk(pm.Tokens)
					}
				}
			}
		}
	}
	walk(m.Tokens)
	return out
}

func sortedNames(set map[string]icu.Placeholder) []string {
	ns := make([]string, 0, len(set))
	for n := range set {
		ns = append(ns, n)
	}
	sort.Strings(ns)
	return ns
}

func (r *ResultData) comparePlaceholders(key string, bm, tm *icu.Message) {
	base := argMap(bm)
	targ := argMap(tm)
	var missing, extra []string
	for n := range base {
		if _, ok := targ[n]; !ok {
			missing = append(missing, n)
		}
	}
	for n := range targ {
		if _, ok := base[n]; !ok {
			extra = append(extra, n)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 || len(extra) > 0 {
		r.Issues = append(r.Issues, Issue{
			Code: "placeholder_set", Language: r.Language, Key: key,
			Detail:   "占位符集合不一致",
			Expected: sortedNames(base), Actual: sortedNames(targ),
			Fingerprint: issueFingerprint("placeholder_set", r.Language, key,
				join(missing), join(extra)),
		})
	}
	names := make([]string, 0)
	for n := range base {
		if _, ok := targ[n]; ok {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		ba := base[n]
		ta := targ[n]
		if ba.Type != ta.Type {
			r.Issues = append(r.Issues, Issue{
				Code: "placeholder_type", Language: r.Language, Key: key,
				Detail:   "占位符 " + n + " 类型漂移",
				Expected: []string{ba.Type}, Actual: []string{ta.Type},
				Fingerprint: issueFingerprint("placeholder_type", r.Language, key, n),
			})
			continue
		}
		if ba.Type == "plural" || ba.Type == "select" || ba.Type == "selectordinal" {
			bc := branchSet(ba)
			tc := branchSet(ta)
			var miss []string
			for c := range bc {
				if !tc[c] {
					miss = append(miss, c)
				}
			}
			sort.Strings(miss)
			if len(miss) > 0 {
				r.Issues = append(r.Issues, Issue{
					Code: "plural_missing", Language: r.Language, Key: key,
					Detail:   "占位符 " + n + " 缺少分支: " + join(miss),
					Expected: setSorted(bc), Actual: setSorted(tc),
					Fingerprint: issueFingerprint("plural_missing", r.Language, key, n, join(miss)),
				})
			}
		}
	}
}

func branchSet(p icu.Placeholder) map[string]bool {
	s := map[string]bool{}
	for _, b := range p.Branches {
		s[b.Key] = true
	}
	return s
}

func setSorted(s map[string]bool) []string {
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func join(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += ","
		}
		out += x
	}
	return out
}

func (r *ResultData) compareTags(key string, bm, tm *icu.Message) {
	if len(bm.Unbalanced) > 0 {
		names := tagNames(bm.Unbalanced)
		r.Issues = append(r.Issues, Issue{
			Code: "tag_imbalance", Language: r.Language, Key: key,
			Detail:      "基准富文本标签不平衡: " + join(names),
			Actual:      names,
			Fingerprint: issueFingerprint("tag_imbalance", r.Language, key, "base", join(names)),
		})
	}
	if len(tm.Unbalanced) > 0 {
		names := tagNames(tm.Unbalanced)
		r.Issues = append(r.Issues, Issue{
			Code: "tag_imbalance", Language: r.Language, Key: key,
			Detail:      "目标富文本标签不平衡: " + join(names),
			Actual:      names,
			Fingerprint: issueFingerprint("tag_imbalance", r.Language, key, "target", join(names)),
		})
	}
	bt := map[string]bool{}
	tt := map[string]bool{}
	for _, n := range bm.TagNames {
		bt[n] = true
	}
	for _, n := range tm.TagNames {
		tt[n] = true
	}
	var missing, extra []string
	for n := range bt {
		if !tt[n] {
			missing = append(missing, n)
		}
	}
	for n := range tt {
		if !bt[n] {
			extra = append(extra, n)
		}
	}
	if len(missing) > 0 || len(extra) > 0 {
		sort.Strings(missing)
		sort.Strings(extra)
		r.Issues = append(r.Issues, Issue{
			Code: "tag_set", Language: r.Language, Key: key,
			Detail:   "富文本标签集合不一致",
			Expected: bm.TagNames, Actual: tm.TagNames,
			Fingerprint: issueFingerprint("tag_set", r.Language, key, join(missing), join(extra)),
		})
	}
}

func tagNames(t []icu.TagOccurrence) []string {
	seen := map[string]bool{}
	var out []string
	for _, o := range t {
		if !seen[o.Name] {
			seen[o.Name] = true
			out = append(out, o.Name)
		}
	}
	sort.Strings(out)
	return out
}
