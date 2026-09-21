package catalog

import "testing"

func file(lang string, m map[string]string) *File {
	f := &File{Language: lang, Messages: m}
	return f
}

func TestValidateMissingAndExtra(t *testing.T) {
	base := file("zh", map[string]string{"a": "x", "b": "y"})
	targ := file("ja", map[string]string{"a": "x", "c": "z"})
	issues := ValidateTarget(base, targ, Edges{})
	types := map[string]int{}
	for _, i := range issues {
		types[i.Type+":"+i.Key]++
	}
	if types["missing_key:b"] != 1 {
		t.Fatalf("expected missing b: %+v", issues)
	}
	if types["extra_key:c"] != 1 {
		t.Fatalf("expected extra c: %+v", issues)
	}
}

func TestValidateRenameMapping(t *testing.T) {
	base := file("zh", map[string]string{"new": "hi {name}"})
	targ := file("ja", map[string]string{"old": "hi {name}"})
	issues := ValidateTarget(base, targ, Edges{"old": "new"})
	for _, i := range issues {
		if i.Type == "missing_key" || i.Type == "extra_key" {
			t.Fatalf("rename mapping not honored: %+v", issues)
		}
	}
}

func TestPlaceholderTypeDrift(t *testing.T) {
	base := file("zh", map[string]string{"a": "{count, number}"})
	targ := file("ja", map[string]string{"a": "{count, date}"})
	issues := ValidateTarget(base, targ, Edges{})
	found := false
	for _, i := range issues {
		if i.Type == IssuePlaceholderType && i.Name == "count" && i.Expected == "number" && i.Actual == "date" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected type drift issue: %+v", issues)
	}
}

func TestPluralCategories(t *testing.T) {
	base := file("zh", map[string]string{"a": "{n, plural, one {1} other {#}}"})
	targ := file("ja", map[string]string{"a": "{n, plural, other {#}}"})
	issues := ValidateTarget(base, targ, Edges{})
	found := false
	for _, i := range issues {
		if i.Type == IssuePluralCategory && i.Expected == "one" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected missing one-category: %+v", issues)
	}
}

func TestTagSetMismatch(t *testing.T) {
	base := file("zh", map[string]string{"a": "<b>{x}</b>"})
	targ := file("ja", map[string]string{"a": "<i>{x}</i>"})
	issues := ValidateTarget(base, targ, Edges{})
	var names []string
	for _, i := range issues {
		if i.Type == IssueTagMismatch {
			names = append(names, i.Name)
		}
	}
	if len(names) != 2 {
		t.Fatalf("expected b/i mismatch, got %+v", issues)
	}
}

func TestCycleDetection(t *testing.T) {
	e := Edges{"a": "b", "b": "c"}
	if _, cyc := e.ResolveChain("a"); cyc {
		t.Fatal("no cycle expected")
	}
	e2 := Edges{"a": "b", "b": "a"}
	if _, cyc := e2.ResolveChain("a"); !cyc {
		t.Fatal("cycle expected")
	}
}

func TestParseErrorReported(t *testing.T) {
	base := file("zh", map[string]string{"a": "hello {name}"})
	targ := file("ja", map[string]string{"a": "hello {name"})
	issues := ValidateTarget(base, targ, Edges{})
	found := false
	for _, i := range issues {
		if i.Type == IssueParseError {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected parse_error issue: %+v", issues)
	}
}
