package service

import (
	"encoding/json"
	"fmt"

	"messagecatalog/internal/catalog"
)

// BatchFixItem is one language in a batch correction request.
type BatchFixItem struct {
	Language    string `json:"language"`
	BaseVersion string `json:"baseVersion"`
	Content     []byte `json:"content"`
}

// BatchFixRequest applies corrections to several languages at once.
type BatchFixRequest struct {
	Items []BatchFixItem `json:"items"`
}

// BatchFixResponse reports each language independently.
type BatchFixResponse struct {
	BatchID    string                  `json:"batchId"`
	Results    map[string]*BatchResult `json:"results"`
	Succeeded  []string                `json:"succeeded"`
	Conflicted []string                `json:"conflicted"`
	// Overall snapshot of the first successful application; partial success
	// means successful languages are committed independently.
	CommittedSeq int `json:"committedSeq,omitempty"`
}

func (r *BatchFixResponse) snapID() string { return r.BatchID }
func (r *BatchFixResponse) snapSeq() int   { return r.CommittedSeq }

// BatchFix applies corrections per language. One language's version conflict
// never rolls back already successful languages; retries with the same op id
// only process the unfinished items.
func (s *Service) BatchFix(opID string, req BatchFixRequest) (*BatchFixResponse, error) {
	if err := s.checkWritable(); err != nil {
		return nil, err
	}
	if len(req.Items) == 0 {
		return nil, &RequestError{Msg: "batch requires at least one item"}
	}
	// Deterministic item keying for the idempotent partial record.
	type itemKey struct {
		language string
		reqSHA   string
	}
	keys := make([]itemKey, len(req.Items))
	serial := map[string]json.RawMessage{}
	for i, it := range req.Items {
		c, err := catalog.Parse(it.Language, it.Content)
		if err != nil {
			return nil, &RequestError{Msg: it.Language + ": " + err.Error()}
		}
		b, _ := json.Marshal(map[string]any{
			"language": it.Language, "baseVersion": it.BaseVersion,
			"contentVersion": c.Fingerprint(), "rawSha": rawSHA(it.Content),
		})
		keys[i] = itemKey{it.Language, rawSHA(b)}
		serial[it.Language] = b
	}
	allSHA := func() string {
		var buf []byte
		for _, k := range keys {
			buf = append(buf, []byte(k.language+":"+k.reqSHA+"\n")...)
		}
		return rawSHA(buf)
	}

	s.mu.Lock()
	if existing, ok := s.state.Batches[opID]; ok {
		s.mu.Unlock()
		return s.replayBatch(opID, existing, req, allSHA())
	}
	batch := &Batch{ID: opID, Results: map[string]*BatchResult{}, CreatedAt: s.nowRFC()}
	for _, it := range req.Items {
		batch.Items = append(batch.Items, &BatchItem{
			Language: it.Language, BaseVersion: it.BaseVersion,
			Raw: append([]byte(nil), it.Content...), RequestSHA: func() string {
				b, _ := json.Marshal(serial[it.Language])
				return rawSHA(b)
			}(),
		})
	}
	s.state.Batches[opID] = batch
	s.mu.Unlock()

	resp := &BatchFixResponse{BatchID: opID, Results: map[string]*BatchResult{}}
	for _, it := range req.Items {
		// Each item is a distinct op under the batch id; skip already-done.
		s.mu.RLock()
		done := s.state.Batches[opID].Results[it.Language]
		s.mu.RUnlock()
		if done != nil && done.Status == "success" {
			resp.Results[it.Language] = done
			resp.Succeeded = append(resp.Succeeded, it.Language)
			resp.CommittedSeq = done.Seq
			continue
		}
		br := s.applyBatchItem(opID, it)
		resp.Results[it.Language] = br
		if br.Status == "success" {
			resp.Succeeded = append(resp.Succeeded, it.Language)
			resp.CommittedSeq = br.Seq
		} else {
			resp.Conflicted = append(resp.Conflicted, it.Language)
		}
	}
	sortStrings(resp.Succeeded)
	sortStrings(resp.Conflicted)

	// Record an umbrella operation for duplicate-detection across restarts.
	s.mu.Lock()
	rb, _ := json.Marshal(resp)
	s.state.Operations[opID] = &Operation{
		ID: opID, Kind: "batch", RequestSHA: allSHA(), Status: "applied",
		ResultJSON: string(rb), Seq: resp.CommittedSeq, CreatedAt: s.nowRFC(),
	}
	s.mu.Unlock()

	return resp, nil
}

