package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

type ProofRequest struct {
	Seq  int64 `json:"seq"`  // pin a snapshot; 0 = latest committed
	AsOf int64 `json:"as_of"` // evaluation time for exemptions; 0 = snapshot commit time
}

type ProofOutcome struct {
	Record     *ProofRecord `json:"record"`
	Bytes      []byte       `json:"-"`
	Idempotent bool         `json:"idempotent_replay,omitempty"`
}

// GenerateProof builds the proof for one immutable snapshot. The same
// (snapshot, asOf) always yields byte-identical output.
func (s *Store) GenerateProof(opKey string, req ProofRequest) (*ProofOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readOnly {
		return nil, ErrReadOnly
	}
	st := s.state
	if opKey != "" {
		if id, ok := st.ProofOps[opKey]; ok {
			if existing, ok2 := st.Proofs[id]; ok2 {
				if data, err := s.readBlob("proof", id); err == nil {
					return &ProofOutcome{Record: &existing, Bytes: data, Idempotent: true}, nil
				}
			}
			return nil, ErrReadOnly
		}
	}
	seq := req.Seq
	if seq == 0 {
		seq = st.Seq
	}
	snap, ok := st.Snapshots[seq]
	if !ok {
		return nil, fmt.Errorf("%w: snapshot seq %d", ErrNotFound, seq)
	}
	asOf := req.AsOf
	if asOf == 0 {
		asOf = snap.At
	}
	doc := s.buildProofDoc(snap, asOf)
	id := doc.ProofID
	if existing, ok := st.Proofs[id]; ok {
		data, err := s.readBlob("proof", id)
		if err == nil {
			cur := existing
			cur.Current = snap.Seq == st.Seq
			cur.Seq = snap.Seq
			return &ProofOutcome{Record: &cur, Bytes: data, Idempotent: true}, nil
		}
	}
	data := mustProofJSON(doc)
	// Blob first, fsynced, then the referencing event -> no torn proof is
	// ever marked valid.
	if _, err := s.putBlobAs("proof", id, data); err != nil {
		return nil, err
	}
	rec := &ProofRecord{ID: id, Seq: snap.Seq, AsOf: asOf,
		Current: snap.Seq == st.Seq, Document: doc, SavedAt: s.now()}
	ev := &Event{Type: "proof", At: s.now(), Proof: &ProofEvent{Proof: rec, OpKey: opKey}}
	if err := s.commit(ev); err != nil {
		return nil, err
	}
	saved := st.Proofs[id]
	return &ProofOutcome{Record: &saved, Bytes: data}, nil
}

func (s *Store) buildProofDoc(snap *Snapshot, asOf int64) ProofDocument {
	fps := map[string]string{}
	for name, fp := range snap.LangFPs {
		fps[name] = fp
	}
	var exempts []ProofExemption
	unexempted := 0
	active := map[string]bool{}
	for _, is := range snap.Issues {
		if ex, ok := snap.Exemptions[is.ID]; ok && exemptionActive(ex, asOf) {
			active[is.ID] = true
			exempts = append(exempts, ProofExemption{
				IssueID: is.ID, Reason: ex.Reason, Deadline: ex.Deadline,
			})
			continue
		}
		unexempted++
	}
	sort.Slice(exempts, func(a, b int) bool { return exempts[a].IssueID < exempts[b].IssueID })
	doc := ProofDocument{
		Kind:              "message-catalog-release-proof/v1",
		ParserVersion:     snap.ParserVersion,
		SnapshotID:        snap.ID,
		Seq:               snap.Seq,
		AsOf:              asOf,
		InputFingerprints: fps,
		Baseline:          BaselineRef{Language: snap.Baseline, Fingerprint: snap.LangFPs[snap.Baseline]},
		MappingSig:        snap.MappingSig,
		UnexemptedCount:   unexempted,
		IssueCount:        len(snap.Issues),
		Exemptions:        exempts,
	}
	// Deterministic id over canonical bytes with the id field blank.
	doc.ProofID = "proof-" + contentID(doc)
	return doc
}

// contentID hashes the document excluding ProofID, with sorted map keys.
func contentID(doc ProofDocument) string {
	clone := doc
	clone.ProofID = ""
	b := canonicalJSON(clone)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:20])
}

func mustProofJSON(doc ProofDocument) []byte {
	return canonicalJSON(doc)
}

// canonicalJSON marshals with sorted object keys (Go encoding/json already
// sorts map keys) and a trailing newline, deterministically.
func canonicalJSON(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return buf.Bytes()
}

// proofIsCurrent reports whether the proof matches the latest committed
// catalog/mapping/parser state (exemption timing is captured by asOf and does
// not make a proof stale).
func (s *Store) proofIsCurrent(doc ProofDocument) bool {
	head := s.state.Snapshots[s.state.Seq]
	if head == nil {
		return false
	}
	if head.ParserVersion != doc.ParserVersion ||
		head.MappingSig != doc.MappingSig ||
		head.Baseline != doc.Baseline.Language ||
		head.LangFPs[head.Baseline] != doc.Baseline.Fingerprint {
		return false
	}
	if len(head.LangFPs) != len(doc.InputFingerprints) {
		return false
	}
	for k, v := range head.LangFPs {
		if doc.InputFingerprints[k] != v {
			return false
		}
	}
	return true
}

func (s *Store) ProofBytes(id string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.state.Proofs[id]; !ok {
		return nil, ErrNotFound
	}
	data, err := s.readBlob("proof", id)
	if err != nil {
		return nil, ErrNotFound
	}
	return data, nil
}
