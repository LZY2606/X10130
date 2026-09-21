package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"msgcheck/internal/catalog"
	"msgcheck/internal/state"
)

// ImportResult reports what an import did.
type ImportResult struct {
	Role      string    `json:"role"`
	Language  string    `json:"language"`
	Version   string    `json:"version"`
	UploadID  string    `json:"upload_id"`
	Reused    bool      `json:"reused_content"`
	NewVersion bool     `json:"new_version"`
	Size      int       `json:"size"`
	Snapshot  state.Pin `json:"snapshot"`
	Warnings  []string  `json:"warnings,omitempty"`
}

// Import stores one uploaded file. "baseline" replaces the baseline
// version; "target" replaces that language's version. Identical content
// (same fingerprint regardless of key order/newline style) never creates a
// new content version, but every upload is retained byte-for-byte.
func (a *App) Import(role, language, filename string, raw []byte, expectedSnap, opID string) (*ImportResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := a.advanceLocked(); err != nil {
		return nil, err
	}
	if err := a.checkSnapshotLocked(expectedSnap); err != nil {
		return nil, err
	}
	if role != "baseline" && role != "target" {
		return nil, &BadRequestError{Reason: "role must be baseline or target"}
	}
	if language == "" {
		return nil, &BadRequestError{Reason: "language is required"}
	}
	cat, perr := catalog.Parse(language, raw)
	if cat == nil {
		return nil, &BadRequestError{Reason: perr[0].Detail}
	}
	if role == "target" && a.data.Baseline != nil && language == a.data.Baseline.Language {
		return nil, &ConflictError{Reason: "language " + language + " is the baseline language; import it as baseline", Current: a.pinLocked()}
	}
	reqFP := requestFingerprint(map[string]any{
		"role": role, "language": language, "filename": filename,
		"raw_sha256": sha256hex(raw),
	})
	if replay, err := a.beginOpLocked("import:"+role+":"+language, opID, reqFP); err != nil {
		return nil, err
	} else if replay != nil {
		var res ImportResult
		if err := json.Unmarshal(replay, &res); err == nil {
			res.Snapshot = a.pinLocked()
			return &res, nil
		}
	}

	fp := cat.Fingerprint()
	blob := "raw_" + sha256hex(raw)[:32]
	if err := a.st.WriteBlob(blob, raw); err != nil {
		return nil, err
	}
	now := a.now()
	up := state.Upload{
		ID: "up_" + hashBytes([]byte(blob), []byte(now.Format(timeFormatNano))),
		Role: role, Language: language, Version: fp,
		Blob: blob, Filename: filename, At: now,
	}
	// Stable upload id within one request: derive from blob + seq + role.
	up.ID = "up_" + hashBytes([]byte(blob), []byte(role), []byte{byte(a.data.Seq)})[:24]

	res := &ImportResult{Role: role, Language: language, Version: fp, UploadID: up.ID, Size: len(raw)}

	switch role {
	case "baseline":
		if a.data.Baseline != nil && a.data.Baseline.Version == fp {
			res.Reused = true
		} else {
			res.NewVersion = true
			a.data.Baseline = &state.BaselineRec{
				Language: language, Version: fp, RawBlob: blob,
				Filename: filename, Imported: now,
			}
		}
	case "target":
		if a.data.Baseline == nil {
			return nil, &BadRequestError{Reason: "import a baseline catalog before target languages"}
		}
		if old, ok := a.data.Langs[language]; ok && old.Version == fp {
			res.Reused = true
		} else {
			res.NewVersion = true
			a.data.Langs[language] = &state.LangState{
				Language: language, Version: fp, RawBlob: blob,
				Filename: filename, Imported: now,
			}
		}
	}
	for _, e := range perr {
		res.Warnings = append(res.Warnings, e.Key+": "+e.Detail)
	}
	a.data.Uploads = append(a.data.Uploads, up)
	if err := a.commitLocked(now); err != nil {
		return nil, err
	}
	res.Snapshot = a.pinLocked()
	a.finishOpLocked("import:"+role+":"+language, opID, reqFP, res)
	return res, nil
}

const timeFormatNano = "20060102T150405.000000000Z07:00"

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
