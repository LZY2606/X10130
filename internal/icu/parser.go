// Package icu implements a small ICU MessageFormat parser used to extract
// placeholder/type structure, plural and select branches and rich text tags.
package icu

import (
	"fmt"
	"sort"
	"strings"
)

// Version identifies the parser implementation. Checks are bound to it so
// results can be replayed and selectively invalidated.
const Version = "icu-parser/1.0.0"

// Tag is one rich-text tag occurrence (<x> or </x>) found in literal text.
type Tag struct {
	Name string
	Open bool
}

// Arg is one ICU placeholder.
type Arg struct {
	Name  string
	Type  string // number, date, time, plural, select, selectordinal, ""
	Style string // format style, empty for complex types
	Cases []Case // plural/select branches
}

// Case is one plural/select branch.
type Case struct {
	Key     string
	Message *Message
}

// Message is a parsed message tree.
type Message struct {
	Text string // raw text the message was parsed from
	Args []Arg
	Tags []Tag
}

type parser struct {
	src string
	pos int
}

// Parse parses one message. A parse error is returned for malformed syntax
// (unbalanced braces, unclosed quotes) rather than silently dropping content.
func Parse(src string) (*Message, error) {
	p := &parser{src: src}
	args, tags, err := p.parseChunk(true)
	if err != nil {
		return nil, err
	}
	if p.pos != len(p.src) {
		return nil, fmt.Errorf("icu: unexpected trailing input at %d", p.pos)
	}
	return &Message{Text: src, Args: args, Tags: tags}, nil
}

// parseChunk reads until the matching closing brace or end of input.
func (p *parser) parseChunk(top bool) ([]Arg, []Tag, error) {
	var args []Arg
	var tags []Tag
	var lit strings.Builder
	flush := func() {
		tags = append(tags, scanTags(lit.String())...)
		lit.Reset()
	}
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch c {
		case '}':
			if top {
				return nil, nil, fmt.Errorf("icu: unexpected '}' at %d", p.pos)
			}
			flush()
			return args, tags, nil
		case '{':
			flush()
			a, err := p.parseArg()
			if err != nil {
				return nil, nil, err
			}
			args = append(args, a)
		case '\'':
			// ICU quoting: '' is a literal apostrophe; '...' escapes its content
			// ('{' '}' and tags inside are literal).
			if p.pos+1 < len(p.src) && p.src[p.pos+1] == '\'' {
				lit.WriteByte('\'')
				p.pos += 2
				continue
			}
			p.pos++
			start := p.pos
			for p.pos < len(p.src) && p.src[p.pos] != '\'' {
				p.pos++
			}
			if p.pos >= len(p.src) {
				return nil, nil, fmt.Errorf("icu: unclosed quote at %d", start-1)
			}
			lit.WriteString(p.src[start:p.pos])
			p.pos++ // closing quote
		default:
			lit.WriteByte(c)
			p.pos++
		}
	}
	flush()
	return args, tags, nil
}

func (p *parser) parseArg() (Arg, error) {
	p.pos++ // consume '{'
	var a Arg
	name, err := p.readToken()
	if err != nil {
		return a, err
	}
	a.Name = strings.TrimSpace(name)
	if a.Name == "" {
		return a, fmt.Errorf("icu: empty placeholder name at %d", p.pos)
	}
	if p.pos >= len(p.src) {
		return a, fmt.Errorf("icu: unterminated placeholder %q", a.Name)
	}
	if p.src[p.pos] == '}' {
		p.pos++
		return a, nil
	}
	if p.src[p.pos] != ',' {
		return a, fmt.Errorf("icu: expected ',' or '}' at %d", p.pos)
	}
	p.pos++
	typ, err := p.readToken()
	if err != nil {
		return a, err
	}
	a.Type = strings.TrimSpace(typ)
	if p.pos >= len(p.src) {
		return a, fmt.Errorf("icu: unterminated placeholder %q", a.Name)
	}
	if p.src[p.pos] == '}' {
		p.pos++
		return a, nil
	}
	if p.src[p.pos] != ',' {
		return a, fmt.Errorf("icu: expected ',' at %d", p.pos)
	}
	p.pos++
	switch a.Type {
	case "plural", "select", "selectordinal":
		cases, err := p.parseSelector()
		if err != nil {
			return a, err
		}
		a.Cases = cases
	default:
		style, err := p.readSimpleStyle()
		if err != nil {
			return a, err
		}
		a.Style = strings.TrimSpace(style)
	}
	if p.pos >= len(p.src) || p.src[p.pos] != '}' {
		return a, fmt.Errorf("icu: expected '}' closing placeholder %q at %d", a.Name, p.pos)
	}
	p.pos++
	return a, nil
}

// readToken reads a bare identifier/type token up to ',' or '}'.
func (p *parser) readToken() (string, error) {
	start := p.pos
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if c == ',' || c == '}' {
			break
		}
		if c == '{' {
			return "", fmt.Errorf("icu: unexpected '{' at %d", p.pos)
		}
		p.pos++
	}
	return p.src[start:p.pos], nil
}

// readSimpleStyle reads a format style, allowing quoted content; it stops at
// the placeholder-closing brace while tracking nested braces in quotes-free
// contexts (simple styles normally contain none).
func (p *parser) readSimpleStyle() (string, error) {
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
			if p.pos >= len(p.src) {
				return "", fmt.Errorf("icu: unclosed quote in style")
			}
			p.pos++
			continue
		}
		if c == '{' {
			depth++
		}
		if c == '}' {
			if depth == 0 {
				return p.src[start:p.pos], nil
			}
			depth--
		}
		p.pos++
	}
	return "", fmt.Errorf("icu: unterminated style")
}

