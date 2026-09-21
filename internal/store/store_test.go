package store

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := base
	s.now = func() time.Time { return clock }
	return s
}

func importMust(t *testing.T, s *Store, role, lang, content, op string) *ImportOutcome {
	t.Helper()
	out, _, err := s.Import(ImportRequest{Role: role, Language: lang, Filename: lang + ".json", Content: []byte(content), OperationID: op})
	if err != nil {
		t.Fatalf("import %s: %v", lang, err)
	}
	return out
}

const baseV1 = `{"greeting":"Hello {name}!","items":"{count, plural, one {# item} other {# items}}"}`
const deV1 = `{"greeting":"Hallo {name}!","items":"{count, plural, other {# Artikel}}"}`

func TestImportVersionDedupAndRawBytes(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	o1, _, err := s.Import(ImportRequest{Role: "baseline", Language: "en", Filename: "a.json", Content: []byte("{\n  \"greeting\": \"Hi {name}\"\n}")})
	if err != nil {
		t.Fatal(err)
	}
	raw2 := []byte("{\r\n\t\"greeting\": \"Hi {name}\"\r\n}")
	o2, _, err := s.Import(ImportRequest{Role: "baseline", Language: "en", Filename: "b.json", Content: raw2})
	if err != nil {
		t.Fatal(err)
	}
	if o1.VersionID != o2.VersionID || o2.VersionCreated {
		t.Fatalf("expected same version, created=%v ids %s %s", o2.VersionCreated, o1.VersionID, o2.VersionID)
	}
	got, up, err := s.RawUpload(o2.UploadID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw2) {
		t.Fatalf("raw bytes mismatch")
	}
	if up.Size != len(raw2) {
		t.Fatalf("size %d", up.Size)
	}
}

func TestIdempotentImportReplayAndMismatch(t *testing.T) {
	s := openTestStore(t)
	o1, rep1, _ := s.Import(ImportRequest{Role: "baseline", Language: "en", Filename: "en.json", Content: []byte(baseV1), OperationID: "op1"})
	if rep1 != nil {
		t.Fatal("first call should not replay")
	}
	o2, rep2, _ := s.Import(ImportRequest{Role: "baseline", Language: "en", Filename: "en.json", Content: []byte(baseV1), OperationID: "op1"})
	if rep2 == nil || o2.VersionID != o1.VersionID {
		t.Fatal("expected equivalent replay")
	}
	_, _, err := s.Import(ImportRequest{Role: "baseline", Language: "en", Filename: "en.json", Content: []byte(`{"other":"x"}`), OperationID: "op1"})
	if err == nil || !strings.Contains(err.Error(), "idempotency") {
		t.Fatalf("want idempotency conflict, got %v", err)
	}
}

func TestRestartPersistsEverything(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	bv := importMust(t, s, "baseline", "en", baseV1, "b")
	importMust(t, s, "target", "de", deV1, "d")
	bv2, _, err := s.Import(ImportRequest{Role: "baseline", Language: "en", Filename: "en2.json", Content: []byte(`{"hello":"Hello {name}!","items":"{count, plural, one {# item} other {# items}}"}`)})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.ConfirmRename(RenameRequest{OperationID: "r", FromVersionID: bv.VersionID, ToVersionID: bv2.VersionID, FromKey: "greeting", ToKey: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	exp := time.Now().Add(24 * time.Hour)
	iss, err := s.Issues("de", "", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.SetExemption(ExemptionRequest{OperationID: "e", Action: "grant", IssueFingerprint: iss.Issues[0].Issue.Fingerprint, Language: "de", Code: iss.Issues[0].Issue.Code, Key: "k", Reason: "r", Owner: "o", ExpiresAt: &exp})
	if err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ov := s2.Overview(time.Now())
	if ov.BaseVersion.ID != bv2.VersionID || len(ov.Edges) != 1 || len(ov.Exemptions) != 1 {
		t.Fatalf("restored state wrong: %+v", ov)
	}
}

func TestRenameRejections(t *testing.T) {
	s := openTestStore(t)
	b1 := importMust(t, s, "baseline", "en", `{"a":"1","b":"2"}`, "1")
	b2 := importMust(t, s, "baseline", "en", `{"x":"1","y":"2"}`, "2")
	if _, _, err := s.ConfirmRename(RenameRequest{OperationID: "o1", FromVersionID: b1.VersionID, ToVersionID: b2.VersionID, FromKey: "a", ToKey: "x"}); err != nil {
		t.Fatal(err)
	}
	// merge two old keys onto one new key
	_, _, err := s.ConfirmRename(RenameRequest{OperationID: "o2", FromVersionID: b1.VersionID, ToVersionID: b2.VersionID, FromKey: "b", ToKey: "x"})
	if err == nil || !strings.Contains(err.Error(), "合并") {
		t.Fatalf("merge should be rejected: %v", err)
	}
	// stale snapshot
	_, _, err = s.ConfirmRename(RenameRequest{SnapshotID: "snap-0-nope", FromVersionID: b1.VersionID, ToVersionID: b2.VersionID, FromKey: "b", ToKey: "y"})
	if err == nil {
		t.Fatal("stale snapshot must conflict")
	}
}

func TestRenameCycleAcrossVersions(t *testing.T) {
	s := openTestStore(t)
	// v1 has a,b ; v2 drops a (b kept) ; v3 re-introduces nothing new.
	// Construct an explicit chain a->b within one transition pair, then a
	// reverse edge b->a across a later pair that still lists both versions.
	v1 := importMust(t, s, "baseline", "en", `{"a":"1","b":"2","keep":"k"}`, "1")
	v2 := importMust(t, s, "baseline", "en", `{"b":"2","c":"3","keep":"k"}`, "2")
	v3 := importMust(t, s, "baseline", "en", `{"a":"1","b":"2","c":"3","keep":"k"}`, "3")
	if _, _, err := s.ConfirmRename(RenameRequest{OperationID: "e1", FromVersionID: v1.VersionID, ToVersionID: v2.VersionID, FromKey: "a", ToKey: "c"}); err != nil {
		t.Fatal(err)
	}
	// c -> a would create a path a->c->a.
	_, _, err := s.ConfirmRename(RenameRequest{OperationID: "e2", FromVersionID: v2.VersionID, ToVersionID: v3.VersionID, FromKey: "c", ToKey: "a"})
	if err == nil || !strings.Contains(err.Error(), "环") {
		t.Fatalf("cycle should be rejected: %v", err)
	}
}
