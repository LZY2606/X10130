package icu

import "testing"

func TestQuotingRules(t *testing.T) {
	cases := []struct {
		in        string
		wantText  string
		wantArgs  int
		wantLit   string
	}{
		{"It''s fine", "It's fine", 0, ""},
		{"'{name}' stays literal", "{name} stays literal", 0, ""},
		{"use '{''}'", "use {'}", 0, ""},
		{"hi {name}", "hi {name}", 1, ""},
		{"a {count, plural, one {# one} other {# many}}", "a {count}", 1, ""},
		{"unpaired ' apostrophe", "unpaired ' apostrophe", 0, ""},
	}
	for _, c := range cases {
		m, err := Parse(c.in)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		got := tokensText(m.Tokens)
		if c.wantText != "" && got != c.wantText {
			t.Errorf("%q: text=%q want %q", c.in, got, c.wantText)
		}
		if c.wantArgs == 1 && len(m.Placeholders) != 1 {
			t.Errorf("%q: want 1 placeholder, got %d", c.in, len(m.Placeholders))
		}
	}
}

func TestPluralBranchesAndNestedArgs(t *testing.T) {
	m, err := Parse("{count, plural, one {one {name}} other {{count} items}}")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Placeholders) != 1 {
		t.Fatalf("want 1 arg, got %d", len(m.Placeholders))
	}
	p := m.Placeholders[0]
	if p.Type != "plural" {
		t.Fatalf("type=%s", p.Type)
	}
	got := map[string]bool{}
	for _, b := range p.Branches {
		got[b.Key] = true
	}
	if !got["one"] || !got["other"] {
		t.Fatalf("branches=%v", got)
	}
	foundNested := false
	for _, n := range p.NestedArgs {
		if n == "name" {
			foundNested = true
		}
	}
	if !foundNested {
		t.Fatalf("nested name not collected: %v", p.NestedArgs)
	}
}

func TestPluralRequiresOther(t *testing.T) {
	if _, err := Parse("{n, plural, one {x}}"); err == nil {
		t.Fatal("expected missing other error")
	}
	if _, err := Parse("{n, select, male {m}}"); err == nil {
		t.Fatal("expected select missing other error")
	}
}

func TestNestedTags(t *testing.T) {
	m, err := Parse("<a><b>{x}</b></a>")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Unbalanced) != 0 {
		t.Fatalf("want balanced, got %v", m.Unbalanced)
	}
	if len(m.TagNames) != 2 {
		t.Fatalf("tags=%v", m.TagNames)
	}
	m2, _ := Parse("<a><b>{x}</a>")
	if len(m2.Unbalanced) == 0 {
		t.Fatal("expected imbalance")
	}
	m3, _ := Parse("plain '<b>' literal")
	if len(m3.TagNames) != 0 {
		t.Fatalf("quoted tag should not count, got %v", m3.TagNames)
	}
}

func TestTypeDrift(t *testing.T) {
	a, _ := Parse("{count, number}")
	b, _ := Parse("{count, plural, other {x}}")
	if a.Placeholders[0].Type == b.Placeholders[0].Type {
		t.Fatal("types should differ")
	}
}

func TestSimpleStyleAndBraces(t *testing.T) {
	m, err := Parse("{d, date, yyyy-MM-dd}")
	if err != nil {
		t.Fatal(err)
	}
	if m.Placeholders[0].Style != "yyyy-MM-dd" {
		t.Fatalf("style=%q", m.Placeholders[0].Style)
	}
	if _, err := Parse("{name"); err == nil {
		t.Fatal("expected unclosed brace")
	}
}
