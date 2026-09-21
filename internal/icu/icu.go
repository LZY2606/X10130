// Package icu parses ICU MessageFormat patterns with rich-text tags.
package icu

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ParserVersion identifies the grammar/semantics used to build parsed
// structures. Validation results are bound to it.
const ParserVersion = "icu-parser/1"

// Node is one element of a parsed message pattern.
type Node struct {
	Kind     string           `json:"kind"` // message, text, arg, tag, pound
	Text     string           `json:"text,omitempty"`
	Name     string           `json:"name,omitempty"`
	ArgType  string           `json:"argType,omitempty"`
	Style    string           `json:"style,omitempty"`
	Tag      string           `json:"tag,omitempty"`
	Options  map[string]*Node `json:"options,omitempty"`
	Children []*Node          `json:"children,omitempty"`
}

// Error is a parse error. Tag is true for rich-text tag balance problems.
type Error struct {
	Message string
	Tag     bool
}

func (e *Error) Error() string { return e.Message }

var (
	openTagRe      = regexp.MustCompile(`^<([a-zA-Z][a-zA-Z0-9-]*)>`)
	selfCloseTagRe = regexp.MustCompile(`^<([a-zA-Z][a-zA-Z0-9-]*)/>`)
	closeTagRe     = regexp.MustCompile(`^</([a-zA-Z][a-zA-Z0-9-]*)>`)
)

type parser struct {
	s        string
	i        int
	inPlural bool
}

// Parse parses a message pattern into a message Node.
func Parse(s string) (*Node, error) {
	p := &parser{s: s}
	children, err := p.parseSeq(false, "")
	if err != nil {
		return nil, err
	}
	return &Node{Kind: "message", Children: children}, nil
}

func isSyntaxChar(c byte) bool {
	switch c {
	case '{', '}', '<', '>', '#', '|':
		return true
	}
	return false
}

// parseApostrophe handles ICU apostrophe quoting. p.s[p.i] == '\”.
func (p *parser) parseApostrophe() string {
	if p.i+1 < len(p.s) && p.s[p.i+1] == '\'' {
		p.i += 2
		return "'"
	}
	if p.i+1 < len(p.s) && isSyntaxChar(p.s[p.i+1]) {
		p.i++ // consume opening quote
		var b strings.Builder
		for p.i < len(p.s) {
			c := p.s[p.i]
			if c == '\'' {
				if p.i+1 < len(p.s) && p.s[p.i+1] == '\'' {
					b.WriteByte('\'')
					p.i += 2
					continue
				}
				p.i++
				return b.String()
			}
			b.WriteByte(c)
			p.i++
		}
		return b.String() // unterminated quote: rest is literal (ICU-lenient)
	}
	p.i++
	return "'"
}

// parseSeq parses nodes until EOF, an unmatched '}' (when stopBrace), or the
// matching closing tag (when tag != "").
func (p *parser) parseSeq(stopBrace bool, tag string) ([]*Node, error) {
	var nodes []*Node
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			nodes = append(nodes, &Node{Kind: "text", Text: text.String()})
			text.Reset()
		}
	}
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch {
		case c == '\'':
			text.WriteString(p.parseApostrophe())
		case c == '{':
			flush()
			arg, err := p.parseArg()
			if err != nil {
				return nil, err
			}
			nodes = append(nodes, arg)
		case c == '}':
			if stopBrace {
				flush()
				return nodes, nil
			}
			return nil, &Error{Message: fmt.Sprintf("unmatched '}' at offset %d", p.i)}
		case c == '<':
			rest := p.s[p.i:]
			if m := closeTagRe.FindStringSubmatch(rest); m != nil {
				if tag != "" && m[1] == tag {
					flush()
					p.i += len(m[0])
					return nodes, nil
				}
				return nil, &Error{Message: fmt.Sprintf("unbalanced closing tag </%s>", m[1]), Tag: true}
			}
			if m := selfCloseTagRe.FindStringSubmatch(rest); m != nil {
				flush()
				nodes = append(nodes, &Node{Kind: "tag", Tag: m[1]})
				p.i += len(m[0])
				continue
			}
			if m := openTagRe.FindStringSubmatch(rest); m != nil {
				flush()
				p.i += len(m[0])
				children, err := p.parseSeq(stopBrace, m[1])
				if err != nil {
					return nil, err
				}
				nodes = append(nodes, &Node{Kind: "tag", Tag: m[1], Children: children})
				continue
			}
			text.WriteByte('<')
			p.i++
		case c == '#' && p.inPlural:
			flush()
			nodes = append(nodes, &Node{Kind: "pound"})
			p.i++
		default:
			text.WriteByte(c)
			p.i++
		}
	}
	if tag != "" {
		return nil, &Error{Message: fmt.Sprintf("unclosed tag <%s>", tag), Tag: true}
	}
	if stopBrace {
		return nil, &Error{Message: "unmatched '{'"}
	}
	flush()
	return nodes, nil
}

