// Package service implements the catalog workbench business logic on top of
// the local crash-safe store.
package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"messagecatalog/internal/catalog"
	"messagecatalog/internal/icu"
	"messagecatalog/internal/store"
)

// Clock allows tests/demo to control time deterministically.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

// ConflictError signals an optimistic/state precondition failure.
type ConflictError struct{ Msg string }

func (e *ConflictError) Error() string { return e.Msg }

// RequestError signals a malformed request.
type RequestError struct{ Msg string }

func (e *RequestError) Error() string { return e.Msg }

// Service holds the working state.
type Service struct {
	mu      sync.RWMutex
	st      *store.Store
	state   *State
	version int
	dataDir string

	clockMu sync.Mutex
	clock   Clock

	degraded     bool
	integrityMsg string
}

// New opens or initializes a service rooted at dataDir.
func New(dataDir string) (*Service, error) {
	st, err := store.Open(dataDir)
	if err != nil {
		return nil, err
	}
	s := &Service{st: st, state: newState(), dataDir: dataDir, clock: realClock{}}
	ver, err := st.Load(s.state)
	if err != nil {
		return nil, err
	}
	s.version = ver
	if err := s.bootVerify(); err != nil {
		s.degraded = true
		s.integrityMsg = err.Error()
	}
	return s, nil
}

func newState() *State {
	return &State{
		Uploads:    map[string]*Upload{},
		Catalogs:   map[string]*CatalogVersion{},
		Mapping:    map[string]string{},
		Exemptions: map[string]*Exemption{},
		Operations: map[string]*Operation{},
		Batches:    map[string]*Batch{},
		Reports:    map[string]*ReportRecord{},
	}
}

// Now returns the (possibly controlled) time.
func (s *Service) Now() time.Time {
	s.clockMu.Lock()
	defer s.clockMu.Unlock()
	return s.clock.Now()
}

// SetClock swaps the time source (demo/test support, not persisted).
func (s *Service) SetClock(c Clock) {
	s.clockMu.Lock()
	defer s.clockMu.Unlock()
	s.clock = c
}

func (s *Service) nowRFC() string { return s.Now().Format(time.RFC3339) }

// Degraded reports startup integrity problems.
func (s *Service) Degraded() (bool, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.degraded, s.integrityMsg
}

// bootVerify ensures every blob referenced by state exists and is intact.
func (s *Service) bootVerify() error {
	var ids []string
	for _, u := range s.state.Uploads {
		ids = append(ids, u.RawBlob)
	}
	for _, c := range s.state.Catalogs {
		ids = append(ids, c.Blob)
	}
	for _, r := range s.state.Reports {
		ids = append(ids, r.Blob)
	}
	for _, p := range s.state.Proofs {
		ids = append(ids, p.Blob)
	}
	bad := s.st.VerifyBlobs(ids)
	if len(bad) > 0 {
		short := make([]string, len(bad))
		for i, b := range bad {
			short[i] = store.Short(b)
		}
		return fmt.Errorf("%d referenced blob(s) missing or corrupt: %s; service is read-only until repaired",
			len(bad), strings.Join(short, ", "))
	}
	return nil
}

func (s *Service) checkWritable() error {
	if s.degraded {
		return &ConflictError{Msg: "service is in read-only degraded mode: " + s.integrityMsg}
	}
	return nil
}

// snapshotState is a consistent read-only copy for snapshot semantics.
type snapshotState struct {
	state   *State
	version int
	at      time.Time
}

func (s *Service) readState() snapshotState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return snapshotState{state: s.state, version: s.version, at: s.Now()}
}

// commit stores new blobs first, then atomically replaces state. Blobs are
// content-addressed and never mutated, so a crash before the state swap leaves
// only unreferenced files; a crash during the swap restores the previous
// complete state from the backup envelope.
func (s *Service) commitLocked(note string) (*Snapshot, error) {
	// Seq must already have been incremented for the records being added.
	s.state.ParserVersion = icu.Version
	snap := s.buildSnapshotLocked(note)
	s.state.Snapshots = append(s.state.Snapshots, snap)
	// cap snapshot metadata history
	if len(s.state.Snapshots) > 200 {
		s.state.Snapshots = s.state.Snapshots[len(s.state.Snapshots)-200:]
	}
	newVer := s.version + 1
	if err := s.st.Commit(s.state, s.version, newVer); err != nil {
		s.state.Seq--
		s.state.Snapshots = s.state.Snapshots[:len(s.state.Snapshots)-1]
		return nil, err
	}
	s.version = newVer
	return snap, nil
}

