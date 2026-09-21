// Package icu implements a small ICU MessageFormat parser with
// apostrophe quoting, plural/select arguments and rich-text tags.
package icu

import (
	"fmt"
	"sort"
	"strings"
)

// ParserVersion is stamped onto every validation result so old results
// can be replayed against the parser that produced them.
const ParserVersion = "icu-v1"

// Message is a parsed ICU message.
type Message struct {
	Parts []Part `json:"parts"`
}

// Part is one syntactic element of a message.
type Part struct {
	Kind    string              `json:"kind"` // text | arg | pound | tagOpen | tagClose | tagSelf
	Text    string              `json:"text,omitempty"`
	Name    string              `json:"name,omitempty"`
	ArgType string              `json:"argType,omitempty"`
	Style   string              `json:"style,omitempty"`
	Offset  int                 `json:"offset,omitempty"`
	Options map[string]*Message `json:"options,omitempty"`
}

// ArgInfo describes one placeholder found in a message.
type ArgInfo struct {
	Name string   `json:"name"`
	Type string   `json:"type"`
	Cats []string `json:"cats,omitempty"`
}

type parser struct {
	s        string
	i        int
	inPlural bool
}

// Parse parses an ICU message. Rich-text tag balance is not checked here;
// use CheckTags for that.
func Parse(s string) (*Message, error) {
	p := &parser{s: s}
	return p.parseMessage(false)
}

func isQuoteSyntax(c byte) bool {
	return c == '{' || c == '}' || c == '#' || c == '|'
}

func (p *parser) parseMessage(stopAtBrace bool) (*Message, error) {
	var parts []Part
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			parts = append(parts, Part{Kind: "text", Text: text.String()})
			text.Reset()
		}
	}
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch c {
		case '}':
			if stopAtBrace {
				flush()
				return &Message{Parts: parts}, nil
			}
			return nil, fmt.Errorf("unbalanced '}' at offset %d", p.i)
		case '{':
			flush()
			arg, err := p.parseArg()
			if err != nil {
				return nil, err
			}
			parts = append(parts, *arg)
		case '#':
			if p.inPlural {
				flush()
				parts = append(parts, Part{Kind: "pound", Text: "#"})
				p.i++
			} else {
				text.WriteByte(c)
				p.i++
			}
		case '\'':
			p.parseQuote(&text)
		case '<':
			part, ok, err := p.parseTag()
			if err != nil {
				return nil, err
			}
			if ok {
				flush()
				parts = append(parts, *part)
			} else {
				text.WriteByte(c)
				p.i++
			}
		default:
			text.WriteByte(c)
			p.i++
		}
	}
	flush()
	if stopAtBrace {
		return nil, fmt.Errorf("missing closing '}'")
	}
	return &Message{Parts: parts}, nil
}

// parseQuote handles ICU apostrophe quoting: ” is a literal apostrophe,
// and an apostrophe before a syntax char starts a quoted literal.
func (p *parser) parseQuote(text *strings.Builder) {
	if p.i+1 < len(p.s) && p.s[p.i+1] == '\'' {
		text.WriteByte('\'')
		p.i += 2
		return
	}
	if p.i+1 < len(p.s) && isQuoteSyntax(p.s[p.i+1]) {
		j := p.i + 1
		var lit strings.Builder
		for j < len(p.s) {
			if p.s[j] == '\'' {
				if j+1 < len(p.s) && p.s[j+1] == '\'' {
					lit.WriteByte('\'')
					j += 2
					continue
				}
				text.WriteString(lit.String())
				p.i = j + 1
				return
			}
			lit.WriteByte(p.s[j])
			j++
		}
		// Unterminated quote: the opening apostrophe is literal (ICU rule).
		text.WriteByte('\'')
		p.i++
		return
	}
	text.WriteByte('\'')
	p.i++
}

