// Package service implements workbench business logic over the store.
package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"messagecatalog/internal/catalog"
	"messagecatalog/internal/store"
)

const roleBaseline = "baseline"
const roleTarget = "target"

// Clock allows tests to control time.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

// ControlledClock is an in-memory adjustable clock for demos/tests.
type ControlledClock struct {
	mu  sync.Mutex
	now time.Time
}

func NewControlledClock(t time.Time) *ControlledClock { return &ControlledClock{now: t} }
func (c *ControlledClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now.UTC()
}
func (c *ControlledClock) Set(t time.Time) {
	c.mu.Lock()
	c.now = t.UTC()
	c.mu.Unlock()
}
func (c *ControlledClock) Add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// ConflictError indicates an optimistic-concurrency / precondition failure.
type ConflictError struct{ Msg string }

func (e *ConflictError) Error() string { return e.Msg }

// Service holds application state.
type Service struct {
	Store *store.Store
	Clock Clock
}

func New(st *store.Store) *Service {
	return &Service{Store: st, Clock: realClock{}}
}

func (s *Service) now() time.Time { return s.Clock.Now().UTC() }

// Snapshot is a consistent read view with stable identifiers.
type Snapshot struct {
	Seq           int64
	Stamp         string
	State         *store.State
	ParserVersion string
}

// Snapshot returns the current consistent state view.
func (s *Service) Snapshot() Snapshot {
	st := s.Store.Snapshot()
	return Snapshot{
		Seq:           st.Seq,
		Stamp:         stateStamp(st),
		State:         st,
		ParserVersion: catalog.ParserVersion,
	}
}

// stateStamp identifies the logical committed content. It deliberately
// excludes the monotonically incremented Seq, because bookkeeping commits
// (such as saving the proof describing the current content) must not make the
// just-generated proof immediately look historical. GeneratedAtState seq is
// tracked separately on the proof.
func stateStamp(st *store.State) string {
	b, _ := json.Marshal(struct {
		Versions    []store.VersionRecord    `json:"v"`
		Current     map[string]string        `json:"c"`
		Roles       map[string]string        `json:"r"`
		Edges       []store.RenameEdge       `json:"e"`
		Exemptions  []store.Exemption        `json:"x"`
		Validations []store.ValidationRecord `json:"val"`
		Parser      string                   `json:"p"`
	}{st.Versions, st.CurrentByLang, st.RoleByLang, st.RenameEdges,
		st.Exemptions, st.Validations, catalog.ParserVersion})
	sum := sha256.Sum256(b)
	return "snap-" + hex.EncodeToString(sum[:])[:16]
}

func hashRequest(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		b = []byte(fmt.Sprint(v))
	}
	sum := sha256.Sum256(b)
	return "req:" + hex.EncodeToString(sum[:])
}

// opResult is returned for idempotent replays.
type opResult struct {
	Status   int
	Response string
}

// withIdempotency runs fn under a stored idempotency key inside one commit.
func (s *Service) withIdempotency(key, kind string, request any, fn func(st *store.State, now time.Time) (int, string, error)) (int, string, bool, error) {
	if key == "" {
		// Generate an ephemeral key; still record for auditing simplicity.
		key = "auto-" + randID()
	}
	reqHash := hashRequest(request)
	var replay bool
	var result opResult
	err := s.Store.Commit(func(st *store.State) error {
		for _, op := range st.Operations {
			if op.Key == key {
				if op.RequestHash != reqHash {
					return fmt.Errorf("%w: operation %s was previously submitted with different content", store.ErrIdempotency, key)
				}
				result = opResult{Status: op.Status, Response: op.Response}
				replay = true
				return nil
			}
		}
		status, resp, err := fn(st, s.now())
		if err != nil {
			return err
		}
		result = opResult{Status: status, Response: resp}
		st.Operations = append(st.Operations, store.OperationRecord{
			Key: key, RequestHash: reqHash, Kind: kind,
			Status: status, Response: resp, RecordedAt: s.now(),
		})
		return nil
	})
	if err != nil {
		return 0, "", false, err
	}
	return result.Status, result.Response, replay, nil
}

// versionID builds a stable version id.
func versionID(language, fingerprint string) string {
	return language + ":" + strings.TrimPrefix(fingerprint, "sha256:")[:12]
}

// edges builds a catalog.Edges view from state.
func edges(st *store.State) catalog.Edges {
	e := catalog.Edges{}
	for _, edge := range st.RenameEdges {
		e[edge.From] = edge.To
	}
	return e
}

func mappingSig(st *store.State) string {
	es := append([]store.RenameEdge(nil), st.RenameEdges...)
	sort.Slice(es, func(i, j int) bool {
		if es[i].From != es[j].From {
			return es[i].From < es[j].From
		}
		return es[i].To < es[j].To
	})
	b, _ := json.Marshal(es)
	sum := sha256.Sum256(b)
	return "map-" + hex.EncodeToString(sum[:])[:16]
}

func hasCycle(e catalog.Edges) bool {
	for start := range e {
		_, cyc := e.ResolveChain(start)
		if cyc {
			return true
		}
	}
	return false
}

// wouldCycle reports whether adding from->to creates a cycle.
func wouldCycle(e catalog.Edges, from, to string) bool {
	next := catalog.Edges{}
	for k, v := range e {
		next[k] = v
	}
	next[from] = to
	_, cyc := next.ResolveChain(to)
	if cyc {
		return true
	}
	// also walk from->to->... reaching from
	cur := to
	seen := map[string]bool{from: true}
	for {
		n, ok := next[cur]
		if !ok {
			return false
		}
		if n == from {
			return true
		}
		if seen[n] {
			return true
		}
		seen[n] = true
		cur = n
	}
}

// incomingCount counts edges targeting dst.
func incomingCount(e catalog.Edges, dst string) int {
	n := 0
	for _, v := range e {
		if v == dst {
			n++
		}
	}
	return n
}

var errNotFound = errors.New("not found")

func randID() string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano()))))[:16]
}
