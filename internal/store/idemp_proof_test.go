package store

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func baseAndTarget(t *testing.T) *Store {
	st := openTestStore(t, 1000)
	mustImport(t, st, "en", marshalCatalog(map[string]string{"a": "Hello {name}"}), true, "e")
	mustImport(t, st, "ja", marshalCatalog(map[string]string{}), false, "j")
	return st
}

func TestExemptionExpiry(t *testing.T) {
	st := baseAndTarget(t)
	view, _ := st.View(0)
	if view.UnexemptedCount != 1 {
		t.Fatalf("expected 1 unexempted, got %d", view.UnexemptedCount)
	}
	issueID := view.Issues[0].Issue.ID
	// Deadline 100 seconds after clock start 1000.
	if _, err := st.SetExemption("ex1", ExemptionRequest{
		IssueID: issueID, Reason: "translator notified", Deadline: 1100,
		BaseSeq: st.CurrentSeq(),
	}); err != nil {
		t.Fatal(err)
	}
	v2, _ := st.View(0)
	if v2.UnexemptedCount != 0 {
		t.Fatalf("active exemption should zero count, got %d", v2.UnexemptedCount)
	}
	// Move clock past deadline: expiry is immediate, no state change needed.
	st.mu.Lock()
	st.clock = func() time.Time { return time.Unix(1200, 0) }
	st.mu.Unlock()
	v3, _ := st.View(0)
	if v3.UnexemptedCount != 1 {
		t.Fatalf("expired exemption must restore count, got %d", v3.UnexemptedCount)
	}
}

func TestIdempotencySameKeySameAndDifferentContent(t *testing.T) {
	st := baseAndTarget(t)
	r1 := mustImport(t, st, "de", marshalCatalog(map[string]string{"a": "Hallo {name}"}), false, "dup-import")
	seq := st.CurrentSeq()
	r2 := mustImport(t, st, "de", marshalCatalog(map[string]string{"a": "Hallo {name}"}), false, "dup-import")
	if st.CurrentSeq() != seq {
		t.Fatal("same op key + same content must not create new events")
	}
	if !r2.Idempotent {
		t.Fatal("expected idempotent replay marker")
	}
	if r1.RawSHA == "" {
		t.Fatal("replay should carry equivalent result")
	}
	// Same key, different content => hard conflict.
	_, err := st.Import("dup-import", ImportRequest{
		Language: "de", Raw: marshalCatalog(map[string]string{"a": "CHANGED"}),
	})
	if err != ErrIdemConflict {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
}

func TestExemptionDoesNotExtendOnReplay(t *testing.T) {
	st := baseAndTarget(t)
	issueID := st.Summary()
	_ = issueID
	view, _ := st.View(0)
	id := view.Issues[0].Issue.ID
	req := ExemptionRequest{IssueID: id, Reason: "r", Deadline: 5000, BaseSeq: st.CurrentSeq()}
	r1, err := st.SetExemption("k", req)
	if err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	st.clock = func() time.Time { return time.Unix(2000, 0) }
	st.mu.Unlock()
	// Replay returns stored deadline even though "now" moved.
	r2, err := st.SetExemption("k", req)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Exemption.Deadline != r1.Exemption.Deadline {
		t.Fatal("replay must not extend deadline")
	}
}

func TestProofDeterministicAndSnapshotBound(t *testing.T) {
	st := baseAndTarget(t)
	view, _ := st.View(0)
	id := view.Issues[0].Issue.ID
	if _, err := st.SetExemption("ex", ExemptionRequest{
		IssueID: id, Reason: "ok", Deadline: 5000, BaseSeq: st.CurrentSeq(),
	}); err != nil {
		t.Fatal(err)
	}
	p1, err := st.GenerateProof("proof1", ProofRequest{})
	if err != nil {
		t.Fatal(err)
	}
	seq := st.CurrentSeq()
	p2, err := st.GenerateProof("proof1", ProofRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(p1.Bytes, p2.Bytes) {
		t.Fatal("repeated proof generation must be byte-identical")
	}
	if st.CurrentSeq() != seq {
		t.Fatal("identical proof must not add a state version")
	}
	if p1.Record.Document.UnexemptedCount != 0 || p1.Record.Document.IssueCount != 1 {
		t.Fatalf("proof counts wrong: %+v", p1.Record.Document)
	}
	if len(p1.Record.Document.Exemptions) != 1 || p1.Record.Document.Exemptions[0].Reason != "ok" {
		t.Fatal("proof must record each exemption reason")
	}
	if !p1.Record.Current {
		t.Fatal("fresh proof should be current")
	}
	// Historical replay with a later asOf: expired exemption increases count.
	p3, err := st.GenerateProof("proof2", ProofRequest{AsOf: 6000})
	if err != nil {
		t.Fatal(err)
	}
	if p3.Record.Document.UnexemptedCount != 1 {
		t.Fatalf("as-of after deadline should count the issue again, got %d", p3.Record.Document.UnexemptedCount)
	}
	if bytes.Equal(p1.Bytes, p3.Bytes) {
		t.Fatal("different asOf should produce a different proof")
	}
	// Same asOf again -> identical bytes.
	p3b, _ := st.GenerateProof("proof2b", ProofRequest{AsOf: 6000})
	p3c, _ := st.GenerateProof("proof2b", ProofRequest{AsOf: 6000})
	if !bytes.Equal(p3b.Bytes, p3c.Bytes) {
		t.Fatal("same snapshot+asOf proofs must be byte-identical")
	}
}

func TestSnapshotStableReadAcrossMutation(t *testing.T) {
	st := baseAndTarget(t)
	oldSnap, _ := st.Snapshot(0)
	// Read a historical snapshot after further imports: it stays fixed.
	mustImport(t, st, "ja", marshalCatalog(map[string]string{"a": "こんにちは {name}"}), false, "j2")
	pinned, err := st.View(oldSnap.Seq)
	if err != nil {
		t.Fatal(err)
	}
	if pinned.ID != oldSnap.ID || pinned.TotalIssues != 1 {
		t.Fatalf("pinned snapshot changed: %+v", pinned)
	}
	// A write based on the stale snapshot must be rejected, current decision kept.
	_, err = st.SetExemption("stale", ExemptionRequest{
		IssueID: pinned.Issues[0].Issue.ID, Reason: "x", Deadline: 9000,
		BaseSeq: oldSnap.Seq,
	})
	if err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("stale write must conflict, got %v", err)
	}
}
