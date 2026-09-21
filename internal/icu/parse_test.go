package icu

import (
	"strings"
	"testing"
)

func TestQuoting(t *testing.T) {
	// '{' is a literal brace; '' is a literal apostrophe.
	m := Parse("It won''t break, use '{name}' literally")
	if len(m.ParseErrors) != 0 {
		t.Fatalf("unexpected errors: %v", m.ParseErrors)
	}
	if got := placeholderNames(m); len(got) != 0 {
		t.Fatalf("quoted brace must not be an argument, got %v", got)
	}
	all := allText(m.Nodes)
	if !strings.Contains(all, "won't break") {
		t.Fatalf("'' must render as apostrophe, got %q", all)
	}
	if !strings.Contains(all, "{name}") {
		t.Fatalf("quoted '{name}' must stay literal text, got %q", all)
	}
}

func TestArgumentAndType(t *testing.T) {
	m := Parse("Hi {name}, you have {count, number} messages")
	names := placeholderNames(m)
	if len(names) != 2 {
		t.Fatalf("want 2 placeholders, got %v", names)
	}
	p := m.PlaceholderMap()
	if p["count"].Type != "number" {
		t.Fatalf("count type=%q want number", p["count"].Type)
	}
}

func TestPluralCategories(t *testing.T) {
	m := Parse("{count, plural, one {one item} other {{count} items}}")
	p := m.PlaceholderMap()["count"]
	if p.Type != "plural" {
		t.Fatalf("type=%q", p.Type)
	}
	keys := p.CategoryKeys()
	if len(keys) != 2 || keys[0] != "one" || keys[1] != "other" {
		t.Fatalf("categories=%v", keys)
	}
	// Nested {count} inside a branch must be collected too.
	if _, ok := m.PlaceholderMap()["count"]; !ok {
		t.Fatalf("nested count placeholder missing")
	}
}

func TestNestedTagsBalanced(t *testing.T) {
	m := Parse("Click <a><b>here</b></a> with {name}")
	if m.HasUnbalancedTags() {
		t.Fatalf("balanced tags flagged: %v", m.ParseErrors)
	}
	if len(m.Tags) != 2 {
		t.Fatalf("tags=%v", m.Tags)
	}
}

func TestUnbalancedTags(t *testing.T) {
	for _, src := range []string{
		"Hello <b>world",
		"Hello </b>world",
		"<a>x<b>y</a>z",
	} {
		m := Parse(src)
		if !m.HasUnbalancedTags() {
			t.Fatalf("src=%q should be unbalanced, errs=%v", src, m.ParseErrors)
		}
	}
}

func TestStyleWithBracesAndQuotes(t *testing.T) {
	m := Parse("{d, date, :: h 'o''clock'}")
	if len(m.ParseErrors) != 0 {
		t.Fatalf("parse errors: %v", m.ParseErrors)
	}
	p := m.PlaceholderMap()["d"]
	if p == nil || p.Type != "date" {
		t.Fatalf("missing date placeholder: %#v", p)
	}
	if p.Style != ":: h 'o''clock'" {
		t.Fatalf("style=%q", p.Style)
	}
}

func placeholderNames(m *Message) []string {
	out := make([]string, 0, len(m.Placeholders))
	for _, p := range m.Placeholders {
		out = append(out, p.Name)
	}
	return out
}

func containsText(nodes []*Node, want string) bool {
	for _, n := range nodes {
		if n.Kind == "text" && n.Text == want {
			return true
		}
		if len(n.Children) > 0 && containsText(n.Children, want) {
			return true
		}
		if n.Kind == "arg" && n.Arg != nil {
			for _, sub := range n.Arg.Cases {
				if containsText(sub.Nodes, want) {
					return true
				}
			}
		}
	}
	return false
}

func allText(nodes []*Node) string {
	var sb strings.Builder
	var walk func([]*Node)
	walk = func(ns []*Node) {
		for _, n := range ns {
			switch n.Kind {
			case "text":
				sb.WriteString(n.Text)
			case "tag":
				walk(n.Children)
			case "arg":
				if n.Arg != nil {
					for _, sub := range n.Arg.Cases {
						walk(sub.Nodes)
					}
				}
			}
		}
	}
	walk(nodes)
	return sb.String()
}
