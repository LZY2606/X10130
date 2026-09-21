package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

type ExemptionRequest struct {
	IssueID  string `json:"issue_id"`
	Reason   string `json:"reason"`
	Deadline int64  `json:"deadline"` // unix seconds; 0 with revoke=false is allowed (no expiry)
	BaseSeq  int64  `json:"base_seq"`
	Revoke   bool   `json:"revoke"`
}

func (r ExemptionRequest) hash() string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%d\x00%v\x00", r.IssueID, r.Reason, r.Deadline, r.Revoke)
	return hex.EncodeToString(h.Sum(nil))
}

type ExemptionResult struct {
	Exemption  Exemption `json:"exemption"`
	Active     bool      `json:"active"`
	SnapshotID string    `json:"snapshot_id"`
	Seq        int64     `json:"seq"`
	Idempotent bool      `json:"idempotent_replay,omitempty"`
}

func (s *Store) SetExemption(opKey string, req ExemptionRequest) (*ExemptionResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readOnly {
		return nil, ErrReadOnly
	}
	reqHash := req.hash()
	if rec, dup, err := s.checkIdem(opKey, reqHash, "exemption"); err != nil {
		return nil, err
	} else if dup {
		snap := s.state.Snapshots[rec.Seq]
		ex := snap.Exemptions[req.IssueID]
		return &ExemptionResult{Exemption: ex, Active: exemptionActive(ex, s.now()),
			SnapshotID: snap.ID, Seq: rec.Seq, Idempotent: true}, nil
	}
	st := s.state
	if req.BaseSeq != 0 && req.BaseSeq != st.Seq {
		return nil, fmt.Errorf("%w: state changed since the issue was viewed (seq %d -> %d)",
			ErrConflict, req.BaseSeq, st.Seq)
	}
	// Issue must currently exist (exemptions bind to findings, not arbitrary ids).
	if !issueExists(st, req.IssueID) {
		return nil, fmt.Errorf("%w: issue %q does not exist in the current state",
			ErrBadRequest, req.IssueID)
	}
	if !req.Revoke && req.Reason == "" {
		return nil, fmt.Errorf("%w: exemption reason is required", ErrBadRequest)
	}
	if !req.Revoke && req.Deadline != 0 && req.Deadline <= s.now() {
		return nil, fmt.Errorf("%w: deadline must be in the future", ErrBadRequest)
	}
	ev := &Event{Type: "exemption", At: s.now(), Exemption: &ExemptionEvent{
		OpKey: opKey, ReqHash: reqHash, IssueID: req.IssueID, Reason: req.Reason,
		Deadline: req.Deadline, BaseSeq: req.BaseSeq, Revoke: req.Revoke,
	}}
	if err := s.commit(ev); err != nil {
		return nil, err
	}
	snap := st.Snapshots[st.Seq]
	ex := snap.Exemptions[req.IssueID]
	return &ExemptionResult{Exemption: ex, Active: exemptionActive(ex, s.now()),
		SnapshotID: snap.ID, Seq: st.Seq}, nil
}

func issueExists(st *State, id string) bool {
	sn := st.Snapshots[st.Seq]
	for _, is := range sn.Issues {
		if is.ID == id {
			return true
		}
	}
	return false
}

func exemptionActive(e Exemption, now int64) bool {
	if e.Revoked || e.IssueID == "" {
		return false
	}
	if e.Deadline == 0 {
		return true
	}
	return now < e.Deadline
}
