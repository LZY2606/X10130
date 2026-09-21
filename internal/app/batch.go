package app

import (
	"crypto/sha256"

	"msgcheck/internal/catalog"
	"msgcheck/internal/state"
)

// BatchItemRequest is one language's fix payload.
type BatchItemRequest struct {
	Language    string `json:"language"`
	Content     []byte `json:"-"`
	Filename    string `json:"filename,omitempty"`
	BaseVersion string `json:"base_version"`
}

// BatchRequest applies fixes to several languages in one call.
type BatchRequest struct {
	Items            []BatchItemRequest `json:"items"`
	ExpectedSnapshot string             `json:"expected_snapshot,omitempty"`
	OpID             string             `json:"op_id,omitempty"`
}

// BatchResponse is the idempotent per-language outcome.
type BatchResponse struct {
	OpID     string            `json:"op_id"`
	Items    []state.BatchItem `json:"items"`
	Complete bool              `json:"complete"`
	Snapshot state.Pin         `json:"snapshot"`
}

// BatchFix applies each language independently. A version conflict on one
// language never rolls back others. Retrying the same operation id only
// processes languages that did not previously succeed, and re-applying an
// already-fixed language creates no new version. Each successful language
// plus the updated receipt lands in exactly one durable frame.
func (a *App) BatchFix(req BatchRequest) (*BatchResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := a.advanceLocked(); err != nil {
		return nil, err
	}
	if err := a.checkSnapshotLocked(req.ExpectedSnapshot); err != nil {
		return nil, err
	}
	if a.data.Baseline == nil {
		return nil, &BadRequestError{Reason: "import a baseline catalog first"}
	}
	if len(req.Items) == 0 {
		return nil, &BadRequestError{Reason: "batch has no items"}
	}
	h := sha256.New()
	for _, it := range req.Items {
		h.Write([]byte(it.Language))
		h.Write([]byte{0})
		h.Write([]byte(it.BaseVersion))
		h.Write([]byte{0})
		h.Write(it.Content)
		h.Write([]byte{0})
	}
	reqFP := hexEncode(h.Sum(nil))

	if req.OpID != "" {
		if op, ok := a.data.Ops[req.OpID]; ok {
			if op.Request != reqFP {
				return nil, &ConflictError{Reason: "operation id " + req.OpID + " was already submitted with different content", Current: a.pinLocked()}
			}
			if b, ok := a.data.Batches[req.OpID]; ok {
				return a.batchResponse(b), nil
			}
		}
	}

	batch := a.data.Batches[req.OpID]
	if batch == nil {
		batch = &state.Batch{OpID: req.OpID, CreatedAt: a.now()}
		for _, it := range req.Items {
			batch.Items = append(batch.Items, state.BatchItem{
				Language: it.Language, OldVersion: it.BaseVersion, Status: "pending",
			})
		}
	}
	now := a.now()
	itemByLang := map[string]int{}
	for i := range batch.Items {
		itemByLang[batch.Items[i].Language] = i
	}
	bookkeepingDirty := false

	persist := func() error {
		if req.OpID != "" {
			a.data.Batches[req.OpID] = batch
		}
		return a.commitLocked(now)
	}

	for _, it := range req.Items {
		idx, ok := itemByLang[it.Language]
		if !ok {
			batch.Items = append(batch.Items, state.BatchItem{Language: it.Language, OldVersion: it.BaseVersion, Status: "pending"})
			idx = len(batch.Items) - 1
			itemByLang[it.Language] = idx
		}
		bi := &batch.Items[idx]
		if bi.Status == "success" {
			continue // already applied: never re-version
		}
		cat, perr := catalog.Parse(it.Language, it.Content)
		if cat == nil {
			bi.Status = "conflict"
			bi.Reason = perr[0].Detail
			bookkeepingDirty = true
			continue
		}
		fp := cat.Fingerprint()
		current, exists := a.data.Langs[it.Language]
		if it.BaseVersion != "" && exists && it.BaseVersion != current.Version {
			bi.Status = "conflict"
			bi.Reason = "version conflict: expected base " + it.BaseVersion + " but current is " + current.Version
			bookkeepingDirty = true
			continue
		}
		blob := "raw_" + sha256hex(it.Content)[:32]
		if err := a.st.WriteBlob(blob, it.Content); err != nil {
			return nil, err
		}
		a.data.Langs[it.Language] = &state.LangState{
			Language: it.Language, Version: fp, RawBlob: blob,
			Filename: it.Filename, Imported: now,
		}
		a.data.Uploads = append(a.data.Uploads, state.Upload{
			ID: "up_" + hashBytes([]byte(blob), []byte("batch"), []byte{byte(a.data.Seq + 1)})[:24],
			Role: "target", Language: it.Language, Version: fp,
			Blob: blob, Filename: it.Filename, At: now,
		})
		bi.Status = "success"
		bi.NewVersion = fp
		bi.Blob = blob
		bi.Reason = ""
		t := now
		bi.AppliedAt = &t
		if err := persist(); err != nil {
			return nil, err
		}
		bookkeepingDirty = false
	}
	if bookkeepingDirty {
		// Only conflicts changed and no successful version frame followed.
		if err := persist(); err != nil {
			return nil, err
		}
	}
	batch.UpdatedAt = now
	resp := a.batchResponse(batch)
	if req.OpID != "" {
		a.finishOpLocked("batch", req.OpID, reqFP, resp)
	}
	return resp, nil
}

func (a *App) batchResponse(b *state.Batch) *BatchResponse {
	complete := true
	items := make([]state.BatchItem, len(b.Items))
	copy(items, b.Items)
	for _, it := range items {
		if it.Status != "success" {
			complete = false
		}
	}
	return &BatchResponse{OpID: b.OpID, Items: items, Complete: complete, Snapshot: a.pinLocked()}
}

// Batch returns a stored batch receipt.
func (a *App) Batch(opID string) (*state.Batch, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	b, ok := a.data.Batches[opID]
	return b, ok
}

func hexEncode(b []byte) string {
	const hexd = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hexd[v>>4]
		out[i*2+1] = hexd[v&0xf]
	}
	return string(out)
}
