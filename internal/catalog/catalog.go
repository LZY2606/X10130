// Package catalog handles message-file parsing, newline normalization and
// content fingerprints. A fingerprint depends only on (key, normalized text,
// context) tuples, not key order or CRLF/LF style.
package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

type Message struct {
	Key     string `json:"key"`
	Text    string `json:"text"`
	Context string `json:"context,omitempty"`
}

// ParseImport accepts either:
//
//	{"messages":[{"key":"k","text":"...","context":"..."}]}
//
// or a bare {"key":"text", ...} object.
func ParseImport(raw []byte) ([]Message, error) {
	var head map[string]json.RawMessage
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if rm, ok := head["messages"]; ok {
		var msgs []Message
		if err := json.Unmarshal(rm, &msgs); err != nil {
			return nil, fmt.Errorf("invalid messages array: %w", err)
		}
		return checkDups(msgs)
	}
	// Bare object map. JSON numbers/booleans are coerced to strings.
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("invalid catalog JSON: %w", err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	msgs := make([]Message, 0, len(keys))
	for _, k := range keys {
		var s string
		if err := json.Unmarshal(m[k], &s); err != nil {
			var v any
			if err2 := json.Unmarshal(m[k], &v); err2 != nil {
				return nil, fmt.Errorf("message %q: %w", k, err)
			}
			b, _ := json.Marshal(v)
			s = string(b)
		}
		msgs = append(msgs, Message{Key: k, Text: s})
	}
	return msgs, nil
}

func checkDups(msgs []Message) ([]Message, error) {
	seen := map[string]bool{}
	for _, m := range msgs {
		if m.Key == "" {
			return nil, errors.New("message with empty key")
		}
		if seen[m.Key] {
			return nil, fmt.Errorf("duplicate key %q", m.Key)
		}
		seen[m.Key] = true
	}
	return msgs, nil
}

// NormalizeText converts CRLF to LF and removes exactly one trailing newline.
func NormalizeText(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\r' && i+1 < len(s) && s[i+1] == '\n' {
			out = append(out, '\n')
			i++
			continue
		}
		out = append(out, s[i])
	}
	t := string(out)
	if len(t) > 0 && t[len(t)-1] == '\n' {
		t = t[:len(t)-1]
	}
	return t
}

// Fingerprint is the content hash of normalized messages.
func Fingerprint(msgs []Message) string {
	cp := make([]Message, len(msgs))
	copy(cp, msgs)
	sort.Slice(cp, func(a, b int) bool { return cp[a].Key < cp[b].Key })
	h := sha256.New()
	for _, m := range cp {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00", m.Key, NormalizeText(m.Text), m.Context)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// HashBytes is a sha256 hex of raw bytes.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