func (s *Service) buildSnapshotLocked(note string) *Snapshot {
	langs := map[string]string{}
	var base string
	for lang, c := range s.state.Catalogs {
		langs[lang] = c.Version
		if lang == s.state.BaselineLanguage {
			base = c.Version
		}
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d|%s|%s|%v", s.state.Seq, base, s.state.MappingVersion, langs)))
	id := "snap-" + hex.EncodeToString(sum[:8])
	return &Snapshot{
		ID: id, Seq: s.state.Seq, CreatedAt: s.nowRFC(),
		BaselineVersion: base, Languages: langs,
		MappingVersion: s.state.MappingVersion, ParserVersion: icu.Version, Note: note,
	}
}

// storeCatalogBlob serializes the parsed canonical catalog into a blob.
func (s *Service) storeCatalogBlob(c *catalog.Catalog) (string, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return s.st.PutBlob(b)
}

// loadCatalogBlob reads a catalog blob.
func (s *Service) loadCatalogBlob(id string) (*catalog.Catalog, error) {
	b, err := s.st.GetBlob(id)
	if err != nil {
		return nil, err
	}
	var c catalog.Catalog
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// tupleSHA identifies a validation basis.
func tupleSHA(parserVersion, baseVer, targetVer string, mappingVersion int, language string) string {
	h := sha256.Sum256([]byte(strings.Join([]string{
		parserVersion, baseVer, targetVer, fmt.Sprint(mappingVersion), language,
	}, "|")))
	return hex.EncodeToString(h[:])
}

// ensureReportLocked returns the cached report for the current tuple or
// computes, persists (before state swap) and caches a fresh one.
func (s *Service) ensureReportLocked(language string) (*Report, string, error) {
	base, ok := s.state.Catalogs[s.state.BaselineLanguage]
	if !ok {
		return nil, "", &RequestError{Msg: "no baseline catalog imported"}
	}
	tv, ok := s.state.Catalogs[language]
	if !ok {
		return nil, "", &RequestError{Msg: "no catalog for language " + language}
	}
	tuple := tupleSHA(icu.Version, base.Version, tv.Version, s.state.MappingVersion, language)
	if rec, ok := s.state.Reports[tuple]; ok {
		b, err := s.st.GetBlob(rec.Blob)
		if err == nil {
			var r Report
			if json.Unmarshal(b, &r) == nil {
				return &r, tuple, nil
			}
		}
		delete(s.state.Reports, tuple)
	}
	bc, err := s.loadCatalogBlob(base.Blob)
	if err != nil {
		return nil, "", err
	}
	tc, err := s.loadCatalogBlob(tv.Blob)
	if err != nil {
		return nil, "", err
	}
	r := validateLanguage(bc, tc, s.state.Mapping, s.state.MappingVersion, s.nowRFC())
	rb, err := json.Marshal(r)
	if err != nil {
		return nil, "", err
	}
	blobID, err := s.st.PutBlob(rb)
	if err != nil {
		return nil, "", err
	}
	s.state.Reports[tuple] = &ReportRecord{
		TupleSHA: tuple, ParserVersion: icu.Version,
		BaselineVersion: base.Version, TargetVersion: tv.Version,
		MappingVersion: s.state.MappingVersion, Language: language,
		Blob: blobID, CreatedAt: s.nowRFC(),
	}
	return r, tuple, nil
}

// reportsLocked computes current reports for every imported language.
func (s *Service) reportsLocked() (map[string]*Report, map[string]string, error) {
	out := map[string]*Report{}
	tuples := map[string]string{}
	langs := append([]string(nil), mapKeys(s.state.Catalogs)...)
	sort.Strings(langs)
	for _, lang := range langs {
		r, tuple, err := s.ensureReportLocked(lang)
		if err != nil {
			return nil, nil, err
		}
		out[lang] = r
		tuples[lang] = tuple
	}
	return out, tuples, nil
}

func mapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// rawSHA is the hash of the exact uploaded bytes.
func rawSHA(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func jsonEqual(a, b string) bool {
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal([]byte(b), &y) != nil {
		return a == b
	}
	return bytes.Equal([]byte(a), []byte(b)) || canonical(x) == canonical(y)
}

func canonical(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
