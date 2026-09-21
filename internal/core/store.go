package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Store owns all durable state. Every mutation is committed by atomically
// replacing state.json (tmp file + fsync + rename, previous copy kept as
// state.json.bak). Blobs and proofs are content-addressed files committed
// the same way, so a crash can only ever leave a fully old or fully new
// visible state; leftover *.tmp files are cleaned on open and reported.
type Store struct {
	dir     string
	st      *State
	notices []string
}

type envelope struct {
	SHA256  string          `json:"sha256"`
	Payload json.RawMessage `json:"payload"`
}

func Open(dir string) (*Store, error) {
	s := &Store{dir: dir}
	for _, sub := range []string{"", "blobs", "proofs"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, err
		}
	}
	s.cleanTempFiles("")
	s.cleanTempFiles("blobs")
	s.cleanTempFiles("proofs")

	st, notice, err := s.loadState()
	if err != nil {
		return nil, err
	}
	if notice != "" {
		s.notices = append(s.notices, notice)
	}
	if st == nil {
		st = NewState()
	}
	s.st = st
	s.verifyLatestProof()
	return s, nil
}

func (s *Store) cleanTempFiles(sub string) {
	dir := filepath.Join(s.dir, sub)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			os.Remove(filepath.Join(dir, e.Name()))
			s.notices = append(s.notices, fmt.Sprintf("recovery: removed incomplete temp file %s", filepath.Join(sub, e.Name())))
		}
	}
}

func (s *Store) loadState() (*State, string, error) {
	mainPath := filepath.Join(s.dir, "state.json")
	bakPath := filepath.Join(s.dir, "state.json.bak")

	st, mainErr := readStateFile(mainPath)
	if mainErr == nil {
		return st, "", nil
	}
	if os.IsNotExist(mainErr) {
		st, bakErr := readStateFile(bakPath)
		if bakErr == nil {
			return st, "recovery: state.json missing, restored last consistent state from state.json.bak", nil
		}
		return nil, "", nil // fresh start, no notice needed
	}
	// main exists but is corrupt/undecodable: never silently reset.
	st, bakErr := readStateFile(bakPath)
	if bakErr == nil {
		return st, fmt.Sprintf("recovery: state.json unreadable (%v); restored previous consistent state from state.json.bak", mainErr), nil
	}
	return nil, "", fmt.Errorf("state.json and state.json.bak are both unreadable; refusing to silently reset data: %v", mainErr)
}

func readStateFile(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("decode envelope: %w", err)
	}
	sum := sha256.Sum256(env.Payload)
	if hex.EncodeToString(sum[:]) != env.SHA256 {
		return nil, fmt.Errorf("checksum mismatch")
	}
	var st State
	if err := json.Unmarshal(env.Payload, &st); err != nil {
		return nil, fmt.Errorf("decode state: %w", err)
	}
	st.ensure()
	return &st, nil
}

// Save atomically commits the current state.
func (s *Store) Save() error {
	payload, err := json.Marshal(s.st)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(payload)
	env, err := json.Marshal(envelope{SHA256: hex.EncodeToString(sum[:]), Payload: payload})
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.dir, "state.json.tmp")
	if err := writeFileAtomic(tmp, env); err != nil {
		return err
	}
	main := filepath.Join(s.dir, "state.json")
	bak := filepath.Join(s.dir, "state.json.bak")
	if _, err := os.Stat(main); err == nil {
		if err := os.Rename(main, bak); err != nil {
			return err
		}
	}
	return os.Rename(tmp, main)
}

func writeFileAtomic(tmp string, data []byte) error {
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// WriteBlob stores raw upload bytes content-addressed; returns the hash.
func (s *Store) WriteBlob(data []byte) (string, error) {
	sum := sha256.Sum256(data)
	h := hex.EncodeToString(sum[:])
	path := filepath.Join(s.dir, "blobs", h)
	if _, err := os.Stat(path); err == nil {
		return h, nil
	}
	tmp := path + ".tmp"
	if err := writeFileAtomic(tmp, data); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	return h, nil
}

func (s *Store) ReadBlob(hash string) ([]byte, error) {
	return os.ReadFile(filepath.Join(s.dir, "blobs", hash))
}

// WriteProof atomically commits a proof document. The caller updates the
// latest-proof pointer in state only after this succeeds, so a crash can
// never expose a truncated proof as valid.
func (s *Store) WriteProof(id string, data []byte) error {
	path := filepath.Join(s.dir, "proofs", id+".json")
	tmp := path + ".tmp"
	if err := writeFileAtomic(tmp, data); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Store) ReadProof(id string) ([]byte, error) {
	return os.ReadFile(filepath.Join(s.dir, "proofs", id+".json"))
}

func (s *Store) verifyLatestProof() {
	latest := s.st.LatestProof
	if latest == "" {
		return
	}
	data, err := s.ReadProof(latest)
	if err != nil {
		s.notices = append(s.notices, fmt.Sprintf("recovery: latest proof %s unreadable, pointer cleared (proofs are regenerable)", latest))
		s.st.LatestProof = ""
		return
	}
	var probe map[string]any
	if err := json.Unmarshal(data, &probe); err != nil {
		s.notices = append(s.notices, fmt.Sprintf("recovery: latest proof %s is corrupt, pointer cleared", latest))
		s.st.LatestProof = ""
	}
}

func (s *Store) State() *State     { return s.st }
func (s *Store) Notices() []string { return append([]string(nil), s.notices...) }

// sortedKeys is a small helper used for deterministic hashing.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
