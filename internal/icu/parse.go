// Package icu parses ICU MessageFormat-like patterns into a structural form
// used for catalog validation. It is intentionally a small subset parser:
// arguments ({name}, {name,type}, {name,type,style}), plural/select
// branches and XML-ish rich-text tags. ICU single-quote escaping rules are
// honored: 'x' is literal text and '' is a literal apostrophe.
package icu

import (
	"sort"
	"strings"
)

const Version = "icu-v3"

// Placeholder describes one ICU argument, including plural/select branches.
type Placeholder struct {
	Name   string              `json:"name"`
	Type   string              `json:"type,omitempty"`
	Style  string              `json:"style,omitempty"`
	Offset string              `json:"offset,omitempty"`
	Cases  map[string]*Message `json:"cases,omitempty"`
}

// Node is one element of a parsed message tree.
type Node struct {
	Kind     string   `json:"kind"` // text | tag | arg
	Text     string   `json:"text,omitempty"`
	Tag      string   `json:"tag,omitempty"`
	Children []*Node  `json:"children,omitempty"`
	Arg      *Placeholder `json:"arg,omitempty"`
}

// Message is a fully parsed pattern.
type Message struct {
	Raw          string        `json:"raw"`
	Nodes        []*Node       `json:"nodes"`
	Placeholders []Placeholder `json:"placeholders"`
	Tags         []string      `json:"tags"`
	ParseErrors  []string      `json:"parse_errors,omitempty"`
}

type parser struct {
	src string
	pos int
	errs []string
}

// Parse parses a single ICU message pattern.
func Parse(src string) *Message {
	p := &parser{src: src}
	nodes := p.parseSegment(false)
	m := &Message{Raw: src, Nodes: nodes, ParseErrors: p.errs}
	collect(m, nodes)
	return m
}

func (p *parser) parseSegment(inArg bool) []*Node {
	var root []*Node
	stack := []*Node{}
	var text strings.Builder

	flush := func() {
		if text.Len() > 0 {
			n := &Node{Kind: "text", Text: text.String()}
			if len(stack) == 0 {
				root = append(root, n)
			} else {
				top := stack[len(stack)-1]
				top.Children = append(top.Children, n)
			}
			text.Reset()
		}
	}

	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch {
		case c == '\'':
			if p.pos+1 < len(p.src) && p.src[p.pos+1] == '\'' {
				text.WriteByte('\'')
				p.pos += 2
				continue
			}
			p.pos++
			start := p.pos
			for p.pos < len(p.src) && p.src[p.pos] != '\'' {
				p.pos++
			}
			text.WriteString(p.src[start:p.pos])
			if p.pos < len(p.src) {
				p.pos++ // closing quote
			} else {
				p.errs = append(p.errs, "unterminated quoted literal")
			}
		case c == '{':
			flush()
			arg := p.parseArg()
			n := &Node{Kind: "arg", Arg: arg}
			if len(stack) == 0 {
				root = append(root, n)
			} else {
				top := stack[len(stack)-1]
				top.Children = append(top.Children, n)
			}
		case c == '}':
			if inArg {
				flush()
				for _, t := range stack {
					p.errs = append(p.errs, "tag <"+t.Tag+"> not closed before '}'")
				}
				return root
			}
			p.errs = append(p.errs, "unmatched '}'")
			p.pos++
		case c == '<':
			p.pos++
			closing := p.pos < len(p.src) && p.src[p.pos] == '/'
			if closing {
				p.pos++
			}
			name := p.readTagName()
			if name == "" {
				text.WriteByte('<')
				if closing {
					text.WriteByte('/')
				}
				continue
			}
			selfClose := false
			p.skipSpaces()
			if p.pos < len(p.src) && p.src[p.pos] == '/' {
				selfClose = true
				p.pos++
			}
			if p.pos >= len(p.src) || p.src[p.pos] != '>' {
				p.errs = append(p.errs, "malformed tag <"+name+">")
				text.WriteString("<" + name + ">")
				continue
			}
			p.pos++ // consume >
			flush()
			if closing {
				if len(stack) == 0 || stack[len(stack)-1].Tag != name {
					if len(stack) == 0 {
						p.errs = append(p.errs, "unexpected closing tag </"+name+">")
					} else {
						p.errs = append(p.errs, "expected closing tag </"+stack[len(stack)-1].Tag+">, got </"+name+">")
						top := stack[len(stack)-1]
						p.errs = append(p.errs, "tag <"+top.Tag+"> not closed")
						stack = stack[:len(stack)-1]
					}
					continue
				}
				done := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if len(stack) == 0 {
					root = append(root, done)
				} else {
					stack[len(stack)-1].Children = append(stack[len(stack)-1].Children, done)
				}
			} else if selfClose {
				n := &Node{Kind: "tag", Tag: name}
				if len(stack) == 0 {
					root = append(root, n)
				} else {
					stack[len(stack)-1].Children = append(stack[len(stack)-1].Children, n)
				}
			} else {
				stack = append(stack, &Node{Kind: "tag", Tag: name})
			}
		default:
			text.WriteByte(c)
			p.pos++
		}
	}
	flush()
	for _, t := range stack {
		p.errs = append(p.errs, "tag <"+t.Tag+"> not closed")
		if len(stack) == 1 {
			root = append(root, t)
		}
	}
	if len(stack) > 1 {
		// attach nested unclosed chain to root's view anyway
		root = append(root, stack[0])
	}
	return root
}

