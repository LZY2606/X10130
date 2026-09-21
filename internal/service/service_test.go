package service

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"messagecatalog/internal/catalog"
	"messagecatalog/internal/store"
)

type testEnv struct {
	svc    *Service
	dir    string
	server *httptest.Server
}

func newEnv(t *testing.T, start time.Time) *testEnv {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc := New(st)
	svc.Clock = NewControlledClock(start)
	// minimal assets for Handler usage in HTTP tests
	return &testEnv{svc: svc, dir: dir}
}

func mustJSON(t *testing.T, m map[string]string) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (e *testEnv) imp(t *testing.T, op, lang, role string, m map[string]string) ImportResult {
	t.Helper()
	res, _, err := e.svc.Import(op, ImportRequest{
		Language: lang, Role: role, Filename: lang + ".json", Content: mustJSON(t, m),
	})
	if err != nil {
		t.Fatalf("import %s: %v", lang, err)
	}
	return res
}

func demoBase() map[string]string {
	return map[string]string{
		"greeting": "你好 {name}",
		"items":    "{n, plural, one {# 项} other {# 项}}",
		"rich":     "<b>{name}</b>",
	}
}

func TestImportFingerprintStableAcrossOrderAndNewlines(t *testing.T) {
	e := newEnv(t, time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC))
	r1 := e.imp(t, "op-1", "zh", roleBaseline, map[string]string{"a": "x", "b": "y\nz"})
	r2 := e.imp(t, "op-2", "zh", roleBaseline, map[string]string{"b": "y\r\nz", "a": "x"})
	if r1.VersionID != r2.VersionID {
		t.Fatalf("content version changed: %s vs %s", r1.VersionID, r2.VersionID)
	}
	if r2.NewVersion {
		t.Fatalf("re-import of same content created a new version")
	}
}

func TestEveryUploadRetrievableByteForByte(t *testing.T) {
	e := newEnv(t, time.Now().UTC())
	content := []byte("{\n  \"b\": \"y\\r\\nz\",\n  \"a\": \"x\"\n}")
	r1, _, err := e.svc.Import("u1", ImportRequest{Language: "zh", Role: roleBaseline, Filename: "crlf.json", Content: content})
	if err != nil {
		t.Fatal(err)
	}
	// Same normalized content, different raw bytes/filename.
	content2 := []byte("{\"a\":\"x\",\"b\":\"y\\nz\"}")
	r2, _, err := e.svc.Import("u2", ImportRequest{Language: "zh", Role: roleBaseline, Filename: "lf.json", Content: content2})
	if err != nil {
		t.Fatal(err)
	}
	if r1.VersionID != r2.VersionID {
		t.Fatalf("versions should be equal for same content")
	}
	b1, fn1, err := e.svc.RawUpload(r1.UploadID)
	if err != nil {
		t.Fatal(err)
	}
	b2, fn2, _ := e.svc.RawUpload(r2.UploadID)
	if !bytes.Equal(b1, content) {
		t.Fatalf("raw 1 not byte-identical (fn=%s)", fn1)
	}
	if !bytes.Equal(b2, content2) {
		t.Fatalf("raw 2 not byte-identical (fn=%s)", fn2)
	}
	if fn1 != "crlf.json" || fn2 != "lf.json" {
		t.Fatalf("filenames lost: %s %s", fn1, fn2)
	}
}

func TestPersistAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	func() {
		st, _ := store.Open(dir)
		svc := New(st)
		svc.Clock = NewControlledClock(start)
		if _, _, err := svc.Import("p1", ImportRequest{Language: "zh", Role: roleBaseline,
			Filename: "zh.json", Content: mustJSON(t, demoBase())}); err != nil {
			t.Fatal(err)
		}
	}()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc := New(st)
	svc.Clock = NewControlledClock(start)
	langs := svc.Languages()
	if len(langs) != 1 || langs[0].Language != "zh" || langs[0].MessageCount != 3 {
		t.Fatalf("state did not survive restart: %+v", langs)
	}
}

func TestIdempotentReplayNoNewVersion(t *testing.T) {
	e := newEnv(t, time.Now().UTC())
	req := ImportRequest{Language: "zh", Role: roleBaseline, Filename: "zh.json", Content: mustJSON(t, demoBase())}
	r1, rep1, err := e.svc.Import("same-op", req)
	if err != nil || rep1 {
		t.Fatalf("first call: %v replay=%v", err, rep1)
	}
	r2, rep2, err := e.svc.Import("same-op", req)
	if err != nil || !rep2 {
		t.Fatalf("retry: %v replay=%v", err, rep2)
	}
	if r1.VersionID != r2.VersionID || r1.UploadID != r2.UploadID {
		t.Fatalf("replay created new state: %+v vs %+v", r1, r2)
	}
	uploads := e.svc.Uploads()
	if len(uploads) != 1 {
		t.Fatalf("expected one upload record, got %d", len(uploads))
	}
}

func TestIdempotentSameKeyDifferentContentRejected(t *testing.T) {
	e := newEnv(t, time.Now().UTC())
	_, _, err := e.svc.Import("k", ImportRequest{Language: "zh", Role: roleBaseline, Filename: "a.json",
		Content: mustJSON(t, map[string]string{"a": "1"})})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = e.svc.Import("k", ImportRequest{Language: "zh", Role: roleBaseline, Filename: "a.json",
		Content: mustJSON(t, map[string]string{"a": "2"})})
	if err == nil || !isConflict(err) {
		t.Fatalf("expected conflict, got %v", err)
	}
}

func TestRenameCycleAndMergeRejected(t *testing.T) {
	e := newEnv(t, time.Now().UTC())
	base := map[string]string{"newA": "x", "newB": "y"}
	e.imp(t, "b", "zh", roleBaseline, base)
	// Simulate an old baseline version by importing previous content first is
	// needed for candidates; direct confirm should still validate structure.
	if _, _, err := e.svc.ConfirmRename("r1", ConfirmRenameRequest{From: "oldA", To: "newA"}); err != nil {
		t.Fatal(err)
	}
	// cycle oldB->newA->... not directly possible; construct via two edges where
	// newA is current and also "from": chain to itself.
	if _, _, err := e.svc.ConfirmRename("r2", ConfirmRenameRequest{From: "newA", To: "newB"}); err != nil {
		t.Fatal(err)
	}
	// now edge newB -> newA would cycle (newA->newB->newA)
	_, _, err := e.svc.ConfirmRename("r3", ConfirmRenameRequest{From: "newB", To: "newA"})
	if err == nil || !isConflict(err) {
		t.Fatalf("expected cycle conflict, got %v", err)
	}
	// two old keys into one new key
	_, _, err = e.svc.ConfirmRename("r4", ConfirmRenameRequest{From: "otherOld", To: "newB"})
	if err == nil || !isConflict(err) {
		t.Fatalf("expected merge conflict, got %v", err)
	}
}

func isConflict(err error) bool {
	if err == nil {
		return false
	}
	var ce *ConflictError
	return asConflict(err, &ce) || errIs(err, store.ErrIdempotency)
}

func asConflict(err error, ce **ConflictError) bool {
	for err != nil {
		if c, ok := err.(*ConflictError); ok {
			*ce = c
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func errIs(err, target error) bool {
	for err != nil {
		if err.Error() == target.Error() {
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

var _ = catalog.ParserVersion
var _ = httptest.NewServer
var _ = multipart.NewWriter
var _ = http.StatusOK
var _ = os.MkdirAll
var _ = filepath.Join
