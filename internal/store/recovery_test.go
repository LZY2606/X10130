package store

import (
	"os"
	"path/filepath"
	"testing"
)

func writeUsable(t *testing.T, dir string) *Store {
	t.Helper()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Commit(func(s *State) error {
		s.RoleByLang["zh"] = "baseline"
		s.CurrentByLang["zh"] = "v1"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestRecoverFromPrevWhenCurrentCorrupt(t *testing.T) {
	dir := t.TempDir()
	writeUsable(t, dir)
	// Create another commit so prev is the first good snapshot.
	st2, _ := Open(dir)
	if err := st2.Commit(func(s *State) error { s.CurrentByLang["zh"] = "v2"; return nil }); err != nil {
		t.Fatal(err)
	}
	// Corrupt current: truncate tail.
	cur := filepath.Join(dir, dirState, stateFile)
	b, _ := os.ReadFile(cur)
	if err := os.WriteFile(cur, b[:len(b)-3], 0o644); err != nil {
		t.Fatal(err)
	}
	st3, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !st3.Degraded() || !st3.ReadOnly() {
		t.Fatalf("expected degraded read-only mode, got degraded=%v ro=%v", st3.Degraded(), st3.ReadOnly())
	}
	snap := st3.Snapshot()
	if snap.CurrentByLang["zh"] != "v1" {
		t.Fatalf("did not recover previous complete snapshot: %+v", snap.CurrentByLang)
	}
	evs := st3.RecoveryEvents()
	found := false
	for _, e := range evs {
		if e.Scope == "state" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected recovery event, got %+v", evs)
	}
	// Quarantine contains the bad current file.
	qEntries, _ := os.ReadDir(filepath.Join(dir, dirQuar))
	if len(qEntries) == 0 {
		t.Fatal("corrupt file was not quarantined")
	}
	// Writes blocked.
	if err := st3.Commit(func(s *State) error { return nil }); err != ErrReadOnly {
		t.Fatalf("expected read-only error, got %v", err)
	}
}

func TestLeftoverTmpSwept(t *testing.T) {
	dir := t.TempDir()
	writeUsable(t, dir)
	tmp := filepath.Join(dir, dirState, stateFile+tmpSuffix)
	if err := os.WriteFile(tmp, []byte("{partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		// may be quarantined rather than deleted
		q, _ := os.ReadDir(filepath.Join(dir, dirQuar))
		if len(q) == 0 {
			t.Fatalf("tmp file not swept/quarantined")
		}
	}
	found := false
	for _, e := range st.RecoveryEvents() {
		if e.Scope == dirState {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected tmp recovery event: %+v", st.RecoveryEvents())
	}
}

func TestMissingReferencedBlobTriggersReadOnly(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir)
	blob, sum, err := st.WriteRaw("a.json", []byte(`{"k":"v"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Commit(func(s *State) error {
		s.Uploads = append(s.Uploads, UploadRecord{
			ID: "up-1", Language: "zh", Role: "baseline", Filename: "a.json",
			BlobName: blob, SHA256: sum,
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Delete the blob.
	if err := os.Remove(filepath.Join(dir, dirRaw, filepath.Base(blob))); err != nil {
		t.Fatal(err)
	}
	st2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !st2.ReadOnly() {
		t.Fatal("missing referenced blob must put store into read-only mode")
	}
}
