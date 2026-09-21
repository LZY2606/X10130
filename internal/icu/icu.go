// Package icu parses ICU-message-format-like message text into a
// structured summary: placeholders (with types), plural/select categories
// and rich-text tag balance. It implements the ICU apostrophe quoting
// rules: ” is a literal apostrophe, and ' starts a quoted literal only
// when followed by a syntax character ({, }, or # inside a plural).
package icu

import (
	"regexp"
	"sort"
	"strings"
)

// ParserVersion identifies the structure produced by this parser. Any
// change to parsing semantics must bump this version so that validation
// results bound to older parser versions become visibly invalid.
const ParserVersion = "icu-parser/1"

// Placeholder is a single {name} or {name, type, ...} argument.
type Placeholder struct {
	Name string `json:"name"`
	Type string `json:"type"` // "" for simple {name}; otherwise number/date/plural/select/...
}

// Plural describes one plural/selectordinal argument and its categories.
type Plural struct {
	Name       string   `json:"name"`
	Categories []string `json:"categories"`
}

// Structure is the parsed summary of one message.
type Structure struct {
	Placeholders []Placeholder `json:"placeholders"`
	Plurals      []Plural      `json:"plurals"`
	Tags         []string      `json:"tags"`
	TagError     string        `json:"tagError,omitempty"`
}

// Parse parses message text and returns its structure. Parsing never
// fails: malformed fragments are treated as literal text, except tag
// imbalance which is reported in TagError.
func Parse(text string) Structure {
	p := &parser{
		s:       text,
		ph:      map[string]string{},
		plurals: map[string]map[string]bool{},
	}
	p.parseMessage(false)
	st := Structure{}
	for _, n := range p.phOrder {
		st.Placeholders = append(st.Placeholders, Placeholder{Name: n, Type: p.ph[n]})
	}
	for _, n := range p.plOrder {
		cats := make([]string, 0, len(p.plurals[n]))
		for c := range p.plurals[n] {
			cats = append(cats, c)
		}
		sort.Strings(cats)
		st.Plurals = append(st.Plurals, Plural{Name: n, Categories: cats})
	}
	st.Tags, st.TagError = checkTags(strings.Join(p.texts, ""))
	return st
}

type parser struct {
	s       string
	i       int
	ph      map[string]string
	phOrder []string
	// plurals maps argument name -> set of categories.
	plurals map[string]map[string]bool
	plOrder []string
	texts   []string
}

func isWS(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func (p *parser) skipWS() {
	for p.i < len(p.s) && isWS(p.s[p.i]) {
		p.i++
	}
}

func (p *parser) addPlaceholder(name, typ string) {
	if name == "" {
		return
	}
	if _, ok := p.ph[name]; !ok {
		p.ph[name] = typ
		p.phOrder = append(p.phOrder, name)
	}
}

func (p *parser) addPluralCategory(name, cat string) {
	if name == "" || cat == "" {
		return
	}
	if _, ok := p.plurals[name]; !ok {
		p.plurals[name] = map[string]bool{}
		p.plOrder = append(p.plOrder, name)
	}
	p.plurals[name][cat] = true
}

// quoteStarts reports whether an apostrophe at p.i starts a quoted
// literal per ICU rules: only when the next char is a syntax char.
func (p *parser) quoteStarts(inPlural bool) bool {
	if p.i+1 >= len(p.s) {
		return false
	}
	n := p.s[p.i+1]
	if n == '{' || n == '}' {
		return true
	}
	if inPlural && n == '#' {
		return true
	}
	return false
}

// parseMessage consumes literal text and arguments. When stop is true it
// returns after consuming the matching '}' of the enclosing argument.
func (p *parser) parseMessage(stop bool) {
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			p.texts = append(p.texts, text.String())
			text.Reset()
		}
	}
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch c {
		case '\'':
			if p.i+1 < len(p.s) && p.s[p.i+1] == '\'' {
				text.WriteByte('\'')
				p.i += 2
				continue
			}
			if p.quoteStarts(stop) {
				p.i++ // consume opening '
				for p.i < len(p.s) {
					if p.s[p.i] == '\'' {
						if p.i+1 < len(p.s) && p.s[p.i+1] == '\'' {
							text.WriteByte('\'')
							p.i += 2
							continue
						}
						p.i++ // consume closing '
						break
					}
					text.WriteByte(p.s[p.i])
					p.i++
				}
				continue
			}
			text.WriteByte('\'')
			p.i++
		case '{':
			flush()
			p.parseArg()
		case '}':
			if stop {
				p.i++
				flush()
				return
			}
			text.WriteByte(c)
			p.i++
		default:
			text.WriteByte(c)
			p.i++
		}
	}
	flush()
}

