package service

import (
	"encoding/json"
	"fmt"
	"time"

	"messagecatalog/internal/catalog"
	"messagecatalog/internal/store"
)

// ImportRequest is one catalog upload.
type ImportRequest struct {
	Language string `json:"language"`
	Role     string `json:"role"`
	Filename string `json:"filename"`
	Content  []byte `json:"content"`
}

// ImportResult describes what an import did.
type ImportResult struct {
	UploadID       string `json:"uploadId"`
	VersionID      string `json:"versionId"`
	Language       string `json:"language"`
	Role           string `json:"role"`
	Fingerprint    string `json:"fingerprint"`
	ContentChanged bool   `json:"contentChanged"`
	NewVersion     bool   `json:"newVersion"`
	DownloadURL    string `json:"downloadUrl"`
}

func parserVersion() string { return catalog.ParserVersion }

func (s *Service) Import(opID string, req ImportRequest) (ImportResult, bool, error) {
	if req.Language == "" {
		return ImportResult{}, false, fmt.Errorf("language is required")
	}
	role := req.Role
	if role != roleBaseline {
		role = roleTarget
	}
	file, err := catalog.ParseFile(req.Language, req.Content)
	if err != nil {
		return ImportResult{}, false, err
	}
	fp := catalog.ContentFingerprint(file.Messages)
	vid := versionID(req.Language, fp)

	blobName, sum, err := s.Store.WriteRaw(req.Filename, req.Content)
	if err != nil {
		return ImportResult{}, false, err
	}

	canonicalReq := struct {
		Language string `json:"language"`
		Role     string `json:"role"`
		Filename string `json:"filename"`
		SHA256   string `json:"sha256"`
	}{req.Language, role, req.Filename, sum}

	_, raw, replay, err := s.withIdempotency(opID, "import", canonicalReq, func(st *store.State, now time.Time) (int, string, error) {
		existing := false
		for _, v := range st.Versions {
			if v.ID == vid {
				existing = true
				break
			}
		}
		newVersion := !existing
		if newVersion {
			st.Versions = append(st.Versions, store.VersionRecord{
				ID: vid, Language: req.Language, Role: role, Fingerprint: fp,
				ParserVersion: parserVersion(), Messages: catalog.BuildMessages(file.Messages), CreatedAt: now,
			})
		}
		prev := st.CurrentByLang[req.Language]
		changed := prev != vid

		upID := "up-" + randID()
		up := store.UploadRecord{
			ID: upID, Language: req.Language, Role: role, Filename: req.Filename,
			BlobName: blobName, Size: int64(len(req.Content)), SHA256: sum,
			UploadedAt: now, VersionID: vid,
		}
		st.Uploads = append(st.Uploads, up)
		for i := range st.Versions {
			if st.Versions[i].ID == vid {
				st.Versions[i].UploadIDs = append(st.Versions[i].UploadIDs, upID)
			}
		}
		st.RoleByLang[req.Language] = role
		st.CurrentByLang[req.Language] = vid
		if newVersion || (prev != "" && prev != vid) {
			s.invalidateAfterImport(st, req.Language, role, vid, fp)
		}
		res := ImportResult{
			UploadID: upID, VersionID: vid, Language: req.Language, Role: role,
			Fingerprint: fp, ContentChanged: changed, NewVersion: newVersion,
			DownloadURL: "/api/uploads/" + upID + "/raw",
		}
		b, _ := json.Marshal(res)
		return 200, string(b), nil
	})
	if err != nil {
		return ImportResult{}, false, err
	}
	var res ImportResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return ImportResult{}, replay, err
	}
	return res, replay, nil
}
