// Package store persists the domain state to local files with atomic
// commits, checksum verification and crash recovery.
//
// Layout inside the data directory:
//
//	state.json        current committed state (checksum envelope)
//	state.prev.json   previous committed state (fallback)
//	blobs/<sha256>    raw uploaded files, content addressed
//	proofs/<id>.json  generated release proofs
//
// Every commit writes a tmp file, fsyncs, then renames. A crash therefore
// leaves either the full old state or the full new state; stray tmp files
// are discarded at startup. A corrupted main state falls back to the
// previous committed state and is reported, never silently reset.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"msgcat/internal/core"
)

type envelope struct {
	Hash    string          `json:"hash"`
	Payload json.RawMessage `json:"payload"`
}

// Store manages the data directory.
type Store struct {
	Dir      string
	State    *core.State
	Warnings []string
}

// Open loads the store, recovering from interrupted commits and reporting
// any detectable integrity problems via Warnings.
func Open(dir string) (*Store, error) {
	s := &Store{Dir: dir}
	for _, sub := range []string{"", "blobs", "proofs"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, err
		}
	}
	s.cleanTemp()
	state, warn, err := s.loadState()
	if err != nil {
		return nil, err
	}
	s.State = state
	s.Warnings = append(s.Warnings, warn...)
	s.verifyBlobs()
	return s, nil
}

func (s *Store) cleanTemp() {
	_ = filepath.Walk(s.Dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(path, ".tmp") {
			_ = os.Remove(path)
		}
		return nil
	})
}

func readEnvelope(path string) (*core.State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("not a state envelope: %w", err)
	}
	sum := sha256.Sum256(env.Payload)
	if hex.EncodeToString(sum[:]) != env.Hash {
		return nil, fmt.Errorf("checksum mismatch")
	}
	var st core.State
	if err := json.Unmarshal(env.Payload, &st); err != nil {
		return nil, fmt.Errorf("state payload unreadable: %w", err)
	}
	return &st, nil
}

func (s *Store) loadState() (*core.State, []string, error) {
	mainPath := filepath.Join(s.Dir, "state.json")
	prevPath := filepath.Join(s.Dir, "state.prev.json")
	var warnings []string
	if _, err := os.Stat(mainPath); err == nil {
		st, err := readEnvelope(mainPath)
		if err == nil {
			return st, warnings, nil
		}
		warnings = append(warnings, fmt.Sprintf("state.json 校验失败 (%v)，回退到上一个已提交状态", err))
		if st2, err2 := readEnvelope(prevPath); err2 == nil {
			warnings = append(warnings, "已恢复 state.prev.json；最近一次的修改未提交，已丢弃")
			return st2, warnings, nil
		}
		return nil, warnings, fmt.Errorf("state.json 与 state.prev.json 均不可用，拒绝静默重置数据")
	}
	if _, err := os.Stat(prevPath); err == nil {
		st, err := readEnvelope(prevPath)
		if err == nil {
			warnings = append(warnings, "state.json 缺失，已从 state.prev.json 恢复")
			return st, warnings, nil
		}
		return nil, warnings, fmt.Errorf("state.prev.json 存在但校验失败，拒绝静默重置数据")
	}
	return core.NewState(), warnings, nil
}

// verifyBlobs checks that every blob referenced by the state exists.
func (s *Store) verifyBlobs() {
	for _, id := range core.SortedKeys(s.State.Imports) {
		imp := s.State.Imports[id]
		if _, err := os.Stat(s.blobPath(imp.BlobHash)); err != nil {
			s.Warnings = append(s.Warnings,
				fmt.Sprintf("导入 %s 的原始文件 blob 缺失 (hash=%s)，该文件将无法下载", imp.ID, imp.BlobHash))
		}
	}
	for _, id := range core.SortedKeys(s.State.Proofs) {
		if _, err := os.Stat(s.ProofPath(id)); err != nil {
			s.Warnings = append(s.Warnings,
				fmt.Sprintf("证明 %s 的文件缺失，已将其标记为无效", id))
			delete(s.State.Proofs, id)
		}
	}
}

// Commit atomically persists the in-memory state.
func (s *Store) Commit() error {
	payload, err := json.Marshal(s.State)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(payload)
	env, err := json.Marshal(envelope{Hash: hex.EncodeToString(sum[:]), Payload: payload})
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.Dir, "state.json.tmp")
	if err := writeFileSync(tmp, env); err != nil {
		return err
	}
	mainPath := filepath.Join(s.Dir, "state.json")
	prevPath := filepath.Join(s.Dir, "state.prev.json")
	if _, err := os.Stat(mainPath); err == nil {
		if err := os.Rename(mainPath, prevPath); err != nil {
			return err
		}
	}
	if err := os.Rename(tmp, mainPath); err != nil {
		return err
	}
	return syncDir(s.Dir)
}

func writeFileSync(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (s *Store) blobPath(hash string) string {
	return filepath.Join(s.Dir, "blobs", hash)
}

// WriteBlob stores raw bytes content-addressed and returns the hash.
// Existing blobs are kept; the write is atomic.
func (s *Store) WriteBlob(data []byte) (string, error) {
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	path := s.blobPath(hash)
	if _, err := os.Stat(path); err == nil {
		return hash, nil
	}
	tmp := path + ".tmp"
	if err := writeFileSync(tmp, data); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	return hash, syncDir(filepath.Join(s.Dir, "blobs"))
}

// ReadBlob returns the raw bytes for a hash.
func (s *Store) ReadBlob(hash string) ([]byte, error) {
	return os.ReadFile(s.blobPath(hash))
}

// ProofPath returns the path of a stored proof.
func (s *Store) ProofPath(id string) string {
	return filepath.Join(s.Dir, "proofs", id+".json")
}

// WriteProof atomically stores proof bytes.
func (s *Store) WriteProof(id string, data []byte) error {
	path := s.ProofPath(id)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	tmp := path + ".tmp"
	if err := writeFileSync(tmp, data); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDir(filepath.Join(s.Dir, "proofs"))
}

// ReadProof returns stored proof bytes.
func (s *Store) ReadProof(id string) ([]byte, error) {
	return os.ReadFile(s.ProofPath(id))
}

// SortedProofIDs lists proof IDs in deterministic order.
func (s *Store) SortedProofIDs() []string {
	ids := make([]string, 0, len(s.State.Proofs))
	for id := range s.State.Proofs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
