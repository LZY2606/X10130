package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Message is one catalog entry: raw text plus parsed structure.
type Message struct {
	Key    string `json:"key"`
	Raw    string `json:"raw"`
	Parsed Parsed `json:"parsed"`
}

// File represents one uploaded language file.
type File struct {
	Language string            `json:"language"`
	Messages map[string]string `json:"messages"`
	// ParseErrors maps key -> parser error strings found while parsing.
	ParseErrors map[string][]string `json:"parseErrors,omitempty"`
}

// NormalizeNewlines converts CRLF and lone CR to LF.
func NormalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return s
}

// ParseFile decodes raw JSON into a File. Accepted shapes:
//   {"key": "text"}
//   {"messages": {"key": "text"}}
// Language comes from the explicit parameter.
func ParseFile(language string, raw []byte) (*File, error) {
	var flat map[string]string
	if err := json.Unmarshal(raw, &flat); err == nil {
		f := &File{Language: language, Messages: map[string]string{}}
		for k, v := range flat {
			f.Messages[k] = NormalizeNewlines(v)
		}
		f.collectParseErrors()
		return f, nil
	}
	var wrapped struct {
		Language string            `json:"language"`
		Messages map[string]string `json:"messages"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return nil, fmt.Errorf("file must be a JSON object of key->string or {\"messages\":{...}}: %w", err)
	}
	if wrapped.Messages == nil {
		return nil, fmt.Errorf("missing \"messages\" object")
	}
	if language == "" {
		language = wrapped.Language
	}
	f := &File{Language: language, Messages: map[string]string{}}
	for k, v := range wrapped.Messages {
		f.Messages[k] = NormalizeNewlines(v)
	}
	f.collectParseErrors()
	return f, nil
}

func (f *File) collectParseErrors() {
	for k, v := range f.Messages {
		p := Parse(v)
		if len(p.Errors) > 0 {
			if f.ParseErrors == nil {
				f.ParseErrors = map[string][]string{}
			}
			f.ParseErrors[k] = append([]string(nil), p.Errors...)
		}
	}
}

// BuildMessages parses every message into structured form, sorted by key.
func BuildMessages(m map[string]string) []Message {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]Message, 0, len(keys))
	for _, k := range keys {
		out = append(out, Message{Key: k, Raw: m[k], Parsed: Parse(m[k])})
	}
	return out
}

// ContentFingerprint identifies message content independently of key order and
// line-ending style. Input is the parsed, newline-normalized key->text map.
func ContentFingerprint(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s\x00%s\x00", k, NormalizeNewlines(m[k]))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// StructureFingerprint identifies the parsed structure of one message.
func StructureFingerprint(p Parsed) string {
	b, _ := canonicalJSON(struct {
		ParserVersion string `json:"parserVersion"`
		Nodes         []Node `json:"nodes"`
	}{ParserVersion, p.Nodes})
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