func (p *parser) parseSelector() ([]Case, error) {
	// skip spaces before first branch
	for p.pos < len(p.src) && (p.src[p.pos] == ' ' || p.src[p.pos] == '\t' || p.src[p.pos] == '\n') {
		p.pos++
	}
	var cases []Case
	for {
		if p.pos < len(p.src) && p.src[p.pos] == '}' {
			if len(cases) == 0 {
				return nil, fmt.Errorf("icu: selector has no branches")
			}
			return cases, nil
		}
		// selector keyword: e.g. "one", "other", "=0"
		start := p.pos
		for p.pos < len(p.src) && p.src[p.pos] != '{' && p.src[p.pos] != '}' {
			p.pos++
		}
		if p.pos >= len(p.src) {
			return nil, fmt.Errorf("icu: unterminated selector")
		}
		if p.src[p.pos] == '}' {
			return nil, fmt.Errorf("icu: selector branch missing message")
		}
		key := strings.TrimSpace(p.src[start:p.pos])
		if strings.HasPrefix(key, "offset:") {
			// plural offset declaration, continue to next keyword
			continue
		}
		if key == "" {
			return nil, fmt.Errorf("icu: empty selector key at %d", start)
		}
		p.pos++ // consume '{'
		args, tags, err := p.parseChunk(false)
		if err != nil {
			return nil, err
		}
		sub := &Message{Args: args, Tags: tags}
		// recover raw text of the sub message for display
		cases = append(cases, Case{Key: key, Message: sub})
		for p.pos < len(p.src) && (p.src[p.pos] == ' ' || p.src[p.pos] == '\t' || p.src[p.pos] == '\n') {
			p.pos++
		}
	}
}

// scanTags extracts <name>/</name> tags from literal text, skipping quoted
// sections. A '<' that does not form a tag is treated as literal text.
func scanTags(s string) []Tag {
	var tags []Tag
	for i := 0; i < len(s); {
		c := s[i]
		if c == '\'' {
			if i+1 < len(s) && s[i+1] == '\'' {
				i += 2
				continue
			}
			i++
			for i < len(s) && s[i] != '\'' {
				i++
			}
			if i < len(s) {
				i++
			}
			continue
		}
		if c == '<' {
			if name, n, ok := matchTag(s[i:]); ok {
				tags = append(tags, Tag{Name: name, Open: !strings.HasPrefix(name, "/")})
				i += n
				continue
			}
		}
		i++
	}
	return tags
}

func matchTag(s string) (name string, n int, ok bool) {
	closeTag := false
	i := 1
	if i < len(s) && s[i] == '/' {
		closeTag = true
		i++
	}
	start := i
	for i < len(s) && s[i] != '>' {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return "", 0, false
		}
		i++
	}
	if i >= len(s) || i == start {
		return "", 0, false
	}
	name = s[start:i]
	if closeTag {
		name = "/" + name
	}
	return name, i + 1, true
}

// PlaceholderNames returns the set of placeholder names (recursively).
func (m *Message) PlaceholderNames() map[string]bool {
	out := map[string]bool{}
	var walk func(m *Message)
	walk = func(m *Message) {
		for _, a := range m.Args {
			out[a.Name] = true
			for _, c := range a.Cases {
				walk(c.Message)
			}
		}
	}
	walk(m)
	return out
}

// TypeMap returns placeholder name -> type signature (recursive). A later
// type with a different signature is a type drift.
func (m *Message) TypeMap() map[string]string {
	out := map[string]string{}
	var walk func(m *Message)
	walk = func(m *Message) {
		for _, a := range m.Args {
			sig := a.Type
			if a.Type == "plural" || a.Type == "select" || a.Type == "selectordinal" {
				keys := make([]string, 0, len(a.Cases))
				for _, c := range a.Cases {
					keys = append(keys, c.Key)
				}
				sort.Strings(keys)
				sig = a.Type + "(" + strings.Join(keys, ",") + ")"
			} else if a.Style != "" {
				sig = a.Type + ":" + a.Style
			}
			if existing, ok := out[a.Name]; ok && existing != sig {
				out[a.Name] = existing + "|" + sig
			} else if !ok {
				out[a.Name] = sig
			}
			for _, c := range a.Cases {
				walk(c.Message)
			}
		}
	}
	walk(m)
	return out
}

// PluralCategories returns plural/selectordinal name -> category keys.
func (m *Message) PluralCategories() map[string][]string {
	out := map[string][]string{}
	var walk func(m *Message)
	walk = func(m *Message) {
		for _, a := range m.Args {
			if a.Type == "plural" || a.Type == "selectordinal" {
				if _, ok := out[a.Name]; !ok {
					keys := make([]string, len(a.Cases))
					for i, c := range a.Cases {
						keys[i] = c.Key
					}
					out[a.Name] = keys
				}
			}
			for _, c := range a.Cases {
				walk(c.Message)
			}
		}
	}
	walk(m)
	return out
}

// AllTags returns every tag including those inside plural branches.
func (m *Message) AllTags() []Tag {
	out := append([]Tag(nil), m.Tags...)
	for _, a := range m.Args {
		for _, c := range a.Cases {
			out = append(out, c.Message.AllTags()...)
		}
	}
	return out
}

// TagsBalanced reports whether every opened tag is closed in stack order.
func TagsBalanced(tags []Tag) bool {
	var stack []string
	for _, t := range tags {
		if t.Open {
			stack = append(stack, t.Name)
		} else {
			if len(stack) == 0 || stack[len(stack)-1] != t.Name {
				return false
			}
			stack = stack[:len(stack)-1]
		}
	}
	return len(stack) == 0
}
