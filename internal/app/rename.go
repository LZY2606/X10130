package app

import (
	"encoding/json"

	"msgcheck/internal/state"
)

// RenameRequest confirms that oldKey was renamed to newKey between two
// baseline versions.
type RenameRequest struct {
	OldKey           string `json:"old_key"`
	NewKey           string `json:"new_key"`
	FromVersion      string `json:"from_version"`
	ToVersion        string `json:"to_version"`
	ExpectedSnapshot string `json:"expected_snapshot,omitempty"`
	OpID             string `json:"op_id,omitempty"`
}

// RenameResult reports the confirmed edge.
type RenameResult struct {
	OldKey   string    `json:"old_key"`
	NewKey   string    `json:"new_key"`
	Snapshot state.Pin `json:"snapshot"`
}

// ConfirmRename establishes an old -> new lineage edge after explicit user
// confirmation. It refuses cycles, silent merges and stale preconditions.
func (a *App) ConfirmRename(req RenameRequest) (*RenameResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := a.advanceLocked(); err != nil {
		return nil, err
	}
	if err := a.checkSnapshotLocked(req.ExpectedSnapshot); err != nil {
		return nil, err
	}
	if a.data.Baseline == nil {
		return nil, &BadRequestError{Reason: "no baseline catalog imported"}
	}
	if req.OldKey == "" || req.NewKey == "" {
		return nil, &BadRequestError{Reason: "old_key and new_key are required"}
	}
	reqFP := requestFingerprint(req)
	if replay, err := a.beginOpLocked("rename", req.OpID, reqFP); err != nil {
		return nil, err
	} else if replay != nil {
		var res RenameResult
		if err := json.Unmarshal(replay, &res); err == nil {
			res.Snapshot = a.pinLocked()
			return &res, nil
		}
	}
	cur := a.data.Baseline.Version
	if req.ToVersion != "" && req.ToVersion != cur {
		return nil, &ConflictError{Reason: "baseline version is now " + cur + ", not the " + req.ToVersion + " your confirmation was based on", Current: a.pinLocked()}
	}
	base, err := a.loadCatalogLocked(a.data.Baseline.Language, a.data.Baseline.RawBlob)
	if err != nil {
		return nil, err
	}
	if _, ok := base.Entries[req.NewKey]; !ok {
		return nil, &BadRequestError{Reason: "new key " + req.NewKey + " is not present in the current baseline"}
	}
	for _, r := range a.data.Renames {
		if r.OldKey == req.OldKey && r.NewKey == req.NewKey {
			res := &RenameResult{OldKey: req.OldKey, NewKey: req.NewKey, Snapshot: a.pinLocked()}
			a.finishOpLocked("rename", req.OpID, reqFP, res)
			return res, nil
		}
	}
	if err := checkRenameValid(a.data.Renames, req.OldKey, req.NewKey); err != nil {
		return nil, err
	}
	a.data.Renames = append(a.data.Renames, state.Rename{
		OldKey: req.OldKey, NewKey: req.NewKey,
		Language: a.data.Baseline.Language, ConfirmedAt: a.now(),
		FromVersion: req.FromVersion, ToVersion: cur,
	})
	if err := a.commitLocked(a.now()); err != nil {
		return nil, err
	}
	res := &RenameResult{OldKey: req.OldKey, NewKey: req.NewKey, Snapshot: a.pinLocked()}
	a.finishOpLocked("rename", req.OpID, reqFP, res)
	return res, nil
}

// Renames returns the confirmed mapping chain.
func (a *App) Renames() []state.Rename {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]state.Rename, len(a.data.Renames))
	copy(out, a.data.Renames)
	return out
}
