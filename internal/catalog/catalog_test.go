package catalog

import "testing"

func TestFingerprintOrderAndNewlineInvariant(t *testing.T) {
	a := []byte("{\n  \"b\": \"line1\\nline2\",\n  \"a\": \"x\"\n}")
	b := []byte("{\"a\":\"x\",\"b\":\"line1\\r\\nline2\"}")
	ca, err := Decode(a)
	if err != nil {
		t.Fatal(err)
	}
	cb, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if ca.Fingerprint() != cb.Fingerprint() {
		t.Fatalf("fingerprints differ:\n%s\n%s", ca.Fingerprint().SHA256, cb.Fingerprint().SHA256)
	}
	if string(ca.CanonicalJSON()) != string(cb.CanonicalJSON()) {
		t.Fatalf("canonical differs:\n%s\n%s", ca.CanonicalJSON(), cb.CanonicalJSON())
	}
	if ca.Entries[0].Key != "a" {
		t.Fatalf("entries not sorted: %s", ca.Entries[0].Key)
	}
}

func TestShapes(t *testing.T) {
	cases := [][]byte{
		[]byte(`{"k":"v"}`),
		[]byte(`{"messages":{"k":"v"},"contexts":{"k":"ctx"}}`),
		[]byte(`[{"key":"k","message":"v"}]`),
	}
	for i, c := range cases {
		cat, err := Decode(c)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if cat.Entries[0].Key != "k" || cat.Entries[0].Message != "v" {
			t.Fatalf("case %d bad entry %+v", i, cat.Entries[0])
		}
	}
	ctx, _ := Decode([]byte(`{"messages":{"k":"v"},"contexts":{"k":"ctx"}}`))
	if ctx.Entries[0].Context != "ctx" {
		t.Fatalf("context=%q", ctx.Entries[0].Context)
	}
}

func TestInvalidAndDuplicate(t *testing.T) {
	for _, in := range [][]byte{[]byte("not json"), []byte(`{"a":1}`), []byte(`[{"key":"a","message":"1"},{"key":"a","message":"2"}]`), []byte("")} {
		if _, err := Decode(in); err == nil {
			t.Fatalf("expected error for %s", in)
		}
	}
}

func TestCRNormalization(t *testing.T) {
	c, err := Decode([]byte("{\"k\":\"a\\rb\\r\\nc\"}"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Entries[0].Message != "a\nb\nc" {
		t.Fatalf("got %q", c.Entries[0].Message)
	}
}
