package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"catalogcheck/internal/catalog"
	"catalogcheck/internal/icu"
	"catalogcheck/internal/validate"
)

// ErrConflict means the caller's precondition (version or mapping state) no
// longer matches the committed state.
var ErrConflict = errors.New("conflict: underlying state has changed")

// ErrIdemMismatch means an operation id was reused with different content.
var ErrIdemMismatch = errors.New("idempotency conflict: same operation id with different content")

// Failure describes one integrity problem detected at startup.
type Failure struct {
	Scope  string `json:"scope"`
	Detail string `json:"detail"`
}

const formatVersion = 3

// Store is the durable state machine.
type Store struct {
	mu  sync.Mutex
	dir string
	now func() time.Time
	st  *State
}

type envelope struct {
	FormatVersion int    `json:"format_version"`
	SHA256        string `json:"sha256"`
	Body          []byte `json:"body"`
}

// Open loads the store from dir, creating it when absent.
func Open(dir string) (*Store, error) {
	st := &Store{dir: dir, now: time.Now}
	if err := os.MkdirAll(filepath.Join(dir, "blobs", "raw"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "blobs", "canonical"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "blobs", "results"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "blobs", "certs"), 0o755); err != nil {
		return nil, err
	}
	if err := st.load(); err != nil {
		return nil, err
	}
	return st, nil
}

func (s *Store) statePath() string  { return filepath.Join(s.dir, "state.json") }
func (s *Store) backupPath() string { return filepath.Join(s.dir, "state.json.bak") }

func (s *Store) load() error {
	stateBytes, src, err := readEnvelope(s.statePath(), s.backupPath())
	if err != nil {
		if errors.Is(err, errNoState) {
			s.st = s.freshState()
			return nil
		}
		return err
	}
	var st State
	if err := json.Unmarshal(stateBytes, &st); err != nil {
		return fmt.Errorf("state body is unreadable; refusing to reset data (%s): %w", src, err)
	}
	s.st = &st
	failures := s.verifyBlobs()
	if len(failures) > 0 {
		var msgs []string
		for _, f := range failures {
			msgs = append(msgs, f.Scope+": "+f.Detail)
		}
		s.st.Warnings = append(s.st.Warnings, "启动时发现完整性问题（已保留完整数据，受影响项被标记）: "+strings.Join(msgs, "; "))
		if err := s.commitLocked(); err != nil {
			return err
		}
	}
	return nil
}

var errNoState = errors.New("no state file")

func readEnvelope(primary, backup string) ([]byte, string, error) {
	bp, perr := os.ReadFile(primary)
	bb, berr := os.ReadFile(backup)
	if perr != nil && berr != nil {
		if os.IsNotExist(perr) && os.IsNotExist(berr) {
			return nil, "", errNoState
		}
	}
	try := func(b []byte, name string) ([]byte, error) {
		var env envelope
		if err := json.Unmarshal(b, &env); err != nil {
			return nil, fmt.Errorf("%s envelope truncated or corrupt: %w", name, err)
		}
		sum := sha256.Sum256(env.Body)
		if hex.EncodeToString(sum[:]) != env.SHA256 {
			return nil, fmt.Errorf("%s checksum mismatch", name)
		}
		if env.FormatVersion != formatVersion {
			return nil, fmt.Errorf("%s unsupported format version %d", name, env.FormatVersion)
		}
		if !json.Valid(env.Body) {
			return nil, fmt.Errorf("%s body is not valid JSON", name)
		}
		return env.Body, nil
	}
	if perr == nil {
		if body, err := try(bp, "state.json"); err == nil {
			return body, "state.json", nil
		} else if berr == nil {
			if body, berr2 := try(bb, "state.json.bak"); berr2 == nil {
				return body, "state.json.bak", nil
			} else {
				return nil, "", fmt.Errorf("state.json damaged (%v) and backup damaged (%v)", err, berr2)
			}
		} else {
			return nil, "", err
		}
	}
	if berr == nil {
		if body, err := try(bb, "state.json.bak"); err == nil {
			return body, "state.json.bak", nil
		} else {
			return nil, "", fmt.Errorf("state.json unreadable and backup damaged (%v)", err)
		}
	}
	return nil, "", perr
}

func (s *Store) freshState() *State {
	return &State{
		FormatVersion: formatVersion,
		ParserVersion: icu.Version,
		LangVersion:   map[string]string{},
		Versions:      map[string]VersionRef{},
		Uploads:       map[string]Upload{},
		Results:       map[string]ResultMeta{},
		Idem:          map[string]IdemRecord{},
	}
}

