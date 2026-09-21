// Package catalog parses imported message files into language catalogs and
// computes content fingerprints that ignore key ordering and line-ending
// style differences.
package catalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"msgcheck/internal/icu"
)

// Entry is one message in a catalog.
type Entry struct {
	Key     string `json:"key"`
	Pattern string `json:"pattern"`
	Context string `json:"context,omitempty"`
}

// Catalog is a parsed language catalog.
type Catalog struct {
	Language string
	Entries  map[string]Entry
	Messages map[string]*icu.Message
}

// ParseError describes a single problem in an imported file.
type ParseError struct {
	Key    string `json:"key,omitempty"`
	Detail string `json:"detail"`
}

// Parse decodes a JSON message file. Two shapes are accepted:
//
//	{"key": "pattern", ...}
//	{"key": {"pattern": "...", "context": "..."}, ...}
//
// Key order in the JSON document is irrelevant for later fingerprinting.
func Parse(language string, raw []byte) (*Catalog, []ParseError) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, []ParseError{{Detail: "file is not a JSON object: " + err.Error()}}
	}
	c := &Catalog{
		Language: language,
		Entries:  map[string]Entry{},
		Messages: map[string]*icu.Message{},
	}
	var errs []ParseError
	for k, rv := range top {
		var s string
		if err := json.Unmarshal(rv, &s); err == nil {
			c.add(Entry{Key: k, Pattern: s})
			continue
		}
		var obj struct {
			Pattern string `json:"pattern"`
			Text    string `json:"text"`
			Message string `json:"message"`
			Context string `json:"context"`
		}
		if err := json.Unmarshal(rv, &obj); err != nil {
			errs = append(errs, ParseError{Key: k, Detail: "message must be a string or {pattern,context} object"})
			continue
		}
		pattern := obj.Pattern
		if pattern == "" {
			pattern = obj.Text
		}
		if pattern == "" {
			pattern = obj.Message
		}
		c.add(Entry{Key: k, Pattern: pattern, Context: obj.Context})
	}
	return c, errs
}

func (c *Catalog) add(e Entry) {
	c.Entries[e.Key] = e
	c.Messages[e.Key] = icu.Parse(NormalizeNewlines(e.Pattern))
}

// Keys returns sorted entry keys.
func (c *Catalog) Keys() []string {
	out := make([]string, 0, len(c.Entries))
	for k := range c.Entries {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// NormalizeNewlines converts CRLF and lone CR to LF. The original bytes are
// kept untouched; normalization only feeds parsing and fingerprinting.
func NormalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return s
}

// Fingerprint returns the content fingerprint of a catalog. It covers the
// normalized key/pattern/context triples in sorted key order, so reordering
// keys or changing CRLF vs LF does not change it.
func (c *Catalog) Fingerprint() string {
	keys := c.Keys()
	var buf bytes.Buffer
	for _, k := range keys {
		e := c.Entries[k]
		row := []string{k, NormalizeNewlines(e.Pattern), e.Context}
		b, _ := json.Marshal(row)
		buf.Write(b)
		buf.WriteByte('\n')
	}
	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:16])
}

// CanonicalJSON renders normalized content deterministically (debug/export aid).
func (c *Catalog) CanonicalJSON() ([]byte, error) {
	keys := c.Keys()
	var buf bytes.Buffer
	buf.WriteString("{\n")
	for i, k := range keys {
		e := c.Entries[k]
		kb, _ := json.Marshal(k)
		buf.WriteString("  ")
		buf.Write(kb)
		buf.WriteString(": ")
		if e.Context == "" {
			pb, _ := json.Marshal(NormalizeNewlines(e.Pattern))
			buf.Write(pb)
		} else {
			obj, _ := json.Marshal(map[string]string{
				"pattern": NormalizeNewlines(e.Pattern),
				"context": e.Context,
			})
			buf.Write(obj)
		}
		if i < len(keys)-1 {
			buf.WriteString(",")
		}
		buf.WriteString("\n")
	}
	buf.WriteString("}")
	if !json.Valid(buf.Bytes()) {
		return nil, fmt.Errorf("internal: produced invalid canonical json")
	}
	return buf.Bytes(), nil
}
