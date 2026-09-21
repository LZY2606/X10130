package catalog

import (
	"strings"
	"unicode"
)

// ParserVersion identifies the parsing rules.
const ParserVersion = "icu-parser-3"

// SimpleArgumentTypes are the non-complex ICU argument types.
var SimpleArgumentTypes = map[string]bool{
	"": true, "number": true, "date": true, "time": true,
	"spellout": true, "duration": true,
}

// VoidTags never need a closing tag.
var VoidTags = map[string]bool{
	"br": true, "hr": true, "img": true, "input": true,
}

type scanner struct {
	src  []rune
	pos  int
	errs []string
}

// Parse parses an ICU-style message.
func Parse(text string) Parsed {
	s := &scanner{src: []rune(text)}
	nodes := s.parseUntil(func(r rune) bool { return false })
	out := Parsed{Nodes: nodes}
	s.collect(nodes, &out)
	if !checkTags(out.Tags) {
		out.Errors = append(out.Errors, "unbalanced rich-text tags")
	}
	out.Errors = append(out.Errors, s.errs...)
	return out
}

func (s *scanner) atEnd() bool { return s.pos >= len(s.src) }
func (s *scanner) peek() rune {
	if s.atEnd() {
		return 0
	}
	return s.src[s.pos]
}

// parseUntil consumes until stop rune (which is consumed) or EOF.
func (s *scanner) parseUntil(stop func(rune) bool) []Node {
	var nodes []Node
	var literal strings.Builder
	flush := func() {
		if literal.Len() > 0 {
			nodes = append(nodes, Node{Kind: KindLiteral, Text: literal.String()})
			literal.Reset()
		}
	}
	for !s.atEnd() {
		r := s.peek()
		if stop(r) {
			s.pos++
			flush()
			return nodes
		}
		switch r {
		case '\'':
			lit, ok := s.readQuote(nil)
			literal.WriteString(lit)
			if !ok {
				s.errs = append(s.errs, "unterminated quoted segment")
			}
		case '{':
			flush()
			if n, ok := s.readArgument(); ok {
				nodes = append(nodes, n)
			}
		case '}':
			s.errs = append(s.errs, "unmatched closing brace")
			s.pos++
		default:
			literal.WriteRune(r)
			s.pos++
		}
	}
	flush()
	return nodes
}

// readQuote consumes starting at an apostrophe. The returned string is the
// emitted literal (quotes stripped). When raw is non-nil (we are inside an
// argument body), the original quoted spelling is preserved in raw so that
// downstream branch parsing still honors the quoting.
func (s *scanner) readQuote(raw *strings.Builder) (string, bool) {
	// s.src[s.pos] == '\''
	if s.pos+1 < len(s.src) && s.src[s.pos+1] == '\'' {
		s.pos += 2
		if raw != nil {
			raw.WriteString("''")
		}
		return "'", true
	}
	start := s.pos
	s.pos++ // consume opening quote
	var b strings.Builder
	for !s.atEnd() {
		r := s.src[s.pos]
		if r == '\'' {
			if s.pos+1 < len(s.src) && s.src[s.pos+1] == '\'' {
				b.WriteRune('\'')
				s.pos += 2
				continue
			}
			end := s.pos + 1 // include closing quote in preserved spelling
			s.pos++
			if raw != nil {
				raw.WriteString(string(s.src[start:end]))
			}
			return b.String(), true
		}
		b.WriteRune(r)
		s.pos++
	}
	if raw != nil {
		raw.WriteString(string(s.src[start:s.pos]))
	}
	return "'" + b.String(), false
}

func trimToken(t string) string { return strings.TrimSpace(t) }

// splitTop splits on commas outside quotes/braces/parens, PRESERVING quotes so
// branch bodies keep quoting information.
func splitTop(s string) []string {
	var parts []string
	depth := 0
	inQuote := false
	var cur strings.Builder
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if r == '\'' {
			cur.WriteRune(r)
			if i+1 < len(rs) && rs[i+1] == '\'' {
				cur.WriteRune('\'')
				i++
				continue
			}
			inQuote = !inQuote
			continue
		}
		if !inQuote {
			switch r {
			case '{', '(':
				depth++
			case '}', ')':
				depth--
			case ',':
				if depth == 0 {
					parts = append(parts, cur.String())
					cur.Reset()
					continue
				}
			}
		}
		cur.WriteRune(r)
	}
	parts = append(parts, cur.String())
	return parts
}

