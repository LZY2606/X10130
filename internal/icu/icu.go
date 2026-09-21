// Package icu parses a practical subset of ICU MessageFormat plus XML-like
// rich-text tags, producing a stable structural summary used for catalog
// comparison. The parser never fails: unparseable input degrades to plain
// text, and tag imbalance is reported on the returned structure.
package icu

import "sort"

// ParserVersion identifies the exact parsing semantics. Validation results
// and attestations are bound to this version.
const ParserVersion = "icu-parser/1.0.0"

// Placeholder is a single ICU argument occurrence, e.g. {name} or
// {count, number}.
type Placeholder struct {
	Name string `json:"name"`
	Type string `json:"type"` // "" for a simple argument
}

// Structure is the parsed shape of one message.
type Structure struct {
	Placeholders []Placeholder       `json:"placeholders,omitempty"`
	Plurals      map[string][]string `json:"plurals,omitempty"` // arg -> sorted selectors
	Tags         []string            `json:"tags,omitempty"`    // sorted distinct tag names
	Balanced     bool                `json:"balanced"`
	TagErrors    []string            `json:"tag_errors,omitempty"`
}

// PlaceholderSet returns name -> type for the first occurrence of each name.
func (s *Structure) PlaceholderSet() map[string]string {
	m := map[string]string{}
	for _, p := range s.Placeholders {
		if _, ok := m[p.Name]; !ok {
			m[p.Name] = p.Type
		}
	}
	return m
}

type parser struct {
	s           string
	i           int
	st          *Structure
	pluralDepth int
	tagStack    []string
	tagSeen     map[string]bool
	errSeen     map[string]bool
}

// Parse analyzes msg and returns its structure.
func Parse(msg string) *Structure {
	p := &parser{
		s:       msg,
		st:      &Structure{Balanced: true},
		tagSeen: map[string]bool{},
		errSeen: map[string]bool{},
	}
	p.parseText(false)
	if len(p.tagStack) > 0 {
		p.tagError("unclosed tag <" + p.tagStack[len(p.tagStack)-1] + ">")
		p.tagStack = nil
	}
	for name := range p.tagSeen {
		p.st.Tags = append(p.st.Tags, name)
	}
	sort.Strings(p.st.Tags)
	return p.st
}

func (p *parser) tagError(msg string) {
	if !p.errSeen[msg] {
		p.errSeen[msg] = true
		p.st.TagErrors = append(p.st.TagErrors, msg)
	}
	p.st.Balanced = false
}

func (p *parser) addPlaceholder(name, typ string) {
	for _, q := range p.st.Placeholders {
		if q.Name == name && q.Type == typ {
			return
		}
	}
	p.st.Placeholders = append(p.st.Placeholders, Placeholder{Name: name, Type: typ})
}

func (p *parser) addPluralSelectors(arg string, sels []string) {
	if p.st.Plurals == nil {
		p.st.Plurals = map[string][]string{}
	}
	seen := map[string]bool{}
	var merged []string
	for _, s := range append(p.st.Plurals[arg], sels...) {
		if !seen[s] {
			seen[s] = true
			merged = append(merged, s)
		}
	}
	sort.Strings(merged)
	p.st.Plurals[arg] = merged
}

func (p *parser) parseText(stopOnBrace bool) {
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch c {
		case '}':
			if stopOnBrace {
				return
			}
			p.i++
		case '\'':
			p.skipQuoted()
		case '{':
			p.parsePlaceholder()
		case '<':
			if !p.parseTag() {
				p.i++
			}
		case '#':
			if p.pluralDepth > 0 {
				p.addPlaceholder("#", "number")
			}
			p.i++
		default:
			p.i++
		}
	}
}

