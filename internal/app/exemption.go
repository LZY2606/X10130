package app

import (
	"encoding/json"
	"time"

	"msgcheck/internal/state"
)

// ExemptRequest grants or extends an exemption for one issue identity.
type ExemptRequest struct {
	IssueKey         string     `json:"issue_key"`
	Reason           string     `json:"reason"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
	ExpectedSnapshot string     `json:"expected_snapshot,omitempty"`
	OpID             string     `json:"op_id,omitempty"`
}

// RevokeRequest removes an exemption.
type RevokeRequest struct {
	IssueKey         string `json:"issue_key"`
	ExpectedSnapshot string `json:"expected_snapshot,omitempty"`
	OpID             string `json:"op_id,omitempty"`
}

type exemptResult struct {
	Exemption state.Exemption `json:"exemption"`
	Snapshot  state.Pin       `json:"snapshot"`
}

// GrantExemption attaches an owner decision with optional deadline to an
// issue. The caller must base the request on a known snapshot.
func (a *App) GrantExemption(req ExemptRequest) (state.Exemption, state.Pin, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := a.advanceLocked(); err != nil {
		return state.Exemption{}, state.Pin{}, err
	}
	pin := a.pinLocked()
	if err := a.checkSnapshotLocked(req.ExpectedSnapshot); err != nil {
		return state.Exemption{}, pin, err
	}
	if req.IssueKey == "" {
		return state.Exemption{}, pin, &BadRequestError{Reason: "issue_key is required"}
	}
	if req.Reason == "" {
		return state.Exemption{}, pin, &BadRequestError{Reason: "reason is required for an exemption"}
	}
	reqFP := requestFingerprint(req)
	if replay, err := a.beginOpLocked("exempt", req.OpID, reqFP); err != nil {
		return state.Exemption{}, pin, err
	} else if replay != nil {
		var res exemptResult
		if err := json.Unmarshal(replay, &res); err == nil {
			return res.Exemption, a.pinLocked(), nil
		}
	}
	now := a.now()
	if req.ExpiresAt != nil && !req.ExpiresAt.After(now) {
		return state.Exemption{}, pin, &BadRequestError{Reason: "expires_at must be in the future"}
	}
	for i := range a.data.Exemptions {
		if a.data.Exemptions[i].IssueKey == req.IssueKey {
			a.data.Exemptions[i].Reason = req.Reason
			a.data.Exemptions[i].ExpiresAt = req.ExpiresAt
			a.data.Exemptions[i].GrantedAt = now
			if err := a.commitLocked(now); err != nil {
				return state.Exemption{}, pin, err
			}
			res := exemptResult{Exemption: a.data.Exemptions[i], Snapshot: a.pinLocked()}
			a.finishOpLocked("exempt", req.OpID, reqFP, res)
			return res.Exemption, res.Snapshot, nil
		}
	}
	ex := state.Exemption{
		ID: "ex_" + hashBytes([]byte(req.IssueKey), []byte(now.Format(timeFormatNano)))[:24],
		IssueKey: req.IssueKey, Reason: req.Reason,
		GrantedAt: now, ExpiresAt: req.ExpiresAt,
	}
	a.data.Exemptions = append(a.data.Exemptions, ex)
	if err := a.commitLocked(now); err != nil {
		return state.Exemption{}, pin, err
	}
	res := exemptResult{Exemption: ex, Snapshot: a.pinLocked()}
	a.finishOpLocked("exempt", req.OpID, reqFP, res)
	return ex, res.Snapshot, nil
}

// RevokeExemption removes an exemption before its deadline.
func (a *App) RevokeExemption(req RevokeRequest) (state.Pin, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := a.advanceLocked(); err != nil {
		return state.Pin{}, err
	}
	pin := a.pinLocked()
	if err := a.checkSnapshotLocked(req.ExpectedSnapshot); err != nil {
		return pin, err
	}
	reqFP := requestFingerprint(req)
	if replay, err := a.beginOpLocked("revoke", req.OpID, reqFP); err != nil {
		return pin, err
	} else if replay != nil {
		return a.pinLocked(), nil
	}
	kept := a.data.Exemptions[:0]
	found := false
	for _, ex := range a.data.Exemptions {
		if ex.IssueKey == req.IssueKey {
			found = true
			continue
		}
		kept = append(kept, ex)
	}
	if !found {
		return pin, &NotFoundError{Reason: "no active exemption for that issue"}
	}
	a.data.Exemptions = kept
	if err := a.commitLocked(a.now()); err != nil {
		return pin, err
	}
	a.finishOpLocked("revoke", req.OpID, reqFP, map[string]string{"issue_key": req.IssueKey})
	return a.pinLocked(), nil
}
