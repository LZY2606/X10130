package catalog

import "strings"

// scanTags extracts rich-text XML-like tags from literal text.
// Supported shapes: <a>, </a>, <b x="y">, <br/>.
func scanTags(text string) []TagUse {
	var tags []TagUse
	i := 0
	for i < len(text) {
		c := text[i]
		if c != '<' {
			i++
			continue
		}
		end := strings.IndexByte(text[i:], '>')
		if end < 0 {
			break
		}
		raw := text[i+1 : i+end]
		i += end + 1
		name, open, closeTag, selfClose := classifyTag(raw)
		if name == "" {
			continue
		}
		tags = append(tags, TagUse{Name: name, Open: open, Close: closeTag, SelfClose: selfClose})
	}
	return tags
}

func classifyTag(raw string) (name string, open, closeTag, selfClose bool) {
	t := strings.TrimSpace(raw)
	if t == "" || strings.ContainsAny(t, "<>") {
		return "", false, false, false
	}
	if strings.HasPrefix(t, "!") || strings.HasPrefix(t, "?") {
		return "", false, false, false
	}
	if strings.HasPrefix(t, "/") {
		n := strings.TrimSpace(t[1:])
		if !validName(n) {
			return "", false, false, false
		}
		return n, false, true, false
	}
	selfClose = strings.HasSuffix(t, "/")
	if selfClose {
		t = strings.TrimSpace(t[:len(t)-1])
	}
	// split name from attributes
	n := t
	if sp := strings.IndexAny(t, " \t"); sp >= 0 {
		n = t[:sp]
	}
	n = strings.TrimSpace(n)
	if !validName(n) {
		return "", false, false, false
	}
	return n, true, false, selfClose
}

func validName(n string) bool {
	if n == "" {
		return false
	}
	for i := 0; i < len(n); i++ {
		c := n[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == ':' || c == '.' {
			continue
		}
		return false
	}
	return true
}
