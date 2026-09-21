// Package icu parses ICU-message-format-like text into a structure that can be
// compared across languages. It supports {arg}, typed args such as
// {n, number}, plural/select option blocks, rich-text tags (<b>...</b>,
// <br/>) and ICU apostrophe quoting rules.
package icu

import (
	"sort"
	"strings"
)

// ParserVersion identifies the grammar implemented by this package. Validation
// runs are bound to it so old results can be replayed against their basis.
const ParserVersion = "icu-1"

// Node is one element of a parsed message.
type Node struct {
	Kind     string              `json:"kind"` // "text" | "arg" | "tag"
	Text     string              `json:"text,omitempty"`
	Name     string              `json:"name,omitempty"`
	ArgType  string              `json:"argType,omitempty"`
	Style    string              `json:"style,omitempty"`
	Options  map[string]*Message `json:"options,omitempty"`
	Children []*Node             `json:"children,omitempty"`
}

// Message is a parsed message plus non-fatal warnings (e.g. unbalanced tags).
type Message struct {
	Nodes    []*Node  `json:"nodes"`
	Warnings []string `json:"warnings,omitempty"`
}

// Parse parses s as an ICU message. Parse errors that can be tolerated are
// recorded as warnings instead of aborting, so the catalogue stays viewable.
func Parse(s string) *Message {
	p := &parser{s: s}
	nodes := p.parseNodes(false, "")
	return &Message{Nodes: nodes, Warnings: p.warnings}
}

type parser struct {
	s        string
	i        int
	warnings []string
}

func (p *parser) warn(msg string) {
	for _, w := range p.warnings {
		if w == msg {
			return
		}
	}
	p.warnings = append(p.warnings, msg)
}

func isSyntaxStart(c byte) bool { return c == '{' || c == '<' || c == '#' || c == '|' }

// parseNodes parses until end of input, an unmatched '}', or the matching
// close tag (when closeTag != ""). The caller consumes the terminator.
func (p *parser) parseNodes(inPlural bool, closeTag string) []*Node {
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
			text.WriteString(p.parseQuoted(inPlural))
		case c == '{':
			flush()
			nodes = append(nodes, p.parseArg())
		case c == '}':
			flush()
			return nodes
		case c == '<':
			name, closing, selfClosing, ok := p.scanTag()
			if !ok {
				text.WriteByte(c)
				p.i++
				continue
			}
			flush()
			switch {
			case selfClosing:
				nodes = append(nodes, &Node{Kind: "tag", Name: name})
			case closing:
				if closeTag == name {
					return nodes
				}
				if closeTag == "" {
					p.warn("unbalanced tag: stray </" + name + ">")
				} else {
					p.warn("unbalanced tag: expected </" + closeTag + "> but found </" + name + ">")
				}
			default:
				children := p.parseNodes(inPlural, name)
				nodes = append(nodes, &Node{Kind: "tag", Name: name, Children: children})
			}
		case c == '#' && inPlural:
			text.WriteByte('#')
			p.i++
		default:
			text.WriteByte(c)
			p.i++
		}
	}
	flush()
	if closeTag != "" {
		p.warn("unbalanced tag: <" + closeTag + "> never closed")
	}
	return nodes
}

// parseQuoted handles ICU apostrophe rules. '' is a literal apostrophe; an
// apostrophe directly before a syntax char starts a quoted literal that runs
// to the next single apostrophe; any other apostrophe is literal text.
func (p *parser) parseQuoted(inPlural bool) string {
	// p.s[p.i] == '\''
	if p.i+1 < len(p.s) && p.s[p.i+1] == '\'' {
		p.i += 2
		return "'"
	}
	next := byte(0)
	if p.i+1 < len(p.s) {
		next = p.s[p.i+1]
	}
	startsQuote := next == '{' || next == '<' || next == '|' || (next == '#' && inPlural) || next == '}'
	if !startsQuote {
		p.i++
		return "'"
	}
	p.i++ // consume opening apostrophe
	var b strings.Builder
	for p.i < len(p.s) {
		c := p.s[p.i]
		if c == '\'' {
			if p.i+1 < len(p.s) && p.s[p.i+1] == '\'' {
				b.WriteByte('\'')
				p.i += 2
				continue
			}
			p.i++ // closing apostrophe
			return b.String()
		}
		b.WriteByte(c)
		p.i++
	}
	// unterminated quote: rest of input is literal
	return b.String()
}

