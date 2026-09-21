package service

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"messagecatalog/internal/catalog"
	"messagecatalog/internal/store"
)

// RenameCandidate is a suggested old->new baseline key mapping.
type RenameCandidate struct {
	From      string `json:"from"`
	To        string `json:"to"`
	FromSeen  bool   `json:"fromSeen"`
	ToSeen    bool   `json:"toSeen"`
	Confirmed bool   `json:"confirmed"`
	Reason    string `json:"reason,omitempty"`
}

// ConfirmRenameRequest confirms one rename mapping.
type ConfirmRenameRequest struct {
	From                string `json:"from"`
	To                  string `json:"to"`
	ExpectedBaseVersion string `json:"expectedBaseVersion"`
	ExpectedMappingSig  string `json:"expectedMappingSig"`
}

// ConfirmRenameResult is returned after confirming.
type ConfirmRenameResult struct {
	Edge          RenameCandidate `json:"edge"`
	BaseVersionID string          `json:"baseVersionId"`
	MappingSig    string          `json:"mappingSig"`
	SnapshotStamp string          `json:"snapshotStamp"`
}

// RenameCandidates lists old baseline keys missing now and new keys not mapped.
func (s *Service) RenameCandidates() ([]RenameCandidate, error) {
	snap := s.Snapshot()
	st := snap.State
	base := currentBaseVersion(st)
	if base == nil {
		return nil, fmt.Errorf("no baseline catalog imported")
	}
	cur := map[string]bool{}
	for _, m := range base.Messages {
		cur[m.Key] = true
	}
	mappedFrom := map[string]bool{}
	mappedTo := map[string]bool{}
	var out []RenameCandidate
	for _, edge := range st.RenameEdges {
		mappedFrom[edge.From] = true
		mappedTo[edge.To] = true
		out = append(out, RenameCandidate{From: edge.From, To: edge.To,
			FromSeen: cur[edge.From], ToSeen: cur[edge.To], Confirmed: true})
	}
	// old keys: keys referenced as origins of chains but not in current baseline.
	// We derive "old" from upload history of baseline versions.
	oldKeys := map[string]bool{}
	for _, v := range st.Versions {
		if v.Role == roleBaseline && v.ID != base.ID {
			for _, m := range v.Messages {
				if !cur[m.Key] {
					oldKeys[m.Key] = true
				}
			}
		}
	}
	for k := range oldKeys {
		if mappedFrom[k] {
			continue
		}
		out = append(out, RenameCandidate{From: k, FromSeen: false})
	}
	// new keys in current baseline with no incoming mapping
	for k := range cur {
		if !mappedTo[k] {
			// roots themselves: list as possible targets
			out = append(out, RenameCandidate{To: k, ToSeen: true})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		return out[i].To < out[j].To
	})
	return out, nil
}

// ConfirmRename applies a rename mapping after precondition checks.
func (s *Service) ConfirmRename(opID string, req ConfirmRenameRequest) (*ConfirmRenameResult, bool, error) {
	if req.From == "" || req.To == "" {
		return nil, false, fmt.Errorf("from and to are required")
	}
	if req.From == req.To {
		return nil, false, fmt.Errorf("cannot map a key to itself")
	}
	_, raw, replay, err := s.withIdempotency(opID, "rename", req, func(st *store.State, now time.Time) (int, string, error) {
		base := currentBaseVersion(st)
		if base == nil {
			return 0, "", fmt.Errorf("no baseline catalog imported")
		}
		if req.ExpectedBaseVersion != "" && req.ExpectedBaseVersion != base.ID {
			return 0, "", &ConflictError{Msg: fmt.Sprintf("baseline version changed: expected %s, current %s", req.ExpectedBaseVersion, base.ID)}
		}
		ms := mappingSig(st)
		if req.ExpectedMappingSig != "" && req.ExpectedMappingSig != ms {
			return 0, "", &ConflictError{Msg: "rename mapping state changed since it was viewed"}
		}
		e := edges(st)
		// Refuse two old keys silently merging into one new key.
		if incomingCount(e, req.To) > 0 {
			existing := ""
			for from, to := range e {
				if to == req.To {
					existing = from
				}
			}
			return 0, "", &ConflictError{Msg: fmt.Sprintf("new key %q is already mapped from %q; mapping two old keys to one new key is not allowed", req.To, existing)}
		}
		if ex, ok := e[req.From]; ok {
			if ex != req.To {
				return 0, "", &ConflictError{Msg: fmt.Sprintf("key %q is already mapped to %q", req.From, ex)}
			}
			// idempotent no-op within same op key handled upstream
		}
		if wouldCycle(e, req.From, req.To) {
			return 0, "", &ConflictError{Msg: fmt.Sprintf("mapping %q -> %q would form a cycle", req.From, req.To)}
		}
		// validate from/to exist: from must be an old baseline key or currently
		// unseen; to must be in the current baseline.
		cur := map[string]bool{}
		for _, m := range base.Messages {
			cur[m.Key] = true
		}
		if !cur[req.To] {
			return 0, "", &ConflictError{Msg: fmt.Sprintf("target key %q is not in the current baseline", req.To)}
		}
		edge := store.RenameEdge{From: req.From, To: req.To, ConfirmedAt: now, Version: base.ID}
		st.RenameEdges = append(st.RenameEdges, edge)
		mappingChanged(st)
		res := ConfirmRenameResult{
			Edge:          RenameCandidate{From: req.From, To: req.To, Confirmed: true, ToSeen: true},
			BaseVersionID: base.ID, MappingSig: mappingSig(st),
			SnapshotStamp: stateStamp(st),
		}
		b, _ := json.Marshal(res)
		return 200, string(b), nil
	})
	if err != nil {
		return nil, false, err
	}
	var res ConfirmRenameResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return nil, replay, err
	}
	return &res, replay, nil
}

var _ = catalog.Edges(nil)
