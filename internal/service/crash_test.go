package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestProofAtomicArtifacts ensures no truncated proof is exposed as valid.
func TestProofTmpNotExposed(t *testing.T) {
	e := setupDemo(t)
	validateAll(t, e)
	p, rec, _, err := e.svc.GenerateProof("crash-proof", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Current {
		t.Fatal("new proof should be current")
	}
	// Simulate crash leaving a tmp proof file.
	proofDir := filepath.Join(e.dir, "blobs", "proofs")
	tmp := filepath.Join(proofDir, "proof-truncated.json.tmp")
	if err := os.WriteFile(tmp, []byte("{incomplete"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Reopen: tmp swept, complete proof still readable.
	st, err := openStore(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	svc := New(st)
	svc.Clock = NewControlledClock(time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC))
	b, current, err := svc.ReadProof(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 || !current {
		t.Fatalf("last complete proof lost: len=%d current=%v", len(b), current)
	}
	entries, _ := os.ReadDir(proofDir)
	for _, en := range entries {
		if filepath.Ext(en.Name()) == ".tmp" {
			t.Fatalf("truncated tmp proof left in place: %s", en.Name())
		}
	}
}

// TestRestartReplaysIdempotentOp verifies a retry after restart returns the
// stored result and creates no new version.
func TestRestartReplaysIdempotentOp(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	run := func() (ImportResult, bool, error) {
		st, err := openStore(dir)
		if err != nil {
			return ImportResult{}, false, err
		}
		svc := New(st)
		svc.Clock = NewControlledClock(start)
		return svc.Import("restart-op", ImportRequest{
			Language: "zh", Role: roleBaseline, Filename: "zh.json",
			Content: []byte(`{"a":"1","b":"2"}`),
		})
	}
	r1, rep1, err := run()
	if err != nil || rep1 {
		t.Fatalf("first: %v rep=%v", err, rep1)
	}
	r2, rep2, err := run()
	if err != nil || !rep2 {
		t.Fatalf("second after restart: %v rep=%v", err, rep2)
	}
	if r1.UploadID != r2.UploadID || r1.VersionID != r2.VersionID {
		t.Fatal("restart replay changed result")
	}
}

// TestNoHalfRawStructureState verifies that a failed commit leaves old state
// and the referenced raw consistent (no viewable-but-not-downloadable entry).
func TestNoHalfStateOnFailedCommit(t *testing.T) {
	e := setupDemo(t)
	// Trigger an idempotency conflict after the raw blob is written; state must
	// remain the old complete state.
	_, _, err := e.svc.Import("half-op", ImportRequest{Language: "de", Role: roleTarget,
		Filename: "de.json", Content: []byte(`{"x":"1"}`)})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = e.svc.Import("half-op", ImportRequest{Language: "de", Role: roleTarget,
		Filename: "de.json", Content: []byte(`{"x":"2"}`)})
	if err == nil {
		t.Fatal("expected idempotency conflict")
	}
	infos := e.svc.Languages()
	for _, l := range infos {
		if l.Language == "de" && l.Fingerprint == "" {
			t.Fatal("de in half state")
		}
	}
	// All referenced uploads are downloadable.
	for _, u := range e.svc.Uploads() {
		b, _, err := e.svc.RawUpload(u.ID)
		if err != nil || len(b) == 0 {
			t.Fatalf("upload %s not downloadable: %v", u.ID, err)
		}
	}
}
