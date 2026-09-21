package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"messagecatalog/internal/catalog"
)

// ImportResult is returned for catalog imports.
type ImportResult struct {
	Language       string         `json:"language"`
	ContentVersion string         `json:"contentVersion"`
	VersionChanged bool           `json:"versionChanged"`
	UploadID       string         `json:"uploadId"`
	SnapshotID     string         `json:"snapshotId"`
	Seq            int            `json:"seq"`
	PendingRename  *PendingRename `json:"pendingRename,omitempty"`
}

// ImportCatalog validates and stores one uploaded message file.
func (s *Service) ImportCatalog(opID, language string, raw []byte) (*ImportResult, error) {
	if err := s.checkWritable(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(language) == "" {
		return nil, &RequestError{Msg: "language is required"}
	}
	cat, err := catalog.Parse(language, raw)
	if err != nil {
		return nil, &RequestError{Msg: err.Error()}
	}
	req := map[string]any{"language": language, "rawSha": rawSHA(raw)}
	resp := func(stored string) (any, error) {
		var r ImportResult
		if err := json.Unmarshal([]byte(stored), &r); err != nil {
			return nil, err
		}
		return &r, nil
	}
	out, err := s.idempotent(opID, "import", req, resp, func() (any, string, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		r, note, err := s.applyImportLocked(language, raw, cat)
		return r, note, err
	})
	if err != nil {
		return nil, err
	}
	return out.(*ImportResult), nil
}

func (s *Service) applyImportLocked(language string, raw []byte, cat *catalog.Catalog) (*ImportResult, string, error) {
	rawBlob, err := s.st.PutBlob(raw)
	if err != nil {
		return nil, "", err
	}
	catBlob, err := s.storeCatalogBlob(cat)
	if err != nil {
		return nil, "", err
	}
	newVersion := cat.Fingerprint()

	var changed bool
	var pending *PendingRename
	prev := s.state.Catalogs[language]
	if prev == nil || prev.Version != newVersion {
		changed = true
	}

	s.state.Seq++ // reserved before snapshot; commitLocked accounts for it
	seq := s.state.Seq
	up := &Upload{
		ID: "up-" + rawBlob[:16], Language: language, RawBlob: rawBlob,
		Size: len(raw), ImportedAt: s.nowRFC(), Seq: seq,
	}
	s.state.Uploads[language] = up
	cv := &CatalogVersion{
		Language: language, Version: newVersion, Blob: catBlob,
		UploadID: up.ID, ImportedAt: s.nowRFC(), Seq: seq,
	}
	s.state.Catalogs[language] = cv

	isBaseline := s.state.BaselineLanguage == "" || language == s.state.BaselineLanguage
	if isBaseline {
		if s.state.BaselineLanguage == "" {
			s.state.BaselineLanguage = language
		}
		if prev != nil && prev.Version != newVersion {
			pending = s.buildPendingLocked(prev, cat)
			s.state.PendingRenames = pending
		}
	}

	// Recompute reports for all languages (results live under tuple blobs;
	// unaffected tuples remain cached).
	if _, _, err := s.reportsLocked(); err != nil {
		return nil, "", err
	}

	snap, err := s.commitLocked("import:" + language)
	if err != nil {
		return nil, "", err
	}
	up.SnapshotID = snap.ID

	res := &ImportResult{
		Language: language, ContentVersion: newVersion, VersionChanged: changed,
		UploadID: up.ID, SnapshotID: snap.ID, Seq: snap.Seq, PendingRename: pending,
	}
	note := fmt.Sprintf("import %s version=%s", language, short(newVersion))
	return res, note, nil
}

// buildPendingLocked detects removed/added baseline keys and suggests 1:1
// renames only when the counts line up exactly.
func (s *Service) buildPendingLocked(prev *CatalogVersion, newCat *catalog.Catalog) *PendingRename {
	oldCat, err := s.loadCatalogBlob(prev.Blob)
	if err != nil {
		return nil
	}
	oldSet := map[string]bool{}
	for _, e := range oldCat.Entries {
		oldSet[e.Key] = true
	}
	newSet := map[string]bool{}
	for _, e := range newCat.Entries {
		newSet[e.Key] = true
	}
	// apply already-confirmed mappings so they are not re-proposed
	var removed, added []string
	for k := range oldSet {
		if !newSet[k] {
			root := resolveRoot(s.state.Mapping, k)
			if !newSet[root] {
				removed = append(removed, k)
			}
		}
	}
	for k := range newSet {
		if !oldSet[k] {
			// still added if no old key maps to it
			hasIncoming := false
			for old, newK := range s.state.Mapping {
				_ = old
				if newK == k && oldSet[resolveRoot(s.state.Mapping, old)] {
					hasIncoming = true
				}
			}
			if !hasIncoming {
				added = append(added, k)
			}
		}
	}
	sort.Strings(removed)
	sort.Strings(added)
	p := &PendingRename{
		OldVersion: prev.Version, NewVersion: newCat.Fingerprint(),
		Removed: removed, Added: added, Suggested: map[string]string{},
		CreatedAt: s.nowRFC(),
	}
	// Only suggest when exactly one removed/one added (never silently merge).
	if len(removed) == 1 && len(added) == 1 {
		p.Suggested[removed[0]] = added[0]
	}
	return p
}

// ConfirmRenameResult reports the applied edges.
type ConfirmRenameResult struct {
	Edges          map[string]string `json:"edges"`
	MappingVersion int               `json:"mappingVersion"`
	SnapshotID     string            `json:"snapshotId"`
	Seq            int               `json:"seq"`
}

// ConfirmRename validates and installs old->new edges.
func (s *Service) ConfirmRename(opID, baseVersion string, edges map[string]string) (*ConfirmRenameResult, error) {
	if err := s.checkWritable(); err != nil {
		return nil, err
	}
	if len(edges) == 0 {
		return nil, &RequestError{Msg: "at least one rename edge is required"}
	}
	req := map[string]any{"baseVersion": baseVersion, "edges": edges}
	out, err := s.idempotent(opID, "rename", req, func(stored string) (any, error) {
		var r ConfirmRenameResult
		if err := json.Unmarshal([]byte(stored), &r); err != nil {
			return nil, err
		}
		return &r, nil
	}, func() (any, string, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		r, note, err := s.applyRenameLocked(baseVersion, edges)
		return r, note, err
	})
	if err != nil {
		return nil, err
	}
	return out.(*ConfirmRenameResult), nil
}

func (s *Service) applyRenameLocked(baseVersion string, edges map[string]string) (*ConfirmRenameResult, string, error) {
	base := s.state.Catalogs[s.state.BaselineLanguage]
	if base == nil {
		return nil, "", &RequestError{Msg: "no baseline catalog"}
	}
	if base.Version != baseVersion {
		return nil, "", &ConflictError{
			Msg: fmt.Sprintf("baseline version changed since you viewed it: viewed=%s current=%s",
				short(baseVersion), short(base.Version)),
		}
	}
	cat, err := s.loadCatalogBlob(base.Blob)
	if err != nil {
		return nil, "", err
	}
	currentKeys := map[string]bool{}
	for _, e := range cat.Entries {
		currentKeys[e.Key] = true
	}
	// Validate every edge.
	newTargets := map[string]int{}
	proposed := map[string]string{}
	for old, new := range edges {
		if old == "" || new == "" {
			return nil, "", &RequestError{Msg: "rename edge keys must be non-empty"}
		}
		if _, mapped := s.state.Mapping[old]; !mapped {
			if s.state.PendingRenames == nil || !contains(s.state.PendingRenames.Removed, old) {
				return nil, "", &RequestError{
					Msg: "old key is not part of the current rename proposal: " + old,
				}
			}
		}
		if !currentKeys[new] {
			return nil, "", &RequestError{Msg: "rename target key does not exist in baseline: " + new}
		}
		if path, cyc := createsCycle(s.state.Mapping, proposed, old, new); cyc {
			return nil, "", &RequestError{Msg: "rename would form a cycle: " + strings.Join(path, " -> ")}
		}
		proposed[old] = new
		newTargets[new]++
	}
	// Two old keys must not merge into one new key, considering both this
	// request and already-installed edges.
	existingSource := map[string]string{} // new -> old
	for o, n := range s.state.Mapping {
		if prev, clash := existingSource[n]; clash && prev != o {
			return nil, "", &RequestError{Msg: "stored mapping already merges two keys into " + n}
		}
		existingSource[n] = o
	}
	for new, n := range newTargets {
		if n > 1 {
			return nil, "", &RequestError{Msg: "multiple old keys would merge into " + new}
		}
		if oldSrc, ok := existingSource[new]; ok && oldSrc != edgesSource(edges, new) {
			return nil, "", &RequestError{Msg: "rename would merge two old keys into " + new}
		}
	}

	for old, new := range proposed {
		if existing, ok := s.state.Mapping[old]; ok && existing != new {
			return nil, "", &ConflictError{
				Msg: fmt.Sprintf("key %s already mapped to %s", old, short(existing)),
			}
		}
		s.state.Mapping[old] = new
	}
	s.state.MappingVersion++
	s.state.PendingRenames = nil
	if _, _, err := s.reportsLocked(); err != nil {
		return nil, "", err
	}
	snap, err := s.commitLocked("rename-confirm")
	if err != nil {
		return nil, "", err
	}
	res := &ConfirmRenameResult{
		Edges: proposed, MappingVersion: s.state.MappingVersion,
		SnapshotID: snap.ID, Seq: snap.Seq,
	}
	return res, "rename confirm", nil
}

func edgesSource(edges map[string]string, target string) string {
	for o, n := range edges {
		if n == target {
			return o
		}
	}
	return ""
}

// createsCycle simulates adding old->new and returns a path if a cycle forms.
func createsCycle(existing, proposed map[string]string, old, new string) ([]string, bool) {
	g := map[string]string{}
	for k, v := range existing {
		g[k] = v
	}
	for k, v := range proposed {
		g[k] = v
	}
	g[old] = new
	path := []string{new}
	cur := new
	seen := map[string]bool{old: true, new: true}
	for {
		nxt, ok := g[cur]
		if !ok {
			return nil, false
		}
		path = append(path, nxt)
		if nxt == old {
			return append([]string{old}, path...), true
		}
		if seen[nxt] {
			return path, true
		}
		seen[nxt] = true
		cur = nxt
	}
}

func short(x string) string {
	if len(x) > 12 {
		return x[:12]
	}
	return x
}

func sha16(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
