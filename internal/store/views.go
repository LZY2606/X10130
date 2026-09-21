package store

import "sort"

type IssueView struct {
	Issue     Issue     `json:"issue"`
	Exemption *Exemption `json:"exemption,omitempty"`
	Active    bool      `json:"exempt_active"`
}

type SnapshotView struct {
	Seq             int64        `json:"seq"`
	ID              string       `json:"id"`
	At              int64        `json:"at"`
	ParserVersion   string       `json:"parser_version"`
	Baseline        string       `json:"baseline"`
	LangFPs         map[string]string `json:"lang_fps"`
	MappingSig      string       `json:"mapping_sig"`
	RunID           string       `json:"run_id"`
	RunStatus       string       `json:"run_status"`
	RunInvalidReason string      `json:"run_invalid_reason,omitempty"`
	Proposals       []RenameProposal `json:"proposals"`
	Issues          []IssueView  `json:"issues"`
	UnexemptedCount int          `json:"unexempted_count"`
	TotalIssues     int          `json:"total_issues"`
	Head            bool         `json:"head"`
}

func (s *Store) CurrentSeq() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Seq
}

func (s *Store) Snapshot(seq int64) (*Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if seq == 0 {
		seq = s.state.Seq
	}
	sn, ok := s.state.Snapshots[seq]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *sn
	return &cp, nil
}

func (s *Store) View(seq int64) (*SnapshotView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if seq == 0 {
		seq = s.state.Seq
	}
	sn, ok := s.state.Snapshots[seq]
	if !ok {
		return nil, ErrNotFound
	}
	now := s.now()
	view := &SnapshotView{
		Seq: sn.Seq, ID: sn.ID, At: sn.At, ParserVersion: sn.ParserVersion,
		Baseline: sn.Baseline, LangFPs: map[string]string{},
		MappingSig: sn.MappingSig, RunID: sn.RunID, Head: seq == s.state.Seq,
	}
	for k, v := range sn.LangFPs {
		view.LangFPs[k] = v
	}
	if r, ok := s.state.Runs[sn.RunID]; ok {
		view.RunStatus = r.Status
		view.RunInvalidReason = r.InvalidReason
	}
	for _, p := range sn.Proposals {
		view.Proposals = append(view.Proposals, p)
	}
	sort.Slice(view.Proposals, func(a, b int) bool {
		return view.Proposals[a].ID < view.Proposals[b].ID
	})
	for _, is := range sn.Issues {
		iv := IssueView{Issue: is}
		if ex, ok := sn.Exemptions[is.ID]; ok {
			e := ex
			iv.Exemption = &e
			iv.Active = exemptionActive(ex, now)
			if !iv.Active {
				view.UnexemptedCount++
			}
		} else {
			view.UnexemptedCount++
		}
		view.Issues = append(view.Issues, iv)
	}
	view.TotalIssues = len(sn.Issues)
	return view, nil
}

type LangMessageView struct {
	Language string           `json:"language"`
	Version  string           `json:"version"`
	Message  *catalogMessage  `json:"message,omitempty"`
	FoundAt  string           `json:"found_at,omitempty"` // actual key located (alias)
}

type KeyView struct {
	Key       string            `json:"key"`
	Snapshot  *SnapshotView     `json:"snapshot"`
	Languages []LangMessageView `json:"languages"`
}

// KeyView returns the side-by-side parsed structures for one baseline key
// across all languages, resolving confirmed rename aliases.
func (s *Store) KeyView(seq int64, key string) (*KeyView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if seq == 0 {
		seq = s.state.Seq
	}
	sn, ok := s.state.Snapshots[seq]
	if !ok {
		return nil, ErrNotFound
	}
	cl := closure(sn.Edges)
	// aliases landing on key
	var aliases []string
	for old, cur := range cl {
		if cur == key && old != key {
			aliases = append(aliases, old)
		}
	}
	names := make([]string, 0, len(sn.LangFPs))
	for n := range sn.LangFPs {
		names = append(names, n)
	}
	sort.Strings(names)
	out := &KeyView{Key: key}
	for _, name := range names {
		fp := sn.LangFPs[name]
		ver := s.state.Versions[fp]
		lv := LangMessageView{Language: name, Version: fp}
		if ver != nil {
			if m := findMessage(ver, key); m != nil {
				lv.Message = m
				lv.FoundAt = key
			} else {
				for _, a := range aliases {
					if m := findMessage(ver, a); m != nil {
						cp := *m
						cp.Key = key // show under effective key
						lv.Message = &cp
						lv.FoundAt = a
						break
					}
				}
			}
		}
		out.Languages = append(out.Languages, lv)
	}
	return out, nil
}

func findMessage(ver *VersionEntry, key string) *catalogMessage {
	for i := range ver.Messages {
		if ver.Messages[i].Key == key {
			return &ver.Messages[i]
		}
	}
	return nil
}

type StateSummary struct {
	Seq             int64             `json:"seq"`
	SnapshotID      string            `json:"snapshot_id"`
	ParserVersion   string            `json:"parser_version"`
	Baseline        string            `json:"baseline"`
	Languages       []LangSummary     `json:"languages"`
	MappingSig      string            `json:"mapping_sig"`
	Edges           []Edge            `json:"edges"`
	OpenProposals   []RenameProposal  `json:"open_proposals"`
	Proofs          []ProofRecord     `json:"proofs"`
	Recovery        RecoveryReport    `json:"recovery"`
	ReadOnly        bool              `json:"read_only"`
	LastProofID     string            `json:"last_proof_id"`
}

type LangSummary struct {
	Name      string   `json:"name"`
	CurrentFP string   `json:"current_fp"`
	IsBaseline bool    `json:"is_baseline"`
	Imports   []Import `json:"imports"`
}

func (s *Store) Summary() *StateSummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.state
	sum := &StateSummary{
		Seq: st.Seq, ParserVersion: parserVersionConst, Baseline: st.Baseline,
		MappingSig: MappingSignature(st.Edges), Edges: append([]Edge{}, st.Edges...),
		Recovery: s.recovery, ReadOnly: s.readOnly, LastProofID: st.LastProofID,
	}
	if sn, ok := st.Snapshots[st.Seq]; ok {
		sum.SnapshotID = sn.ID
	}
	names := make([]string, 0, len(st.Langs))
	for n := range st.Langs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		ls := st.Langs[n]
		sum.Languages = append(sum.Languages, LangSummary{
			Name: n, CurrentFP: ls.CurrentFP, IsBaseline: n == st.Baseline,
			Imports: append([]Import{}, ls.Imports...),
		})
	}
	for _, p := range st.Proposals {
		if !p.Resolved {
			sum.OpenProposals = append(sum.OpenProposals, p)
		}
	}
	sort.Slice(sum.OpenProposals, func(a, b int) bool {
		return sum.OpenProposals[a].At < sum.OpenProposals[b].At
	})
	for _, pr := range st.Proofs {
		pr.Current = s.proofIsCurrent(pr.Document)
		sum.Proofs = append(sum.Proofs, pr)
	}
	sort.Slice(sum.Proofs, func(a, b int) bool { return sum.Proofs[a].SavedAt < sum.Proofs[b].SavedAt })
	return sum
}

func (s *Store) Runs() []Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Run
	for _, r := range s.state.Runs {
		out = append(out, *r)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Seq > out[b].Seq })
	return out
}

func (s *Store) SnapshotSeqList() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []int64
	for seq := range s.state.Snapshots {
		out = append(out, seq)
	}
	sort.Slice(out, func(a, b int) bool { return out[a] > out[b] })
	return out
}
