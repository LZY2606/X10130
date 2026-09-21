package store

import (
	"encoding/json"

	"msgcatalog/internal/catalog"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

func (s *Store) blobPath(kind, sha string) string {
	if len(sha) <= 2 {
		return filepath.Join(s.dir, "blobs", kind, sha)
	}
	// Strip a "proof-" / "raw-" style prefix before sharding.
	hashPart := sha
	if i := indexByte(hashPart, '-'); i >= 0 {
		hashPart = hashPart[i+1:]
	}
	shard := hashPart
	if len(shard) > 2 {
		shard = shard[:2]
	}
	return filepath.Join(s.dir, "blobs", kind, shard, sha)
}

// putBlob writes data content-addressed with an atomic temp+rename and fsyncs
// both the file and the directory before returning.
func (s *Store) putBlob(kind string, data []byte) (string, error) {
	sha := catalog.HashBytes(data)
	return s.putBlobAs(kind, sha, data)
}

func (s *Store) putBlobAs(kind, name string, data []byte) (string, error) {
	sha := name
	dst := s.blobPath(kind, sha)
	if _, err := os.Stat(dst); err == nil {
		return sha, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	tmp := dst + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return "", err
	}
	s.fsyncDir(filepath.Dir(dst))
	return sha, nil
}

func (s *Store) readBlob(kind, sha string) ([]byte, error) {
	return os.ReadFile(s.blobPath(kind, sha))
}

func (s *Store) fsyncDir(dir string) {
	if f, err := os.Open(dir); err == nil {
		_ = f.Sync()
		_ = f.Close()
	}
}

// SetFaultCrashAfter enables crash injection: the next nth fsync of the WAL
// calls os.Exit style fault via panicKill. Only used by tests.
func (s *Store) SetFaultCrashAfter(n int) {
	s.mu.Lock()
	s.faultCrashAfter = n
	s.mu.Unlock()
}

// commit serializes ev, appends it, fsyncs, then applies it to state and
// materializes a snapshot.
func (s *Store) commit(ev *Event) error {
	if s.readOnly {
		return ErrReadOnly
	}
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if err := writeFrame(s.f, data); err != nil {
		return err
	}
	if err := s.f.Sync(); err != nil {
		return err
	}
	s.fsyncDir(s.dir)
	s.fsyncCount++
	if s.faultCrashAfter > 0 && s.fsyncCount >= s.faultCrashAfter {
		s.faultCrashAfter = 0
		hardExit()
	}
	s.apply(ev)
	s.materializeSnapshot(ev)
	return nil
}

// replay loads the WAL and rebuilds state. A torn or corrupt tail is moved to
// quarantine; previously confirmed complete data is retained.
func (s *Store) replay() error {
	walPath := filepath.Join(s.dir, "wal.log")
	fi, err := os.Stat(walPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	f, err := os.Open(walPath)
	if err != nil {
		return err
	}
	defer f.Close()
	all, err := os.ReadFile(walPath)
	if err != nil {
		return err
	}
	payloads, trailing, err := readFrames(all)
	if err != nil {
		return err
	}
	for i, pl := range payloads {
		var ev Event
		if err := json.Unmarshal(pl, &ev); err != nil {
			return fmt.Errorf("wal frame %d: %w", i, err)
		}
		s.apply(&ev)
		s.materializeSnapshot(&ev)
	}
	if trailing > 0 {
		if err := s.quarantineTail(fi.Size(), trailing); err != nil {
			return err
		}
		s.recovery.Healthy = false
		s.recovery.Quarantined = trailing
		s.recovery.Notes = append(s.recovery.Notes,
			fmt.Sprintf("detected %d torn/corrupt trailing bytes in WAL; quarantined and retained the last complete state", trailing))
		s.readOnly = true
	}
	return nil
}

func (s *Store) quarantineTail(total int64, n int) error {
	good := total - int64(n)
	in, err := os.Open(filepath.Join(s.dir, "wal.log"))
	if err != nil {
		return err
	}
	tail := make([]byte, n)
	if _, err := in.ReadAt(tail, good); err != nil {
		in.Close()
		return err
	}
	in.Close()
	q := filepath.Join(s.dir, "quarantine", fmt.Sprintf("wal-tail-%d", total))
	if err := os.WriteFile(q, tail, 0o644); err != nil {
		return err
	}
	if err := os.Truncate(filepath.Join(s.dir, "wal.log"), good); err != nil {
		return err
	}
	return nil
}

func shortName(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// verifyBlobs checks that every raw/proof blob referenced by state exists and
// matches its sha. Any mismatch marks the store degraded and read-only rather
// than silently resetting data.
func (s *Store) verifyBlobs() error {
	var bad []string
	// raw blobs
	seen := map[string]bool{}
	for _, ls := range s.state.Langs {
		for _, im := range ls.Imports {
			if seen[im.Raw.BlobSHA] {
				continue
			}
			seen[im.Raw.BlobSHA] = true
			data, err := s.readBlob("raw", im.Raw.BlobSHA)
			if err != nil || catalog.HashBytes(data) != im.Raw.BlobSHA {
				bad = append(bad, "raw/"+shortName(im.Raw.BlobSHA))
			}
		}
	}
	for _, pr := range s.state.Proofs {
		data, err := s.readBlob("proof", pr.ID)
		if err != nil {
			bad = append(bad, "proof/"+shortName(pr.ID))
			continue
		}
		var doc ProofDocument
		if err := json.Unmarshal(data, &doc); err != nil || doc.ProofID != pr.ID {
			bad = append(bad, "proof/"+shortName(pr.ID))
		}
	}
	if len(bad) > 0 {
		s.recovery.Healthy = false
		s.recovery.Notes = append(s.recovery.Notes,
			fmt.Sprintf("storage checksum mismatch for %v; store opened read-only, complete data retained", bad))
		s.readOnly = true
	}
	// Orphan proof blobs (torn write artifacts) are removed; they were never
	// referenced by a committed event, so no valid proof is lost.
	root := filepath.Join(s.dir, "blobs", "proof")
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		sha := filepath.Base(path)
		if _, ok := s.state.Proofs[sha]; !ok {
			_ = os.Remove(path)
			s.recovery.Notes = append(s.recovery.Notes,
				"removed uncommitted proof artifact "+shortName(sha))
		}
		return nil
	})
	sort.Strings(s.recovery.Notes)
	return nil
}



func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
