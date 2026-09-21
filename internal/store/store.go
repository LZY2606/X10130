package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Sentinel errors.
var (
	ErrReadOnly    = errors.New("storage is in read-only recovery mode")
	ErrCorrupt     = errors.New("state file is corrupt")
	ErrIdempotency = errors.New("idempotency key was already used with different content")
)

// Store is the durable application store.
type Store struct {
	root string
	mu   sync.Mutex
	st   *State

	degraded bool
	readOnly bool
}

const (
	dirState  = "state"
	dirRaw    = "blobs/raw"
	dirProofs = "blobs/proofs"
	dirQuar   = "quarantine"
	stateFile = "state.json"
	prevFile  = "state.prev.json"
	tmpSuffix = ".tmp"
)

// Open loads or initializes the store at root and verifies integrity.
func Open(root string) (*Store, error) {
	for _, d := range []string{dirState, dirRaw, dirProofs, dirQuar} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			return nil, err
		}
	}
	s := &Store{root: root}
	if err := s.load(); err != nil {
		return nil, err
	}
	if err := s.verifyBlobs(); err != nil {
		return nil, err
	}
	if err := s.sweepTempFiles(); err != nil {
		return nil, err
	}
	return s, nil
}

type envelope struct {
	Seq     int64           `json:"seq"`
	SHA256  string          `json:"sha256"`
	Payload json.RawMessage `json:"payload"`
}

func (s *Store) load() error {
	cur := filepath.Join(s.root, dirState, stateFile)
	prev := filepath.Join(s.root, dirState, prevFile)

	st, curErr := readState(cur)
	if curErr == nil {
		s.st = st
		return nil
	}
	// Current is corrupt or missing: quarantine it and try prev.
	events := []RecoveryEvent{}
	if !errors.Is(curErr, os.ErrNotExist) {
		if qErr := s.quarantine(cur, "state-current"); qErr != nil {
			return qErr
		}
		events = append(events, RecoveryEvent{Time: time.Now().UTC(), Scope: "state",
			Message: "current state unreadable (" + curErr.Error() + "); quarantined and recovered previous snapshot"})
	}
	stPrev, prevErr := readState(prev)
	if prevErr == nil {
		s.st = stPrev
		s.st.RecoveryEvents = append(s.st.RecoveryEvents, events...)
		s.degraded = true
		s.readOnly = true
		// Persist recovery note without changing data: best-effort; cannot commit
		// in read-only mode, keep note in memory.
		return nil
	}
	if !errors.Is(prevErr, os.ErrNotExist) {
		if qErr := s.quarantine(prev, "state-prev"); qErr != nil {
			return qErr
		}
		events = append(events, RecoveryEvent{Time: time.Now().UTC(), Scope: "state",
			Message: "previous state also unreadable (" + prevErr.Error() + "); quarantined"})
	}
	if errors.Is(curErr, os.ErrNotExist) && errors.Is(prevErr, os.ErrNotExist) {
		s.st = newState()
		return nil
	}
	// Both corrupt: start empty-ish but block mutations; retain nothing.
	s.st = newState()
	s.st.RecoveryEvents = append(s.st.RecoveryEvents, events...)
	s.degraded = true
	s.readOnly = true
	return nil
}

func readState(path string) (*State, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var env envelope
	if err := json.Unmarshal(b, &env); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	sum := sha256.Sum256(env.Payload)
	if "sha256:"+hex.EncodeToString(sum[:]) != env.SHA256 {
		return nil, fmt.Errorf("%w: checksum mismatch", ErrCorrupt)
	}
	if !json.Valid(env.Payload) {
		return nil, fmt.Errorf("%w: invalid payload", ErrCorrupt)
	}
	var st State
	if err := json.Unmarshal(env.Payload, &st); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if st.RoleByLang == nil {
		st.RoleByLang = map[string]string{}
	}
	if st.CurrentByLang == nil {
		st.CurrentByLang = map[string]string{}
	}
	return &st, nil
}

// commit runs mutate against a deep copy; on success it atomically persists.
func (s *Store) commit(mutate func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readOnly {
		return ErrReadOnly
	}
	next := deepCopy(s.st)
	if err := mutate(next); err != nil {
		return err
	}
	next.Seq++
	if err := s.persist(next); err != nil {
		return err
	}
	s.st = next
	return nil
}

func (s *Store) persist(st *State) error {
	payload, err := marshalState(st)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(payload)
	env := envelope{Seq: st.Seq, SHA256: "sha256:" + hex.EncodeToString(sum[:]), Payload: payload}
	data, err := json.Marshal(&env)
	if err != nil {
		return err
	}
	stateDir := filepath.Join(s.root, dirState)
	cur := filepath.Join(stateDir, stateFile)
	prev := filepath.Join(stateDir, prevFile)
	tmp := filepath.Join(stateDir, stateFile+tmpSuffix)
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if _, err := os.Stat(cur); err == nil {
		// rotate current -> prev atomically (rename is atomic on same fs)
		if err := os.Rename(cur, prev); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	if err := os.Rename(tmp, cur); err != nil {
		// Try to restore prev so we never leave no current.
		_ = os.Rename(prev, cur)
		return err
	}
	return nil
}

// (payload checksum is computed in-memory above; the rename publishes it)

func (s *Store) quarantine(path, label string) error {
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	dst := filepath.Join(s.root, dirQuar, fmt.Sprintf("%s-%d.json", label, time.Now().UnixNano()))
	return os.Rename(path, dst)
}

// sweepTempFiles removes and reports leftover tmp files from a crash.
func (s *Store) sweepTempFiles() error {
	events := []RecoveryEvent{}
	for _, dir := range []string{dirState, dirRaw, dirProofs} {
		full := filepath.Join(s.root, dir)
		entries, err := os.ReadDir(full)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != tmpSuffix {
				continue
			}
			path := filepath.Join(full, e.Name())
			if err := s.quarantine(path, "tmp-"+filepath.Base(dir)); err == nil {
				events = append(events, RecoveryEvent{Time: time.Now().UTC(), Scope: dir,
					Message: "removed incomplete temporary file: " + e.Name()})
			}
		}
	}
	if len(events) > 0 {
		s.st.RecoveryEvents = append(s.st.RecoveryEvents, events...)
	}
	return nil
}