func (p *parser) readTagName() string {
	start := p.pos
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if c == '>' || c == '/' || c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			break
		}
		if c == '<' || c == '}' || c == '{' {
			break
		}
		p.pos++
	}
	return p.src[start:p.pos]
}

func (p *parser) skipSpaces() {
	for p.pos < len(p.src) {
		switch p.src[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *parser) parseArg() *Placeholder {
	p.pos++ // consume {
	p.skipSpaces()
	name := p.readToken()
	ph := &Placeholder{Name: name}
	p.skipSpaces()
	if p.pos < len(p.src) && p.src[p.pos] == ',' {
		p.pos++
		p.skipSpaces()
		typeStart := p.pos
		for p.pos < len(p.src) {
			c := p.src[p.pos]
			if c == ',' || c == '}' || c == ' ' || c == '\t' || c == '\n' || c == '\r' {
				break
			}
			p.pos++
		}
		ph.Type = p.src[typeStart:p.pos]
		p.skipSpaces()
		switch ph.Type {
		case "plural", "select", "selectordinal":
			p.parseOptions(ph)
			p.skipSpaces()
			if p.pos < len(p.src) && p.src[p.pos] == '}' {
				p.pos++
			} else {
				p.errs = append(p.errs, "unterminated "+ph.Type+" argument "+name)
			}
			return ph
		}
		if p.pos < len(p.src) && p.src[p.pos] == ',' {
			p.pos++
			p.skipSpaces()
			style := p.readUntilCloseBrace()
			ph.Style = strings.TrimSpace(style)
		}
		if p.pos < len(p.src) && p.src[p.pos] == '}' {
			p.pos++
		} else {
			p.errs = append(p.errs, "unterminated argument "+name)
		}
	} else if p.pos < len(p.src) && p.src[p.pos] == '}' {
		p.pos++
	} else {
		p.errs = append(p.errs, "unterminated argument "+name)
	}
	return ph
}

func (p *parser) readToken() string {
	start := p.pos
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if c == ',' || c == '}' || c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			break
		}
		p.pos++
	}
	return p.src[start:p.pos]
}

// readUntilCloseBrace reads a simple style up to the closing brace, honoring
// quoted literals and nested braces.
func (p *parser) readUntilCloseBrace() string {
	start := p.pos
	depth := 0
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if c == '\'' {
			if p.pos+1 < len(p.src) && p.src[p.pos+1] == '\'' {
				p.pos += 2
				continue
			}
			p.pos++
			for p.pos < len(p.src) && p.src[p.pos] != '\'' {
				p.pos++
			}
			if p.pos < len(p.src) {
				p.pos++
			}
			continue
		}
		if c == '{' {
			depth++
		}
		if c == '}' {
			if depth == 0 {
				return p.src[start:p.pos]
			}
			depth--
		}
		p.pos++
	}
	return p.src[start:p.pos]
}

