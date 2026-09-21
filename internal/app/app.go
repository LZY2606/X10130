// Package app holds the application core: validation, versioning, rename
// mappings, exemptions, batch fixes, idempotent operations and release
// certificates, all backed by a local frame-log store.
package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"msgcheck/internal/catalog"
	"msgcheck/internal/icu"
	"msgcheck/internal/state"
	"msgcheck/internal/store"
)

// Clock allows tests to drive deterministic time.
type Clock interface{ Now() time.Time }

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

// ConflictError signals an optimistic-concurrency or idempotency clash.
type ConflictError struct {
	Reason  string
	Current state.Pin
}

func (e *ConflictError) Error() string { return "conflict: " + e.Reason }

// BadRequestError signals invalid caller input.
type BadRequestError struct{ Reason string }

func (e *BadRequestError) Error() string { return e.Reason }

// NotFoundError signals a missing referenced object.
type NotFoundError struct{ Reason string }

func (e *NotFoundError) Error() string { return e.Reason }

// App is the application service.
type App struct {
	st          *store.Store
	mu          sync.Mutex
	data        *state.Data
	clock       Clock
	notes       []store.Note
	parserVer   string

	// CrashCert, when set, injects a crash while writing a cert blob:
	// "tmp" fails after writing the temp file, "swap" before rename.
	CrashCert string
}

// New opens the store and loads the newest complete state.
func New(st *store.Store) (*App, []store.Note, error) {
	notes, err := st.Open()
	if err != nil {
		return nil, notes, err
	}
	a := &App{st: st, clock: wallClock{}, parserVer: icu.Version}
	raw, _, err := st.Load()
	if err != nil {
		return nil, notes, err
	}
	if raw != nil {
		d := state.NewData()
		if err := json.Unmarshal(raw, d); err != nil {
			return nil, notes, fmt.Errorf("unmarshal state frame: %w", err)
		}
		a.data = d
	} else {
		a.data = state.NewData()
	}
	a.notes = notes
	return a, notes, nil
}

// SetClock overrides the clock (tests).
func (a *App) SetClock(c Clock) { a.clock = c }

// SetParserVersion simulates a parser upgrade (tests).
func (a *App) SetParserVersion(v string) {
	a.mu.Lock()
	a.parserVer = v
	a.mu.Unlock()
}

// Notes returns startup recovery notes.
func (a *App) Notes() []store.Note {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]store.Note, len(a.notes))
	copy(out, a.notes)
	return out
}

func (a *App) now() time.Time { return a.clock.Now().UTC().Truncate(time.Second) }

// snapshotHash computes the stable content hash of committed state. Meta
// bookkeeping (idempotent operations, cert index, validation cache) is
// excluded, as it is derived/auxiliary and never changes catalog semantics.
func snapshotHash(d *state.Data) string {
	view := struct {
		Seq         int64
		Baseline    *state.BaselineRec
		Langs       map[string]*state.LangState
		Uploads     []state.Upload
		Renames     []state.Rename
		Exemptions  []state.Exemption
		Batches     map[string]*state.Batch
		CommittedAt time.Time
	}{
		Seq:         d.Seq,
		Baseline:    d.Baseline,
		Langs:       d.Langs,
		Uploads:     d.Uploads,
		Renames:     d.Renames,
		Exemptions:  d.Exemptions,
		Batches:     d.Batches,
		CommittedAt: d.CommittedAt,
	}
	b, _ := json.Marshal(view)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (a *App) pinLocked() state.Pin {
	p := state.Pin{Seq: a.data.Seq, Snapshot: snapshotHash(a.data), Parser: a.parserVer, MappingsFP: mappingsFP(a.data)}
	if a.data.Baseline != nil {
		p.BaselineVer = a.data.Baseline.Version
		p.BaselineLang = a.data.Baseline.Language
	}
	return p
}

// Pin returns the current committed state pin.
func (a *App) Pin() state.Pin {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pinLocked()
}

func (a *App) checkSnapshotLocked(want string) error {
	if want != "" && want != a.pinLocked().Snapshot {
		return &ConflictError{Reason: "state changed since your view was loaded; reload and retry", Current: a.pinLocked()}
	}
	return nil
}

// commitLocked serializes the full state and appends one durable frame.
func (a *App) commitLocked(at time.Time) error {
	a.data.Seq++
	a.data.CommittedAt = at
	b, err := json.Marshal(a.data)
	if err != nil {
		return err
	}
	return a.st.Save(a.data.Seq, b)
}

// advanceLocked applies system-driven state transitions before an
// observation or mutation: exemptions whose deadline has been reached are
// dropped immediately ("到期即视为过期"), producing a new committed state.
func (a *App) advanceLocked() (bool, error) {
	now := a.now()
	kept := a.data.Exemptions[:0]
	changed := false
	for _, ex := range a.data.Exemptions {
		if ex.ExpiresAt != nil && !ex.ExpiresAt.After(now) {
			changed = true
			continue
		}
		kept = append(kept, ex)
	}
	if !changed {
		return false, nil
	}
	a.data.Exemptions = kept
	if err := a.commitLocked(now); err != nil {
		return false, err
	}
	return true, nil
}

// hashBytes is a small helper.
func hashBytes(parts ...[]byte) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// loadCatalogLocked reads and parses a stored catalog blob.
func (a *App) loadCatalogLocked(language, blob string) (*catalog.Catalog, error) {
	raw, err := a.st.ReadBlob(blob)
	if err != nil {
		return nil, err
	}
	c, _ := catalog.Parse(language, raw)
	if c == nil {
		return nil, &BadRequestError{Reason: "stored catalog is unreadable"}
	}
	return c, nil
}
