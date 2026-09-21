package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
)

type RenameRequest struct {
	ProposalID string `json:"proposal_id"`
	Edges      []Edge `json:"edges"`
	BaseSeq    int64  `json:"base_seq"`
	MappingSig string `json:"mapping_sig"`
}

func (r RenameRequest) hash() string {
	h := sha256.New()
	h.Write([]byte(r.ProposalID))
	h.Write([]byte{0})
	edges := make([]Edge, len(r.Edges))
	copy(edges, r.Edges)
	sort.Slice(edges, func(a, b int) bool {
		if edges[a].OldKey != edges[b].OldKey {
			return edges[a].OldKey < edges[b].OldKey
		}
		return edges[a].NewKey < edges[b].NewKey
	})
	for _, e := range edges {
		h.Write([]byte(e.OldKey))
		h.Write([]byte{0})
		h.Write([]byte(e.NewKey))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

type RenameResult struct {
	Edges      []Edge `json:"edges"`
	MappingSig string `json:"mapping_sig"`
	SnapshotID string `json:"snapshot_id"`
	Seq        int64  `json:"seq"`
	Idempotent bool   `json:"idempotent_replay,omitempty"`
}

func (s *Store) ConfirmRename(opKey string, req RenameRequest) (*RenameResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readOnly {
		return nil, ErrReadOnly
	}
	reqHash := req.hash()
	if rec, dup, err := s.checkIdem(opKey, reqHash, "rename"); err != nil {
		return nil, err
	} else if dup {
		snap := s.state.Snapshots[rec.Seq]
		return &RenameResult{Edges: snap.Edges, MappingSig: snap.MappingSig,
			SnapshotID: snap.ID, Seq: rec.Seq, Idempotent: true}, nil
	}
	st := s.state
	if req.BaseSeq != 0 && req.BaseSeq != st.Seq {
		return nil, fmt.Errorf("%w: catalog state changed since you viewed it (you saw seq %d, current is %d)",
			ErrConflict, req.BaseSeq, st.Seq)
	}
	if req.MappingSig != "" && req.MappingSig != MappingSignature(st.Edges) {
		return nil, fmt.Errorf("%w: rename mapping changed since you viewed it", ErrConflict)
	}
	prop, ok := st.Proposals[req.ProposalID]
	if !ok {
		return nil, fmt.Errorf("%w: unknown rename proposal %q", ErrBadRequest, req.ProposalID)
	}
	if prop.Resolved {
		return nil, fmt.Errorf("%w: proposal already resolved", ErrConflict)
	}
	// Validate edges against proposal sets.
	oldSet := map[string]bool{}
	newSet := map[string]bool{}
	for _, k := range prop.OldOnly {
		oldSet[k] = true
	}
	for _, k := range prop.NewOnly {
		newSet[k] = true
	}
	newUsed := map[string]int{}
	for _, e := range req.Edges {
		if e.OldKey == e.NewKey {
			return nil, fmt.Errorf("%w: edge %q maps to itself", ErrBadRequest, e.OldKey)
		}
		if !oldSet[e.OldKey] {
			return nil, fmt.Errorf("%w: %q is not a removed key in this proposal", ErrBadRequest, e.OldKey)
		}
		if !newSet[e.NewKey] {
			return nil, fmt.Errorf("%w: %q is not an added key in this proposal", ErrBadRequest, e.NewKey)
		}
		newUsed[e.NewKey]++
	}
	// No two old keys may merge into one new key.
	var merged []string
	for nk, n := range newUsed {
		if n > 1 {
			merged = append(merged, nk)
		}
	}
	if len(merged) > 0 {
		sort.Strings(merged)
		return nil, fmt.Errorf("%w: multiple old keys would merge into %v", ErrBadRequest, merged)
	}
	// Combine with existing edges and verify function + acyclic.
	combined := append(append([]Edge{}, st.Edges...), req.Edges...)
	if err := validateEdges(combined); err != nil {
		return nil, err
	}

	ev := &Event{Type: "rename", At: s.now(), Rename: &RenameEvent{
		OpKey: opKey, ReqHash: reqHash, Edges: req.Edges, Resolve: prop.ID,
	}}
	if err := s.commit(ev); err != nil {
		return nil, err
	}
	snap := st.Snapshots[st.Seq]
	return &RenameResult{Edges: snap.Edges, MappingSig: snap.MappingSig,
		SnapshotID: snap.ID, Seq: st.Seq}, nil
}

// validateEdges ensures each old key maps to at most one new key, no new key
// is reached from two old keys, and the graph is acyclic.
func validateEdges(edges []Edge) error {
	byOld := map[string]string{}
	byNew := map[string]int{}
	for _, e := range edges {
		if ex, ok := byOld[e.OldKey]; ok && ex != e.NewKey {
			return fmt.Errorf("%w: key %q mapped to two targets %q and %q",
				ErrBadRequest, e.OldKey, ex, e.NewKey)
		}
		byOld[e.OldKey] = e.NewKey
	}
	for _, e := range edges {
		byNew[e.NewKey]++
	}
	for nk, n := range byNew {
		if n > 1 {
			return fmt.Errorf("%w: two old keys merge into %q", ErrBadRequest, nk)
		}
	}
	for start := range byOld {
		seen := map[string]bool{start: true}
		cur := start
		for {
			nxt, ok := byOld[cur]
			if !ok {
				break
			}
			if seen[nxt] {
				return fmt.Errorf("%w: rename chain forms a cycle at %q -> %q",
					ErrBadRequest, cur, nxt)
			}
			seen[nxt] = true
			cur = nxt
		}
	}
	return nil
}