func (p *parser) parseOptions(ph *Placeholder) {
	ph.Cases = map[string]*Message{}
	// Options follow the type after a separating comma: {n, plural, one..}.
	if p.pos < len(p.src) && p.src[p.pos] == ',' {
		p.pos++
	}
	for {
		p.skipSpaces()
		if p.pos >= len(p.src) || p.src[p.pos] == '}' {
			return
		}
		keyStart := p.pos
		if p.src[p.pos] == '\'' {
			p.pos++
			qStart := p.pos
			for p.pos < len(p.src) && p.src[p.pos] != '\'' {
				p.pos++
			}
			key := p.src[qStart:p.pos]
			if p.pos < len(p.src) {
				p.pos++
			}
			p.skipSpaces()
			if p.pos < len(p.src) && p.src[p.pos] == '{' {
				ph.Cases[key] = Parse(p.readBalanced())
			}
			continue
		}
		for p.pos < len(p.src) && p.src[p.pos] != ' ' && p.src[p.pos] != '\t' && p.src[p.pos] != '\n' && p.src[p.pos] != '\r' && p.src[p.pos] != '}' {
			p.pos++
		}
		key := p.src[keyStart:p.pos]
		p.skipSpaces()
		if strings.HasPrefix(key, "offset:") {
			ph.Offset = strings.TrimPrefix(key, "offset:")
			continue
		}
		if key == "" {
			p.pos++
			continue
		}
		if p.pos >= len(p.src) || p.src[p.pos] != '{' {
			p.errs = append(p.errs, "missing nested message for "+ph.Type+" case "+key)
			// resync: drop one byte to guarantee progress
			if p.pos < len(p.src) {
				p.pos++
			}
			continue
		}
		inner := p.readBalanced()
		ph.Cases[key] = Parse(inner)
	}
}

// readBalanced assumes src[pos]=='{' and returns the content inside the
// matching brace, honoring quotes and nested tags/braces.
func (p *parser) readBalanced() string {
	p.pos++ // consume {
	start := p.pos
	depth := 1
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if c == '\'' {
			if p.pos+1 < len(p.src) && p.src[p.pos+1] == '\'' {
				p.pos += 2
				continue
			}
			p.pos++
			for p.pos < len(p.src) && p.src[p.pos] != '\'' {
				p.pos++
			}
			if p.pos < len(p.src) {
				p.pos++
			}
			continue
		}
		if c == '{' {
			depth++
		}
		if c == '}' {
			depth--
			if depth == 0 {
				inner := p.src[start:p.pos]
				p.pos++
				return inner
			}
		}
		p.pos++
	}
	p.errs = append(p.errs, "unterminated nested message")
	return p.src[start:p.pos]
}

func collect(m *Message, nodes []*Node) {
	seen := map[string]int{}
	var walk func([]*Node)
	walk = func(ns []*Node) {
		for _, n := range ns {
			switch n.Kind {
			case "tag":
				m.Tags = append(m.Tags, n.Tag)
				if len(n.Children) > 0 {
					walk(n.Children)
				}
			case "arg":
				if n.Arg != nil {
					if idx, ok := seen[n.Arg.Name]; ok {
						mergePlaceholder(&m.Placeholders[idx], n.Arg)
					} else {
						seen[n.Arg.Name] = len(m.Placeholders)
						m.Placeholders = append(m.Placeholders, *n.Arg)
					}
					if n.Arg.Cases != nil {
						for _, sub := range n.Arg.Cases {
							collect(sub, sub.Nodes)
						}
					}
				}
			}
		}
	}
	walk(nodes)
}

func mergePlaceholder(dst *Placeholder, src *Placeholder) {
	if dst.Type == "" && src.Type != "" {
		dst.Type = src.Type
	}
	if dst.Style == "" {
		dst.Style = src.Style
	}
	if src.Cases != nil {
		if dst.Cases == nil {
			dst.Cases = map[string]*Message{}
		}
		for k, v := range src.Cases {
			if _, ok := dst.Cases[k]; !ok {
				dst.Cases[k] = v
			}
		}
	}
}

// CategoryKeys returns plural/selectordinal branch keys (including =n forms).
func (ph *Placeholder) CategoryKeys() []string {
	if ph == nil || ph.Cases == nil {
		return nil
	}
	out := make([]string, 0, len(ph.Cases))
	for k := range ph.Cases {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// PlaceholderMap indexes placeholders by name.
func (m *Message) PlaceholderMap() map[string]*Placeholder {
	out := map[string]*Placeholder{}
	for i := range m.Placeholders {
		out[m.Placeholders[i].Name] = &m.Placeholders[i]
	}
	return out
}

// HasUnbalancedTags reports whether parsing found tag balance problems.
func (m *Message) HasUnbalancedTags() bool {
	for _, e := range m.ParseErrors {
		if strings.Contains(e, "tag") {
			return true
		}
	}
	return false
}
