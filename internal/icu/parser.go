// Package icu parses a useful subset of ICU MessageFormat into a stable
// structural representation used for catalog comparison.
package icu

import (
	"fmt"
	"strings"
)

// Version identifies the parser implementation. Changing it invalidates
// validation results that were produced by an older parser.
const Version = "icu-parser-3"

// Placeholder is one ICU argument with its format type and, for plural or
// select style messages, its branch categories.
type Placeholder struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	Style      string   `json:"style,omitempty"`
	Branches   []Branch `json:"branches,omitempty"`
	Offset     int      `json:"offset,omitempty"`
	NestedArgs []string `json:"nested_args,omitempty"`
}

// Branch is a plural category or select key with its sub-message structure.
type Branch struct {
	Key     string `json:"key"`
	Message string `json:"message"`
	// fields below are filled after tokenization
}

// TagOccurrence records one rich-text tag for imbalance reporting.
type TagOccurrence struct {
	Name    string `json:"name"`
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Closing bool   `json:"closing"`
	Self    bool   `json:"self"`
}

// Token is one piece of a parsed message.
type Token struct {
	Kind   string       `json:"kind"` // text, arg, tag
	Text   string       `json:"text,omitempty"`
	Arg    *Placeholder `json:"arg,omitempty"`
	Tag    string       `json:"tag,omitempty"`
	Close  bool         `json:"close,omitempty"`
	Self   bool         `json:"self,omitempty"`
	Line   int          `json:"line,omitempty"`
	Column int          `json:"column,omitempty"`
}

// Message is the parsed structure of one catalog message string.
type Message struct {
	Raw          string          `json:"raw"`
	Tokens       []Token         `json:"tokens,omitempty"`
	Placeholders []Placeholder   `json:"placeholders,omitempty"`
	TagNames     []string        `json:"tag_names,omitempty"`
	Unbalanced   []TagOccurrence `json:"unbalanced,omitempty"`
	ParseError   string          `json:"parse_error,omitempty"`
}

type parser struct {
	src  []rune
	pos  int
	line int
	col  int
}

// Parse parses a message. Tag imbalance is recorded on the result; syntax
// errors (malformed braces) are returned as an error.
func Parse(input string) (*Message, error) {
	p := &parser{src: []rune(input), line: 1, col: 1}
	toks, err := p.parseChunks(false, 0)
	if err != nil {
		return nil, err
	}
	msg := &Message{Raw: input, Tokens: toks}
	msg.Placeholders = collectArgs(toks, map[string]bool{})
	msg.TagNames = collectTags(toks, map[string]bool{})
	msg.Unbalanced = checkBalance(toks)
	return msg, nil
}

// MustParse parses or returns a Message carrying the parse error.
func MustParse(input string) *Message {
	m, err := Parse(input)
	if err != nil {
		return &Message{Raw: input, ParseError: err.Error()}
	}
	return m
}

func (p *parser) peek() rune {
	if p.pos >= len(p.src) {
		return 0
	}
	return p.src[p.pos]
}

func (p *parser) advance() rune {
	r := p.src[p.pos]
	p.pos++
	if r == '\n' {
		p.line++
		p.col = 1
	} else {
		p.col++
	}
	return r
}

func (p *parser) location() (int, int) { return p.line, p.col }

// parseChunks parses until end of input, a closing brace (inArg>0), or a
// plural/select branch terminator. pluralContext enables the '#' token.
func (p *parser) parseChunks(pluralContext bool, inArg int) ([]Token, error) {
	var toks []Token
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			toks = append(toks, Token{Kind: "text", Text: text.String()})
			text.Reset()
		}
	}
	for p.pos < len(p.src) {
		r := p.peek()
		switch {
		case inArg > 0 && r == '}':
			flush()
			return toks, nil
		case r == '{':
			flush()
			line, col := p.location()
			p.advance()
			arg, err := p.parseArg()
			if err != nil {
				return nil, err
			}
			toks = append(toks, Token{Kind: "arg", Arg: arg, Line: line, Column: col})
		case pluralContext && r == '#':
			flush()
			line, col := p.location()
			p.advance()
			toks = append(toks, Token{Kind: "text", Text: "#", Line: line, Column: col})
		case r == '\'':
			line, col := p.location()
			lit := p.consumeQuoted()
			text.WriteString(lit)
			_ = line
			_ = col
		case r == '<' && isTagStart(p.src, p.pos):
			flush()
			line, col := p.location()
			tok, err := p.parseTag()
			if err != nil {
				return nil, err
			}
			tok.Line, tok.Column = line, col
			toks = append(toks, tok)
		default:
			p.advance()
			text.WriteRune(r)
		}
	}
	flush()
	if inArg > 0 {
		return nil, fmt.Errorf("icu: unclosed '{'")
	}
	return toks, nil
}

// consumeQuoted handles ICU quoting at the current apostrophe position.
// ” -> literal apostrophe; 'text' -> text until the next apostrophe;
// an unpaired trailing apostrophe is literal.
func (p *parser) consumeQuoted() string {
	// current char is apostrophe
	p.advance()
	if p.pos < len(p.src) && p.peek() == '\'' {
		p.advance()
		return "'"
	}
	var sb strings.Builder
	for p.pos < len(p.src) {
		r := p.advance()
		if r == '\'' {
			if p.pos < len(p.src) && p.peek() == '\'' {
				p.advance()
				sb.WriteRune('\'')
				continue
			}
			return sb.String()
		}
		sb.WriteRune(r)
	}
	// unpaired apostrophe at end of message is literal
	return "'" + sb.String()
}

func isTagStart(src []rune, pos int) bool {
	// < followed by / or a letter
	if pos+1 >= len(src) {
		return false
	}
	c := src[pos+1]
	return c == '/' || isNameStart(c)
}

func (p *parser) parseTag() (Token, error) {
	// current char is '<'
	p.advance()
	closing := false
	if p.peek() == '/' {
		closing = true
		p.advance()
	}
	start := p.pos
	for p.pos < len(p.src) && isNameChar(p.peek()) {
		p.advance()
	}
	name := string(p.src[start:p.pos])
	if name == "" {
		return Token{}, fmt.Errorf("icu: invalid tag at %d:%d", p.line, p.col)
	}
	// skip attributes
	for p.pos < len(p.src) && p.peek() != '>' {
		if p.peek() == '<' {
			return Token{}, fmt.Errorf("icu: malformed tag <%s", name)
		}
		p.advance()
	}
	self := false
	if p.pos >= 1 && false {
	}
	// detect self-closing: the char before '>' is '/'
	if p.pos > 0 && p.src[p.pos-1] == '/' {
		self = true
	}
	if p.pos >= len(p.src) {
		return Token{}, fmt.Errorf("icu: unclosed tag <%s", name)
	}
	p.advance() // '>'
	return Token{Kind: "tag", Tag: name, Close: closing, Self: self}, nil
}

func isNameStart(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
}
func isNameChar(r rune) bool {
	return isNameStart(r) || r >= '0' && r <= '9' || r == '-' || r == '_'
}

func isSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
}
