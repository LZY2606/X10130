// Package catalog decodes message files into a normalized, fingerprintable form.
package catalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"catalogcheck/internal/icu"
)

// Entry is one message in a catalog.
type Entry struct {
	Key     string `json:"key"`
	Message string `json:"message"`
	Context string `json:"context,omitempty"`
}

// Catalog is a decoded message file.
type Catalog struct {
	Entries []Entry                 `json:"entries"`
	Parsed  map[string]*icu.Message `json:"-"`
}

// Fingerprint is a content hash insensitive to key order and newline style.
type Fingerprint struct {
	SHA256 string `json:"sha256"`
	Keys   int    `json:"keys"`
}

// NormalizeText canonicalizes newline style: CRLF and lone CR become LF.
func NormalizeText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return s
}

// Decode parses a JSON message file. Supported shapes:
//   - {"key": "message", ...}
//   - {"messages": {"key": "message", ...}} possibly with "contexts"
//   - [{"key": "k", "message": "m", "context": "c"}, ...]
func Decode(raw []byte) (*Catalog, error) {
	data := bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("empty message file")
	}
	var entries []Entry
	switch trimmed[0] {
	case '{':
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &obj); err != nil {
			return nil, fmt.Errorf("invalid JSON: %w", err)
		}
		if msgs, ok := obj["messages"]; ok {
			var flat map[string]string
			if err := json.Unmarshal(msgs, &flat); err != nil {
				return nil, fmt.Errorf("invalid messages object: %w", err)
			}
			contexts := map[string]string{}
			if c, ok := obj["contexts"]; ok {
				_ = json.Unmarshal(c, &contexts)
			}
			for k, v := range flat {
				entries = append(entries, Entry{Key: k, Message: v, Context: contexts[k]})
			}
		} else {
			var flat map[string]string
			if err := json.Unmarshal(trimmed, &flat); err != nil {
				return nil, fmt.Errorf("invalid message object: %w", err)
			}
			for k, v := range flat {
				entries = append(entries, Entry{Key: k, Message: v})
			}
		}
	case '[':
		if err := json.Unmarshal(trimmed, &entries); err != nil {
			return nil, fmt.Errorf("invalid message array: %w", err)
		}
		for i := range entries {
			if entries[i].Key == "" {
				return nil, fmt.Errorf("entry %d has empty key", i)
			}
		}
	default:
		return nil, fmt.Errorf("unsupported message file format: expected JSON object or array")
	}
	seen := map[string]bool{}
	for i := range entries {
		e := &entries[i]
		if e.Key == "" {
			return nil, fmt.Errorf("message with empty key")
		}
		if seen[e.Key] {
			return nil, fmt.Errorf("duplicate key %q", e.Key)
		}
		seen[e.Key] = true
		e.Message = NormalizeText(e.Message)
		if e.Context != "" {
			e.Context = NormalizeText(e.Context)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	cat := &Catalog{Entries: entries, Parsed: map[string]*icu.Message{}}
	for _, e := range entries {
		cat.Parsed[e.Key] = icu.MustParse(e.Message)
	}
	return cat, nil
}

// CanonicalJSON renders entries in a stable byte form.
func (c *Catalog) CanonicalJSON() []byte {
	type canonEntry struct {
		Context string `json:"context,omitempty"`
		Key     string `json:"key"`
		Message string `json:"message"`
	}
	out := make([]canonEntry, 0, len(c.Entries))
	for _, e := range c.Entries {
		out = append(out, canonEntry{Context: e.Context, Key: e.Key, Message: e.Message})
	}
	b, _ := json.Marshal(out)
	return b
}

// Fingerprint hashes normalized content.
func (c *Catalog) Fingerprint() Fingerprint {
	sum := sha256.Sum256(c.CanonicalJSON())
	return Fingerprint{SHA256: hex.EncodeToString(sum[:]), Keys: len(c.Entries)}
}

// Entry returns the entry with the given key.
func (c *Catalog) Entry(key string) (Entry, bool) {
	i := sort.Search(len(c.Entries), func(i int) bool { return c.Entries[i].Key >= key })
	if i < len(c.Entries) && c.Entries[i].Key == key {
		return c.Entries[i], true
	}
	return Entry{}, false
}
