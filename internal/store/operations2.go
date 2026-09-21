package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"catalogcheck/internal/validate"
)

// ---------- rename confirmation ----------

// RenameOutcome reports a confirmed mapping edge.
type RenameOutcome struct {
	Snapshot Snapshot             `json:"snapshot"`
	Edge     validate.MappingEdge `json:"edge"`
}

func chainIndex(chain []string, id string) int {
	for i, v := range chain {
		if v == id {
			return i
		}
	}
	return -1
}

// ConfirmRename validates and records one explicit rename edge.
func (s *Store) ConfirmRename(req RenameRequest) (*RenameOutcome, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowTime()
	if req.OperationID != "" {
		key := idemKey("rename", req.OperationID)
		if rep, err := s.beginIdem(key, req.contentHash()); err != nil {
			return nil, nil, err
		} else if rep != nil {
			var out RenameOutcome
			if err := json.Unmarshal(rep.Response, &out); err == nil {
				return &out, rep.Response, nil
			}
		}
	}
	cand := cloneState(s.st)
	if err := s.checkSnapshot(cand, req.SnapshotID); err != nil {
		return nil, nil, err
	}
	fi := chainIndex(cand.BaseChain, req.FromVersionID)
	ti := chainIndex(cand.BaseChain, req.ToVersionID)
	if fi < 0 || ti < 0 {
		return nil, nil, fmt.Errorf("%w: 改名涉及的版本不存在", ErrConflict)
	}
	if fi >= ti {
		return nil, nil, errors.New("源版本必须早于目标版本")
	}
	fromCat, err := s.loadCatalog(cand.Versions[req.FromVersionID])
	if err != nil {
		return nil, nil, err
	}
	toCat, err := s.loadCatalog(cand.Versions[req.ToVersionID])
	if err != nil {
		return nil, nil, err
	}
	if _, ok := fromCat.Entry(req.FromKey); !ok {
		return nil, nil, fmt.Errorf("%w: 旧 key %q 不在基准版本 %s 中", ErrConflict, req.FromKey, req.FromVersionID)
	}
	if _, ok := toCat.Entry(req.ToKey); !ok {
		return nil, nil, fmt.Errorf("%w: 新 key %q 不在基准版本 %s 中", ErrConflict, req.ToKey, req.ToVersionID)
	}
	for _, e := range cand.Edges {
		if e.FromVersionID == req.FromVersionID && e.ToVersionID == req.ToVersionID &&
			e.FromKey == req.FromKey && e.ToKey == req.ToKey {
			return nil, nil, errors.New("该改名映射已存在")
		}
	}
	for _, e := range cand.Edges {
		if e.ToKey == req.ToKey && e.FromKey != req.FromKey {
			return nil, nil, fmt.Errorf("%w: 拒绝合并: 新 key %q 已由旧 key %q 映射，不能把两个旧 key 并到同一个新 key", ErrConflict, req.ToKey, e.FromKey)
		}
	}
	if reaches(cand.Edges, req.ToKey, req.FromKey) {
		return nil, nil, errors.New("拒绝映射: 该连接会形成改名环")
	}
	edge := validate.MappingEdge{
		FromVersionID: req.FromVersionID, FromKey: req.FromKey,
		ToVersionID: req.ToVersionID, ToKey: req.ToKey,
	}
	cand.Edges = append(cand.Edges, edge)
	if err := s.refreshResultsLocked(cand, now); err != nil {
		return nil, nil, err
	}
	out := &RenameOutcome{Edge: edge, Snapshot: cand.snapshot()}
	if req.OperationID != "" {
		_, err = s.finishIdem(cand, idemKey("rename", req.OperationID), "rename", req.contentHash(), 200, out, nil)
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

// reaches follows edges forward from start and reports whether target is
// reachable (cycle-safe).
func reaches(edges []validate.MappingEdge, start, target string) bool {
	visited := map[string]bool{}
	cur := start
	for {
		if cur == target {
			return true
		}
		if visited[cur] {
			return false
		}
		visited[cur] = true
		next := ""
		for _, e := range edges {
			if e.FromKey == cur {
				next = e.ToKey
				break
			}
		}
		if next == "" {
			return false
		}
		cur = next
	}
}

// ---------- exemptions ----------

// SetExemption grants or revokes an exemption.
func (s *Store) SetExemption(req ExemptionRequest) (*ExemptionOutcome, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowTime()
	if req.OperationID != "" {
		key := idemKey("exemption", req.OperationID)
		if rep, err := s.beginIdem(key, req.contentHash()); err != nil {
			return nil, nil, err
		} else if rep != nil {
			var out ExemptionOutcome
			if err := json.Unmarshal(rep.Response, &out); err == nil {
				return &out, rep.Response, nil
			}
		}
	}
	cand := cloneState(s.st)
	if err := s.checkSnapshot(cand, req.SnapshotID); err != nil {
		return nil, nil, err
	}
	out := &ExemptionOutcome{Action: req.Action}
	var ex *Exemption
	switch req.Action {
	case "grant":
		if req.IssueFingerprint == "" || req.Language == "" {
			return nil, nil, errors.New("grant 需要 issue_fingerprint 和 language")
		}
		if req.ExpiresAt == nil || !req.ExpiresAt.After(now) {
			return nil, nil, errors.New("grant 需要一个未来的到期时间")
		}
		ex = &Exemption{
			ID:               "ex-" + shortHash(req.IssueFingerprint+req.Language+now.UTC().Format(time.RFC3339Nano)),
			OperationID:      req.OperationID,
			IssueFingerprint: req.IssueFingerprint,
			Language:         req.Language, Key: req.Key, Code: req.Code,
			Reason: req.Reason, Owner: req.Owner,
			CreatedAt: now, ExpiresAt: req.ExpiresAt.UTC(),
			SnapshotID: cand.snapshot().ID,
		}
		cand.Exemptions = append(cand.Exemptions, *ex)
	case "revoke":
		idx := -1
		for i := range cand.Exemptions {
			if cand.Exemptions[i].ID == req.ExemptionID {
				idx = i
				break
			}
		}
		if idx < 0 {
			return nil, nil, fmt.Errorf("%w: 豁免 %s 不存在", ErrConflict, req.ExemptionID)
		}
		t := now
		cand.Exemptions[idx].RevokedAt = &t
		ex = &cand.Exemptions[idx]
	default:
		return nil, nil, errors.New("action 必须是 grant 或 revoke")
	}
	out.Exemption = ex
	out.Snapshot = cand.snapshot()
	var err error
	if req.OperationID != "" {
		_, err = s.finishIdem(cand, idemKey("exemption", req.OperationID), "exemption", req.contentHash(), 200, out, nil)
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

func shortHash(x string) string {
	return hashBytes([]byte(x))[:10]
}

// ---------- batch fixes ----------

// ApplyBatch stores per-language fixes independently in one commit.
func (s *Store) ApplyBatch(req BatchRequest) (*BatchOutcome, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowTime()
	key := ""
	existingRecord := false
	if req.OperationID != "" {
		key = idemKey("batch", req.OperationID)
		// Full replay: the whole request set and content are identical.
		if rec, ok := s.st.Idem[key]; ok {
			if rec.RequestSHA == req.contentHash() {
				var out BatchOutcome
				if err := json.Unmarshal(rec.Response, &out); err == nil {
					return &out, rec.Response, nil
				}
			}
			existingRecord = true
		}
	}
	cand := cloneState(s.st)
	if err := s.checkSnapshot(cand, req.SnapshotID); err != nil {
		return nil, nil, err
	}
	var prior map[string]SubReceipt
	if req.OperationID != "" {
		if rec, ok := cand.Idem[key]; ok {
			prior = rec.Subs
		}
	}
	subs := map[string]SubReceipt{}
	if prior != nil {
		for k, v := range prior {
			subs[k] = v
		}
	}
	// Partial-success retry semantics: already-succeeded languages must not be
	// resubmitted. Re-sent successful items must carry identical content, and
	// newly submitted items for previously conflicting languages must match
	// their earlier attempt when one was recorded.
	if existingRecord {
		for _, item := range req.Items {
			contentSHA := hashBytes(item.Content)
			if old, ok := subs[item.Language]; ok {
				if old.Status == "success" {
					return nil, nil, fmt.Errorf("%w: 语言 %s 已成功，不得在同标识重试中再次提交（其结果已保留）", ErrIdemMismatch, item.Language)
				}
				if old.ContentSHA != "" && old.ContentSHA != contentSHA {
					return nil, nil, fmt.Errorf("%w: 语言 %s 的重试内容与首次提交不同", ErrIdemMismatch, item.Language)
				}
			}
		}
	}
	baseOK := true
	if len(cand.BaseChain) == 0 || cand.BaseChain[len(cand.BaseChain)-1] != req.ExpectedBaseVer {
		cur := ""
		if len(cand.BaseChain) > 0 {
			cur = cand.BaseChain[len(cand.BaseChain)-1]
		}
		baseOK = false
		_ = cur
	}
	for _, item := range req.Items {
		if old, ok := subs[item.Language]; ok && old.Status == "success" {
			continue
		}
		rec := SubReceipt{Language: item.Language, RecordedAt: now, ContentSHA: hashBytes(item.Content)}
		if !baseOK {
			rec.Status = "conflict"
			rec.Reason = "基准目录版本已变化，目标修正未应用"
			rec.ExpectedVerID = req.ExpectedBaseVer
			subs[item.Language] = rec
			continue
		}
		cur := cand.LangVersion[item.Language]
		if item.ExpectedTargetVersion != "" && item.ExpectedTargetVersion != cur {
			rec.Status = "conflict"
			rec.Reason = fmt.Sprintf("语言 %s 的当前版本为 %s，与修改依据 %s 不一致", item.Language, cur, item.ExpectedTargetVersion)
			rec.ExpectedVerID = item.ExpectedTargetVersion
			rec.ActualVerID = cur
			subs[item.Language] = rec
			continue
		}
		v, _, created, err := s.storeCatalogLocked(cand, "target", item.Language, item.Filename, item.Content)
		if err != nil {
			rec.Status = "conflict"
			rec.Reason = "内容无法解析: " + err.Error()
			subs[item.Language] = rec
			continue
		}
		cand.LangVersion[item.Language] = v.ID
		rec.Status = "success"
		rec.VersionID = v.ID
		rec.Fingerprint = v.Fingerprint
		rec.Keys = v.Keys
		_ = created
		subs[item.Language] = rec
	}
	if err := s.refreshResultsLocked(cand, now); err != nil {
		return nil, nil, err
	}
	langs := make([]string, 0, len(subs))
	for l := range subs {
		langs = append(langs, l)
	}
	sort.Strings(langs)
	out := &BatchOutcome{OperationID: req.OperationID, ExpectedBaseVer: req.ExpectedBaseVer, BaseOK: baseOK}
	all := true
	for _, l := range langs {
		sub := subs[l]
		out.Subs = append(out.Subs, sub)
		if sub.Status != "success" {
			all = false
		}
	}
	out.AllSucceeded = all

	if req.OperationID != "" {
		if _, ierr := s.finishIdem(cand, key, "batch", req.contentHash(), 200, out, subs); ierr != nil {
			return nil, nil, ierr
		}
	}
	s.st = cand
	if err := s.commitLocked(); err != nil {
		return nil, nil, err
	}
	return out, nil, nil
}
