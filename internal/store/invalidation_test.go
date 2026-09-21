package store

import "testing"

func TestSelectiveInvalidationOnTargetChange(t *testing.T) {
	st := openTestStore(t, 1000)
	mustImport(t, st, "en", marshalCatalog(map[string]string{"a": "Hello {name}"}), true, "e")
	mustImport(t, st, "ja", marshalCatalog(map[string]string{"a": "こんにちは {name}"}), false, "j")
	mustImport(t, st, "fr", marshalCatalog(map[string]string{"a": "bonjour {name}"}), false, "f")
	runs := st.Runs()
	if len(runs) != 1 || runs[0].Status != "current" {
		t.Fatalf("expected one current run, got %+v", runs)
	}
	currentRun := runs[0].ID
	// Only ja changes: any run covering ja is invalid; fr-specific success is
	// not separately stored, so the shared run is invalidated but its reason
	// names ja only.
	mustImport(t, st, "ja", marshalCatalog(map[string]string{"a": "こんにちは {name}!"}), false, "j2")
	runs2 := st.Runs()
	var old *Run
	for i := range runs2 {
		if runs2[i].ID == currentRun {
			old = &runs2[i]
		}
	}
	if old == nil {
		t.Fatal("old run must remain visible (not deleted)")
	}
	if old.Status != "invalid" || !contains(old.InvalidReason, "ja") {
		t.Fatalf("old run invalidation should name ja, got %+v", old)
	}
	if contains(old.InvalidReason, "fr") {
		t.Fatalf("unaffected language fr must not be named: %s", old.InvalidReason)
	}
	// Old snapshot/run replay still readable.
	pinned, err := st.View(old.Seq)
	if err != nil || pinned.RunStatus != "invalid" {
		t.Fatalf("pinned invalid run must be explicitly visible, got %+v err=%v", pinned, err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestProofSwitchedStateMarksHistorical(t *testing.T) {
	st := openTestStore(t, 1000)
	mustImport(t, st, "en", marshalCatalog(map[string]string{"a": "Hi"}), true, "e")
	p, err := st.GenerateProof("old", ProofRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Record.Current {
		t.Fatal("should be current at first")
	}
	mustImport(t, st, "en", marshalCatalog(map[string]string{"a": "Hi", "b": "new"}), true, "e2")
	sum := st.Summary()
	oldProof := sum.Proofs[0]
	if oldProof.Current {
		t.Fatal("proof tied to an older state must be marked historical")
	}
}
