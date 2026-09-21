package validate

import (
	"testing"
	"time"

	"catalogcheck/internal/catalog"
)

func mustCat(t *testing.T, s string) *catalog.Catalog {
	t.Helper()
	c, err := catalog.Decode([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func codes(r *ResultData) map[string]int {
	m := map[string]int{}
	for _, i := range r.Issues {
		m[i.Code]++
	}
	return m
}

func TestMissingExtraAndType(t *testing.T) {
	base := mustCat(t, `{"a":"{n, number}","b":"x"}`)
	tar := mustCat(t, `{"a":"{n, plural, other {x}}","c":"y"}`)
	r := Validate("de", "v1", "v2", base, tar, nil, "p1", time.Now())
	got := codes(r)
	if got["missing_key"] != 1 || got["extra_key"] != 1 || got["placeholder_type"] != 1 {
		t.Fatalf("codes=%v", got)
	}
}

func TestPluralCategoryMissing(t *testing.T) {
	base := mustCat(t, `{"a":"{n, plural, one {#} other {#}}"}`)
	tar := mustCat(t, `{"a":"{n, plural, other {#}}"}`)
	r := Validate("de", "v1", "v2", base, tar, nil, "p1", time.Now())
	if codes(r)["plural_missing"] != 1 {
		t.Fatalf("codes=%v", codes(r))
	}
}

func TestTagImbalanceAndSet(t *testing.T) {
	base := mustCat(t, `{"a":"<b>{x}</b>","c":"<i>z</i>"}`)
	tar := mustCat(t, `{"a":"<b>{x}","c":"<i>z</i>"}`)
	r := Validate("de", "v1", "v2", base, tar, nil, "p1", time.Now())
	got := codes(r)
	if got["tag_imbalance"] == 0 {
		t.Fatalf("expected imbalance, %v", got)
	}
	tar2 := mustCat(t, `{"a":"<i>{x}</i>","c":"<i>z</i>"}`)
	r2 := Validate("de", "v1", "v2", base, tar2, nil, "p1", time.Now())
	if codes(r2)["tag_set"] == 0 {
		t.Fatalf("expected tag set mismatch, %v", codes(r2))
	}
}

func TestRenameEdgeMatch(t *testing.T) {
	base := mustCat(t, `{"new":"Hello {name}!"}`)
	tar := mustCat(t, `{"old":"Hallo {name}!"}`)
	edges := []MappingEdge{{FromVersionID: "v0", FromKey: "old", ToVersionID: "v1", ToKey: "new"}}
	r := Validate("de", "v1", "v2", base, tar, edges, "p1", time.Now())
	var match *Match
	for i := range r.Matches {
		if r.Matches[i].BaseKey == "new" {
			match = &r.Matches[i]
		}
	}
	if match == nil || match.TargetKey != "old" || !match.Renamed {
		t.Fatalf("match=%v", r.Matches)
	}
	if codes(r)["missing_key"] != 0 {
		t.Fatalf("unexpected missing %v", codes(r))
	}
}

func TestDeterministicIssuesAndID(t *testing.T) {
	base := mustCat(t, `{"b":"x","a":"{n,number}"}`)
	tar := mustCat(t, `{"a":"{n,date}","b":"x"}`)
	r1 := Validate("de", "v1", "v2", base, tar, nil, "p1", time.Now())
	r2 := Validate("de", "v1", "v2", base, tar, nil, "p1", time.Now())
	if r1.ID != r2.ID {
		t.Fatalf("ids differ %s %s", r1.ID, r2.ID)
	}
	if r1.Issues[0].Fingerprint == "" {
		t.Fatal("empty fingerprint")
	}
}
