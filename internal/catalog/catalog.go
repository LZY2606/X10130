// Package catalog defines imported message catalogs and their content
// fingerprints, which ignore key order and CRLF/LF differences.
package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Entry is one message.
type Entry struct {
	Key     string `json:"key"`
	Text    string `json:"text"`
	Context string `json:"context,omitempty"`
}

// Catalog is one language file.
type Catalog struct {
	Language string  `json:"language"`
	Entries  []Entry `json:"entries"`
}

// Index returns key -> entry.
func (c *Catalog) Index() map[string]Entry {
	m := make(map[string]Entry, len(c.Entries))
	for _, e := range c.Entries {
		m[e.Key] = e
	}
	return m
}

// rawFile accepts {"key": "text"} and {"key": {"text","context"}}.
type rawFile map[string]json.RawMessage

type richEntry struct {
	Text    string `json:"text"`
	Context string `json:"context"`
}

// Parse decodes an uploaded JSON message file. Language is assigned by the
// caller because the file itself does not carry it.
func Parse(language string, data []byte) (*Catalog, error) {
	var raw rawFile
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("invalid JSON catalog: %w", err)
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	c := &Catalog{Language: language}
	for _, k := range keys {
		rv := raw[k]
		var e Entry
		var asString string
		if err := json.Unmarshal(rv, &asString); err == nil {
			e = Entry{Key: k, Text: asString}
		} else {
			var re richEntry
			if err := json.Unmarshal(rv, &re); err != nil {
				return nil, fmt.Errorf("message %q must be a string or {text,context}", k)
			}
			e = Entry{Key: k, Text: re.Text, Context: re.Context}
		}
		c.Entries = append(c.Entries, e)
	}
	return c, nil
}

// NormalizeText converts CRLF/CR to LF. Fingerprints use normalized text so
// line-ending changes do not create new content versions.
func NormalizeText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return s
}

type canonEntry struct {
	Key     string `json:"key"`
	Text    string `json:"text"`
	Context string `json:"context"`
}

type canonCatalog struct {
	Entries []canonEntry `json:"entries"`
}

// canonicalBytes renders the order-independent, newline-normalized form.
func (c *Catalog) canonicalBytes() []byte {
	cc := canonCatalog{Entries: make([]canonEntry, 0, len(c.Entries))}
	for _, e := range c.Entries {
		cc.Entries = append(cc.Entries, canonEntry{
			Key:     e.Key,
			Text:    NormalizeText(e.Text),
			Context: e.Context,
		})
	}
	sort.Slice(cc.Entries, func(i, j int) bool { return cc.Entries[i].Key < cc.Entries[j].Key })
	b, _ := json.Marshal(cc)
	return b
}

// Fingerprint is the content hash. Language is intentionally excluded: the
// same messages in another language produce different text anyway.
func (c *Catalog) Fingerprint() string {
	sum := sha256.Sum256(c.canonicalBytes())
	return hex.EncodeToString(sum[:])
}

// ShortID returns the first 12 hex chars for display.
func ShortID(fingerprint string) string {
	if len(fingerprint) <= 12 {
		return fingerprint
	}
	return fingerprint[:12]
}
