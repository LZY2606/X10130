package icu

import "sort"

func matchBrace(s string, start, end int) int {
	depth := 0
	i := start
	for i < end {
		if s[i] == '\'' {
			ni := skipQuote(s, i, end)
			if ni < 0 {
				return -1
			}
			i = ni
			continue
		}
		if s[i] == '{' {
			depth++
		} else if s[i] == '}' {
			depth--
			if depth == 0 {
				return i
			}
		}
		i++
	}
	return -1
}

func skipQuote(s string, i, end int) int {
	if i+1 < end && s[i+1] == '\'' {
		return i + 2
	}
	j := i + 1
	for j < end && s[j] != '\'' {
		j++
	}
	if j >= end {
		return -1
	}
	return j + 1
}

func splitCommas(s string) []string {
	var out []string
	depth, last := 0, 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\'' {
			ni := skipQuote(s, i, len(s))
			if ni < 0 {
				return []string{s}
			}
			i = ni - 1
			continue
		}
		switch c {
		case '{':
			depth++
		case '}':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[last:i])
				last = i + 1
			}
		}
	}
	return append(out, s[last:])
}

func nthTopComma(s string, start, end, n int) int {
	depth, seen := 0, 0
	for i := start; i < end; i++ {
		c := s[i]
		if c == '\'' {
			ni := skipQuote(s, i, end)
			if ni < 0 {
				return start - 1
			}
			i = ni - 1
			continue
		}
		switch c {
		case '{':
			depth++
		case '}':
			depth--
		case ',':
			if depth == 0 {
				seen++
				if seen == n {
					return i
				}
			}
		}
	}
	return start - 1
}

func optionKeys(style string) []string {
	var keys []string
	i := 0
	depth := 0
	for i < len(style) {
		c := style[i]
		if c == '\'' {
			ni := skipQuote(style, i, len(style))
			if ni < 0 {
				return keys
			}
			i = ni
			continue
		}
		switch c {
		case '{':
			depth++
			i++
		case '}':
			depth--
			i++
		default:
			if depth == 0 {
				for i < len(style) && isSpace(style[i]) {
					i++
				}
				start := i
				for i < len(style) && !isSpace(style[i]) && style[i] != '{' {
					i++
				}
				if i > start {
					keys = append(keys, style[start:i])
				}
			} else {
				i++
			}
		}
	}
	return keys
}

func allTags(src string) []string {
	set := map[string]bool{}
	i := 0
	for i < len(src) {
		if src[i] != '<' {
			i++
			continue
		}
		j := i + 1
		if j < len(src) && src[j] == '/' {
			j++
		}
		ns := j
		for j < len(src) && isName(src[j]) {
			j++
		}
		if j == ns {
			i++
			continue
		}
		e := j
		for e < len(src) && src[e] != '>' {
			e++
		}
		if e < len(src) && !(e > j && src[e-1] == '/') {
			set[src[ns:j]] = true
		}
		i = e + 1
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// dedupeParams merges repeated placeholders. Incompatible non-any types are
// marked with a "|" combined type so type drift is detectable within a
// message too.
func dedupeParams(in []Param) []Param {
	seen := map[string]int{}
	var out []Param
	for _, pr := range in {
		if idx, ok := seen[pr.Name]; ok {
			a, b := norm(out[idx].Type), norm(pr.Type)
			if a != b && a != "any" && b != "any" {
				out[idx].Type = a + "|" + b
			} else if a == "any" {
				out[idx].Type = pr.Type
			}
			continue
		}
		seen[pr.Name] = len(out)
		out = append(out, pr)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

func norm(t string) string {
	if t == "" {
		return "any"
	}
	return t
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

func trim(s string) string {
	for len(s) > 0 && isSpace(s[0]) {
		s = s[1:]
	}
	for len(s) > 0 && isSpace(s[len(s)-1]) {
		s = s[:len(s)-1]
	}
	return s
}
