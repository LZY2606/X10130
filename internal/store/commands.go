package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"msgcatalog/internal/catalog"
	"msgcatalog/internal/icu"
)

// ImportResult is the outcome of one import.
type ImportResult struct {
	Language    string         `json:"language"`
	Fingerprint string         `json:"fingerprint"`
	Version     *VersionEntry  `json:"version"`
	NewVersion  bool           `json:"new_version"`
	Repeated    bool           `json:"repeated"`
	RawSHA      string         `json:"raw_sha"`
	SnapshotID  string         `json:"snapshot_id"`
	Seq         int64          `json:"seq"`
	IsBaseline  bool           `json:"is_baseline"`
	Proposal    *RenameProposal `json:"proposal,omitempty"`
	Idempotent  bool           `json:"idempotent_replay,omitempty"`
}

type ImportRequest struct {
	Language   string `json:"language"`
	Raw        []byte `json:"-"`
	AsBaseline bool   `json:"as_baseline"`
}

func (r ImportRequest) contentHash() string {
	h := sha256.New()
	h.Write([]byte(r.Language))
	h.Write([]byte{0})
	h.Write(r.Raw)
	h.Write([]byte{0})
	if r.AsBaseline {
		h.Write([]byte{1})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (s *Store) checkIdem(opKey, reqHash, kind string) (*IdemRecord, bool, error) {
	if opKey == "" {
		return nil, false, nil
	}
	if rec, ok := s.state.Idem[opKey]; ok {
		if rec.ReqHash != reqHash {
			return nil, false, ErrIdemConflict
		}
		return &rec, true, nil
	}
	return nil, false, nil
}

func (s *Store) Import(opKey string, req ImportRequest) (*ImportResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readOnly {
		return nil, ErrReadOnly
	}
	reqHash := req.contentHash()
	if rec, dup, err := s.checkIdem(opKey, reqHash, "import"); err != nil {
		return nil, err
	} else if dup {
		return s.importReplay(rec), nil
	}
	if strings.TrimSpace(req.Language) == "" {
		return nil, fmt.Errorf("%w: language is required", ErrBadRequest)
	}
	msgs, err := catalog.ParseImport(req.Raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadRequest, err)
	}
	fp := catalog.Fingerprint(msgs)
	rawSHA, err := s.putBlob("raw", req.Raw)
	if err != nil {
		return nil, err
	}
	var ver *VersionEntry
	_, existed := s.state.Versions[fp]
	if existed {
		ver = s.state.Versions[fp]
	} else {
		ver = buildVersion(fp, msgs, s.now())
	}

	newBaseline := ""
	if req.AsBaseline {
		newBaseline = req.Language
	} else if s.state.Baseline == "" {
		newBaseline = req.Language
	}

	var proposal *RenameProposal
	// Baseline re-import with changed keys => proposal, never auto rename.
	if (req.AsBaseline || s.state.Baseline == req.Language) && s.state.Baseline != "" {
		if old := s.state.Langs[req.Language]; old != nil && old.CurrentFP != "" && old.CurrentFP != fp {
			if ov := s.state.Versions[old.CurrentFP]; ov != nil {
				proposal = buildProposal(ov, ver, s.now())
			}
		}
	}

	ev := &Event{Type: "import", At: s.now(), Import: &ImportEvent{
		Language: req.Language, AsBaseline: req.AsBaseline, Version: ver,
		Raw: RawRef{BlobSHA: rawSHA, Size: len(req.Raw), SavedAt: s.now()},
		OpKey: opKey, ReqHash: reqHash, NewBaseline: newBaseline,
		Proposal: proposal,
	}}
	if err := s.commit(ev); err != nil {
		return nil, err
	}
	snap := s.state.Snapshots[s.state.Seq]
	return &ImportResult{
		Language: req.Language, Fingerprint: fp, Version: ver,
		NewVersion: !existed, Repeated: existed, RawSHA: rawSHA,
		SnapshotID: snap.ID, Seq: s.state.Seq,
		IsBaseline: s.state.Baseline == req.Language, Proposal: proposal,
	}, nil
}

func (s *Store) importReplay(rec *IdemRecord) *ImportResult {
	var res ImportResult
	if rec.Result != "" {
		_ = jsonUnmarshal([]byte(rec.Result), &res)
	}
	snap := s.state.Snapshots[rec.Seq]
	res.SnapshotID = snap.ID
	res.Seq = rec.Seq
	res.Idempotent = true
	return &res
}

func buildVersion(fp string, msgs []catalog.Message, now int64) *VersionEntry {
	cms := make([]catalogMessage, 0, len(msgs))
	for _, m := range msgs {
		cms = append(cms, catalogMessage{
			Key: m.Key, Text: catalog.NormalizeText(m.Text),
			Context: m.Context, AST: icu.Parse(catalog.NormalizeText(m.Text)),
		})
	}
	sort.Slice(cms, func(a, b int) bool { return cms[a].Key < cms[b].Key })
	return &VersionEntry{Fingerprint: fp, ParserVersion: icu.ParserVersion,
		Messages: cms, CreatedAt: now}
}

func buildProposal(old, new *VersionEntry, now int64) *RenameProposal {
	oldSet := map[string]bool{}
	newSet := map[string]bool{}
	for _, m := range old.Messages {
		oldSet[m.Key] = true
	}
	for _, m := range new.Messages {
		newSet[m.Key] = true
	}
	var oldOnly, newOnly, common []string
	for k := range oldSet {
		if newSet[k] {
			common = append(common, k)
		} else {
			oldOnly = append(oldOnly, k)
		}
	}
	for k := range newSet {
		if !oldSet[k] {
			newOnly = append(newOnly, k)
		}
	}
	sort.Strings(oldOnly)
	sort.Strings(newOnly)
	sort.Strings(common)
	id := "proposal-" + hashStrings(old.Fingerprint, new.Fingerprint)
	return &RenameProposal{ID: id, BeforeFP: old.Fingerprint, AfterFP: new.Fingerprint,
		OldOnly: oldOnly, NewOnly: newOnly, Common: common, At: now}
}

func (s *Store) RawBytes(blobSHA string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.readBlob("raw", blobSHA)
	if err != nil {
		return nil, ErrNotFound
	}
	if catalog.HashBytes(data) != blobSHA {
		return nil, fmt.Errorf("%w: blob checksum mismatch", ErrReadOnly)
	}
	return data, nil
}

func stableJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
