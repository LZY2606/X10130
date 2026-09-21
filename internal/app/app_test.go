package app

import (
	"path/filepath"
	"testing"
	"time"

	"msgcheck/internal/store"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time             { return c.t }
func (c *fakeClock) Advance(d time.Duration)    { c.t = c.t.Add(d) }

func newTestApp(t *testing.T) (*App, *fakeClock) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	st := store.New(dir)
	a, _, err := New(st)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	clk := &fakeClock{t: time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)}
	a.SetClock(clk)
	return a, clk
}

func mustImport(t *testing.T, a *App, role, lang string, raw string) *ImportResult {
	t.Helper()
	res, err := a.Import(role, lang, lang+".json", []byte(raw), "", "")
	if err != nil {
		t.Fatalf("import %s/%s: %v", role, lang, err)
	}
	return res
}

const baseEn = `{
  "greeting": "Hello {name}!",
  "items": "{count, plural, one {one item} other {{count} items}}",
  "link": "Open <a>{label}</a>",
  "quote": "It won't fail"
}`

const targetJaGood = `{
  "greeting": "こんにちは {name}！",
  "items": "{count, plural, one {1件} other {{count}件}}",
  "link": "<a>{label}を開く</a>",
  "quote": "失敗しません"
}`

func TestImportVersionStabilityAndRawDownload(t *testing.T) {
	a, _ := newTestApp(t)
	r1 := mustImport(t, a, "baseline", "en", "{\n  \"a\": \"x\",\n  \"b\": \"y\"\n}\n")
	// Different key order + CRLF style => same content version.
	r2 := mustImport(t, a, "baseline", "en", "{\r\n  \"b\": \"y\",\r\n  \"a\": \"x\"\r\n}\r\n")
	if r1.Version != r2.Version {
		t.Fatalf("expected same version, %s vs %s", r1.Version, r2.Version)
	}
	if r2.NewVersion {
		t.Fatalf("reorder/CRLF must not create a new version")
	}
	// Both raw uploads downloadable byte-identical.
	ups := a.Uploads().Uploads
	if len(ups) != 2 {
		t.Fatalf("uploads=%d", len(ups))
	}
	b1, _, _, err := a.RawUpload(ups[0].ID)
	if err != nil || string(b1) != "{\r\n  \"b\": \"y\",\r\n  \"a\": \"x\"\r\n}\r\n" {
		t.Fatalf("raw download mismatch: %q err=%v", string(b1), err)
	}
}

func TestValidationIssues(t *testing.T) {
	a, _ := newTestApp(t)
	mustImport(t, a, "baseline", "en", baseEn)
	// ja is missing "quote", has extra "ja.only"; items missing "one".
	ja := `{
	  "greeting": "こんにちは {name}！",
	  "items": "{count, plural, other {{count}件}}",
	  "link": "<a>{label}を開く</a>",
	  "ja.only": "余分",
	  "broken": "<b>閉じ忘れ"
	}`
	mustImport(t, a, "target", "ja", ja)
	rep, err := a.Issues("ja")
	if err != nil {
		t.Fatal(err)
	}
	codes := map[string]int{}
	for _, iv := range rep.Issues {
		codes[iv.Code]++
	}
	if codes["missing_key"] == 0 {
		t.Errorf("expected missing_key, got %v", codes)
	}
	if codes["extra_key"] != 1 {
		t.Errorf("expected 1 extra_key, got %d", codes["extra_key"])
	}
	if codes["plural_categories"] != 1 {
		t.Errorf("expected 1 plural issue, got %d", codes["plural_categories"])
	}
	if codes["tags_unbalanced"] == 0 {
		t.Errorf("expected tags_unbalanced, got %v", codes)
	}
}

func TestTypeDriftAndPlaceholderMismatch(t *testing.T) {
	a, _ := newTestApp(t)
	mustImport(t, a, "baseline", "en", `{"g": "{name}", "n": "{count, number}"}`)
	mustImport(t, a, "target", "fr", `{"g": "Bonjour", "n": "{count, date}"}`)
	rep, err := a.Issues("fr")
	if err != nil {
		t.Fatal(err)
	}
	var drift, mismatch bool
	for _, iv := range rep.Issues {
		if iv.Code == "type_drift" && iv.Placeholder == "count" {
			drift = true
		}
		if iv.Code == "placeholder_mismatch" && iv.Key == "g" {
			mismatch = true
		}
	}
	if !drift {
		t.Errorf("type drift not detected: %+v", rep.Issues)
	}
	if !mismatch {
		t.Errorf("placeholder mismatch not detected")
	}
}