// skipQuoted implements ICU apostrophe rules: ” is a literal apostrophe;
// ' starts quoted literal text only before {, }, # or | and ends at the next
// ' (auto-closing at end of input); otherwise ' is literal.
func (p *parser) skipQuoted() {
	next := byte(0)
	if p.i+1 < len(p.s) {
		next = p.s[p.i+1]
	}
	if next == '\'' {
		p.i += 2
		return
	}
	if next == '{' || next == '}' || next == '#' || next == '|' {
		p.i++
		for p.i < len(p.s) {
			if p.s[p.i] == '\'' {
				if p.i+1 < len(p.s) && p.s[p.i+1] == '\'' {
					p.i += 2
					continue
				}
				p.i++
				return
			}
			p.i++
		}
		return
	}
	p.i++
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

func (p *parser) skipSpaces() {
	for p.i < len(p.s) && isSpace(p.s[p.i]) {
		p.i++
	}
}

func (p *parser) readName() string {
	start := p.i
	for p.i < len(p.s) {
		c := p.s[p.i]
		if isSpace(c) || c == ',' || c == '}' || c == '{' || c == '\'' {
			break
		}
		p.i++
	}
	return p.s[start:p.i]
}

func (p *parser) parsePlaceholder() {
	p.i++ // consume '{'
	p.skipSpaces()
	name := p.readName()
	if name == "" {
		return
	}
	p.skipSpaces()
	if p.i >= len(p.s) {
		return
	}
	if p.s[p.i] == '}' {
		p.i++
		p.addPlaceholder(name, "")
		return
	}
	if p.s[p.i] != ',' {
		return
	}
	p.i++
	p.skipSpaces()
	typ := p.readName()
	p.skipSpaces()
	switch typ {
	case "plural", "select", "selectordinal":
		if p.i < len(p.s) && p.s[p.i] == ',' {
			p.i++
		}
		p.parseSelectors(name, typ)
	default:
		if p.i < len(p.s) && p.s[p.i] == ',' {
			p.i++
			p.skipStyle()
		}
		if p.i < len(p.s) && p.s[p.i] == '}' {
			p.i++
		}
		if typ != "" {
			p.addPlaceholder(name, typ)
		}
	}
}

// skipStyle skips a number/date/time style up to the closing brace,
// honoring apostrophe quoting.
func (p *parser) skipStyle() {
	for p.i < len(p.s) {
		c := p.s[p.i]
		if c == '\'' {
			p.skipQuoted()
			continue
		}
		if c == '}' {
			return
		}
		p.i++
	}
}

func (p *parser) parseSelectors(name, typ string) {
	isPlural := typ != "select"
	var sels []string
	for {
		p.skipSpaces()
		if p.i >= len(p.s) {
			break
		}
		if p.s[p.i] == '}' {
			p.i++
			break
		}
		sel := p.readName()
		if sel == "" {
			p.i++
			continue
		}
		p.skipSpaces()
		if p.i >= len(p.s) || p.s[p.i] != '{' {
			break
		}
		sels = append(sels, sel)
		p.i++ // consume '{'
		if isPlural {
			p.pluralDepth++
		}
		mark := len(p.tagStack)
		p.parseText(true)
		if isPlural {
			p.pluralDepth--
		}
		if len(p.tagStack) > mark {
			p.tagError("unclosed tag <" + p.tagStack[len(p.tagStack)-1] + "> in " + typ + " branch '" + sel + "'")
			p.tagStack = p.tagStack[:mark]
		}
		if p.i < len(p.s) && p.s[p.i] == '}' {
			p.i++ // consume branch close
		}
	}
	p.addPluralSelectors(name, sels)
	p.addPlaceholder(name, typ)
}

func isLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isLetterOrDigit(c byte) bool {
	return isLetter(c) || (c >= '0' && c <= '9')
}

// parseTag attempts to parse an XML-like tag at '<'. Returns false if the
// text is not a tag and should be treated literally.
func (p *parser) parseTag() bool {
	j := p.i + 1
	closing := false
	if j < len(p.s) && p.s[j] == '/' {
		closing = true
		j++
	}
	if j >= len(p.s) || !isLetter(p.s[j]) {
		return false
	}
	start := j
	for j < len(p.s) && isLetterOrDigit(p.s[j]) {
		j++
	}
	name := p.s[start:j]
	k := j
	for k < len(p.s) && p.s[k] != '>' {
		k++
	}
	if k >= len(p.s) {
		return false
	}
	selfClose := !closing && k > start && p.s[k-1] == '/'
	p.i = k + 1
	p.tagSeen[name] = true
	if closing {
		p.closeTag(name)
	} else if !selfClose {
		p.tagStack = append(p.tagStack, name)
	}
	return true
}

func (p *parser) closeTag(name string) {
	if len(p.tagStack) == 0 {
		p.tagError("unmatched closing tag </" + name + ">")
		return
	}
	if p.tagStack[len(p.tagStack)-1] == name {
		p.tagStack = p.tagStack[:len(p.tagStack)-1]
		return
	}
	p.tagError("mismatched closing tag </" + name + ">")
	for i := len(p.tagStack) - 1; i >= 0; i-- {
		if p.tagStack[i] == name {
			p.tagStack = p.tagStack[:i]
			return
		}
	}
}
