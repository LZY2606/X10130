package service

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"messagecatalog/internal/icu"
)

const currentParser = icu.Version

// ReplayView returns a previously computed report so old results can be
// replayed. It clearly flags whether the basis still matches the current
// state; a stale result can be viewed but never used for new writes.
type ReplayView struct {
	Report        *LanguageReportView `json:"report"`
	Current       bool                `json:"current"`
	CurrentTuple  string              `json:"currentTuple,omitempty"`
	InvalidReason string              `json:"invalidReason,omitempty"`
	ChangedParts  []string            `json:"changedParts,omitempty"`
}

// ReplayTuple looks up an old validation result by its tuple sha.
func (s *Service) ReplayTuple(tupleID string) (*ReplayView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.state.Reports[tupleID]
	if !ok {
		return nil, &RequestError{Msg: "no stored validation result for tuple " + tupleID}
	}
	b, err := s.st.GetBlob(rec.Blob)
	if err != nil {
		return nil, err
	}
	var r Report
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	v := s.decorateLocked(&r, tupleID)

	rv := &ReplayView{Report: v, Current: true}
	base := s.state.Catalogs[s.state.BaselineLanguage]
	tv := s.state.Catalogs[rec.Language]
	var changed []string
	if base == nil || base.Version != rec.BaselineVersion {
		changed = append(changed, "baseline")
	}
	if tv == nil || tv.Version != rec.TargetVersion {
		changed = append(changed, "target:"+rec.Language)
	}
	if s.state.MappingVersion != rec.MappingVersion {
		changed = append(changed, "mapping")
	}
	if rec.ParserVersion != currentParser {
		changed = append(changed, "parser")
	}
	if len(changed) > 0 {
		rv.Current = false
		sort.Strings(changed)
		rv.ChangedParts = changed
		rv.InvalidReason = "basis changed: " + strings.Join(changed, ",")
		v.Current = false
		v.InvalidReason = rv.InvalidReason
	}
	if base != nil && tv != nil {
		rv.CurrentTuple = tupleSHA(currentParser, base.Version, tv.Version,
			s.state.MappingVersion, rec.Language)
	}
	return rv, nil
}

// StoredTuples lists available replay tuples (for the UI).
func (s *Service) StoredTuples() []*ReportRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*ReportRecord, 0, len(s.state.Reports))
	for _, r := range s.state.Reports {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Language != out[j].Language {
			return out[i].Language < out[j].Language
		}
		return out[i].CreatedAt < out[j].CreatedAt
	})
	return out
}

// OperationStatus exposes the idempotency receipt for an op id.
func (s *Service) OperationStatus(opID string) (*Operation, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	op, ok := s.state.Operations[opID]
	return op, ok
}

// FixedClock is a manually controlled time source for tests/demo.
type FixedClock struct{ T time.Time }

func (f *FixedClock) Now() time.Time { return f.T }

// SetFixedTime installs a fixed clock.
func (s *Service) SetFixedTime(t time.Time) {
	s.SetClock(&FixedClock{T: t})
}

// SetRealClock restores wall-clock time.
func (s *Service) SetRealClock() {
	s.SetClock(realClock{})
}