// verifyBlobs checks every referenced blob exists and hashes correctly.
func (s *Store) verifyBlobs() []Failure {
	var fails []Failure
	for id, up := range s.st.Uploads {
		p := filepath.Join(s.dir, up.BlobPath)
		ok, reason := blobOK(p, up.SHA256)
		if !ok {
			up.Missing = true
			s.st.Uploads[id] = up
			for vid, v := range s.st.Versions {
				for _, uid := range v.UploadIDs {
					if uid == id {
						v.BlobMissing = true
						s.st.Versions[vid] = v
					}
				}
			}
			fails = append(fails, Failure{Scope: "raw/" + id, Detail: reason})
		}
	}
	for id, v := range s.st.Versions {
		if v.CanonicalBlob == "" {
			continue
		}
		p := filepath.Join(s.dir, v.CanonicalBlob)
		if _, err := os.Stat(p); err != nil {
			v.BlobMissing = true
			s.st.Versions[id] = v
			fails = append(fails, Failure{Scope: "canonical/" + id, Detail: "canonical blob missing: " + err.Error()})
		}
	}
	for id, r := range s.st.Results {
		p := filepath.Join(s.dir, r.BlobPath)
		if _, err := os.Stat(p); err != nil {
			r.BlobMissing = true
			s.st.Results[id] = r
			fails = append(fails, Failure{Scope: "result/" + r.ID, Detail: err.Error()})
		}
	}
	for i := range s.st.Certs {
		c := s.st.Certs[i]
		ok, reason := blobOK(filepath.Join(s.dir, c.BlobPath), c.SHA256)
		if !ok {
			c.BlobMissing = true
			s.st.Certs[i] = c
			fails = append(fails, Failure{Scope: "cert/" + c.ID, Detail: reason})
		}
	}
	sort.Slice(fails, func(i, j int) bool { return fails[i].Scope < fails[j].Scope })
	return fails
}

func blobOK(path, wantSHA string) (bool, string) {
	f, err := os.Open(path)
	if err != nil {
		return false, err.Error()
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err.Error()
	}
	if hex.EncodeToString(h.Sum(nil)) != wantSHA {
		return false, "checksum mismatch"
	}
	return true, ""
}

// commitLocked serializes state and performs a durable two-file swap.
func (s *Store) computeRootLocked() string {
	type rootDoc struct {
		ParserVersion    string                 `json:"parser_version"`
		BaselineLanguage string                 `json:"baseline_language"`
		LangVersion      map[string]string      `json:"lang_version"`
		Versions         map[string]VersionRef  `json:"versions"`
		Edges            []validate.MappingEdge `json:"edges"`
		Exemptions       []Exemption            `json:"exemptions"`
		Results          map[string]ResultMeta  `json:"results"`
	}
	b, _ := json.Marshal(rootDoc{
		ParserVersion:    s.st.ParserVersion,
		BaselineLanguage: s.st.BaselineLanguage,
		LangVersion:      s.st.LangVersion,
		Versions:         s.st.Versions,
		Edges:            s.st.Edges,
		Exemptions:       s.st.Exemptions,
		Results:          s.st.Results,
	})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (s *Store) commitLocked() error {
	s.st.Root = s.computeRootLocked()
	body, err := json.Marshal(s.st)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	env, err := json.Marshal(envelope{FormatVersion: formatVersion, SHA256: hex.EncodeToString(sum[:]), Body: body})
	if err != nil {
		return err
	}
	statePath := s.statePath()
	backupPath := s.backupPath()
	if _, err := os.Stat(statePath); err == nil {
		// Preserve the last complete state before replacing it.
		if err := copyFile(statePath, backupPath); err != nil {
			return err
		}
		if err := fsyncDir(s.dir); err != nil {
			return err
		}
	}
	tmp := statePath + ".tmp"
	if err := os.WriteFile(tmp, env, 0o644); err != nil {
		return err
	}
	if err := fsyncFile(tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, statePath); err != nil {
		return err
	}
	if err := fsyncDir(s.dir); err != nil {
		return err
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func fsyncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func fsyncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// writeBlob stores content content-addressed under prefix and returns its
// relative path plus sha256. Existing identical blobs are reused.
func (s *Store) writeBlob(prefix string, data []byte) (rel, sha string, err error) {
	sum := sha256.Sum256(data)
	sha = hex.EncodeToString(sum[:])
	name := sha
	rel = filepath.ToSlash(filepath.Join("blobs", prefix, name))
	abs := filepath.Join(s.dir, rel)
	if _, statErr := os.Stat(abs); statErr == nil {
		return rel, sha, nil
	}
	tmpDir := filepath.Join(s.dir, "blobs", prefix)
	tmp, err := os.CreateTemp(tmpDir, "tmp-*")
	if err != nil {
		return "", "", err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return "", "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return "", "", err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", "", err
	}
	if err := os.Rename(tmpName, abs); err != nil {
		cleanup()
		return "", "", err
	}
	if err := fsyncDir(tmpDir); err != nil {
		return "", "", err
	}
	return rel, sha, nil
}

func (s *Store) readBlob(rel string) ([]byte, error) {
	return os.ReadFile(filepath.Join(s.dir, rel))
}

func (s *Store) saveCanonical(c *catalog.Catalog) (rel, sha string, err error) {
	return s.writeBlob("canonical", c.CanonicalJSON())
}

func (s *Store) saveResult(data *validate.ResultData) (rel string, err error) {
	b, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	rel, _, err = s.writeBlob("results", b)
	return rel, err
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Snapshot returns a stable identifier for the current committed state.
func (s *Store) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.snapshot()
}

// StateJSON returns the full state for debugging.
func (s *Store) StateJSON() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := json.MarshalIndent(s.st, "", "  ")
	return b
}

func (s *Store) nowTime() time.Time { return s.now().UTC() }

// cloneState returns a deep copy used to build a candidate commit.
func cloneState(st *State) *State {
	b, _ := json.Marshal(st)
	var cp State
	_ = json.Unmarshal(b, &cp)
	return &cp
}

var _ = bytes.TrimSpace
