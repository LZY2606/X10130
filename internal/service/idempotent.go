package service

import (
	"encoding/json"

	"messagecatalog/internal/store"
)

// applyFn performs the work under whatever lock it needs. It returns:
// result is stored verbatim in the operation record; note describes the commit.
type applyFn func() (result any, note string, err error)

// replayFn converts a stored result JSON into the caller's concrete type.
type replayFn func(storedJSON string) (any, error)

// idempotent runs a mutation exactly once per opID. The same opID with
// different request content is a hard conflict; identical retries replay the
// stored equivalent result without generating new versions.
func (s *Service) idempotent(opID, kind string, request any, replay replayFn, apply applyFn) (any, error) {
	if opID == "" {
		return nil, &RequestError{Msg: "operation id is required"}
	}
	reqJSON, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	reqSHA := rawSHA(reqJSON)

	s.mu.Lock()
	if existing, ok := s.state.Operations[opID]; ok {
		s.mu.Unlock()
		if existing.RequestSHA != reqSHA {
			return nil, &ConflictError{
				Msg: "operation id " + opID + " was already submitted with different content (stored request sha " +
					store.Short(existing.RequestSHA) + " != " + store.Short(reqSHA) + ")",
			}
		}
		return replay(existing.ResultJSON)
	}
	// Reserve the operation id immediately so concurrent duplicates wait/fail.
	s.state.Operations[opID] = &Operation{
		ID: opID, Kind: kind, RequestSHA: reqSHA,
		Status: "running", CreatedAt: s.nowRFC(),
	}
	s.mu.Unlock()

	result, _, err := apply()

	s.mu.Lock()
	defer s.mu.Unlock()
	op := s.state.Operations[opID]
	if err != nil {
		// Failed attempts do not consume the id (unless it was a conflict
		// caused by changed state; the client must inspect the error).
		if op.Status == "running" {
			delete(s.state.Operations, opID)
		}
		return nil, err
	}
	rb, _ := json.Marshal(result)
	op.Status = "applied"
	op.ResultJSON = string(rb)
	if snap, ok := result.(snapshotCarrier); ok {
		op.SnapshotID = snap.snapID()
		op.Seq = snap.snapSeq()
	}
	return result, nil
}

type snapshotCarrier interface {
	snapID() string
	snapSeq() int
}

func (r *ImportResult) snapID() string        { return r.SnapshotID }
func (r *ImportResult) snapSeq() int          { return r.Seq }
func (r *ConfirmRenameResult) snapID() string { return r.SnapshotID }
func (r *ConfirmRenameResult) snapSeq() int   { return r.Seq }