// parseArg parses an argument starting at '{'.
func (p *parser) parseArg() (*Node, error) {
	p.i++ // consume '{'
	start := p.i
	for p.i < len(p.s) && p.s[p.i] != ',' && p.s[p.i] != '}' {
		p.i++
	}
	if p.i >= len(p.s) {
		return nil, &Error{Message: "unmatched '{'"}
	}
	name := strings.TrimSpace(p.s[start:p.i])
	if name == "" {
		return nil, &Error{Message: "empty argument name"}
	}
	if p.s[p.i] == '}' {
		p.i++
		return &Node{Kind: "arg", Name: name, ArgType: "string"}, nil
	}
	p.i++ // consume ','
	tstart := p.i
	for p.i < len(p.s) && p.s[p.i] != ',' && p.s[p.i] != '}' {
		p.i++
	}
	typ := strings.TrimSpace(p.s[tstart:p.i])
	switch typ {
	case "plural", "select", "selectordinal":
		if p.i >= len(p.s) || p.s[p.i] != ',' {
			return nil, &Error{Message: fmt.Sprintf("expected ',' before %s options", typ)}
		}
		p.i++
		return p.parseOptions(name, typ)
	case "number", "date", "time", "spellout", "ordinal", "duration":
		style := ""
		if p.i < len(p.s) && p.s[p.i] == ',' {
			p.i++
			sstart := p.i
			for p.i < len(p.s) {
				c := p.s[p.i]
				if c == '\'' {
					p.i++
					for p.i < len(p.s) && p.s[p.i] != '\'' {
						p.i++
					}
					if p.i < len(p.s) {
						p.i++
					}
					continue
				}
				if c == '}' {
					break
				}
				p.i++
			}
			style = strings.TrimSpace(p.s[sstart:p.i])
		}
		if p.i >= len(p.s) {
			return nil, &Error{Message: "unmatched '{'"}
		}
		p.i++ // consume '}'
		return &Node{Kind: "arg", Name: name, ArgType: typ, Style: style}, nil
	default:
		return nil, &Error{Message: fmt.Sprintf("unknown argument type %q", typ)}
	}
}

func (p *parser) skipSpace() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == '\t' || p.s[p.i] == '\n' || p.s[p.i] == '\r') {
		p.i++
	}
}

