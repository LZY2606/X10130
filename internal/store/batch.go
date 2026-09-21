package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"msgcatalog/internal/catalog"
)

type BatchLangReq struct {
	Language   string `json:"language"`
	ExpectedFP string `json:"expected_fp"`
}

type BatchRequest struct {
	Langs      []BatchLangReq `json:"langs"`
	BaseSeq    int64          `json:"base_seq"`
	MappingSig string         `json:"mapping_sig"`
}

// IntentHash deliberately excludes expected FPs so a retry that refreshes
// expectations is recognizable as the same logical operation while versions
// are still verified explicitly per language.
func (r BatchRequest) intentHash() string {
	h := sha256.New()
	names := make([]string, len(r.Langs))
	for i, l := range r.Langs {
		names[i] = l.Language
	}
	sort.Strings(names)
	for _, n := range names {
		h.Write([]byte(n))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

type BatchResult struct {
	Batch      *BatchFix `json:"batch"`
	SnapshotID string    `json:"snapshot_id"`
	Seq        int64     `json:"seq"`
	Idempotent bool      `json:"idempotent_replay,omitempty"`
}

func (s *Store) ApplyBatch(opKey string, req BatchRequest) (*BatchResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readOnly {
		return nil, ErrReadOnly
	}
	intent := req.intentHash()
	if opKey != "" {
		if existing, ok := s.state.Batches[opKey]; ok {
			if existing.ReqHash != intent {
				return nil, ErrIdemConflict
			}
			// Replay: return stored partial/full result. Successful
			// languages are retained verbatim; only still-pending/conflicted
			// languages are attempted now, without regenerating successes.
			res, err := s.retryPending(opKey, existing, req)
			if err != nil {
				return nil, err
			}
			snap := s.state.Snapshots[s.state.Seq]
			res.SnapshotID = snap.ID
			res.Idempotent = true
			return res, nil
		}
		if rec, ok := s.state.Idem[opKey]; ok {
			// Different kind collision.
			_ = rec
			return nil, ErrIdemConflict
		}
	}
	st := s.state
	if req.BaseSeq != 0 && req.BaseSeq != st.Seq {
		return nil, fmt.Errorf("%w: state changed since the fix was prepared (seq %d -> %d)",
			ErrConflict, req.BaseSeq, st.Seq)
	}
	if req.MappingSig != MappingSignature(st.Edges) {
		return nil, fmt.Errorf("%w: rename mapping changed since the fix was prepared", ErrConflict)
	}
	batch := &BatchFix{OpKey: opKey, ReqHash: intent, At: s.now()}
	s.executeLangs(batch, req)
	if err := s.commitBatch(batch); err != nil {
		return nil, err
	}
	snap := st.Snapshots[st.Seq]
	return &BatchResult{Batch: batch, SnapshotID: snap.ID, Seq: st.Seq}, nil
}

func (s *Store) retryPending(opKey string, existing *BatchFix, req BatchRequest) (*BatchResult, error) {
	// Map latest expected FPs from request.
	expected := map[string]string{}
	for _, l := range req.Langs {
		expected[l.Language] = l.ExpectedFP
	}
	retryReq := BatchRequest{MappingSig: req.MappingSig, BaseSeq: req.BaseSeq}
	var retryLangs []BatchLangReq
	for i := range existing.Langs {
		lf := existing.Langs[i]
		if lf.Status == "applied" || lf.Status == "already-applied" {
			continue
		}
		if exp, ok := expected[lf.Language]; ok {
			retryLangs = append(retryLangs, BatchLangReq{
				Language: lf.Language, ExpectedFP: exp,
			})
		}
	}
	if len(retryLangs) == 0 {
		// Nothing to retry; return stored result as-is.
		snap := s.state.Snapshots[existing.At]
		_ = snap
		return &BatchResult{Batch: existing, Seq: s.state.Seq,
			SnapshotID: s.state.Snapshots[s.state.Seq].ID}, nil
	}
	// Refresh only non-successful entries.
	retryReq.Langs = retryLangs
	fresh := &BatchFix{Langs: nil}
	s.executeLangs(fresh, retryReq)
	byLang := map[string]LangFix{}
	for _, lf := range existing.Langs {
		byLang[lf.Language] = lf
	}
	for _, lf := range fresh.Langs {
		byLang[lf.Language] = lf
		for fp, v := range fresh.NewVersions {
			if existing.NewVersions == nil {
				existing.NewVersions = map[string]*VersionEntry{}
			}
			existing.NewVersions[fp] = v
		}
	}
	var merged []LangFix
	for _, l := range req.Langs {
		if lf, ok := byLang[l.Language]; ok {
			merged = append(merged, lf)
		}
	}
	existing.Langs = merged
	moves := false
	for _, lf := range fresh.Langs {
		if (lf.Status == "applied" || lf.Status == "already-applied") &&
			s.state.Langs[lf.Language] != nil &&
			s.state.Langs[lf.Language].CurrentFP != lf.AppliedFP {
			moves = true
		}
	}
	if len(fresh.NewVersions) > 0 || moves {
		if err := s.commitBatch(existing); err != nil {
			return nil, err
		}
	}
	return &BatchResult{Batch: existing, Seq: s.state.Seq}, nil
}

func (s *Store) executeLangs(batch *BatchFix, req BatchRequest) {
	st := s.state
	if batch.NewVersions == nil {
		batch.NewVersions = map[string]*VersionEntry{}
	}
	cl := closure(st.Edges)
	for _, lreq := range req.Langs {
		ls := st.Langs[lreq.Language]
		if ls == nil {
			batch.Langs = append(batch.Langs, LangFix{
				Language: lreq.Language, Status: "conflict",
				Reason: "language is not imported", At: s.now()})
			continue
		}
		if lreq.ExpectedFP != "" && ls.CurrentFP != lreq.ExpectedFP {
			batch.Langs = append(batch.Langs, LangFix{
				Language: lreq.Language, ExpectedFP: lreq.ExpectedFP,
				Status: "conflict",
				Reason: fmt.Sprintf("version conflict: expected %s but language is at %s",
					shortFP(lreq.ExpectedFP), shortFP(ls.CurrentFP)),
				At: s.now()})
			continue
		}
		ver := st.Versions[ls.CurrentFP]
		if ver == nil {
			batch.Langs = append(batch.Langs, LangFix{
				Language: lreq.Language, Status: "conflict",
				Reason: "current version is unavailable", At: s.now()})
			continue
		}
		// Apply confirmed renames to keys still using historical aliases.
		var nextMsgs []catalog.Message
		changed := false
		for _, m := range ver.Messages {
			key := m.Key
			if cur, ok := cl[m.Key]; ok && cur != m.Key {
				key = cur
				changed = true
			}
			nextMsgs = append(nextMsgs, catalog.Message{Key: key, Text: m.Text, Context: m.Context})
		}
		if !changed {
			batch.Langs = append(batch.Langs, LangFix{
				Language: lreq.Language, ExpectedFP: lreq.ExpectedFP,
				AppliedFP: ver.Fingerprint, Status: "already-applied", At: s.now()})
			continue
		}
		newFP := catalog.Fingerprint(nextMsgs)
		if _, ok := st.Versions[newFP]; ok {
			// Result already exists: move pointer without creating a version.
			batch.Langs = append(batch.Langs, LangFix{
				Language: lreq.Language, ExpectedFP: lreq.ExpectedFP,
				AppliedFP: newFP, Status: "already-applied", At: s.now()})
			// Pointer move itself still happens via the event below.
			batch.NewVersions[newFP] = st.Versions[newFP]
			continue
		}
		nv := buildVersion(newFP, nextMsgs, s.now())
		batch.NewVersions[newFP] = nv
		batch.Langs = append(batch.Langs, LangFix{
			Language: lreq.Language, ExpectedFP: lreq.ExpectedFP,
			AppliedFP: newFP, Status: "applied", At: s.now()})
	}
}

// commitBatch writes one event covering all languages. Because all new
// versions are carried in the event and the event is the only state change,
// conflicted languages are simply left untouched while applied languages
// advance: there is no cross-language rollback.
func (s *Store) commitBatch(b *BatchFix) error {
	if b.At == 0 {
		b.At = s.now()
	}
	ev := &Event{Type: "batch", At: s.now(), Batch: &BatchEvent{Batch: b}}
	return s.commit(ev)
}

func shortFP(fp string) string {
	if len(fp) > 10 {
		return fp[:10]
	}
	return fp
}
