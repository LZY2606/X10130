// Package icu parses a pragmatic subset of ICU MessageFormat:
// simple and typed arguments, plural/selectordinal options, the '#'
// placeholder, apostrophe quoting, and XML-like rich text tags.
package icu

import (
	"sort"
	"strings"
)

// ParserVersion is embedded into every validation run so results stay
// bound to the exact parser semantics that produced them.
const ParserVersion = "icu-parser/1"

type Node struct {
	Kind     string             `json:"kind"` // text, arg, plural, tag, pound
	Text     string             `json:"text,omitempty"`
	Name     string             `json:"name,omitempty"`
	ArgType  string             `json:"argType,omitempty"`
	Style    string             `json:"style,omitempty"`
	Options  map[string][]*Node `json:"options,omitempty"`
	Children []*Node            `json:"children,omitempty"`
}

type Message struct {
	Nodes []*Node `json:"nodes"`
}

// Summary is the semantic fingerprint of a parsed message used by the
// validator to compare languages.
type Summary struct {
	Placeholders map[string]string   `json:"placeholders"` // name -> arg type ("" for simple)
	Plurals      map[string][]string `json:"plurals,omitempty"`
	Tags         []string            `json:"tags,omitempty"`
	Errors       []string            `json:"errors,omitempty"`
}

func Parse(text string) (*Message, []string) {
	p := &parser{s: text}
	nodes, _ := p.parseSequence(seqEOF)
	return &Message{Nodes: nodes}, p.errs
}

func Summarize(m *Message, parseErrs []string) Summary {
	s := Summary{Placeholders: map[string]string{}}
	if len(parseErrs) > 0 {
		s.Errors = append([]string(nil), parseErrs...)
	}
	var walk func(ns []*Node)
	walk = func(ns []*Node) {
		for _, n := range ns {
			switch n.Kind {
			case "arg":
				s.Placeholders[n.Name] = n.ArgType
			case "plural":
				s.Placeholders[n.Name] = n.ArgType
				if s.Plurals == nil {
					s.Plurals = map[string][]string{}
				}
				cats := make([]string, 0, len(n.Options))
				for cat, ch := range n.Options {
					cats = append(cats, cat)
					walk(ch)
				}
				sort.Strings(cats)
				s.Plurals[n.Name] = cats
			case "tag":
				s.Tags = append(s.Tags, n.Name)
				walk(n.Children)
			}
		}
	}
	walk(m.Nodes)
	if len(s.Tags) > 0 {
		sort.Strings(s.Tags)
		s.Tags = dedupe(s.Tags)
	}
	return s
}

func dedupe(in []string) []string {
	out := in[:0]
	for i, v := range in {
		if i == 0 || in[i-1] != v {
			out = append(out, v)
		}
	}
	return out
}

const (
	seqEOF   = ""
	seqBrace = "}"
)

type parser struct {
	s    string
	i    int
	errs []string
}

func (p *parser) errf(msg string) {
	p.errs = append(p.errs, msg)
}

func isSyntaxChar(c byte) bool {
	switch c {
	case '{', '}', '#', '<', '>', '|':
		return true
	}
	return false
}

func isTagNameChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_'
}

// parseSequence parses nodes until EOF, an unquoted '}' (when stop ==
// seqBrace) or a matching closing tag (when stop is a tag name).
// The second return value reports whether the stop condition was met.
func (p *parser) parseSequence(stop string) ([]*Node, bool) {
	var nodes []*Node
	var buf strings.Builder
	flush := func() {
		if buf.Len() > 0 {
			nodes = append(nodes, &Node{Kind: "text", Text: buf.String()})
			buf.Reset()
		}
	}
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch c {
		case '\'':
			if p.i+1 < len(p.s) && p.s[p.i+1] == '\'' {
				buf.WriteByte('\'')
				p.i += 2
				continue
			}
			if p.i+1 < len(p.s) && isSyntaxChar(p.s[p.i+1]) {
				p.i++
				start := p.i
				for p.i < len(p.s) && p.s[p.i] != '\'' {
					p.i++
				}
				buf.WriteString(p.s[start:p.i])
				if p.i < len(p.s) {
					p.i++ // consume closing quote
				} else {
					p.errf("unterminated quoted literal")
				}
				continue
			}
			buf.WriteByte('\'')
			p.i++
		case '{':
			flush()
			nodes = append(nodes, p.parseArg())
		case '}':
			if stop == seqBrace {
				flush()
				p.i++
				return nodes, true
			}
			buf.WriteByte('}')
			p.i++
		case '#':
			flush()
			nodes = append(nodes, &Node{Kind: "pound"})
			p.i++
		case '<':
			tok, ok := p.scanTag()
			if !ok {
				buf.WriteByte('<')
				p.i++
				continue
			}
			flush()
			if tok.closing {
				if stop != seqEOF && stop != seqBrace && stop == tok.name {
					return nodes, true
				}
				p.errf("unexpected closing tag </" + tok.name + ">")
				continue
			}
			node := &Node{Kind: "tag", Name: tok.name}
			if !tok.selfClosing {
				children, closed := p.parseSequence(tok.name)
				node.Children = children
				if !closed {
					p.errf("unclosed tag <" + tok.name + ">")
				}
			}
			nodes = append(nodes, node)
		case '>':
			buf.WriteByte('>')
			p.i++
		default:
			buf.WriteByte(c)
			p.i++
		}
	}
	flush()
	return nodes, false
}