// replayBatch verifies request identity and replays only unfinished items.
func (s *Service) replayBatch(opID string, batch *Batch, req BatchFixRequest, reqSHA string) (*BatchFixResponse, error) {
	// Same op id with different content for an already-successful item is a
	// hard conflict; changed preconditions for unfinished items are allowed
	// (that is exactly the retry-after-conflict flow).
	byLang := map[string]BatchFixItem{}
	for _, it := range req.Items {
		byLang[it.Language] = it
	}
	// Compute request identity per item (content sha).
	for _, stored := range batch.Items {
		it, ok := byLang[stored.Language]
		if !ok {
			continue
		}
		c, err := catalog.Parse(it.Language, it.Content)
		if err != nil {
			return nil, &RequestError{Msg: it.Language + ": " + err.Error()}
		}
		b, _ := json.Marshal(map[string]any{
			"language": it.Language, "baseVersion": it.BaseVersion,
			"contentVersion": c.Fingerprint(), "rawSha": rawSHA(it.Content),
		})
		thisSHA := rawSHA(b)
		got := batch.Results[stored.Language]
		if got != nil && got.Status == "success" && stored.RequestSHA != thisSHA {
			return nil, &ConflictError{Msg: fmt.Sprintf(
				"operation %s already applied a different correction for %s; refusing to overwrite",
				opID, stored.Language)}
		}
	}
	resp := &BatchFixResponse{BatchID: opID, Results: map[string]*BatchResult{}}
	for _, it := range req.Items {
		got := batch.Results[it.Language]
		if got != nil && got.Status == "success" {
			resp.Results[it.Language] = got
			resp.Succeeded = append(resp.Succeeded, it.Language)
			resp.CommittedSeq = got.Seq
			continue
		}
		br := s.applyBatchItem(opID, it)
		resp.Results[it.Language] = br
		if br.Status == "success" {
			resp.Succeeded = append(resp.Succeeded, it.Language)
			resp.CommittedSeq = br.Seq
		} else {
			resp.Conflicted = append(resp.Conflicted, it.Language)
		}
	}
	sortStrings(resp.Succeeded)
	sortStrings(resp.Conflicted)
	// refresh umbrella result
	s.mu.Lock()
	rb, _ := json.Marshal(resp)
	if op, ok := s.state.Operations[opID]; ok {
		op.ResultJSON = string(rb)
	}
	s.mu.Unlock()
	return resp, nil
}

// applyBatchItem commits one language, checking its base version. It never
// touches other languages and does not create a new version on identical
// content.
func (s *Service) applyBatchItem(batchID string, it BatchFixItem) *BatchResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing := s.state.Batches[batchID].Results[it.Language]; existing != nil && existing.Status == "success" {
		return existing
	}
	c, err := catalog.Parse(it.Language, it.Content)
	if err != nil {
		return &BatchResult{Language: it.Language, Status: "error", Reason: err.Error()}
	}
	cur := s.state.Catalogs[it.Language]
	if cur != nil && cur.Version != it.BaseVersion {
		return &BatchResult{
			Language: it.Language, Status: "conflict",
			Reason: fmt.Sprintf("version conflict: viewed=%s current=%s",
				short(it.BaseVersion), short(cur.Version)),
		}
	}
	newVer := c.Fingerprint()
	if cur != nil && cur.Version == newVer {
		// Identical correction already present: no new version/mapping.
		br := &BatchResult{
			Language: it.Language, Status: "success",
			NewVersion: cur.Version, UploadID: cur.UploadID,
			Reason: "content unchanged; no new version created",
			Seq:    cur.Seq, SnapshotID: s.latestSnapshotID(),
		}
		s.state.Batches[batchID].Results[it.Language] = br
		return br
	}
	rawBlob, err := s.st.PutBlob(it.Content)
	if err != nil {
		return &BatchResult{Language: it.Language, Status: "error", Reason: err.Error()}
	}
	catBlob, err := s.storeCatalogBlob(c)
	if err != nil {
		return &BatchResult{Language: it.Language, Status: "error", Reason: err.Error()}
	}
	s.state.Seq++
	seq := s.state.Seq
	up := &Upload{ID: "up-" + rawBlob[:16], Language: it.Language, RawBlob: rawBlob,
		Size: len(it.Content), ImportedAt: s.nowRFC(), Seq: seq}
	s.state.Uploads[it.Language] = up
	s.state.Catalogs[it.Language] = &CatalogVersion{
		Language: it.Language, Version: newVer, Blob: catBlob,
		UploadID: up.ID, ImportedAt: s.nowRFC(), Seq: seq,
	}
	if _, _, err := s.reportsLocked(); err != nil {
		return &BatchResult{Language: it.Language, Status: "error", Reason: err.Error()}
	}
	snap, err := s.commitLocked("batch-fix:" + it.Language)
	if err != nil {
		return &BatchResult{Language: it.Language, Status: "error", Reason: err.Error()}
	}
	up.SnapshotID = snap.ID
	br := &BatchResult{
		Language: it.Language, Status: "success", NewVersion: newVer,
		UploadID: up.ID, Seq: snap.Seq, SnapshotID: snap.ID,
	}
	s.state.Batches[batchID].Results[it.Language] = br
	return br
}

func (s *Service) latestSnapshotID() string {
	if len(s.state.Snapshots) == 0 {
		return ""
	}
	return s.state.Snapshots[len(s.state.Snapshots)-1].ID
}

func sortStrings(x []string) {
	// simple insertion sort to avoid import cycles concerns; stdlib sort is fine
	for i := 1; i < len(x); i++ {
		for j := i; j > 0 && x[j-1] > x[j]; j-- {
			x[j-1], x[j] = x[j], x[j-1]
		}
	}
}
