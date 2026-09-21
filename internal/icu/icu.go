// Package icu parses a constrained subset of ICU MessageFormat:
// placeholders ({name} / {name,type} / {name,type,style}), plural/select
// branches, apostrophe quoting rules, and rich-text tags.
package icu


const ParserVersion = "icu-parser-1"

type Param struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type PluralAST struct {
	Name       string   `json:"name"`
	Categories []string `json:"categories"`
}

type TagProblem struct {
	Tag   string `json:"tag"`
	Kind  string `json:"kind"`
	Index int    `json:"index"`
}

type AST struct {
	Params     []Param      `json:"params"`
	Plurals    []PluralAST  `json:"plurals"`
	Tags       []string     `json:"tags"`
	Unbalanced []TagProblem `json:"unbalanced,omitempty"`
	Error      string       `json:"error,omitempty"`
}

type frame struct {
	tag string
	pos int
}

type parser struct {
	src string
	err string
}

func (p *parser) fail(m string) {
	if p.err == "" {
		p.err = m
	}
}

// Parse parses a message. A non-nil AST is always returned.
func Parse(src string) *AST {
	p := &parser{src: src}
	params, plurals, st := p.region(0, len(src), false)
	ast := &AST{Params: dedupeParams(params), Plurals: plurals}
	for _, f := range st {
		ast.Unbalanced = append(ast.Unbalanced, TagProblem{Tag: f.tag, Kind: "unclosed", Index: f.pos})
	}
	ast.Tags = allTags(src)
	ast.Error = p.err
	return ast
}

// region parses s[start:end]. Open frames that remain (including those opened
// inside nested placeholders) are returned so the caller can track them.
func (p *parser) region(start, end int, inPlural bool) (params []Param, plurals []PluralAST, stack []frame) {
	i := start
	for i < end {
		c := p.src[i]
		switch {
		case c == '\'':
			i = p.quote(i, end)
		case c == '#' && inPlural:
			i++
		case c == '{':
			np, npl, nf, ni := p.placeholder(i, end, inPlural)
			params = append(params, np...)
			plurals = append(plurals, npl...)
			stack = append(stack, nf...)
			if ni <= i {
				i++
			} else {
				i = ni
			}
		case c == '}':
			p.fail("unmatched '}'")
			i++
		case c == '<':
			ni, name, closing, ok := p.tag(i, end)
			if ok {
				if closing {
					if n := len(stack); n > 0 && stack[n-1].tag == name {
						stack = stack[:n-1]
					} else {
						p.fail("unmatched closing tag </" + name + ">")
					}
				} else {
					stack = append(stack, frame{tag: name, pos: i})
				}
			}
			if ni <= i {
				i++
			} else {
				i = ni
			}
		default:
			i++
		}
	}
	return params, plurals, stack
}

func (p *parser) quote(i, end int) int {
	if i+1 < end && p.src[i+1] == '\'' {
		return i + 2
	}
	j := i + 1
	for j < end && p.src[j] != '\'' {
		j++
	}
	if j >= end {
		p.fail("unterminated quoted literal")
		return end
	}
	return j + 1
}

func (p *parser) placeholder(i, end int, inPlural bool) (params []Param, plurals []PluralAST, frames []frame, next int) {
	cl := matchBrace(p.src, i, end)
	if cl < 0 {
		p.fail("unmatched '{'")
		return nil, nil, nil, end
	}
	inner := p.src[i+1 : cl]
	c1 := nthTopComma(inner, 0, len(inner), 1)
	if c1 < 0 {
		name := trim(inner)
		if name == "" {
			p.fail("empty placeholder")
			return nil, nil, nil, cl + 1
		}
		return []Param{{name, "any"}}, nil, nil, cl + 1
	}
	name := trim(inner[:c1])
	if name == "" {
		p.fail("empty placeholder")
		return nil, nil, nil, cl + 1
	}
	c2 := nthTopComma(inner, 0, len(inner), 2)
	var typ, style string
	styleStart := -1
	if c2 < 0 {
		typ = trim(inner[c1+1:])
	} else {
		typ = trim(inner[c1+1 : c2])
		style = inner[c2+1:]
		styleStart = i + 1 + c2 + 1
	}
	params = append(params, Param{name, typ})
	if (typ == "plural" || typ == "selectordinal" || typ == "select") && style != "" {
		cats := optionKeys(style)
		plurals = append(plurals, PluralAST{Name: name, Categories: cats})
		np, npl, nf := p.scanStyleBodies(styleStart, cl)
		params = append(params, np...)
		plurals = append(plurals, npl...)
		frames = nf
	}
	return params, plurals, frames, cl + 1
}

// scanStyleBodies parses each {...} option body in a plural/select style.
func (p *parser) scanStyleBodies(start, end int) (params []Param, plurals []PluralAST, frames []frame) {
	i := start
	for i < end {
		if p.src[i] == '{' {
			cl := matchBrace(p.src, i, end)
			if cl < 0 {
				p.fail("unmatched '{' in options")
				return
			}
			np, npl, nf := p.region(i+1, cl, true)
			params = append(params, np...)
			plurals = append(plurals, npl...)
			frames = append(frames, nf...)
			i = cl + 1
			continue
		}
		if p.src[i] == '\'' {
			i = p.quote(i, end)
			continue
		}
		i++
	}
	return params, plurals, frames
}

func (p *parser) tag(i, end int) (next int, name string, closing, ok bool) {
	j := i + 1
	if j < end && p.src[j] == '/' {
		closing = true
		j++
	}
	ns := j
	for j < end && isName(p.src[j]) {
		j++
	}
	if j == ns {
		return i + 1, "", false, false
	}
	name = p.src[ns:j]
	for j < end && p.src[j] != '>' {
		if p.src[j] == '<' {
			return i + 1, "", false, false
		}
		j++
	}
	if j >= end {
		return i + 1, "", false, false
	}
	if j > ns && p.src[j-1] == '/' {
		return j + 1, "", false, false
	}
	return j + 1, name, closing, true
}

func isName(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_'
}