// parseOptions parses plural/select option clauses after the second comma.
func (p *parser) parseOptions(name, typ string) (*Node, error) {
	n := &Node{Kind: "arg", Name: name, ArgType: typ, Options: map[string]*Node{}}
	p.skipSpace()
	if (typ == "plural" || typ == "selectordinal") && strings.HasPrefix(p.s[p.i:], "offset:") {
		p.i += len("offset:")
		st := p.i
		for p.i < len(p.s) && (p.s[p.i] == '-' || (p.s[p.i] >= '0' && p.s[p.i] <= '9')) {
			p.i++
		}
		n.Style = "offset:" + p.s[st:p.i]
	}
	for {
		p.skipSpace()
		if p.i >= len(p.s) {
			return nil, &Error{Message: "unmatched '{'"}
		}
		if p.s[p.i] == '}' {
			p.i++
			break
		}
		st := p.i
		for p.i < len(p.s) && p.s[p.i] != '{' && p.s[p.i] != '}' && p.s[p.i] != ' ' && p.s[p.i] != '\t' && p.s[p.i] != '\n' {
			p.i++
		}
		key := p.s[st:p.i]
		p.skipSpace()
		if key == "" {
			return nil, &Error{Message: "empty option key"}
		}
		if p.i >= len(p.s) || p.s[p.i] != '{' {
			return nil, &Error{Message: fmt.Sprintf("expected '{' after option %q", key)}
		}
		p.i++ // consume '{'
		sub := &parser{s: p.s, i: p.i, inPlural: typ == "plural" || typ == "selectordinal"}
		children, err := sub.parseSeq(true, "")
		if err != nil {
			return nil, err
		}
		p.i = sub.i + 1 // consume closing '}'
		if _, dup := n.Options[key]; dup {
			return nil, &Error{Message: fmt.Sprintf("duplicate option %q", key)}
		}
		n.Options[key] = &Node{Kind: "message", Children: children}
	}
	if _, ok := n.Options["other"]; !ok {
		return nil, &Error{Message: fmt.Sprintf("%s missing required 'other' option", typ)}
	}
	return n, nil
}

// Analysis summarizes a parsed message for cross-language comparison.
type Analysis struct {
	Placeholders map[string]string   `json:"placeholders"`
	Plurals      map[string][]string `json:"plurals,omitempty"`
	Tags         []string            `json:"tags,omitempty"`
}

// Analyze collects placeholder types, plural categories and tags.
func Analyze(n *Node) *Analysis {
	a := &Analysis{Placeholders: map[string]string{}}
	var walk func(n *Node)
	walk = func(n *Node) {
		switch n.Kind {
		case "arg":
			if _, ok := a.Placeholders[n.Name]; !ok {
				a.Placeholders[n.Name] = n.ArgType
			}
			if n.ArgType == "plural" || n.ArgType == "selectordinal" {
				cats := sortedKeys(n.Options)
				a.Plurals[n.Name] = unionStrings(a.Plurals[n.Name], cats)
			}
			for _, opt := range n.Options {
				walk(opt)
			}
		case "tag":
			a.Tags = append(a.Tags, n.Tag)
			for _, c := range n.Children {
				walk(c)
			}
		default:
			for _, c := range n.Children {
				walk(c)
			}
		}
	}
	walk(n)
	return a
}

func sortedKeys(m map[string]*Node) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func unionStrings(a, b []string) []string {
	set := map[string]bool{}
	for _, s := range a {
		set[s] = true
	}
	for _, s := range b {
		set[s] = true
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// NormalizeNewlines rewrites CRLF and CR to LF.
func NormalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// Canonical renders a parsed pattern in a stable form: order-independent
// option ordering and newline-normalized text. Used for content fingerprints.
func Canonical(n *Node) string {
	var b strings.Builder
	writeCanonical(n, &b)
	return b.String()
}

func writeCanonical(n *Node, b *strings.Builder) {
	switch n.Kind {
	case "message":
		b.WriteString("\x00msg\x00")
		for _, c := range n.Children {
			writeCanonical(c, b)
		}
	case "text":
		b.WriteString("\x00text\x00")
		b.WriteString(NormalizeNewlines(n.Text))
	case "pound":
		b.WriteString("\x00pound\x00")
	case "arg":
		fmt.Fprintf(b, "\x00arg\x00%s\x00%s\x00%s\x00", n.Name, n.ArgType, n.Style)
		if len(n.Options) > 0 {
			for _, k := range sortedKeys(n.Options) {
				b.WriteString(k)
				b.WriteString("\x00")
				writeCanonical(n.Options[k], b)
			}
		}
	case "tag":
		fmt.Fprintf(b, "\x00tag\x00%s\x00", n.Tag)
		for _, c := range n.Children {
			writeCanonical(c, b)
		}
	}
}