// readName reads an argument name / type keyword / selector keyword.
func (p *parser) readName() string {
	start := p.i
	for p.i < len(p.s) {
		c := p.s[p.i]
		if c == ',' || c == '{' || c == '}' || isWS(c) {
			break
		}
		p.i++
	}
	return p.s[start:p.i]
}

func (p *parser) parseArg() {
	p.i++ // consume '{'
	p.skipWS()
	name := p.readName()
	p.skipWS()
	if p.i >= len(p.s) {
		return // unterminated: drop silently (treated as literal-ish)
	}
	if p.s[p.i] == '}' {
		p.i++
		p.addPlaceholder(name, "")
		return
	}
	if p.s[p.i] != ',' {
		return // malformed
	}
	p.i++
	p.skipWS()
	typ := p.readName()
	p.skipWS()
	switch typ {
	case "plural", "selectordinal":
		p.addPlaceholder(name, typ)
		p.parseSelectorBody(name, true)
	case "select":
		p.addPlaceholder(name, typ)
		p.parseSelectorBody(name, false)
	default:
		p.addPlaceholder(name, typ)
		// Skip optional style up to the matching '}', honoring nesting.
		depth := 0
		for p.i < len(p.s) {
			if p.s[p.i] == '{' {
				depth++
			} else if p.s[p.i] == '}' {
				if depth == 0 {
					p.i++
					return
				}
				depth--
			}
			p.i++
		}
	}
}

// parseSelectorBody parses ", [offset: n,] key{msg} key{msg} ... }".
// p.i is positioned after the type keyword.
func (p *parser) parseSelectorBody(name string, isPlural bool) {
	if p.i >= len(p.s) || p.s[p.i] != ',' {
		// allow immediate '}'
		if p.i < len(p.s) && p.s[p.i] == '}' {
			p.i++
		}
		return
	}
	p.i++
	for {
		p.skipWS()
		if p.i >= len(p.s) {
			return
		}
		if p.s[p.i] == '}' {
			p.i++
			return
		}
		sel := p.readName()
		p.skipWS()
		// "offset: N" appears before selectors in plurals.
		if sel == "offset" && p.i < len(p.s) && p.s[p.i] == ':' {
			p.i++
			p.skipWS()
			p.readName() // offset value
			p.skipWS()
			continue
		}
		if p.i >= len(p.s) || p.s[p.i] != '{' {
			return // malformed selector
		}
		p.i++ // consume '{'
		if isPlural {
			p.addPluralCategory(name, sel)
		}
		p.parseMessage(true)
	}
}

var tagRe = regexp.MustCompile(`<(/?)([A-Za-z][A-Za-z0-9]*)(\s*/?)>`)

// checkTags verifies that XML-like rich text tags are balanced and
// properly nested. Returns the tag names in order of appearance and an
// error message if unbalanced.
func checkTags(text string) ([]string, string) {
	var names []string
	var stack []string
	for _, m := range tagRe.FindAllStringSubmatch(text, -1) {
		closing := m[1] == "/"
		selfClose := strings.HasSuffix(m[3], "/")
		name := m[2]
		names = append(names, name)
		if selfClose {
			continue
		}
		if !closing {
			stack = append(stack, name)
			continue
		}
		if len(stack) == 0 {
			return names, "closing tag </" + name + "> without opening"
		}
		top := stack[len(stack)-1]
		if top != name {
			return names, "tag <" + top + "> closed by </" + name + ">"
		}
		stack = stack[:len(stack)-1]
	}
	if len(stack) > 0 {
		return names, "unclosed tag <" + stack[len(stack)-1] + ">"
	}
	return names, ""
}

// pluralCategories lists required CLDR plural categories per language
// (simplified, sufficient for validation of common locales).
var pluralCategories = map[string][]string{
	"en": {"one", "other"},
	"de": {"one", "other"},
	"es": {"one", "many", "other"},
	"fr": {"one", "many", "other"},
	"it": {"one", "many", "other"},
	"pt": {"one", "many", "other"},
	"ja": {"other"},
	"zh": {"other"},
	"ko": {"other"},
	"ru": {"one", "few", "many", "other"},
	"uk": {"one", "few", "many", "other"},
	"pl": {"one", "few", "many", "other"},
	"cs": {"one", "few", "many", "other"},
	"ar": {"zero", "one", "two", "few", "many", "other"},
}

// RequiredCategories returns the plural categories a locale must provide.
func RequiredCategories(lang string) []string {
	base := lang
	if i := strings.IndexAny(base, "-_"); i >= 0 {
		base = base[:i]
	}
	base = strings.ToLower(base)
	if cats, ok := pluralCategories[base]; ok {
		return cats
	}
	return []string{"other"}
}
