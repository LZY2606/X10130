package store

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"catalogcheck/internal/catalog"
	"catalogcheck/internal/validate"
)

// ---------- requests ----------

// ImportRequest uploads one catalog file.
type ImportRequest struct {
	Role        string `json:"role"` // baseline or target
	Language    string `json:"language"`
	Filename    string `json:"filename"`
	Content     []byte `json:"content"`
	OperationID string `json:"operation_id,omitempty"`
	SnapshotID  string `json:"snapshot_id,omitempty"`
}

func (r ImportRequest) contentHash() string {
	h := sha256.New()
	h.Write([]byte(r.Role))
	h.Write([]byte{0})
	h.Write([]byte(r.Language))
	h.Write([]byte{0})
	h.Write([]byte(r.Filename))
	h.Write([]byte{0})
	h.Write(r.Content)
	return hashBytes(h.Sum(nil))
}

// ImportOutcome is the persisted result of an import.
type ImportOutcome struct {
	Snapshot       Snapshot `json:"snapshot"`
	Role           string   `json:"role"`
	Language       string   `json:"language"`
	VersionID      string   `json:"version_id"`
	Fingerprint    string   `json:"fingerprint"`
	Keys           int      `json:"keys"`
	UploadID       string   `json:"upload_id"`
	VersionCreated bool     `json:"version_created"`
}

// RenameRequest confirms a manual key rename mapping.
type RenameRequest struct {
	OperationID   string `json:"operation_id,omitempty"`
	SnapshotID    string `json:"snapshot_id,omitempty"`
	FromVersionID string `json:"from_version_id"`
	FromKey       string `json:"from_key"`
	ToVersionID   string `json:"to_version_id"`
	ToKey         string `json:"to_key"`
}

func (r RenameRequest) contentHash() string {
	b, _ := json.Marshal([]string{r.FromVersionID, r.FromKey, r.ToVersionID, r.ToKey})
	return hashBytes(b)
}