func (p *parser) skipSpaces() {
	for p.i < len(p.s) {
		switch p.s[p.i] {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}

func (p *parser) readWord() string {
	start := p.i
	for p.i < len(p.s) {
		c := p.s[p.i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ',' || c == '}' || c == '{' {
			break
		}
		p.i++
	}
	return p.s[start:p.i]
}

func (p *parser) readOptionKey() string {
	if p.i < len(p.s) && p.s[p.i] == '=' {
		start := p.i
		p.i++
		for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
			p.i++
		}
		return p.s[start:p.i]
	}
	return p.readWord()
}

func (p *parser) parseArg() (*Part, error) {
	open := p.i
	p.i++ // consume '{'
	p.skipSpaces()
	name := p.readWord()
	if name == "" {
		return nil, fmt.Errorf("empty argument name at offset %d", open)
	}
	part := &Part{Kind: "arg", Name: name}
	p.skipSpaces()
	if p.i >= len(p.s) {
		return nil, fmt.Errorf("unclosed argument %q", name)
	}
	if p.s[p.i] == '}' {
		p.i++
		return part, nil
	}
	if p.s[p.i] != ',' {
		return nil, fmt.Errorf("expected ',' or '}' in argument %q", name)
	}
	p.i++
	p.skipSpaces()
	typ := p.readWord()
	part.ArgType = typ
	p.skipSpaces()
	if p.i >= len(p.s) {
		return nil, fmt.Errorf("unclosed argument %q", name)
	}
	if p.s[p.i] == '}' {
		p.i++
		return part, nil
	}
	if p.s[p.i] != ',' {
		return nil, fmt.Errorf("expected ',' or '}' in argument %q", name)
	}
	p.i++
	p.skipSpaces()
	if typ == "plural" || typ == "select" || typ == "selectordinal" {
		if typ != "select" && strings.HasPrefix(p.s[p.i:], "offset:") {
			p.i += len("offset:")
			p.skipSpaces()
			n := 0
			for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
				n = n*10 + int(p.s[p.i]-'0')
				p.i++
			}
			part.Offset = n
			p.skipSpaces()
		}
		options := map[string]*Message{}
		for {
			p.skipSpaces()
			if p.i >= len(p.s) {
				return nil, fmt.Errorf("unclosed %s argument %q", typ, name)
			}
			if p.s[p.i] == '}' {
				p.i++
				break
			}
			key := p.readOptionKey()
			if key == "" {
				return nil, fmt.Errorf("invalid option key in argument %q", name)
			}
			p.skipSpaces()
			if p.i >= len(p.s) || p.s[p.i] != '{' {
				return nil, fmt.Errorf("expected '{' after option %q", key)
			}
			p.i++
			saved := p.inPlural
			p.inPlural = typ != "select"
			sub, err := p.parseMessage(true)
			p.inPlural = saved
			if err != nil {
				return nil, err
			}
			if p.i >= len(p.s) || p.s[p.i] != '}' {
				return nil, fmt.Errorf("missing '}' after option %q", key)
			}
			p.i++ // consume '}'
			options[key] = sub
		}
		part.Options = options
		return part, nil
	}
	// Free-form style: read to the matching '}'.
	depth := 1
	start := p.i
	for p.i < len(p.s) {
		c := p.s[p.i]
		if c == '{' {
			depth++
		} else if c == '}' {
			depth--
			if depth == 0 {
				part.Style = strings.TrimSpace(p.s[start:p.i])
				p.i++
				return part, nil
			}
		}
		p.i++
	}
	return nil, fmt.Errorf("unclosed argument %q", name)
}

func isTagNameStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isTagNameChar(c byte) bool {
	return isTagNameStart(c) || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == ':' || c == '.'
}

func scanTagName(s string, i int) (string, int) {
	start := i
	if i >= len(s) || !isTagNameStart(s[i]) {
		return "", i
	}
	i++
	for i < len(s) && isTagNameChar(s[i]) {
		i++
	}
	return s[start:i], i
}

// parseTag recognises <name>, </name> and <name/>. It returns ok=false
// when the text at the cursor is not a tag (treated as literal text).
func (p *parser) parseTag() (*Part, bool, error) {
	rest := p.s[p.i:]
	if strings.HasPrefix(rest, "</") {
		name, end := scanTagName(p.s, p.i+2)
		if name == "" || end >= len(p.s) || p.s[end] != '>' {
			return nil, false, nil
		}
		p.i = end + 1
		return &Part{Kind: "tagClose", Name: name}, true, nil
	}
	name, end := scanTagName(p.s, p.i+1)
	if name == "" {
		return nil, false, nil
	}
	j := end
	for j < len(p.s) && (p.s[j] == ' ' || p.s[j] == '\t') {
		j++
	}
	if j < len(p.s) && p.s[j] == '>' {
		p.i = j + 1
		return &Part{Kind: "tagOpen", Name: name}, true, nil
	}
	if j+1 < len(p.s) && p.s[j] == '/' && p.s[j+1] == '>' {
		p.i = j + 2
		return &Part{Kind: "tagSelf", Name: name}, true, nil
	}
	return nil, false, nil
}

// CheckTags verifies that rich-text tags are balanced and properly nested.
func CheckTags(m *Message) error {
	if m == nil {
		return nil
	}
	var stack []string
	for _, part := range m.Parts {
		switch part.Kind {
		case "tagOpen":
			stack = append(stack, part.Name)
		case "tagClose":
			if len(stack) == 0 {
				return fmt.Errorf("closing tag </%s> without matching open tag", part.Name)
			}
			top := stack[len(stack)-1]
			if top != part.Name {
				return fmt.Errorf("tag mismatch: <%s> closed by </%s>", top, part.Name)
			}
			stack = stack[:len(stack)-1]
		}
		if part.Options != nil {
			keys := make([]string, 0, len(part.Options))
			for k := range part.Options {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if err := CheckTags(part.Options[k]); err != nil {
					return err
				}
			}
		}
	}
	if len(stack) > 0 {
		return fmt.Errorf("unclosed tag <%s>", stack[len(stack)-1])
	}
	return nil
}

// CollectArgs lists all placeholders, recursing into plural/select options.
func CollectArgs(m *Message) []ArgInfo {
	var out []ArgInfo
	var walk func(mm *Message)
	walk = func(mm *Message) {
		if mm == nil {
			return
		}
		for _, part := range mm.Parts {
			if part.Kind == "arg" {
				info := ArgInfo{Name: part.Name, Type: part.ArgType}
				if len(part.Options) > 0 {
					cats := make([]string, 0, len(part.Options))
					for k := range part.Options {
						cats = append(cats, k)
					}
					sort.Strings(cats)
					info.Cats = cats
				}
				out = append(out, info)
			}
			if part.Options != nil {
				keys := make([]string, 0, len(part.Options))
				for k := range part.Options {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					walk(part.Options[k])
				}
			}
		}
	}
	walk(m)
	return out
}
