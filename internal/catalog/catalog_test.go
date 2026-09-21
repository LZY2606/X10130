package catalog

import "testing"

func TestParseImport(t *testing.T) {
	msgs, err := ParseImport([]byte(`{"messages":[{"key":"a","text":"hello"},{"key":"b","text":"{n}"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("msgs=%d", len(msgs))
	}
	msgs2, err := ParseImport([]byte(`{"b":"{n}","a":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	if Fingerprint(msgs) != Fingerprint(msgs2) {
		t.Fatal("bare map and messages array should fingerprint equal")
	}
	if _, err := ParseImport([]byte(`{"messages":[{"key":"a","text":"x"},{"key":"a","text":"y"}]}`)); err == nil {
		t.Fatal("duplicate keys should be rejected")
	}
}

func TestNewlineNormalization(t *testing.T) {
	a := []Message{{Key: "a", Text: "line1\nline2"}}
	b := []Message{{Key: "a", Text: "line1\r\nline2\n"}}
	c := []Message{{Key: "a", Text: "line1\nline2\n\n"}} // extra blank line differs
	if Fingerprint(a) != Fingerprint(b) {
		t.Fatal("CRLF and single trailing newline should normalize to same fingerprint")
	}
	if Fingerprint(a) == Fingerprint(c) {
		t.Fatal("extra blank line must change fingerprint")
	}
}
