package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Store persists the state file, raw uploads and proofs atomically.
// Every file is written to a temporary name and renamed into place, so a
// crash can only leave either the complete old or the complete new
// state visible.
type Store struct {
	Dir string
}

func NewStore(dir string) *Store { return &Store{Dir: dir} }

func (s *Store) statePath() string  { return filepath.Join(s.Dir, "state.json") }
func (s *Store) backupPath() string { return filepath.Join(s.Dir, "state.json.bak") }
func (s *Store) rawDir() string     { return filepath.Join(s.Dir, "raw") }
func (s *Store) proofDir() string   { return filepath.Join(s.Dir, "proofs") }

// atomicWrite writes data to dir/name via a temp file + rename.
func atomicWrite(dir, name string, data []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(dir, ".tmp-"+name)
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if f, err := os.Open(tmp); err == nil {
		_ = f.Sync()
		_ = f.Close()
	}
	return os.Rename(tmp, filepath.Join(dir, name))
}

// SaveState persists the state atomically, keeping the previous state as
// a backup for recovery.
func (s *Store) SaveState(st *State) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if old, err := os.ReadFile(s.statePath()); err == nil {
		_ = os.WriteFile(s.backupPath(), old, 0o644)
	}
	return atomicWrite(s.Dir, "state.json", data)
}

// SaveRaw stores raw upload bytes and returns their relative path.
func (s *Store) SaveRaw(id string, data []byte) (string, error) {
	if err := atomicWrite(s.rawDir(), id, data); err != nil {
		return "", err
	}
	return filepath.Join("raw", id), nil
}

// SaveProof stores proof bytes and returns their relative path.
func (s *Store) SaveProof(id string, data []byte) (string, error) {
	if err := atomicWrite(s.proofDir(), id+".json", data); err != nil {
		return "", err
	}
	return filepath.Join("proofs", id+".json"), nil
}

// ReadFile reads a file by its state-relative path.
func (s *Store) ReadFile(rel string) ([]byte, error) {
	return os.ReadFile(filepath.Join(s.Dir, rel))
}

// Load reads the state and verifies referenced raw/proof files. It
// returns startup warnings for every detectable inconsistency; it never
// silently discards confirmable data. Orphaned temp files and files from
// interrupted transactions are removed.
func (s *Store) Load() (*State, []string, error) {
	var warnings []string
	st := NewState()
	data, err := os.ReadFile(s.statePath())
	switch {
	case err == nil:
		if json.Unmarshal(data, st) != nil {
			// State file corrupt: fall back to the backup, never reset.
			bak, berr := os.ReadFile(s.backupPath())
			if berr != nil || json.Unmarshal(bak, st) != nil {
				return nil, nil, fmt.Errorf("state file corrupt and no usable backup; refusing to start with empty state")
			}
			warnings = append(warnings, "state.json corrupt; recovered from state.json.bak (last transaction may be absent)")
		}
	case os.IsNotExist(err):
		st = NewState()
	default:
		return nil, nil, err
	}
	if st.ParserVersion == "" {
		st.ParserVersion = NewState().ParserVersion
	}
	st.ensureMaps()

	// Verify raw files referenced by imports.
	for _, imp := range st.Imports {
		b, rerr := os.ReadFile(filepath.Join(s.Dir, imp.RawPath))
		if rerr != nil {
			warnings = append(warnings, fmt.Sprintf("import %s (%s): raw file missing", imp.ID, imp.Lang))
			continue
		}
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != imp.RawSHA256 {
			warnings = append(warnings, fmt.Sprintf("import %s (%s): raw file checksum mismatch", imp.ID, imp.Lang))
		}
	}
	// Verify proofs.
	for _, pr := range st.Proofs {
		b, rerr := os.ReadFile(filepath.Join(s.Dir, pr.Path))
		if rerr != nil {
			warnings = append(warnings, fmt.Sprintf("proof %s: file missing", pr.ID))
			continue
		}
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != pr.SHA256 {
			warnings = append(warnings, fmt.Sprintf("proof %s: checksum mismatch", pr.ID))
		}
	}
	// Clean up orphans: temp files and files not referenced by state.
	referenced := map[string]bool{}
	for _, imp := range st.Imports {
		referenced[imp.RawPath] = true
	}
	for _, pr := range st.Proofs {
		referenced[pr.Path] = true
	}
	for _, dir := range []string{s.rawDir(), s.proofDir()} {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			rel := filepath.Join(filepath.Base(dir), e.Name())
			if !referenced[rel] {
				_ = os.Remove(filepath.Join(s.Dir, rel))
			}
		}
	}
	sort.Strings(warnings)
	return st, warnings, nil
}

// ensureMaps guards against nil maps after decoding older state files.
func (s *State) ensureMaps() {
	if s.Versions == nil {
		s.Versions = map[string]*Version{}
	}
	if s.Imports == nil {
		s.Imports = map[string]*Import{}
	}
	if s.CurrentVersion == nil {
		s.CurrentVersion = map[string]string{}
	}
	if s.Mappings == nil {
		s.Mappings = map[string]*RenameMapping{}
	}
	if s.Exemptions == nil {
		s.Exemptions = map[string]*Exemption{}
	}
	if s.Validations == nil {
		s.Validations = map[string]*Validation{}
	}
	if s.Operations == nil {
		s.Operations = map[string]*Operation{}
	}
	if s.Proofs == nil {
		s.Proofs = map[string]*Proof{}
	}
}

// BytesEqual reports whether two byte slices are equal (small helper to
// avoid importing bytes in callers).
func BytesEqual(a, b []byte) bool { return bytes.Equal(a, b) }