type tagToken struct {
	name        string
	closing     bool
	selfClosing bool
}

func (p *parser) scanTag() (tagToken, bool) {
	var tok tagToken
	j := p.i + 1
	if j < len(p.s) && p.s[j] == '/' {
		tok.closing = true
		j++
	}
	start := j
	for j < len(p.s) && isTagNameChar(p.s[j]) {
		j++
	}
	if j == start {
		return tok, false
	}
	tok.name = p.s[start:j]
	if j < len(p.s) && p.s[j] == '>' {
		p.i = j + 1
		return tok, true
	}
	if !tok.closing && j+1 < len(p.s) && p.s[j] == '/' && p.s[j+1] == '>' {
		tok.selfClosing = true
		p.i = j + 2
		return tok, true
	}
	return tok, false
}

func (p *parser) parseArg() *Node {
	p.i++ // consume '{'
	node := &Node{Kind: "arg"}
	start := p.i
	for p.i < len(p.s) && p.s[p.i] != ',' && p.s[p.i] != '}' {
		p.i++
	}
	node.Name = strings.TrimSpace(p.s[start:p.i])
	if p.i >= len(p.s) {
		p.errf("unclosed argument {" + node.Name)
		return node
	}
	if p.s[p.i] == '}' {
		p.i++
		return node
	}
	p.i++ // consume ','
	start = p.i
	for p.i < len(p.s) && p.s[p.i] != ',' && p.s[p.i] != '}' {
		p.i++
	}
	node.ArgType = strings.TrimSpace(p.s[start:p.i])
	if p.i >= len(p.s) {
		p.errf("unclosed argument {" + node.Name)
		return node
	}
	if p.s[p.i] == '}' {
		p.i++
		return node
	}
	p.i++ // consume second ','
	if node.ArgType == "plural" || node.ArgType == "selectordinal" {
		node.Kind = "plural"
		node.Options = map[string][]*Node{}
		p.parsePluralOptions(node)
		return node
	}
	start = p.i
	for p.i < len(p.s) && p.s[p.i] != '}' {
		p.i++
	}
	node.Style = strings.TrimSpace(p.s[start:p.i])
	if p.i < len(p.s) {
		p.i++
	} else {
		p.errf("unclosed argument {" + node.Name)
	}
	return node
}

func (p *parser) parsePluralOptions(node *Node) {
	for {
		for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == '\t' || p.s[p.i] == '\n' || p.s[p.i] == '\r') {
			p.i++
		}
		if p.i >= len(p.s) {
			p.errf("unclosed plural {" + node.Name)
			return
		}
		if p.s[p.i] == '}' {
			p.i++
			return
		}
		start := p.i
		for p.i < len(p.s) && p.s[p.i] != '{' && p.s[p.i] != '}' {
			p.i++
		}
		cat := strings.TrimSpace(p.s[start:p.i])
		if p.i >= len(p.s) || p.s[p.i] == '}' {
			if cat != "" {
				p.errf("plural option " + cat + " missing message body")
			}
			if p.i < len(p.s) {
				p.i++
			} else {
				p.errf("unclosed plural {" + node.Name)
			}
			return
		}
		p.i++ // consume '{'
		children, closed := p.parseSequence(seqBrace)
		if !closed {
			p.errf("unclosed plural option " + cat)
			return
		}
		if cat != "" {
			node.Options[cat] = children
		}
	}
}