// scanTag attempts to read <name>, </name> or <name/> at the cursor. On
// success the cursor is advanced past the tag.
func (p *parser) scanTag() (name string, closing, selfClosing, ok bool) {
	j := p.i + 1
	if j >= len(p.s) {
		return "", false, false, false
	}
	if p.s[j] == '/' {
		closing = true
		j++
	}
	start := j
	for j < len(p.s) && (isAlphaNum(p.s[j])) {
		j++
	}
	if j == start || !isAlpha(p.s[start]) {
		return "", false, false, false
	}
	name = p.s[start:j]
	if j < len(p.s) && p.s[j] == '/' {
		selfClosing = true
		j++
	}
	if j >= len(p.s) || p.s[j] != '>' {
		return "", false, false, false
	}
	p.i = j + 1
	return name, closing, selfClosing, true
}

func isAlpha(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}
func isAlphaNum(c byte) bool { return isAlpha(c) || c >= '0' && c <= '9' || c == '-' || c == '_' }

// parseArg parses {name}, {name, type} and {name, type, style} where style may
// be a plural/select option list. The cursor must be at '{'.
func (p *parser) parseArg() *Node {
	p.i++ // consume '{'
	p.skipSpaces()
	name := p.readToken(",} \t")
	p.skipSpaces()
	node := &Node{Kind: "arg", Name: name, ArgType: "any"}
	if p.i >= len(p.s) {
		p.warn("unterminated argument {" + name)
		return node
	}
	if p.s[p.i] == '}' {
		p.i++
		return node
	}
	p.i++ // consume ','
	p.skipSpaces()
	typ := p.readToken(",} \t")
	node.ArgType = typ
	p.skipSpaces()
	if p.i >= len(p.s) {
		p.warn("unterminated argument {" + name)
		return node
	}
	if p.s[p.i] == '}' {
		p.i++
		return node
	}
	p.i++ // consume ','
	p.skipSpaces()
	if typ == "plural" || typ == "select" || typ == "selectordinal" {
		node.Options = map[string]*Message{}
		for {
			p.skipSpaces()
			if p.i >= len(p.s) {
				p.warn("unterminated " + typ + " for {" + name)
				break
			}
			if p.s[p.i] == '}' {
				p.i++
				break
			}
			sel := p.readSelector()
			p.skipSpaces()
			if p.i >= len(p.s) || p.s[p.i] != '{' {
				p.warn("malformed " + typ + " option " + sel + " for {" + name)
				break
			}
			p.i++ // consume '{'
			sub := p.parseNodes(typ == "plural" || typ == "selectordinal", "")
			if p.i < len(p.s) && p.s[p.i] == '}' {
				p.i++
			}
			node.Options[sel] = &Message{Nodes: sub}
		}
		return node
	}
	// simple style: read raw until the matching '}'
	depth := 1
	start := p.i
	for p.i < len(p.s) && depth > 0 {
		switch p.s[p.i] {
		case '{':
			depth++
		case '}':
			depth--
		}
		p.i++
	}
	end := p.i - 1
	if depth > 0 {
		p.warn("unterminated argument {" + name)
		end = p.i
	}
	node.Style = strings.TrimSpace(p.s[start:end])
	return node
}

func (p *parser) skipSpaces() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == '\t' || p.s[p.i] == '\n' || p.s[p.i] == '\r') {
		p.i++
	}
}

func (p *parser) readToken(delims string) string {
	start := p.i
	for p.i < len(p.s) && !strings.ContainsRune(delims, rune(p.s[p.i])) {
		p.i++
	}
	return p.s[start:p.i]
}

func (p *parser) readSelector() string {
	start := p.i
	for p.i < len(p.s) && p.s[p.i] != '{' && p.s[p.i] != ' ' && p.s[p.i] != '\t' && p.s[p.i] != '\n' && p.s[p.i] != '\r' {
		p.i++
	}
	return p.s[start:p.i]
}

// Placeholders returns the set of argument names with their types, walking
// into plural/select options and tag children.
func (m *Message) Placeholders() map[string]string {
	out := map[string]string{}
	walkNodes(m.Nodes, func(n *Node) {
		if n.Kind == "arg" {
			if _, seen := out[n.Name]; !seen {
				out[n.Name] = n.ArgType
			}
		}
	})
	return out
}

// PluralCategories returns, per plural argument, the sorted list of selectors.
func (m *Message) PluralCategories() map[string][]string {
	out := map[string][]string{}
	walkNodes(m.Nodes, func(n *Node) {
		if n.Kind == "arg" && (n.ArgType == "plural" || n.ArgType == "selectordinal") {
			var sels []string
			for sel := range n.Options {
				sels = append(sels, sel)
			}
			sort.Strings(sels)
			if cur, seen := out[n.Name]; !seen || len(sels) > len(cur) {
				out[n.Name] = sels
			}
		}
	})
	return out
}

func walkNodes(nodes []*Node, fn func(*Node)) {
	for _, n := range nodes {
		fn(n)
		for _, opt := range n.Options {
			walkNodes(opt.Nodes, fn)
		}
		walkNodes(n.Children, fn)
	}
}
