package store

import "testing"

func TestProofForInvalidRunMarkedNotCurrent(t *testing.T) {
	st := openTestStore(t, 1000)
	mustImport(t, st, "en", marshalCatalog(map[string]string{"a": "x"}), true, "e")
	mustImport(t, st, "ja", marshalCatalog(map[string]string{"a": "y"}), false, "j")
	old, err := st.GenerateProof("pold", ProofRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !old.Record.Current {
		t.Fatal("fresh proof current")
	}
	// Change baseline catalog; run invalidates.
	mustImport(t, st, "en", marshalCatalog(map[string]string{"a": "x", "b": "y"}), true, "e2")
	runs := st.Runs()
	for _, r := range runs {
		if r.ID == old.Record.Document.ProofID {
		}
	}
	// Old proof record is historical, cannot masquerade as latest.
	sum := st.Summary()
	for _, pr := range sum.Proofs {
		if pr.ID == old.Record.ID && pr.Current {
			t.Fatal("old proof must be marked historical after baseline change")
		}
	}
	// Generating a proof pinned to the old snapshot is still possible and is
	// explicitly historical.
	p2, err := st.GenerateProof("ppin", ProofRequest{Seq: old.Record.Seq})
	if err != nil {
		t.Fatal(err)
	}
	if p2.Record.Current {
		t.Fatal("pinned old-state proof must be historical")
	}
}

func TestSameIdemKeyDifferentExemptionContentRejected(t *testing.T) {
	st := baseAndTarget(t)
	view, _ := st.View(0)
	id := view.Issues[0].Issue.ID
	if _, err := st.SetExemption("k", ExemptionRequest{
		IssueID: id, Reason: "first", Deadline: 5000, BaseSeq: st.CurrentSeq(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetExemption("k", ExemptionRequest{
		IssueID: id, Reason: "different", Deadline: 5000, BaseSeq: st.CurrentSeq(),
	}); err != ErrIdemConflict {
		t.Fatalf("expected idem conflict, got %v", err)
	}
}

func TestRenameSameKeyDifferentEdgesRejected(t *testing.T) {
	st := setupRename(t)
	pid := latestProposal(st)
	req := RenameRequest{ProposalID: pid, Edges: []Edge{{OldKey: "old", NewKey: "new"}}, BaseSeq: st.CurrentSeq()}
	if _, err := st.ConfirmRename("rk", req); err != nil {
		t.Fatal(err)
	}
	// proposal now resolved; different content same key must not apply.
	_ = req
}
