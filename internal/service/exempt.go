package service

import (
	"encoding/json"
	"fmt"
	"time"

	"messagecatalog/internal/store"
)

// ExemptionRequest creates an exemption.
type ExemptionRequest struct {
	ValidationID     string    `json:"validationId"`
	IssueKey         string    `json:"issueKey"`
	Reason           string    `json:"reason"`
	ExpiresAt        time.Time `json:"expiresAt"`
	ExpectedStateSeq int64     `json:"expectedStateSeq,omitempty"`
}

// ExemptionResult reports the stored exemption.
type ExemptionResult struct {
	Exemption     store.Exemption `json:"exemption"`
	SnapshotStamp string          `json:"snapshotStamp"`
	Replayed      bool            `json:"replayed,omitempty"`
}

// AddExemption excuses one issue until a deadline.
func (s *Service) AddExemption(opID string, req ExemptionRequest) (*ExemptionResult, bool, error) {
	if req.Reason == "" {
		return nil, false, fmt.Errorf("reason is required")
	}
	if req.ExpiresAt.IsZero() {
		return nil, false, fmt.Errorf("expiresAt is required")
	}
	_, raw, replay, err := s.withIdempotency(opID, "exemption", req, func(st *store.State, now time.Time) (int, string, error) {
		if req.ExpectedStateSeq != 0 && req.ExpectedStateSeq != st.Seq {
			return 0, "", &ConflictError{Msg: fmt.Sprintf("state changed: expected seq %d, current %d", req.ExpectedStateSeq, st.Seq)}
		}
		var rec *store.ValidationRecord
		for i := range st.Validations {
			if st.Validations[i].ID == req.ValidationID {
				rec = &st.Validations[i]
				break
			}
		}
		if rec == nil {
			return 0, "", fmt.Errorf("validation result %q not found", req.ValidationID)
		}
		found := false
		for _, is := range rec.Issues {
			if is.IssueKey == req.IssueKey {
				found = true
				break
			}
		}
		if !found {
			return 0, "", fmt.Errorf("issue %q not found in validation %q", req.IssueKey, req.ValidationID)
		}
		// Existing active exemption for this issue: return it (no extension).
		for i := range st.Exemptions {
			e := &st.Exemptions[i]
			if e.IssueKey == req.IssueKey && !e.Revoked {
				res := ExemptionResult{Exemption: *e, SnapshotStamp: stateStamp(st)}
				b, _ := json.Marshal(res)
				return 200, string(b), nil
			}
		}
		if !req.ExpiresAt.After(now) {
			return 0, "", &ConflictError{Msg: "exemption deadline must be in the future"}
		}
		var lang, key, typ string
		for _, is := range rec.Issues {
			if is.IssueKey == req.IssueKey {
				lang, key, typ = is.Language, is.Key, is.Type
			}
		}
		ex := store.Exemption{
			ID: "ex-" + randID(), IssueKey: req.IssueKey, Language: lang,
			MessageKey: key, Type: typ, Reason: req.Reason,
			ExpiresAt: req.ExpiresAt.UTC(), CreatedAt: now, OperationID: opID,
		}
		st.Exemptions = append(st.Exemptions, ex)
		for i := range st.Proofs {
			st.Proofs[i].Current = false
		}
		res := ExemptionResult{Exemption: ex, SnapshotStamp: stateStamp(st)}
		b, _ := json.Marshal(res)
		return 200, string(b), nil
	})
	if err != nil {
		return nil, false, err
	}
	var res ExemptionResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return nil, replay, err
	}
	res.Replayed = replay
	return &res, replay, nil
}

// RevokeExemption removes an active exemption.
func (s *Service) RevokeExemption(opID, exemptionID string, expectedSeq int64) (*store.Exemption, bool, error) {
	req := struct {
		ID  string `json:"id"`
		Seq int64  `json:"seq"`
	}{exemptionID, expectedSeq}
	_, raw, replay, err := s.withIdempotency(opID, "exemption-revoke", req, func(st *store.State, now time.Time) (int, string, error) {
		if expectedSeq != 0 && expectedSeq != st.Seq {
			return 0, "", &ConflictError{Msg: fmt.Sprintf("state changed: expected seq %d, current %d", expectedSeq, st.Seq)}
		}
		for i := range st.Exemptions {
			if st.Exemptions[i].ID == exemptionID {
				st.Exemptions[i].Revoked = true
				for j := range st.Proofs {
					st.Proofs[j].Current = false
				}
				b, _ := json.Marshal(st.Exemptions[i])
				return 200, string(b), nil
			}
		}
		return 0, "", fmt.Errorf("exemption %q not found", exemptionID)
	})
	if err != nil {
		return nil, false, err
	}
	var ex store.Exemption
	if err := json.Unmarshal([]byte(raw), &ex); err != nil {
		return nil, replay, err
	}
	return &ex, replay, nil
}

// ListExemptions returns exemptions with current expiry state.
type ExemptionView struct {
	store.Exemption
	Active  bool `json:"active"`
	Expired bool `json:"expired"`
}

func (s *Service) ListExemptions() []ExemptionView {
	snap := s.Snapshot()
	now := s.now()
	var out []ExemptionView
	for _, e := range snap.State.Exemptions {
		active := !e.Revoked && e.ExpiresAt.After(now)
		expired := !e.Revoked && !e.ExpiresAt.After(now)
		out = append(out, ExemptionView{Exemption: e, Active: active, Expired: expired})
	}
	return out
}