// ExemptionRequest grants or revokes an exemption.
type ExemptionRequest struct {
	OperationID      string     `json:"operation_id,omitempty"`
	SnapshotID       string     `json:"snapshot_id,omitempty"`
	Action           string     `json:"action"` // grant or revoke
	ExemptionID      string     `json:"exemption_id,omitempty"`
	IssueFingerprint string     `json:"issue_fingerprint,omitempty"`
	Language         string     `json:"language,omitempty"`
	Key              string     `json:"key,omitempty"`
	Code             string     `json:"code,omitempty"`
	Reason           string     `json:"reason,omitempty"`
	Owner            string     `json:"owner,omitempty"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
}

func (r ExemptionRequest) contentHash() string {
	exp := ""
	if r.ExpiresAt != nil {
		exp = r.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	b, _ := json.Marshal([]string{
		r.Action, r.ExemptionID, r.IssueFingerprint, r.Language, r.Key,
		r.Code, r.Reason, r.Owner, exp,
	})
	return hashBytes(b)
}

// ExemptionOutcome reports the stored exemption state.
type ExemptionOutcome struct {
	Snapshot  Snapshot   `json:"snapshot"`
	Action    string     `json:"action"`
	Exemption *Exemption `json:"exemption,omitempty"`
}

// BatchLanguageItem is one language inside a batch fix.
type BatchLanguageItem struct {
	Language              string `json:"language"`
	Filename              string `json:"filename"`
	Content               []byte `json:"content"`
	ExpectedTargetVersion string `json:"expected_target_version_id"`
}

// BatchRequest applies fixes to several languages independently.
type BatchRequest struct {
	OperationID     string              `json:"operation_id,omitempty"`
	SnapshotID      string              `json:"snapshot_id,omitempty"`
	ExpectedBaseVer string              `json:"expected_base_version_id"`
	Items           []BatchLanguageItem `json:"items"`
}

func (r BatchRequest) contentHash() string {
	type item struct {
		Language, Filename string
		ContentSHA         string
		Expected           string
	}
	items := make([]item, 0, len(r.Items))
	for _, it := range r.Items {
		items = append(items, item{it.Language, it.Filename, hashBytes(it.Content), it.ExpectedTargetVersion})
	}
	b, _ := json.Marshal(struct {
		Base  string
		Items []item
	}{r.ExpectedBaseVer, items})
	return hashBytes(b)
}

// BatchOutcome reports per-language receipts.
type BatchOutcome struct {
	Snapshot        Snapshot     `json:"snapshot"`
	OperationID     string       `json:"operation_id"`
	ExpectedBaseVer string       `json:"expected_base_version_id"`
	BaseOK          bool         `json:"base_ok"`
	Subs            []SubReceipt `json:"subs"`
	AllSucceeded    bool         `json:"all_succeeded"`
}

// ---------- idempotency plumbing ----------

func idemKey(kind, opID string) string { return kind + ":" + opID }

type idemReplay struct {
	Status   int
	Response []byte
	Record   *IdemRecord
}

// beginIdem returns an existing record for the same operation id.
func (s *Store) beginIdem(key, reqHash string) (*idemReplay, error) {
	rec, ok := s.st.Idem[key]
	if !ok {
		return nil, nil
	}
	if rec.RequestSHA != reqHash {
		return nil, fmt.Errorf("%w: 操作标识 %s 曾用于不同内容", ErrIdemMismatch, key)
	}
	return &idemReplay{Status: rec.Status, Response: rec.Response, Record: &rec}, nil
}

func encodeResp(v any) ([]byte, error) { return json.Marshal(v) }

func (s *Store) finishIdem(cand *State, key, kind, reqHash string, status int, resp any, subs map[string]SubReceipt) ([]byte, error) {
	b, err := encodeResp(resp)
	if err != nil {
		return nil, err
	}
	cand.Idem[key] = IdemRecord{
		Key: key, Kind: kind, RequestSHA: reqHash, Status: status,
		Response: b, CreatedAt: s.nowTime(), Subs: subs,
	}
	return b, nil
}

func (s *Store) checkSnapshot(cand *State, snapshotID string) error {
	if snapshotID == "" {
		return nil
	}
	if snapshotID != cand.snapshot().ID {
		return fmt.Errorf("%w: 期望快照 %s，当前为 %s", ErrConflict, snapshotID, cand.snapshot().ID)
	}
	return nil
}

// ---------- catalog helpers ----------

func versionID(fp string) string { return "ver-" + fp[:12] }

func (s *Store) loadCatalog(v VersionRef) (*catalog.Catalog, error) {
	b, err := s.readBlob(v.CanonicalBlob)
	if err != nil {
		return nil, err
	}
	return catalog.Decode(b)
}

func (s *Store) currentBaseLocked() (VersionRef, *catalog.Catalog, error) {
	if len(s.st.BaseChain) == 0 {
		return VersionRef{}, nil, errors.New("尚无基准目录")
	}
	id := s.st.BaseChain[len(s.st.BaseChain)-1]
	v := s.st.Versions[id]
	c, err := s.loadCatalog(v)
	return v, c, err
}

// storeVersion writes upload + canonical blob and records a new version if the
// fingerprint is unseen.
func (s *Store) storeCatalogLocked(cand *State, role, language, filename string, content []byte) (*VersionRef, *Upload, bool, error) {
	cat, err := catalog.Decode(content)
	if err != nil {
		return nil, nil, false, err
	}
	fp := cat.Fingerprint()
	upRel, upSHA, err := s.writeBlob(filepath.Join("raw"), content)
	if err != nil {
		return nil, nil, false, err
	}
	upID := "up-" + upSHA[:10] + "-" + strconvItob(s.nowTime().UnixNano())
	up := Upload{
		ID: upID, Filename: filename, SHA256: upSHA, Size: len(content),
		ReceivedAt: s.nowTime(), BlobPath: upRel,
	}
	cand.Uploads[upID] = up
	vid := versionID(fp.SHA256)
	if existing, ok := cand.Versions[vid]; ok {
		existing.UploadIDs = append(existing.UploadIDs, upID)
		cand.Versions[vid] = existing
		return ptrVer(existing), &up, false, nil
	}
	canRel, canSHA, err := s.saveCanonical(cat)
	if err != nil {
		return nil, nil, false, err
	}
	_ = canSHA
	v := VersionRef{
		ID: vid, Language: language, Role: role, Fingerprint: fp.SHA256,
		Keys: fp.Keys, CanonicalBlob: canRel, UploadIDs: []string{upID},
		CreatedAt: s.nowTime(), ParserVersion: s.st.ParserVersion, Seq: len(cand.Versions) + 1,
	}
	cand.Versions[vid] = v
	return ptrVer(v), &up, true, nil
}

func ptrVer(v VersionRef) *VersionRef { return &v }

// refreshResultsLocked recomputes current validation results for all target
// languages against the candidate state's latest baseline. Unrelated results
// keep the same identity and are not rewritten.
func (s *Store) refreshResultsLocked(cand *State, now time.Time) error {
	return s.refreshResultsAgainstLocked(cand, "", nil, now)
}

func (s *Store) refreshResultsAgainstLocked(cand *State, baseID string, baseCatArg *catalog.Catalog, now time.Time) error {
	var baseVer VersionRef
	var baseCat *catalog.Catalog
	if baseCatArg != nil {
		baseVer = cand.Versions[baseID]
		baseCat = baseCatArg
	} else {
		if len(cand.BaseChain) == 0 {
			return nil // no baseline yet; nothing to validate
		}
		id := cand.BaseChain[len(cand.BaseChain)-1]
		var err error
		baseVer = cand.Versions[id]
		baseCat, err = s.loadCatalog(baseVer)
		if err != nil {
			return err
		}
	}
	for _, lang := range sortedLangKeys(cand.LangVersion) {
		tvID := cand.LangVersion[lang]
		tv := cand.Versions[tvID]
		tCat, err := s.loadCatalog(tv)
		if err != nil {
			return err
		}
		edges := relevantEdgesFor(cand.Edges, baseCat, tCat)
		res := validate.Validate(lang, baseVer.ID, tvID, baseCat, tCat, edges, cand.ParserVersion, now)
		if _, exists := cand.Results[res.ID]; exists {
			// Same inputs: keep the historical record.
			continue
		}
		rel, err := s.saveResult(res)
		if err != nil {
			return err
		}
		cand.Results[res.ID] = ResultMeta{
			ID: res.ID, Language: lang, BaseVersionID: baseVer.ID, TargetVersionID: tvID,
			BaseFP: res.BaseFingerprint, TargetFP: res.TargetFingerprint,
			MappingHash: res.MappingHash, ParserVersion: cand.ParserVersion,
			Seq: len(cand.Results) + 1, BlobPath: rel, CreatedAt: now,
		}
	}
	return nil
}

// relevantEdgesFor keeps only rename edges that touch a key either side of the
// comparison still contains, so unrelated renames do not invalidate a language.
func relevantEdgesFor(edges []validate.MappingEdge, base, target *catalog.Catalog) []validate.MappingEdge {
	keys := map[string]bool{}
	for _, e := range target.Entries {
		keys[e.Key] = true
	}
	for _, e := range base.Entries {
		keys[e.Key] = true
	}
	var out []validate.MappingEdge
	for _, e := range edges {
		if keys[e.FromKey] || keys[e.ToKey] {
			out = append(out, e)
		}
	}
	return out
}

func relevantEdges(edges []validate.MappingEdge, target *catalog.Catalog) []validate.MappingEdge {
	return relevantEdgesFor(edges, target, target)
}

func sortedLangKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func strconvItob(n int64) string {
	const digits = "0123456789abcdef"
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = digits[n%16]
		n /= 16
	}
	return string(b[i:])
}

// ---------- import ----------

// Import stores an upload and, for new content fingerprints, a version.
func (s *Store) Import(req ImportRequest) (*ImportOutcome, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowTime()
	if req.OperationID != "" {
		key := idemKey("import", req.OperationID)
		if rep, err := s.beginIdem(key, req.contentHash()); err != nil {
			return nil, nil, err
		} else if rep != nil {
			var out ImportOutcome
			if err := json.Unmarshal(rep.Response, &out); err == nil {
				return &out, rep.Response, nil
			}
		}
	}
	cand := cloneState(s.st)
	if err := s.checkSnapshot(cand, req.SnapshotID); err != nil {
		return nil, nil, err
	}
	if req.Role != "baseline" && req.Role != "target" {
		return nil, nil, errors.New("role 必须是 baseline 或 target")
	}
	if req.Language == "" {
		return nil, nil, errors.New("缺少 language")
	}
	if req.Role == "baseline" && cand.BaselineLanguage != "" && cand.BaselineLanguage != req.Language {
		return nil, nil, fmt.Errorf("基准语言已设置为 %q；如需改名请在界面中调整基准语言名称（需要显式映射）", cand.BaselineLanguage)
	}
	v, up, created, err := s.storeCatalogLocked(cand, req.Role, req.Language, req.Filename, req.Content)
	if err != nil {
		return nil, nil, err
	}
	switch req.Role {
	case "baseline":
		cand.BaselineLanguage = req.Language
		if created {
			if len(cand.BaseChain) > 0 {
				prev := cand.BaseChain[len(cand.BaseChain)-1]
				v.PreviousBaseID = prev
				cand.Versions[v.ID] = *v
			}
			cand.BaseChain = append(cand.BaseChain, v.ID)
		}
	case "target":
		cand.LangVersion[req.Language] = v.ID
	}
	if err := s.refreshResultsLocked(cand, now); err != nil {
		return nil, nil, err
	}
	out := &ImportOutcome{
		Role: req.Role, Language: req.Language, VersionID: v.ID,
		Fingerprint: v.Fingerprint, Keys: v.Keys, UploadID: up.ID,
		VersionCreated: created, Snapshot: cand.snapshot(),
	}
	if req.OperationID != "" {
		_, err = s.finishIdem(cand, idemKey("import", req.OperationID), "import", req.contentHash(), 200, out, nil)
		if err != nil {
			return nil, nil, err
		}
	}
	s.st = cand
	if err := s.commitLocked(); err != nil {
		return nil, nil, err
	}
	return out, nil, nil
}
