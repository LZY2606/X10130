package catalog

import "testing"

func TestFingerprintIgnoresKeyOrderAndNewlines(t *testing.T) {
	a := []byte("{\n  \"b\": \"line1\\nline2\",\n  \"a\": \"hello {name}\"\n}")
	// CRLF inside JSON strings is encoded as the escapes \r\n; after decoding
	// those become raw CR/LF which must normalize to the same fingerprint.
	b := []byte("{\r\n  \"a\": \"hello {name}\",\r\n  \"b\": \"line1\\r\\nline2\"\r\n}")
	ca, erra := Parse("en", a)
	cb, errb := Parse("en", b)
	if len(erra) != 0 || len(errb) != 0 {
		t.Fatalf("parse errors: %v %v", erra, errb)
	}
	if ca.Fingerprint() != cb.Fingerprint() {
		t.Fatalf("fingerprints differ for reordered/CRLF content:\n%q\n%q", ca.Fingerprint(), cb.Fingerprint())
	}
	// A file with CRLF only between JSON lines must match too.
	c := []byte("{\r\n  \"a\": \"hello {name}\",\r\n  \"b\": \"line1\\nline2\"\r\n}")
	cc, _ := Parse("en", c)
	if cc.Fingerprint() != ca.Fingerprint() {
		t.Fatalf("CR/LF line endings changed fingerprint")
	}
}

func TestFingerprintChangesOnContentChange(t *testing.T) {
	a := []byte(`{"k": "hello"}`)
	b := []byte(`{"k": "hello!"}`)
	ca, _ := Parse("en", a)
	cb, _ := Parse("en", b)
	if ca.Fingerprint() == cb.Fingerprint() {
		t.Fatalf("different content produced same fingerprint")
	}
}

func TestObjectFormWithContext(t *testing.T) {
	raw := []byte(`{"k": {"pattern": "{count, plural, one {x} other {y}}", "context": "count label"}}`)
	c, errs := Parse("en", raw)
	if len(errs) != 0 {
		t.Fatalf("errs=%v", errs)
	}
	e := c.Entries["k"]
	if e.Context != "count label" {
		t.Fatalf("context=%q", e.Context)
	}
	if c.Messages["k"].PlaceholderMap()["count"] == nil {
		t.Fatalf("placeholder missing")
	}
}

func TestParseError(t *testing.T) {
	raw := []byte(`{"k": 123}`)
	_, errs := Parse("en", raw)
	if len(errs) == 0 {
		t.Fatalf("expected parse error for numeric message")
	}
}
