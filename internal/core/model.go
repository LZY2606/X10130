// Package core holds the catalog domain model: versions, mappings,
// exemptions, validation results and proofs.
package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"msgcat/internal/icu"
)

// Message is one catalog entry: raw text plus parsed structure.
type Message struct {
	Text       string       `json:"text"`
	Context    string       `json:"context,omitempty"`
	AST        *icu.Message `json:"ast,omitempty"`
	ParseError string       `json:"parseError,omitempty"`
}

// Version is an immutable catalog snapshot for one locale.
type Version struct {
	ID          string             `json:"id"`
	Locale      string             `json:"locale"`
	Fingerprint string             `json:"fingerprint"`
	Messages    map[string]Message `json:"messages"`
	Seq         int64              `json:"seq"`
}

// Import records one uploaded file. The raw bytes live in a blob so the
// exact uploaded file can be downloaded even when the version is unchanged.
type Import struct {
	ID        string `json:"id"`
	Locale    string `json:"locale"`
	Filename  string `json:"filename"`
	BlobHash  string `json:"blobHash"`
	Size      int64  `json:"size"`
	VersionID string `json:"versionId"`
	Seq       int64  `json:"seq"`
}

// LocaleState is the mutable head for one locale.
type LocaleState struct {
	Locale         string   `json:"locale"`
	Role           string   `json:"role"` // base | target
	CurrentVersion string   `json:"currentVersion"`
	Imports        []string `json:"imports"`
}

// Mapping is a confirmed rename from an old base key to a new one.
type Mapping struct {
	ID          string `json:"id"`
	From        string `json:"from"`
	To          string `json:"to"`
	BaseVersion string `json:"baseVersion"`
	Seq         int64  `json:"seq"`
}

// Exemption waives one issue until a deadline (unix seconds).
type Exemption struct {
	ID       string `json:"id"`
	IssueID  string `json:"issueId"`
	Reason   string `json:"reason"`
	Deadline int64  `json:"deadline"`
	Seq      int64  `json:"seq"`
}

// Issue is one validation finding.
type Issue struct {
	ID     string `json:"id"`
	Locale string `json:"locale"`
	Key    string `json:"key"`
	Type   string `json:"type"`
	Detail string `json:"detail"`
}

// Result is a persisted validation outcome bound to its exact basis.
type Result struct {
	ID            string  `json:"id"`
	Locale        string  `json:"locale"`
	BaseVersion   string  `json:"baseVersion"`
	LocaleVersion string  `json:"localeVersion"`
	ParserVersion string  `json:"parserVersion"`
	MappingsHash  string  `json:"mappingsHash"`
	Issues        []Issue `json:"issues"`
	Seq           int64   `json:"seq"`
}

// OpRecord persists the outcome of a mutating operation for idempotent
// retries keyed by a caller-supplied operation ID.
type OpRecord struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	RequestHash string          `json:"requestHash"`
	Result      json.RawMessage `json:"result"`
	Done        bool            `json:"done"`
	Seq         int64           `json:"seq"`
}

// ProofMeta tracks a stored proof file.
type ProofMeta struct {
	ID       string `json:"id"`
	Snapshot int64  `json:"snapshot"`
}

// State is the whole persistent domain state.
type State struct {
	Seq        int64                   `json:"seq"`
	Locales    map[string]*LocaleState `json:"locales"`
	Versions   map[string]*Version     `json:"versions"`
	Imports    map[string]*Import      `json:"imports"`
	Mappings   map[string]*Mapping     `json:"mappings"`
	Exemptions map[string]*Exemption   `json:"exemptions"`
	Results    map[string]*Result      `json:"results"`
	Ops        map[string]*OpRecord    `json:"ops"`
	Proofs     map[string]*ProofMeta   `json:"proofs"`
}

// NewState returns an empty state.
func NewState() *State {
	return &State{
		Locales:    map[string]*LocaleState{},
		Versions:   map[string]*Version{},
		Imports:    map[string]*Import{},
		Mappings:   map[string]*Mapping{},
		Exemptions: map[string]*Exemption{},
		Results:    map[string]*Result{},
		Ops:        map[string]*OpRecord{},
		Proofs:     map[string]*ProofMeta{},
	}
}

// Clone deep-copies the state via a JSON round trip.
func (s *State) Clone() *State {
	data, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	var out State
	if err := json.Unmarshal(data, &out); err != nil {
		panic(err)
	}
	return &out
}

// ParseCatalogFile parses an uploaded catalog file. The format is a JSON
// object mapping keys to either a string or {"text","context"}.
func ParseCatalogFile(content []byte) (map[string]Message, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(content, &raw); err != nil {
		return nil, fmt.Errorf("catalog file must be a JSON object: %w", err)
	}
	out := map[string]Message{}
	for k, v := range raw {
		var text, context string
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			text = s
		} else {
			var obj struct {
				Text    string `json:"text"`
				Context string `json:"context"`
			}
			if err := json.Unmarshal(v, &obj); err != nil {
				return nil, fmt.Errorf("key %q: cannot parse message", k)
			}
			text, context = obj.Text, obj.Context
		}
		m := Message{Text: text, Context: context}
		ast, err := icu.Parse(text)
		if err != nil {
			m.ParseError = err.Error()
		} else {
			m.AST = ast
		}
		out[k] = m
	}
	return out, nil
}

// NormalizeNewlines folds CRLF and CR to LF for fingerprinting.
func NormalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// Fingerprint computes the content fingerprint of a catalog. Key order and
// newline style do not affect it.
func Fingerprint(messages map[string]Message) string {
	keys := SortedKeys(messages)
	h := sha256.New()
	for _, k := range keys {
		m := messages[k]
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write([]byte(m.Context))
		h.Write([]byte{0})
		h.Write([]byte(NormalizeNewlines(m.Text)))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// VersionID derives a locale-scoped version ID from a fingerprint.
func VersionID(locale, fingerprint string) string {
	sum := sha256.Sum256([]byte(locale + "\x00" + fingerprint))
	return "v_" + hex.EncodeToString(sum[:])[:16]
}

// SortedKeys returns sorted message keys.
func SortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Chain flattens mappings into a from->to lookup.
func Chain(mappings map[string]*Mapping) map[string]string {
	out := map[string]string{}
	for _, m := range mappings {
		out[m.From] = m.To
	}
	return out
}

// Resolve follows the mapping chain from k to its latest key.
func Resolve(chain map[string]string, k string) string {
	seen := map[string]bool{}
	for {
		next, ok := chain[k]
		if !ok || seen[k] {
			return k
		}
		seen[k] = true
		k = next
	}
}

// WouldCycle reports whether adding from->to would create a cycle.
func WouldCycle(chain map[string]string, from, to string) bool {
	x := to
	for {
		if x == from {
			return true
		}
		next, ok := chain[x]
		if !ok {
			return false
		}
		x = next
	}
}

// MappingsHash fingerprints the active mapping set.
func MappingsHash(mappings map[string]*Mapping) string {
	lines := make([]string, 0, len(mappings))
	for _, m := range mappings {
		lines = append(lines, m.From+"->"+m.To)
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])[:16]
}

// ShortHash hashes arbitrary parts into a short hex id fragment.
func ShortHash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}
