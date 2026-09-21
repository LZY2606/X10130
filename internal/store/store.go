// Package store implements local, dependency-free persistence with crash
// safety: content-addressed blobs plus an atomic state envelope.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	dirBlobs   = "blobs"
	dirTmp     = "tmp"
	stateName  = "state.json"
	backupName = "state.prev.json"
	tmpName    = "state.tmp.json"
)

// Sentinel errors.
var (
	ErrDegraded = errors.New("store is in degraded mode due to integrity problems")
	ErrCorrupt  = errors.New("stored data failed integrity verification")
	ErrNotFound = errors.New("blob not found")
	ErrConflict = errors.New("optimistic state conflict")
)

// Envelope wraps the state JSON with its checksum.
type Envelope struct {
	Version int             `json:"version"`
	SHA256  string          `json:"sha256"`
	State   json.RawMessage `json:"state"`
}

// Store manages a data directory.
type Store struct {
	root string
}

// Open verifies the directory and returns a store.
func Open(root string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(root, dirBlobs), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(root, dirTmp), 0o755); err != nil {
		return nil, err
	}
	s := &Store{root: root}
	if err := os.RemoveAll(filepath.Join(root, dirTmp)); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(root, dirTmp), 0o755); err != nil {
		return nil, err
	}
	return s, nil
}

func blobPath(sha string) string {
	return filepath.Join(dirBlobs, sha[:2], sha[2:4], sha)
}

// PutBlob stores content by sha256 and returns the id. Idempotent.
func (s *Store) PutBlob(data []byte) (string, error) {
	sum := sha256.Sum256(data)
	id := hex.EncodeToString(sum[:])
	p := filepath.Join(s.root, blobPath(id))
	if _, err := os.Stat(p); err == nil {
		return id, nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	tmp := filepath.Join(s.root, dirTmp, "blob-"+id)
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, p); err != nil {
		return "", err
	}
	return id, nil
}

// GetBlob returns and verifies blob content.
func (s *Store) GetBlob(id string) ([]byte, error) {
	p := filepath.Join(s.root, blobPath(id))
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return nil, err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != id {
		return nil, fmt.Errorf("%w: blob %s checksum mismatch", ErrCorrupt, Short(id))
	}
	return data, nil
}

// HasBlob reports whether a verified blob exists.
func (s *Store) HasBlob(id string) bool {
	_, err := s.GetBlob(id)
	return err == nil
}

func Short(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

// Load reads the committed state envelope and decodes it into dst. A missing
// state file initializes dst to its zero JSON form. If both the current and
// backup envelopes are damaged the error is returned rather than resetting.
func (s *Store) Load(dst any) (int, error) {
	env, err := s.readEnvelope(stateName)
	source := stateName
	if err != nil {
		env2, err2 := s.readEnvelope(backupName)
		if err2 != nil {
			if os.IsNotExist(err) {
				return 0, nil
			}
			return 0, fmt.Errorf("state unreadable (and backup unusable): %v", err)
		}
		env = env2
		source = backupName
	}
	if err := json.Unmarshal(env.State, dst); err != nil {
		return env.Version, fmt.Errorf("%w: %s state JSON: %v", ErrCorrupt, source, err)
	}
	return env.Version, nil
}

func (s *Store) readEnvelope(name string) (*Envelope, error) {
	data, err := os.ReadFile(filepath.Join(s.root, name))
	if err != nil {
		return nil, err
	}
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("%w: %s envelope: %v", ErrCorrupt, name, err)
	}
	sum := sha256.Sum256(env.State)
	if hex.EncodeToString(sum[:]) != env.SHA256 {
		return nil, fmt.Errorf("%w: %s envelope checksum", ErrCorrupt, name)
	}
	if !json.Valid(env.State) {
		return nil, fmt.Errorf("%w: %s invalid state json", ErrCorrupt, name)
	}
	return &env, nil
}

// Commit serializes src and atomically replaces the state file, keeping the
// previous good envelope as a crash/corruption fallback.
func (s *Store) Commit(src any, expectedVersion, newVersion int) error {
	data, err := json.Marshal(src)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	env := Envelope{Version: newVersion, SHA256: hex.EncodeToString(sum[:]), State: data}
	raw, err := json.MarshalIndent(&env, "", "  ")
	if err != nil {
		return err
	}
	// Optimistic version check against the currently committed envelope.
	if cur, err := s.currentVersion(); err == nil && cur != expectedVersion {
		return fmt.Errorf("%w: expected %d got %d", ErrConflict, expectedVersion, cur)
	}
	tmp := filepath.Join(s.root, tmpName)
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	if err := syncFile(tmp); err != nil {
		return err
	}
	// Move current good state aside (best effort; missing is fine on first run).
	if _, err := os.Stat(filepath.Join(s.root, stateName)); err == nil {
		if err := os.Rename(filepath.Join(s.root, stateName), filepath.Join(s.root, backupName)); err != nil {
			return err
		}
	}
	if err := os.Rename(tmp, filepath.Join(s.root, stateName)); err != nil {
		return err
	}
	if err := syncDir(s.root); err != nil {
		return err
	}
	return nil
}

func (s *Store) currentVersion() (int, error) {
	env, err := s.readEnvelope(stateName)
	if err != nil {
		return 0, err
	}
	return env.Version, nil
}

// VerifyBlobs checks the given ids exist and match their checksums and
// returns the bad ids. Missing blobs are listed as corrupt too.
func (s *Store) VerifyBlobs(ids []string) []string {
	seen := map[string]bool{}
	var bad []string
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if _, err := s.GetBlob(id); err != nil {
			bad = append(bad, id)
		}
	}
	sort.Strings(bad)
	return bad
}

// AllBlobIDs lists blob files on disk (used by diagnostics).
func (s *Store) AllBlobIDs() ([]string, error) {
	var ids []string
	err := filepath.WalkDir(filepath.Join(s.root, dirBlobs), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if len(name) == 64 && !strings.Contains(name, ".") {
			ids = append(ids, name)
		}
		return nil
	})
	return ids, err
}
