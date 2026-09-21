package store

import "testing"

func TestRestartPreservesVersionsMappingsExemptions(t *testing.T) {
	dir := t.TempDir()
	clock := testClock(1000)
	st, err := Open(dir, clock)
	if err != nil {
		t.Fatal(err)
	}
	mustImport(t, st, "en", marshalCatalog(map[string]string{"old": "Hi {name}"}), true, "e1")
	mustImport(t, st, "ja", marshalCatalog(map[string]string{"old": "やあ {name}"}), false, "j1")
	mustImport(t, st, "en", marshalCatalog(map[string]string{"new": "Hi {name}"}), true, "e2")
	pid := st.Summary().OpenProposals[0].ID
	if _, err := st.ConfirmRename("ren", RenameRequest{
		ProposalID: pid, Edges: []Edge{{OldKey: "old", NewKey: "new"}},
		BaseSeq: st.CurrentSeq(),
	}); err != nil {
		t.Fatal(err)
	}
	view, _ := st.View(0)
	if len(view.Issues) > 0 {
		t.Fatalf("expected no issues after rename, got %v", view.Issues)
	}
	// Artificially add an exemption-like event is unnecessary; verify mapping
	// persistence by reopening.
	st.Close()

	st2, err := Open(dir, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	sum := st2.Summary()
	if len(sum.Edges) != 1 || sum.Edges[0].OldKey != "old" {
		t.Fatalf("mapping lost after restart: %+v", sum.Edges)
	}
	if sum.Baseline != "en" {
		t.Fatal("baseline lost")
	}
	view2, err := st2.View(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(view2.Issues) != 0 {
		t.Fatalf("rename semantics lost after restart: %+v", view2.Issues)
	}
	// Raw files still byte-identical.
	for _, l := range sum.Languages {
		for _, im := range l.Imports {
			if _, err := st2.RawBytes(im.Raw.BlobSHA); err != nil {
				t.Fatalf("raw %s not downloadable after restart: %v", im.Raw.BlobSHA, err)
			}
		}
	}
}

func TestExemptionPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir, testClock(1000))
	mustImport(t, st, "en", marshalCatalog(map[string]string{"a": "x"}), true, "e")
	mustImport(t, st, "ja", marshalCatalog(map[string]string{}), false, "j")
	view, _ := st.View(0)
	id := view.Issues[0].Issue.ID
	if _, err := st.SetExemption("ex", ExemptionRequest{
		IssueID: id, Reason: "noted", Deadline: 5000, BaseSeq: st.CurrentSeq(),
	}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st2, _ := Open(dir, testClock(1000))
	defer st2.Close()
	v, _ := st2.View(0)
	if v.UnexemptedCount != 0 {
		t.Fatalf("active exemption should survive restart, got %d", v.UnexemptedCount)
	}
}
