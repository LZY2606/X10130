package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"messagecatalog/internal/catalog"
	"messagecatalog/internal/store"
)

// BatchFixRequest applies propagated baseline content to target languages.
type BatchFixRequest struct {
	Reason              string   `json:"reason"`
	Languages           []string `json:"languages"`
	ExpectedBaseVersion string   `json:"expectedBaseVersion"`
	// ExpectedVersions maps language -> version fingerprint the client saw.
	ExpectedVersions map[string]string `json:"expectedVersions"`
}

// BatchFixResult reports per-language outcomes.
type BatchFixResult struct {
	BatchID       string                  `json:"batchId"`
	OperationID   string                  `json:"operationId"`
	Reason        string                  `json:"reason"`
	BaseVersionID string                  `json:"baseVersionId"`
	Results       []store.BatchLangResult `json:"results"`
	SnapshotStamp string                  `json:"snapshotStamp"`
	Replayed      bool                    `json:"replayed,omitempty"`
}

// BatchFix generates corrected files from baseline for each language.
func (s *Service) BatchFix(opID string, req BatchFixRequest) (*BatchFixResult, bool, error) {
	if len(req.Languages) == 0 {
		return nil, false, fmt.Errorf("at least one language is required")
	}
	langs := append([]string(nil), req.Languages...)
	sort.Strings(langs)
	// stable canonical request
	canonical := struct {
		Reason              string            `json:"reason"`
		Languages           []string          `json:"languages"`
		ExpectedBaseVersion string            `json:"expectedBaseVersion"`
		ExpectedVersions    map[string]string `json:"expectedVersions"`
	}{req.Reason, langs, req.ExpectedBaseVersion, req.ExpectedVersions}

	_, raw, replay, err := s.withIdempotency(opID, "batch-fix", canonical, func(st *store.State, now time.Time) (int, string, error) {
		base := currentBaseVersion(st)
		if base == nil {
			return 0, "", fmt.Errorf("no baseline catalog imported")
		}
		if req.ExpectedBaseVersion != "" && req.ExpectedBaseVersion != base.ID {
			return 0, "", &ConflictError{Msg: fmt.Sprintf("baseline version changed: expected %s, current %s", req.ExpectedBaseVersion, base.ID)}
		}
		e := edges(st)
		// Build corrected messages: for each baseline key follow mapping chain to
		// the key name a target should use (targets are keyed on the newest name).
		corrected := map[string]string{}
		// Determine target key for each baseline key: follow chain to end.
		for _, m := range base.Messages {
			chain, cyc := e.ResolveChain(m.Key)
			if cyc {
				return 0, "", &ConflictError{Msg: "rename mapping contains a cycle"}
			}
			targetKey := chain[len(chain)-1]
			corrected[targetKey] = m.Raw
		}
		content, err := renderCatalogJSON(corrected)
		if err != nil {
			return 0, "", err
		}
		fp := catalog.ContentFingerprint(corrected)
		vid := versionID("", fp) // per-language id built below

		batchID := "batch-" + randID()
		results := make([]store.BatchLangResult, 0, len(langs))
		for _, lang := range langs {
			langVID := strings.Replace(vid, ":", ":"+lang+":", 1)
			// simpler: build id directly
			langVID = lang + ":" + strings.TrimPrefix(fp, "sha256:")[:12]
			// version conflict: client expected a different fingerprint
			cur := currentVersion(st, lang)
			if cur == nil {
				results = append(results, store.BatchLangResult{Language: lang, Status: "conflict",
					Reason: "no existing catalog for language; import it first"})
				continue
			}
			if want, ok := req.ExpectedVersions[lang]; ok && want != "" && want != cur.Fingerprint {
				results = append(results, store.BatchLangResult{Language: lang, Status: "conflict",
					Reason: fmt.Sprintf("version conflict: expected %s but current is %s", shortFP(want), shortFP(cur.Fingerprint))})
				continue
			}
			if cur.Fingerprint == fp {
				// Already correct: idempotent no-op, no new version/mapping.
				results = append(results, store.BatchLangResult{Language: lang, Status: "skipped",
					VersionID: cur.ID, Reason: "already up to date", AppliedAt: now})
				continue
			}
			// Apply corrected content as a new target version.
			blobName, sum, werr := s.Store.WriteRaw("batch-"+lang+".json", content)
			if werr != nil {
				results = append(results, store.BatchLangResult{Language: lang, Status: "conflict",
					Reason: "storage error: " + werr.Error()})
				continue
			}
			newV := store.VersionRecord{
				ID: langVID, Language: lang, Role: roleTarget, Fingerprint: fp,
				ParserVersion: catalog.ParserVersion, Messages: catalog.BuildMessages(corrected),
				CreatedAt: now,
			}
			exists := false
			for _, v := range st.Versions {
				if v.ID == newV.ID {
					exists = true
				}
			}
			if !exists {
				st.Versions = append(st.Versions, newV)
			}
			upID := "up-" + randID()
			st.Uploads = append(st.Uploads, store.UploadRecord{
				ID: upID, Language: lang, Role: roleTarget, Filename: "batch-" + lang + ".json",
				BlobName: blobName, Size: int64(len(content)), SHA256: sum,
				UploadedAt: now, VersionID: langVID,
			})
			st.CurrentByLang[lang] = langVID
			invalidateTarget(st, lang, langVID)
			results = append(results, store.BatchLangResult{Language: lang, Status: "applied",
				VersionID: langVID, AppliedAt: now})
		}
		for i := range st.Proofs {
			st.Proofs[i].Current = false
		}
		st.Batches = append(st.Batches, store.BatchRecord{
			ID: batchID, OperationID: opID, Reason: req.Reason,
			BaseVersionID: base.ID, Results: results, CreatedAt: now,
		})
		res := BatchFixResult{
			BatchID: batchID, OperationID: opID, Reason: req.Reason,
			BaseVersionID: base.ID, Results: results, SnapshotStamp: stateStamp(st),
		}
		b, _ := json.Marshal(res)
		return 200, string(b), nil
	})
	if err != nil {
		return nil, false, err
	}
	var res BatchFixResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return nil, replay, err
	}
	res.Replayed = replay
	return &res, replay, nil
}

func invalidateTarget(st *store.State, lang, newVersionID string) {
	for i := range st.Validations {
		v := &st.Validations[i]
		if v.Invalidated {
			continue
		}
		if v.TargetLang == lang && v.TargVersionID != newVersionID {
			v.Invalidated = true
			v.InvalidReason = "target catalog changed by batch fix"
			v.Superseded = true
		}
	}
}

func renderCatalogJSON(m map[string]string) ([]byte, error) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	out := map[string]string{}
	for _, k := range keys {
		out[k] = m[k]
	}
	if err := enc.Encode(out); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func shortFP(fp string) string {
	fp = strings.TrimPrefix(fp, "sha256:")
	if len(fp) > 12 {
		return fp[:12]
	}
	return fp
}
