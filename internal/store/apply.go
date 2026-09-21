package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

func newSHA256() interface {
	Write([]byte) (int, error)
	Sum([]byte) []byte
} {
	return sha256.New()
}

func hashStrings(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (s *Store) apply(ev *Event) {
	st := s.state
	st.Seq++
	switch ev.Type {
	case "import":
		e := ev.Import
		if _, ok := st.Versions[e.Version.Fingerprint]; !ok {
			st.Versions[e.Version.Fingerprint] = e.Version
		}
		ls := st.Langs[e.Language]
		if ls == nil {
			ls = &LangState{Name: e.Language}
			st.Langs[e.Language] = ls
		}
		ls.CurrentFP = e.Version.Fingerprint
		ls.Imports = append(ls.Imports, Import{
			Fingerprint: e.Version.Fingerprint,
			Raw:         e.Raw,
			At:          ev.At,
			AsBaseline:  e.AsBaseline,
		})
		if e.NewBaseline != "" {
			st.Baseline = e.NewBaseline
		} else if st.Baseline == "" {
			st.Baseline = e.Language
		}
		if e.Proposal != nil {
			st.Proposals[e.Proposal.ID] = *e.Proposal
		}
		if e.OpKey != "" {
			res := ImportResult{Language: e.Language, Fingerprint: e.Version.Fingerprint,
				Version: e.Version, NewVersion: false, RawSHA: e.Raw.BlobSHA,
				Seq: st.Seq, IsBaseline: st.Baseline == e.Language, Proposal: e.Proposal}
			b, _ := jsonMarshal(res)
			st.Idem[e.OpKey] = IdemRecord{OpKey: e.OpKey, ReqHash: e.ReqHash,
				Kind: "import", Seq: st.Seq, At: ev.At, Result: string(b)}
		}
	case "rename":
		e := ev.Rename
		st.Edges = append(st.Edges, e.Edges...)
		if e.Resolve != "" {
			if p, ok := st.Proposals[e.Resolve]; ok {
				p.Resolved = true
				st.Proposals[e.Resolve] = p
			}
		}
		st.Idem[e.OpKey] = IdemRecord{OpKey: e.OpKey, ReqHash: e.ReqHash,
			Kind: "rename", Seq: st.Seq, At: ev.At}
	case "exemption":
		e := ev.Exemption
		if e.Revoke {
			if x, ok := st.Exemptions[e.IssueID]; ok {
				x.Revoked = true
				st.Exemptions[e.IssueID] = x
			}
		} else {
			st.Exemptions[e.IssueID] = Exemption{
				IssueID: e.IssueID, Reason: e.Reason, Deadline: e.Deadline,
				Created: ev.At, OpKey: e.OpKey,
			}
		}
		st.Idem[e.OpKey] = IdemRecord{OpKey: e.OpKey, ReqHash: e.ReqHash,
			Kind: "exemption", Seq: st.Seq, At: ev.At}
	case "batch":
		b := ev.Batch.Batch
		st.Batches[b.OpKey] = b
		for fp, v := range b.NewVersions {
			if _, ok := st.Versions[fp]; !ok {
				st.Versions[fp] = v
			}
		}
		// Applied versions create new catalog versions and move language
		// pointers; the version and raw entries are stored inside LangFix?
		// They are carried via dedicated maps below.
		for _, lf := range b.Langs {
			if lf.Status == "applied" {
				if v, ok := st.Versions[lf.AppliedFP]; ok {
					if ls := st.Langs[lf.Language]; ls != nil {
						ls.CurrentFP = lf.AppliedFP
						_ = v
					}
				}
			}
		}
		st.Idem[b.OpKey] = IdemRecord{OpKey: b.OpKey, ReqHash: b.ReqHash,
			Kind: "batch", Seq: st.Seq, At: ev.At}
	case "proof":
		pr := ev.Proof.Proof
		st.Proofs[pr.ID] = *pr
		st.LastProofID = pr.ID
		if ev.Proof.OpKey != "" {
			st.ProofOps[ev.Proof.OpKey] = pr.ID
		}
		st.Idem["proof:"+pr.ID] = IdemRecord{OpKey: "proof:" + pr.ID,
			Kind: "proof", Seq: st.Seq, At: ev.At}
	}
}

// materializeSnapshot stores the immutable snapshot for the new seq.
func (s *Store) materializeSnapshot(ev *Event) {
	st := s.state
	issues := Validate(st, ev.At)
	sig := MappingSignature(st.Edges)
	langFPs := map[string]string{}
	for name, ls := range st.Langs {
		langFPs[name] = ls.CurrentFP
	}
	edgesCopy := make([]Edge, len(st.Edges))
	copy(edgesCopy, st.Edges)
	props := map[string]RenameProposal{}
	for k, v := range st.Proposals {
		props[k] = v
	}
	exCopy := map[string]Exemption{}
	for k, v := range st.Exemptions {
		exCopy[k] = v
	}
	issuesCopy := make([]Issue, len(issues))
	copy(issuesCopy, issues)
	id := SnapshotID(st.Seq, langFPs, sig)
	snap := &Snapshot{
		Seq: st.Seq, ID: id, At: ev.At, Baseline: st.Baseline,
		LangFPs: langFPs, MappingSig: sig, Edges: edgesCopy,
		Proposals: props, Exemptions: exCopy, Issues: issuesCopy,
		ParserVersion: icuParserVersion(), RunID: "",
	}
	st.Snapshots[st.Seq] = snap
	s.updateRuns(ev.At)
}

func icuParserVersion() string { return parserVersionConst }
