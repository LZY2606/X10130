package catalog

import (
	"strings"
	"testing"
)

func TestQuotingRules(t *testing.T) {
	cases := []struct {
		name string
		in   string
		lit  string
		args int
	}{
		{"plain", "hello", "hello", 0},
		{"argument", "hello {name}", "hello ", 1},
		{"quoted brace", "use '{notarg}' end", "use {notarg} end", 0},
		{"doubled apostrophe", "it''s a test", "it's a test", 0},
		{"quote only escapes word", "a 'x{' b {y}", "a x{ b ", 1},
		// Strict ICU: a lone apostrophe starts quoting until the next one, so
		// "don't {x}" quotes the space+brace and does not parse x.
		{"escaped apostrophe and quoted brace", "don''t '{x}'", "don't {x}", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := Parse(c.in)
			var b strings.Builder
			for _, n := range p.Nodes {
				if n.Kind == KindLiteral {
					b.WriteString(n.Text)
				}
			}
			if b.String() != c.lit {
				t.Fatalf("literal = %q, want %q (nodes=%+v)", b.String(), c.lit, p.Nodes)
			}
			count := 0
			for _, n := range p.Nodes {
				if n.Kind == KindArgument {
					count++
				}
			}
			if count != c.args {
				t.Fatalf("arg count = %d, want %d", count, c.args)
			}
		})
	}
}

func TestQuotedPlaceholderNotParsed(t *testing.T) {
	p := Parse("{count, plural, one {# one '{fake}' thing} other {# things}}")
	if len(p.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", p.Errors)
	}
	foundFake := false
	pluralCases := map[string]bool{}
	for _, ph := range p.Placeholders {
		if ph.Name == "fake" {
			foundFake = true
		}
	}
	var walk func([]Node)
	walk = func(ns []Node) {
		for _, n := range ns {
			if n.Kind == KindPlural {
				for _, b := range n.Branches {
					pluralCases[b.Case] = true
				}
			}
			for _, b := range n.Branches {
				walk(b.Children)
			}
		}
	}
	walk(p.Nodes)
	if foundFake {
		t.Fatalf("quoted {fake} was parsed as placeholder")
	}
	if !pluralCases["one"] || !pluralCases["other"] {
		t.Fatalf("missing plural cases: %v", pluralCases)
	}
}

func TestNestedTags(t *testing.T) {
	p := Parse("a <b>bold <i>both</i></b> end")
	if len(p.Errors) != 0 {
		t.Fatalf("balanced tags flagged: %v", p.Errors)
	}
	want := []struct {
		name  string
		open  bool
		close bool
	}{{"b", true, false}, {"i", true, false}, {"i", false, true}, {"b", false, true}}
	if len(p.Tags) != len(want) {
		t.Fatalf("tags=%v", p.Tags)
	}
	for i, w := range want {
		if p.Tags[i].Name != w.name || p.Tags[i].Open != w.open || p.Tags[i].Close != w.close {
			t.Fatalf("tag %d = %+v, want %+v", i, p.Tags[i], w)
		}
	}
}

func TestUnbalancedTags(t *testing.T) {
	for _, in := range []string{"<b>bold", "bold</b>", "<a><b></a></b>"} {
		p := Parse(in)
		ok := false
		for _, e := range p.Errors {
			if strings.Contains(e, "unbalanced") {
				ok = true
			}
		}
		if !ok {
			t.Fatalf("input %q expected unbalanced error, got %v", in, p.Errors)
		}
	}
}

func TestSelfClosingTag(t *testing.T) {
	p := Parse("line<br/>next")
	if len(p.Errors) != 0 {
		t.Fatalf("self-closing tag flagged: %v", p.Errors)
	}
	if len(p.Tags) != 1 || !p.Tags[0].SelfClose {
		t.Fatalf("tags = %+v", p.Tags)
	}
}

func TestNewlineNormalization(t *testing.T) {
	a := map[string]string{"k": "line1\nline2"}
	b := map[string]string{"k": "line1\r\nline2"}
	c := map[string]string{"k": "line1\rline2"}
	fa, fb, fc := ContentFingerprint(a), ContentFingerprint(b), ContentFingerprint(c)
	if fa != fb || fb != fc {
		t.Fatalf("fingerprints differ across newline styles: %s %s %s", fa, fb, fc)
	}
}

func TestKeyOrderIndependence(t *testing.T) {
	a := map[string]string{"a": "1", "b": "2"}
	b := map[string]string{"b": "2", "a": "1"}
	if ContentFingerprint(a) != ContentFingerprint(b) {
		t.Fatalf("fingerprint depends on iteration/order")
	}
}

func TestParseFileShapes(t *testing.T) {
	f1, err := ParseFile("x", []byte(`{"a":"b"}`))
	if err != nil {
		t.Fatal(err)
	}
	if f1.Messages["a"] != "b" {
		t.Fatalf("flat parse: %+v", f1.Messages)
	}
	f2, err := ParseFile("", []byte(`{"language":"y","messages":{"a":"b"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if f2.Language != "y" {
		t.Fatalf("wrapped language = %q", f2.Language)
	}
}
