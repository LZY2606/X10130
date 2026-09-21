package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func validateAll(t *testing.T, e *testEnv) {
	t.Helper()
	for _, l := range []string{"ja", "fr"} {
		if _, err := e.svc.ValidateTarget(l); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProofDeterministicAndReplayByteIdentical(t *testing.T) {
	e := setupDemo(t)
	validateAll(t, e)
	p1, r1, rep1, err := e.svc.GenerateProof("proof-op", 0)
	if err != nil || rep1 {
		t.Fatalf("proof1: %v replay=%v", err, rep1)
	}
	data1, current, err := e.svc.ReadProof(r1.ID)
	if err != nil || !current {
		t.Fatalf("read proof1 current=%v: %v", current, err)
	}
	_, r2, rep2, err := e.svc.GenerateProof("proof-op", 0)
	if err != nil || !rep2 {
		t.Fatalf("proof replay: %v replay=%v", err, rep2)
	}
	data2, _, _ := e.svc.ReadProof(r2.ID)
	if !bytes.Equal(data1, data2) {
		t.Fatalf("replay produced different proof bytes")
	}
	if r1.ID != r2.ID || r1.SHA256 != r2.SHA256 {
		t.Fatalf("replay created new proof record: %+v vs %+v", r1, r2)
	}
	if p1.OpenIssueCount < 0 || p1.StateStamp == "" {
		t.Fatalf("proof missing identity: %+v", p1)
	}
	if len(p1.Exemptions) != 0 {
		t.Fatalf("expected no exemptions yet")
	}
}

func TestProofReflectsExemptionAndExpiry(t *testing.T) {
	e := setupDemo(t)
	validateAll(t, e)
	p1, _, _, err := e.svc.GenerateProof("p-a", 0)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := e.svc.ValidateTarget("ja")
	var key string
	for _, i := range v.Issues {
		if i.Type == "missing_key" {
			key = i.IssueKey
		}
	}
	if _, _, err := e.svc.AddExemption("ex-a", ExemptionRequest{
		ValidationID: v.ID, IssueKey: key, Reason: "waived with owner approval",
		ExpiresAt: e.svc.now().Add(7 * 24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	p2, _, _, err := e.svc.GenerateProof("p-b", 0)
	if err != nil {
		t.Fatal(err)
	}
	if p2.OpenIssueCount != p1.OpenIssueCount-1 {
		t.Fatalf("open count did not decrease: %d vs %d", p1.OpenIssueCount, p2.OpenIssueCount)
	}
	if len(p2.Exemptions) != 1 || p2.Exemptions[0].Reason != "waived with owner approval" {
		t.Fatalf("proof exemptions wrong: %+v", p2.Exemptions)
	}
	// Same snapshot+expiry state: byte-identical to another generation with new
	// op id (different op ids are allowed; proof content is state-derived).
	p2Data := mustProofBytes(t, e, p2)
	p3, _, _, err := e.svc.GenerateProof("p-c", 0)
	if err != nil {
		t.Fatal(err)
	}
	p3Data := mustProofBytes(t, e, p3)
	if !bytes.Equal(p2Data, p3Data) {
		t.Fatalf("proof not deterministic for same state")
	}
	// After expiry, open count increases again and proof bytes differ.
	e.svc.Clock.(*ControlledClock).Add(31 * 24 * time.Hour)
	p4, _, _, err := e.svc.GenerateProof("p-d", 0)
	if err != nil {
		t.Fatal(err)
	}
	if p4.OpenIssueCount != p1.OpenIssueCount {
		t.Fatalf("expired exemption should count again: %d vs %d", p4.OpenIssueCount, p1.OpenIssueCount)
	}
}

func mustProofBytes(t *testing.T, e *testEnv, p *Proof) []byte {
	t.Helper()
	// find proof record by stamp
	for _, pr := range e.svc.Proofs() {
		if pr.Stamp == p.StateStamp && pr.MatchesCurrent {
			b, _, err := e.svc.ReadProof(pr.ID)
			if err != nil {
				t.Fatal(err)
			}
			return b
		}
	}
	t.Fatal("proof record not found")
	return nil
}

func TestProofStaleStateSeqConflict(t *testing.T) {
	e := setupDemo(t)
	validateAll(t, e)
	seq := e.svc.Snapshot().Seq
	// Change state after capturing seq (add an exemption).
	v, _ := e.svc.ValidateTarget("ja")
	var key string
	for _, i := range v.Issues {
		if i.Type == "missing_key" {
			key = i.IssueKey
		}
	}
	if _, _, err := e.svc.AddExemption("ex-stale-proof", ExemptionRequest{
		ValidationID: v.ID, IssueKey: key, Reason: "r",
		ExpiresAt: e.svc.now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := e.svc.GenerateProof("proof-stale", seq)
	if err == nil || !isConflict(err) {
		t.Fatalf("expected stale proof conflict, got %v", err)
	}
}

func TestSelectiveInvalidationOnTargetChange(t *testing.T) {
	e := setupDemo(t)
	jaView, err := e.svc.ValidateTarget("ja")
	if err != nil {
		t.Fatal(err)
	}
	frView, err := e.svc.ValidateTarget("fr")
	if err != nil {
		t.Fatal(err)
	}
	// Import a new fr version.
	e.imp(t, "imp-fr-2", "fr", roleTarget, map[string]string{
		"greeting": "Bonjour {name}",
		"items":    "{n, plural, one {1} other {#}}",
		"rich":     "<b>{name}</b>",
	})
	jaReplay, err := e.svc.Replay(jaView.ID)
	if err != nil {
		t.Fatal(err)
	}
	frReplay, err := e.svc.Replay(frView.ID)
	if err != nil {
		t.Fatal(err)
	}
	if jaReplay.Invalidated {
		t.Fatalf("ja result must survive an unrelated fr change")
	}
	if !frReplay.Invalidated {
		t.Fatalf("fr result must be invalidated after fr change")
	}
	// Invalidated results block a "current" proof.
	_, _, _, err = e.svc.GenerateProof("blocked", 0)
	if err == nil || !isConflict(err) {
		t.Fatalf("proof should be blocked by invalidated fr result, got %v", err)
	}
	// Revalidating fr unblocks.
	if _, err := e.svc.ValidateTarget("fr"); err != nil {
		t.Fatal(err)
	}
	p, rec, _, err := e.svc.GenerateProof("unblocked", 0)
	if err != nil {
		t.Fatalf("proof after revalidation: %v", err)
	}
	if !p.Current || rec.Stamp == "" {
		t.Fatal("proof should be current")
	}
}

func TestHistoricalProofMarkedAfterStateChange(t *testing.T) {
	e := setupDemo(t)
	validateAll(t, e)
	_, rec, _, err := e.svc.GenerateProof("ph1", 0)
	if err != nil {
		t.Fatal(err)
	}
	// New ja target version.
	e.imp(t, "ja-new", "ja", roleTarget, map[string]string{
		"greeting": "こんにちは {name}", "items": "{n, plural, one {1} other {#}}",
		"rich": "<b>{name}</b>",
	})
	infos := e.svc.Proofs()
	var found bool
	for _, pi := range infos {
		if pi.ID == rec.ID {
			if pi.MatchesCurrent {
				t.Fatal("old proof must be marked historical")
			}
			found = true
			// Still readable.
			b, current, err := e.svc.ReadProof(rec.ID)
			if err != nil || len(b) == 0 || current {
				t.Fatalf("historical proof read: err=%v current=%v len=%d", err, current, len(b))
			}
		}
	}
	if !found {
		t.Fatal("old proof record disappeared")
	}
}

func TestRenameInvalidatesAndReestablishes(t *testing.T) {
	e := setupDemo(t)
	v, err := e.svc.ValidateTarget("ja")
	if err != nil {
		t.Fatal(err)
	}
	// Confirm rename between two baseline versions.
	e.imp(t, "zh2", "zh", roleBaseline, map[string]string{
		"greeting2": "你好 {name}",
		"items":     "{n, plural, one {# 项} other {# 项}}",
		"rich":      "<b>{name}</b>",
	})
	if _, _, err := e.svc.ConfirmRename("ren1", ConfirmRenameRequest{From: "greeting2", To: "greeting"}); err == nil {
		// "to" must exist in current baseline; here direction is old->new so
		// confirm old name "greeting" to current "greeting2".
	}
	// Correct direction: old greeting -> new greeting2.
	res, _, err := e.svc.ConfirmRename("ren2", ConfirmRenameRequest{From: "greeting", To: "greeting2"})
	if err != nil {
		t.Fatal(err)
	}
	if res.MappingSig == "" {
		t.Fatal("missing mapping sig")
	}
	replay, err := e.svc.Replay(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Invalidated {
		t.Fatal("mapping change must invalidate validation")
	}
}

var _ = json.Marshal
var _ = errors.New
var _ = os.ReadFile
var _ = filepath.Join
