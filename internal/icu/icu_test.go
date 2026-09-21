package icu

import (
	"reflect"
	"testing"
)

func TestQuoteRules(t *testing.T) {
	cases := []struct {
		src     string
		params  []string
		wantErr bool
	}{
		{"It''s a {thing}", []string{"thing"}, false},          // doubled quote
		{"{count, plural, other {# item}}", []string{"count"}, false},
		{"a '{not}' placeholder", nil, false},                   // quoted braces literal
		{"unterminated '{x}", nil, true},
		{"use '{' and '}' literally", nil, false},
	}
	for _, c := range cases {
		ast := Parse(c.src)
		if c.wantErr && ast.Error == "" {
			t.Errorf("Parse(%q) expected error, got none: %+v", c.src, ast)
		}
		if !c.wantErr && ast.Error != "" {
			t.Errorf("Parse(%q) unexpected error %q", c.src, ast.Error)
		}
		var got []string
		for _, p := range ast.Params {
			got = append(got, p.Name)
		}
		if !reflect.DeepEqual(got, c.params) {
			t.Errorf("Parse(%q) params=%v want %v", c.src, got, c.params)
		}
	}
}

func TestParamTypes(t *testing.T) {
	ast := Parse("{count, number} items for {user}")
	m := map[string]string{}
	for _, p := range ast.Params {
		m[p.Name] = p.Type
	}
	if m["count"] != "number" || m["user"] != "any" {
		t.Fatalf("types=%v", m)
	}
}

func TestPluralCategories(t *testing.T) {
	ast := Parse("{n, plural, one {# item} other {# items}}")
	if len(ast.Plurals) != 1 {
		t.Fatalf("plurals=%+v", ast.Plurals)
	}
	want := map[string]bool{"one": true, "other": true}
	for _, c := range ast.Plurals[0].Categories {
		if !want[c] {
			t.Errorf("category %s", c)
		}
	}
}

func TestNestedTags(t *testing.T) {
	if ast := Parse("<b>hi</b> <a>x</a>"); len(ast.Unbalanced) != 0 {
		t.Errorf("balanced tags flagged: %+v", ast.Unbalanced)
	}
	if ast := Parse("<b>hi"); len(ast.Unbalanced) != 1 {
		t.Errorf("unclosed tag: %+v", ast.Unbalanced)
	}
	if ast := Parse("</b>hi"); ast.Error == "" {
		t.Errorf("unmatched close should error: %+v", ast)
	}
	if ast := Parse("before {n, plural, other {<b>#</b>}} after"); len(ast.Unbalanced) != 0 {
		t.Errorf("tags inside plural: %+v", ast.Unbalanced)
	}
	if ast := Parse("{n, plural, other {<b>#}}"); len(ast.Unbalanced) != 1 {
		t.Errorf("unclosed tag inside plural: %+v", ast.Unbalanced)
	}
}
