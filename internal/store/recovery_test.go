package store

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// helperProcess performs one import and, when CRASH=1, exits right after the
// WAL fsync but before in-memory snapshot materialization.
func TestSubprocessCrashBoundary(t *testing.T) {
	if os.Getenv("STORE_HELPER") == "1" {
		dir := os.Getenv("STORE_DIR")
		st, err := Open(dir, func() time.Time { return time.Unix(1000, 0) })
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		st.SetFaultCrashAfter(1)
		_, _ = st.Import("imp-en", ImportRequest{
			Language: "en", AsBaseline: true,
			Raw:      marshalCatalog(map[string]string{"a": "hello"}),
		})
		os.Exit(0)
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=TestSubprocessCrashBoundary")
	cmd.Env = append(os.Environ(), "STORE_HELPER=1", "STORE_DIR="+dir)
	_ = cmd.Run() // exit code 2 expected
	st, err := Open(dir, func() time.Time { return time.Unix(1000, 0) })
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// Atomicity: either the whole import is visible or none of it is. The
	// blob was written and fsynced first; the WAL event may have landed too.
	sum := st.Summary()
	if len(sum.Languages) == 1 {
		// Fully committed: the raw file must be downloadable and parseable.
		raw, err := st.RawBytes(sum.Languages[0].Imports[len(sum.Languages[0].Imports)-1].Raw.BlobSHA)
		if err != nil {
			t.Fatalf("viewable version without downloadable raw: %v", err)
		}
		view, err := st.View(0)
		if err != nil {
			t.Fatalf("committed state without parseable view: %v", err)
		}
		if !bytes.Contains(raw, []byte("hello")) || view == nil {
			t.Fatal("half state exposed")
		}
	} else if len(sum.Languages) != 0 {
		t.Fatalf("only 0 or 1 language expected, got %d", len(sum.Languages))
	}
}

func TestTornWALTailIsQuarantined(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir, testClock(1000))
	if err != nil {
		t.Fatal(err)
	}
	mustImport(t, st, "en", marshalCatalog(map[string]string{"a": "1"}), true, "ok")
	st.Close()
	wal := filepath.Join(dir, "wal.log")
	info, _ := os.Stat(wal)
	// Append a torn partial frame.
	f, _ := os.OpenFile(wal, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.Write([]byte{0, 0, 0, 0, 0, 0, 0, 9, 'g', 'a'})
	f.Close()
	_ = info
	st2, err := Open(dir, testClock(1000))
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	rec := st2.Recovery()
	if rec.Healthy {
		t.Fatal("torn tail must be reported")
	}
	if rec.Quarantined == 0 {
		t.Fatal("torn bytes must be quarantined")
	}
	// Previously complete data retained and visible.
	if len(st2.Summary().Languages) != 1 {
		t.Fatal("complete prior state must be retained")
	}
	// Mutations rejected in read-only mode rather than mixing state.
	if _, err := st2.Import("x", ImportRequest{Language: "fr",
		Raw: marshalCatalog(map[string]string{"a": "1"})}); err != ErrReadOnly {
		t.Fatalf("expected read-only, got %v", err)
	}
}

func TestOrphanProofBlobRemovedAndNoMixedProof(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir, testClock(1000))
	mustImport(t, st, "en", marshalCatalog(map[string]string{"a": "1"}), true, "e")
	out, err := st.GenerateProof("p", ProofRequest{})
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	// Simulate a torn proof write: a partial blob with no referencing event.
	orphan := filepath.Join(dir, "blobs", "proof", "ab", "proof-torn")
	_ = os.MkdirAll(filepath.Dir(orphan), 0o755)
	_ = os.WriteFile(orphan, []byte("{truncated"), 0o644)
	st2, err := Open(dir, testClock(1000))
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("uncommitted proof artifact must be removed")
	}
	// Last complete proof still readable.
	data, err := st2.ProofBytes(out.Record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), out.Record.ID) {
		t.Fatal("readable proof mismatch")
	}
}
