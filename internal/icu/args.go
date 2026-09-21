package icu

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// parseArg starts right after the opening brace.
func (p *parser) parseArg() (*Placeholder, error) {
	name, err := p.readIdentifier()
	if err != nil {
		return nil, err
	}
	arg := &Placeholder{Name: name, Type: "string"}
	p.skipSpaces()
	if p.pos >= len(p.src) {
		return nil, fmt.Errorf("icu: unclosed placeholder '{%s'", name)
	}
	if p.peek() == '}' {
		p.advance()
		return arg, nil
	}
	if p.peek() != ',' {
		return nil, fmt.Errorf("icu: expected ',' after argument %q", name)
	}
	p.advance()
	p.skipSpaces()
	typ, err := p.readWord()
	if err != nil {
		return nil, err
	}
	arg.Type = normalizeType(typ)
	p.skipSpaces()
	if p.peek() == '}' {
		p.advance()
		return arg, nil
	}
	if p.peek() != ',' {
		return nil, fmt.Errorf("icu: expected ',' after type %q", arg.Type)
	}
	p.advance()
	p.skipSpaces()
	switch arg.Type {
	case "plural", "select":
		branches, offset, err := p.parseBranches(arg.Type)
		if err != nil {
			return nil, err
		}
		arg.Branches = branches
		arg.Offset = offset
	default:
		style, err := p.readSimpleStyle()
		if err != nil {
			return nil, err
		}
		arg.Style = style
	}
	p.skipSpaces()
	if p.pos >= len(p.src) || p.peek() != '}' {
		return nil, fmt.Errorf("icu: unclosed placeholder '{%s'", name)
	}
	p.advance()
	return arg, nil
}

func normalizeType(t string) string {
	switch t {
	case "number", "date", "time", "duration", "spellout", "ordinal":
		return t
	case "plural", "select", "selectordinal":
		return t
	default:
		return t
	}
}

func (p *parser) readIdentifier() (string, error) {
	p.skipSpaces()
	start := p.pos
	for p.pos < len(p.src) && !isSpace(p.peek()) && p.peek() != ',' && p.peek() != '}' {
		if p.peek() == '{' {
			return "", fmt.Errorf("icu: missing argument name")
		}
		p.advance()
	}
	name := string(p.src[start:p.pos])
	if name == "" {
		return "", fmt.Errorf("icu: empty argument name at %d:%d", p.line, p.col)
	}
	return name, nil
}

func (p *parser) readWord() (string, error) {
	start := p.pos
	for p.pos < len(p.src) && !isSpace(p.peek()) && p.peek() != ',' && p.peek() != '}' {
		p.advance()
	}
	w := string(p.src[start:p.pos])
	if w == "" {
		return "", fmt.Errorf("icu: empty format type at %d:%d", p.line, p.col)
	}
	return w, nil
}

func (p *parser) readSimpleStyle() (string, error) {
	// style runs until the matching closing brace, honoring nested braces and
	// ICU quotes (e.g. date patterns).
	depth := 0
	start := p.pos
	for p.pos < len(p.src) {
		r := p.peek()
		if r == '\'' {
			p.consumeQuoted()
			continue
		}
		if r == '{' {
			depth++
			p.advance()
			continue
		}
		if r == '}' && depth == 0 {
			return strings.TrimSpace(string(p.src[start:p.pos])), nil
		}
		if r == '}' {
			depth--
		}
		p.advance()
	}
	return "", fmt.Errorf("icu: unclosed style")
}

func (p *parser) skipSpaces() {
	for p.pos < len(p.src) && isSpace(p.peek()) {
		p.advance()
	}
}

var pluralCategories = map[string]bool{
	"zero": true, "one": true, "two": true, "few": true, "many": true, "other": true,
}

