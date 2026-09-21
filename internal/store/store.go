// Package store provides crash-safe local persistence. Every write goes
// through a temp file + fsync + atomic rename, so after an interruption the
// state is either the complete old state or the complete new state. On load,
// checksum mismatches never silently reset data: the previous committed
// backup is used and the affected operation is reported via notices.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Store is a directory-backed persistence unit.
type Store struct {
	dir string
}

type envelope struct {
	Checksum string          `json:"checksum"`
	Payload  json.RawMessage `json:"payload"`
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// HashBytes exposes the content hash used for blobs.
func HashBytes(b []byte) string { return hashBytes(b) }

// Open prepares the data directory and removes interrupted temp files.
func Open(dir string) (*Store, error) {
	for _, sub := range []string{"", "blobs", "attestations"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, err
		}
	}
	// Clean up temp files left by interrupted writes; they were never
	// committed, so removing them cannot lose committed state.
	for _, sub := range []string{"", "blobs", "attestations"} {
		matches, _ := filepath.Glob(filepath.Join(dir, sub, "*.tmp"))
		for _, m := range matches {
			os.Remove(m)
		}
	}
	return &Store{dir: dir}, nil
}

func writeAtomic(dir, name string, b []byte) error {
	tmp := filepath.Join(dir, name+".tmp")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		os.Remove(tmp)
		return err
	}
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

// SaveState atomically commits a new state payload, rotating the previous
// committed state to state.json.bak for recovery.
func (s *Store) SaveState(payload []byte) error {
	env, err := json.Marshal(envelope{Checksum: hashBytes(payload), Payload: payload})
	if err != nil {
		return err
	}
	final := filepath.Join(s.dir, "state.json")
	if b, err := os.ReadFile(final); err == nil {
		if err := writeAtomic(s.dir, "state.json.bak", b); err != nil {
			return err
		}
	}
	return writeAtomic(s.dir, "state.json", env)
}

// LoadState returns the newest verifiable committed payload. If the primary
// file is missing, truncated or fails its checksum, the backup is tried and
// a notice describes the affected operation. Data is never silently reset.
func (s *Store) LoadState() (payload []byte, notices []string, err error) {
	for _, name := range []string{"state.json", "state.json.bak"} {
		b, rerr := os.ReadFile(filepath.Join(s.dir, name))
		if rerr != nil {
			if !os.IsNotExist(rerr) {
				notices = append(notices, fmt.Sprintf("%s unreadable: %v", name, rerr))
			}
			continue
		}
		var env envelope
		if jerr := json.Unmarshal(b, &env); jerr != nil {
			notices = append(notices, fmt.Sprintf("%s is corrupt (%v); falling back to previous committed state", name, jerr))
			continue
		}
		if hashBytes(env.Payload) != env.Checksum {
			notices = append(notices, fmt.Sprintf("%s checksum mismatch; falling back to previous committed state", name))
			continue
		}
		if name == "state.json.bak" {
			notices = append(notices, "recovered from state.json.bak: the last operation was not committed and its effects were discarded")
		}
		return env.Payload, notices, nil
	}
	return nil, notices, nil
}

// SaveBlob stores raw uploaded bytes content-addressed by their hash.
func (s *Store) SaveBlob(hash string, b []byte) error {
	if hashBytes(b) != hash {
		return fmt.Errorf("blob hash mismatch for %s", hash)
	}
	if _, err := os.Stat(filepath.Join(s.dir, "blobs", hash)); err == nil {
		return nil
	}
	return writeAtomic(filepath.Join(s.dir, "blobs"), hash, b)
}

// ReadBlob returns raw bytes, verifying integrity against the content hash.
func (s *Store) ReadBlob(hash string) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(s.dir, "blobs", hash))
	if err != nil {
		return nil, err
	}
	if hashBytes(b) != hash {
		return nil, fmt.Errorf("blob %s corrupt: checksum mismatch", hash)
	}
	return b, nil
}

// AttestationID computes the deterministic id for attestation content.
func AttestationID(content []byte) string {
	return "att-" + hashBytes(content)[:32]
}

// SaveAttestation atomically stores an attestation document.
func (s *Store) SaveAttestation(id string, b []byte) error {
	if AttestationID(b) != id {
		return fmt.Errorf("attestation id/content mismatch for %s", id)
	}
	if _, err := os.Stat(filepath.Join(s.dir, "attestations", id)); err == nil {
		return nil
	}
	return writeAtomic(filepath.Join(s.dir, "attestations"), id, b)
}

// ReadAttestation returns attestation bytes, verifying integrity. A
// truncated or corrupted file is reported, never served as valid.
func (s *Store) ReadAttestation(id string) ([]byte, error) {
	if !strings.HasPrefix(id, "att-") || strings.ContainsAny(id, "/\\") {
		return nil, fmt.Errorf("invalid attestation id %q", id)
	}
	b, err := os.ReadFile(filepath.Join(s.dir, "attestations", id))
	if err != nil {
		return nil, err
	}
	if AttestationID(b) != id {
		return nil, fmt.Errorf("attestation %s corrupt: content does not match id", id)
	}
	return b, nil
}