// verifyBlobs checks every referenced raw/proof blob exists and hashes right.
func (s *Store) verifyBlobs() error {
	events := []RecoveryEvent{}
	check := func(blobName, wantHash, scope, id string) {
		if blobName == "" {
			return
		}
		path := s.blobPath(blobName)
		f, err := os.Open(path)
		if err != nil {
			events = append(events, RecoveryEvent{Time: time.Now().UTC(), Scope: scope,
				Message: id + ": referenced artifact missing (" + blobName + ")"})
			return
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			events = append(events, RecoveryEvent{Time: time.Now().UTC(), Scope: scope,
				Message: id + ": artifact unreadable: " + err.Error()})
			return
		}
		if "sha256:"+hex.EncodeToString(h.Sum(nil)) != wantHash {
			events = append(events, RecoveryEvent{Time: time.Now().UTC(), Scope: scope,
				Message: id + ": artifact checksum mismatch (" + blobName + ")"})
		}
	}
	for _, u := range s.st.Uploads {
		check(u.BlobName, u.SHA256, "raw-upload", u.ID)
	}
	for _, p := range s.st.Proofs {
		check(p.BlobName, p.SHA256, "proof", p.ID)
	}
	if len(events) > 0 {
		s.st.RecoveryEvents = append(s.st.RecoveryEvents, events...)
		s.degraded = true
		s.readOnly = true
	}
	return nil
}

// --- raw blob handling ---

// WriteRaw stores raw bytes content-addressed by hash and returns blob name.
func (s *Store) WriteRaw(name string, data []byte) (blobName string, sum string, err error) {
	h := sha256.Sum256(data)
	sum = "sha256:" + hex.EncodeToString(h[:])
	blobName = "raw-" + sum[len("sha256:"):][:16] + "-" + safeName(name)
	path := s.blobPath(blobName)
	if _, statErr := os.Stat(path); statErr == nil {
		return blobName, sum, nil
	}
	tmp := path + tmpSuffix
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", "", err
	}
	return blobName, sum, nil
}

// ReadRaw reads a stored raw artifact.
func (s *Store) ReadRaw(blobName string) ([]byte, error) {
	return os.ReadFile(s.blobPath(blobName))
}

func (s *Store) blobPath(blobName string) string {
	// Blob names are namespaced by subdir selection done by caller using root.
	if len(blobName) >= 5 && blobName[:5] == "proof" {
		return filepath.Join(s.root, dirProofs, filepath.Base(blobName))
	}
	if len(blobName) >= 3 && blobName[:3] == "raw" {
		return filepath.Join(s.root, dirRaw, filepath.Base(blobName))
	}
	return filepath.Join(s.root, dirRaw, filepath.Base(blobName))
}

// WriteProof stores a proof blob and returns its name.
func (s *Store) WriteProof(data []byte) (blobName, sum string, size int64, err error) {
	h := sha256.Sum256(data)
	sum = "sha256:" + hex.EncodeToString(h[:])
	blobName = "proof-" + sum[len("sha256:"):][:16] + ".json"
	path := filepath.Join(s.root, dirProofs, blobName)
	if _, statErr := os.Stat(path); statErr == nil {
		return blobName, sum, int64(len(data)), nil
	}
	tmp := path + tmpSuffix
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", "", 0, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", "", 0, err
	}
	return blobName, sum, int64(len(data)), nil
}

// ReadProof reads a stored proof artifact.
func (s *Store) ReadProof(blobName string) ([]byte, error) {
	return os.ReadFile(filepath.Join(s.root, dirProofs, filepath.Base(blobName)))
}

func safeName(name string) string {
	var b []byte
	base := filepath.Base(name)
	for i := 0; i < len(base); i++ {
		c := base[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
			b = append(b, c)
		default:
			b = append(b, '_')
		}
	}
	if len(b) == 0 {
		return "file"
	}
	if len(b) > 80 {
		b = b[len(b)-80:]
	}
	return string(b)
}

// SortedVersions returns version records sorted by language then creation.
func (s *Store) SortedVersions() []VersionRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]VersionRecord(nil), s.st.Versions...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Language != out[j].Language {
			return out[i].Language < out[j].Language
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

// Degraded reports recovery mode.
func (s *Store) Degraded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.degraded
}

// ReadOnly reports whether mutations are blocked.
func (s *Store) ReadOnly() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readOnly
}

// RecoveryEvents returns a copy of startup recovery events.
func (s *Store) RecoveryEvents() []RecoveryEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]RecoveryEvent(nil), s.st.RecoveryEvents...)
}

// Snapshot returns a deep copy of the state for consistent reads.
func (s *Store) Snapshot() *State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return deepCopy(s.st)
}

// Seq returns the current state sequence.
func (s *Store) Seq() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.Seq
}

// Commit exposes the atomic mutator for the service layer.
func (s *Store) Commit(mutate func(*State) error) error { return s.commit(mutate) }

// marshalState produces the canonical payload bytes whose checksum is stored.
func marshalState(st *State) ([]byte, error) {
	// Encode compact and deterministic.
	return json.Marshal(st)
}