// parseBranches parses plural/select branches up to the closing brace of the
// argument. The current position is right after the second comma.
func (p *parser) parseBranches(kind string) ([]Branch, int, error) {
	var branches []Branch
	offset := 0
	for {
		p.skipSpaces()
		if p.pos < len(p.src) && p.peek() == '}' {
			break
		}
		selectorStart := p.pos
		for p.pos < len(p.src) && !isSpace(p.peek()) {
			p.advance()
		}
		selector := string(p.src[selectorStart:p.pos])
		if selector == "" {
			return nil, 0, fmt.Errorf("icu: empty %s selector", kind)
		}
		p.skipSpaces()
		if kind == "plural" && selector == "offset:" {
			n, err := p.readOffsetValue()
			if err != nil {
				return nil, 0, err
			}
			offset = n
			continue
		}
		if kind == "plural" && strings.HasPrefix(selector, "=") {
			if _, err := strconv.Atoi(selector[1:]); err != nil {
				return nil, 0, fmt.Errorf("icu: invalid explicit plural selector %q", selector)
			}
		} else if kind == "plural" && !pluralCategories[selector] {
			return nil, 0, fmt.Errorf("icu: invalid plural category %q", selector)
		}
		if p.pos >= len(p.src) || p.peek() != '{' {
			return nil, 0, fmt.Errorf("icu: %s selector %q must be followed by '{'", kind, selector)
		}
		p.advance()
		toks, err := p.parseChunks(kind == "plural", 1)
		if err != nil {
			return nil, 0, err
		}
		if p.pos >= len(p.src) || p.peek() != '}' {
			return nil, 0, fmt.Errorf("icu: unclosed branch %q", selector)
		}
		p.advance()
		branches = append(branches, Branch{Key: selector, Message: tokensText(toks)})
	}
	if kind == "plural" && !hasCategory(branches, "other") {
		return nil, 0, fmt.Errorf("icu: plural requires an 'other' branch")
	}
	if kind == "select" && !hasCategory(branches, "other") {
		return nil, 0, fmt.Errorf("icu: select requires an 'other' branch")
	}
	return branches, offset, nil
}

func (p *parser) readOffsetValue() (int, error) {
	start := p.pos
	for p.pos < len(p.src) && !isSpace(p.peek()) && p.peek() != '}' {
		p.advance()
	}
	n, err := strconv.Atoi(string(p.src[start:p.pos]))
	if err != nil {
		return 0, fmt.Errorf("icu: invalid offset value")
	}
	return n, nil
}

func hasCategory(branches []Branch, key string) bool {
	for _, b := range branches {
		if b.Key == key {
			return true
		}
	}
	return false
}

func tokensText(toks []Token) string {
	var sb strings.Builder
	for _, t := range toks {
		switch t.Kind {
		case "text":
			sb.WriteString(t.Text)
		case "arg":
			sb.WriteString("{" + t.Arg.Name + "}")
		case "tag":
			if t.Self {
				sb.WriteString("<" + t.Tag + "/>")
			} else if t.Close {
				sb.WriteString("</" + t.Tag + ">")
			} else {
				sb.WriteString("<" + t.Tag + ">")
			}
		}
	}
	return sb.String()
}

func collectArgs(toks []Token, seen map[string]bool) []Placeholder {
	var out []Placeholder
	for _, t := range toks {
		if t.Kind != "arg" {
			continue
		}
		a := *t.Arg
		a.NestedArgs = nil
		nestedSet := map[string]bool{}
		for _, b := range a.Branches {
			m, err := Parse(b.Message)
			if err == nil {
				for _, n := range m.Placeholders {
					nestedSet[n.Name] = true
				}
			}
		}
		for n := range nestedSet {
			a.NestedArgs = append(a.NestedArgs, n)
		}
		sort.Strings(a.NestedArgs)
		out = append(out, a)
	}
	return out
}

func collectTags(toks []Token, seen map[string]bool) []string {
	set := map[string]bool{}
	var collect func(ts []Token)
	collect = func(ts []Token) {
		for _, t := range ts {
			if t.Kind == "tag" {
				set[t.Tag] = true
			}
			if t.Kind == "arg" {
				for _, b := range t.Arg.Branches {
					if m, err := Parse(b.Message); err == nil {
						collect(m.Tokens)
					}
				}
			}
		}
	}
	collect(toks)
	var names []string
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func checkBalance(toks []Token) []TagOccurrence {
	type frame struct {
		name string
		line int
		col  int
	}
	var stack []frame
	var bad []TagOccurrence
	var walk func(ts []Token)
	walk = func(ts []Token) {
		for _, t := range ts {
			if t.Kind == "arg" {
				for _, b := range t.Arg.Branches {
					if m, err := Parse(b.Message); err == nil {
						walk(m.Tokens)
					}
				}
			}
			if t.Kind != "tag" || t.Self {
				continue
			}
			if !t.Close {
				stack = append(stack, frame{t.Tag, t.Line, t.Column})
				continue
			}
			if len(stack) == 0 {
				bad = append(bad, TagOccurrence{Name: t.Tag, Line: t.Line, Column: t.Column, Closing: true})
				continue
			}
			top := stack[len(stack)-1]
			if top.name != t.Tag {
				bad = append(bad, TagOccurrence{Name: top.name, Line: top.line, Column: top.col})
				stack = stack[:len(stack)-1]
				continue
			}
			stack = stack[:len(stack)-1]
		}
	}
	walk(toks)
	for _, f := range stack {
		bad = append(bad, TagOccurrence{Name: f.name, Line: f.line, Column: f.col})
	}
	sort.Slice(bad, func(i, j int) bool {
		if bad[i].Line != bad[j].Line {
			return bad[i].Line < bad[j].Line
		}
		return bad[i].Column < bad[j].Column
	})
	return bad
}
