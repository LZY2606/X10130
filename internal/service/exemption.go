package service

import (
	"encoding/json"
	"fmt"
	"time"

	"messagecatalog/internal/icu"
)

// ExemptionRequest sets or replaces one issue exemption.
type ExemptionRequest struct {
	IssueID        string `json:"issueId"`
	Reason         string `json:"reason"`
	Deadline       string `json:"deadline,omitempty"`
	Language       string `json:"language"`
	BaseVersion    string `json:"baseVersion"`
	TargetVersion  string `json:"targetVersion"`
	MappingVersion int    `json:"mappingVersion"`
}

// ExemptionResult describes the stored exemption.
type ExemptionResult struct {
	Exemption  *Exemption `json:"exemption"`
	Status     string     `json:"status"` // active, expired
	SnapshotID string     `json:"snapshotId"`
	Seq        int        `json:"seq"`
}

func (r *ExemptionResult) snapID() string { return r.SnapshotID }
func (r *ExemptionResult) snapSeq() int   { return r.Seq }

// SetExemption records an exemption bound to the exact validation basis.
func (s *Service) SetExemption(opID string, req ExemptionRequest) (*ExemptionResult, error) {
	if err := s.checkWritable(); err != nil {
		return nil, err
	}
	if req.IssueID == "" || req.Reason == "" {
		return nil, &RequestError{Msg: "issueId and reason are required"}
	}
	if req.Deadline != "" {
		if _, err := time.Parse(time.RFC3339, req.Deadline); err != nil {
			return nil, &RequestError{Msg: "deadline must be RFC3339 UTC, e.g. 2026-10-01T00:00:00Z"}
		}
	}
	out, err := s.idempotent(opID, "exemption", req, func(stored string) (any, error) {
		var r ExemptionResult
		if err := json.Unmarshal([]byte(stored), &r); err != nil {
			return nil, err
		}
		return &r, nil
	}, func() (any, string, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.applyExemptionLocked(req)
	})
	if err != nil {
		return nil, err
	}
	return out.(*ExemptionResult), nil
}

func (s *Service) applyExemptionLocked(req ExemptionRequest) (any, string, error) {
	base := s.state.Catalogs[s.state.BaselineLanguage]
	if base == nil {
		return nil, "", &RequestError{Msg: "no baseline catalog"}
	}
	tv, ok := s.state.Catalogs[req.Language]
	if !ok {
		return nil, "", &RequestError{Msg: "no catalog for language " + req.Language}
	}
	// Refuse to attach an old confirmation to changed text.
	if base.Version != req.BaseVersion {
		return nil, "", &ConflictError{Msg: fmt.Sprintf(
			"baseline version changed: viewed=%s current=%s", short(req.BaseVersion), short(base.Version))}
	}
	if tv.Version != req.TargetVersion {
		return nil, "", &ConflictError{Msg: fmt.Sprintf(
			"target version changed: viewed=%s current=%s", short(req.TargetVersion), short(tv.Version))}
	}
	if s.state.MappingVersion != req.MappingVersion {
		return nil, "", &ConflictError{Msg: fmt.Sprintf(
			"mapping version changed: viewed=%d current=%d", req.MappingVersion, s.state.MappingVersion)}
	}
	// The issue must still exist on this basis.
	report, _, err := s.ensureReportLocked(req.Language)
	if err != nil {
		return nil, "", err
	}
	found := false
	for _, is := range report.Issues {
		if is.ID == req.IssueID {
			found = true
			break
		}
	}
	if !found {
		return nil, "", &ConflictError{Msg: "issue " + req.IssueID + " does not exist on the current validation basis"}
	}
	ex := &Exemption{
		IssueID:         req.IssueID,
		Reason:          req.Reason,
		CreatedAt:       s.nowRFC(),
		Deadline:        req.Deadline,
		BaselineVersion: req.BaseVersion,
		TargetVersion:   req.TargetVersion,
		MappingVersion:  req.MappingVersion,
		ParserVersion:   icu.Version,
	}
	s.state.Exemptions[req.IssueID] = ex
	snap, err := s.commitLocked("exemption:" + req.IssueID[:min(8, len(req.IssueID))])
	if err != nil {
		return nil, "", err
	}
	status := "active"
	if ex.Expired(s.Now()) {
		status = "expired"
	}
	res := &ExemptionResult{Exemption: ex, Status: status, SnapshotID: snap.ID, Seq: snap.Seq}
	return res, "set exemption", nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