func (s *scanner) readArgument() (Node, bool) {
	s.pos++ // consume '{'
	depth := 1
	var raw strings.Builder
	for !s.atEnd() && depth > 0 {
		r := s.src[s.pos]
		switch r {
		case '\'':
			if _, ok := s.readQuote(&raw); !ok {
				s.errs = append(s.errs, "unterminated quoted segment in argument")
				return Node{}, false
			}
		case '{':
			depth++
			raw.WriteRune(r)
			s.pos++
		case '}':
			depth--
			s.pos++
			if depth == 0 {
				goto parsed
			}
			raw.WriteRune(r)
		default:
			raw.WriteRune(r)
			s.pos++
		}
	}
	if depth > 0 {
		s.errs = append(s.errs, "unmatched opening brace")
		return Node{}, false
	}
parsed:
	parts := splitTop(raw.String())
	for i := range parts {
		parts[i] = trimToken(parts[i])
	}
	if len(parts) == 0 || parts[0] == "" {
		s.errs = append(s.errs, "empty argument name")
		return Node{}, false
	}
	node := Node{Kind: KindArgument, Name: parts[0]}
	if len(parts) == 1 {
		return node, true
	}
	typ := parts[1]
	node.Type = typ
	if typ == "plural" || typ == "selectordinal" || typ == "select" {
		body := strings.Join(parts[2:], ",")
		offset, branches := s.parseBranches(body, typ)
		if typ == "select" {
			node.Kind = KindSelect
		} else {
			node.Kind = KindPlural
		}
		node.Offset = offset
		node.Branches = branches
	} else if len(parts) >= 3 {
		node.Style = strings.TrimSpace(strings.Join(parts[2:], ","))
	}
	if !SimpleArgumentTypes[typ] && node.Kind != KindPlural && node.Kind != KindSelect {
		s.errs = append(s.errs, "unknown argument type: "+typ)
	}
	return node, true
}

func (s *scanner) parseBranches(body, kind string) (int, []Branch) {
	rs := []rune(body)
	i := 0
	offset := 0
	for i < len(rs) && unicode.IsSpace(rs[i]) {
		i++
	}
	if kind != "select" && strings.HasPrefix(string(rs[i:]), "offset:") {
		j := i + len("offset:")
		k := j
		for k < len(rs) && !unicode.IsSpace(rs[k]) {
			k++
		}
		v := strings.TrimSpace(string(rs[j:k]))
		n := 0
		valid := v != ""
		for _, r := range v {
			if r < '0' || r > '9' {
				valid = false
			}
			n = n*10 + int(r-'0')
		}
		if valid {
			offset = n
		} else {
			s.errs = append(s.errs, "invalid plural offset: "+v)
		}
		i = k
	}
	var branches []Branch
	for i < len(rs) {
		for i < len(rs) && unicode.IsSpace(rs[i]) {
			i++
		}
		if i >= len(rs) {
			break
		}
		start := i
		for i < len(rs) && !unicode.IsSpace(rs[i]) {
			i++
		}
		caseName := string(rs[start:i])
		for i < len(rs) && unicode.IsSpace(rs[i]) {
			i++
		}
		if i >= len(rs) || rs[i] != '{' {
			if caseName != "" {
				s.errs = append(s.errs, "missing branch body for case: "+caseName)
			}
			break
		}
		i++ // consume '{'
		// Re-parent scanner position; nested scanner shares errors.
		sub := &scanner{src: rs, pos: i, errs: s.errs}
		children := sub.parseUntil(func(r rune) bool { return r == '}' })
		s.errs = sub.errs
		i = sub.pos
		branches = append(branches, Branch{Case: caseName, Children: children})
	}
	return offset, branches
}

func (s *scanner) collect(nodes []Node, out *Parsed) {
	for _, n := range nodes {
		switch n.Kind {
		case KindArgument:
			out.Placeholders = addPlaceholder(out.Placeholders, Placeholder{Name: n.Name, Type: effectiveType(n.Type)})
			for _, b := range n.Branches {
				s.collect(b.Children, out)
			}
		case KindLiteral:
			for _, t := range scanTags(n.Text) {
				out.Tags = append(out.Tags, t)
			}
		}
		for _, b := range n.Branches {
			s.collect(b.Children, out)
		}
		s.collect(n.Children, out)
	}
}

func effectiveType(typ string) string {
	if typ == "selectordinal" {
		return "plural"
	}
	return typ
}

func addPlaceholder(list []Placeholder, p Placeholder) []Placeholder {
	for _, ex := range list {
		if ex.Name == p.Name {
			return list
		}
	}
	return append(list, p)
}

func checkTags(tags []TagUse) bool {
	stack := []string{}
	for _, t := range tags {
		if t.SelfClose || VoidTags[t.Name] {
			continue
		}
		if t.Open {
			stack = append(stack, t.Name)
			continue
		}
		if t.Close {
			if len(stack) == 0 || stack[len(stack)-1] != t.Name {
				return false
			}
			stack = stack[:len(stack)-1]
		}
	}
	return len(stack) == 0
}
