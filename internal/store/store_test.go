package store

import (
	"bytes"
	"testing"
	"time"
)

func testClock(unix int64) Clock {
	return func() time.Time { return time.Unix(unix, 0) }
}

func openTestStore(t *testing.T, unix int64) *Store {
	t.Helper()
	dir := t.TempDir()
	st, err := Open(dir, testClock(unix))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func enMessages(m map[string]string) []byte {
	return marshalCatalog(m)
}

func mustImport(t *testing.T, st *Store, lang string, raw []byte, baseline bool, op string) *ImportResult {
	t.Helper()
	res, err := st.Import(op, ImportRequest{Language: lang, Raw: raw, AsBaseline: baseline})
	if err != nil {
		t.Fatalf("import %s: %v", lang, err)
	}
	return res
}

func TestImportVersionStableAcrossOrderAndNewlines(t *testing.T) {
	st := openTestStore(t, 1000)
	r1 := mustImport(t, st, "en", []byte(`{"messages":[{"key":"a","text":"hello"},{"key":"b","text":"world"}]}`), true, "imp1")
	r2 := mustImport(t, st, "en", []byte("{\"messages\":[{\"key\":\"b\",\"text\":\"world\\r\\n\"},{\"key\":\"a\",\"text\":\"hello\"}]}"), true, "imp2")
	if r1.Fingerprint != r2.Fingerprint {
		t.Fatalf("fingerprints differ: %s vs %s", r1.Fingerprint, r2.Fingerprint)
	}
	if r2.NewVersion {
		t.Fatal("re-import with changed order/newlines must not create a new version")
	}
	// Both raw files are downloadable byte-identical.
	raw1, _ := st.RawBytes(r1.RawSHA)
	raw2, _ := st.RawBytes(r2.RawSHA)
	if !bytes.Equal(raw1, r1Must()) || !bytes.Contains(raw2, []byte("\\r\\n")) {
		t.Fatal("raw files must be retrievable byte-identical per upload")
	}
	if r1.RawSHA == r2.RawSHA {
		t.Fatal("different byte uploads must be stored separately even if version matches")
	}
}

func r1Must() []byte {
	return []byte(`{"messages":[{"key":"a","text":"hello"},{"key":"b","text":"world"}]}`)
}

func TestMissingExtraAndPlaceholderIssues(t *testing.T) {
	st := openTestStore(t, 1000)
	mustImport(t, st, "en", enMessages(map[string]string{
		"greet": "Hello {name}", "count": "{n, plural, one {#} other {#s}}",
	}), true, "en")
	mustImport(t, st, "ja", enMessages(map[string]string{
		"count": "{n, plural, other {#件}}",
	}), false, "ja")
	view, err := st.View(0)
	if err != nil {
		t.Fatal(err)
	}
	codes := map[string]bool{}
	for _, iv := range view.Issues {
		codes[iv.Issue.Code+"|"+iv.Issue.Key] = true
	}
	if !codes["missing-key|greet"] {
		t.Errorf("expected missing greet, issues=%v", codes)
	}
	if !codes["plural-category-missing|count"] {
		t.Errorf("expected missing plural category one, issues=%v", codes)
	}
}

func TestPlaceholderTypeDrift(t *testing.T) {
	st := openTestStore(t, 1000)
	mustImport(t, st, "en", enMessages(map[string]string{"a": "{n, number}"}), true, "e")
	mustImport(t, st, "ja", enMessages(map[string]string{"a": "{n, date}"}), false, "j")
	view, _ := st.View(0)
	found := false
	for _, iv := range view.Issues {
		if iv.Issue.Code == CodePlaceholderType {
			found = true
		}
	}
	if !found {
		t.Fatal("expected placeholder type drift")
	}
}

func TestTagImbalanceDetected(t *testing.T) {
	st := openTestStore(t, 1000)
	mustImport(t, st, "en", enMessages(map[string]string{"a": "<b>bold</b>"}), true, "e")
	mustImport(t, st, "ja", enMessages(map[string]string{"a": "<b>bold"}), false, "j")
	view, _ := st.View(0)
	found := false
	for _, iv := range view.Issues {
		if iv.Issue.Code == CodeTagUnbalanced {
			found = true
		}
	}
	if !found {
		t.Fatal("expected tag unbalanced issue")
	}
}
