package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"msgcheck/internal/state"
)

// mappingsFP identifies the whole rename-map state.
func mappingsFP(d *state.Data) string {
	type edge struct{ Old, New string }
	edges := make([]edge, 0, len(d.Renames))
	for _, r := range d.Renames {
		edges = append(edges, edge{r.OldKey, r.NewKey})
	}
	b, _ := json.Marshal(edges)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

// resolveChain follows old -> new edges starting from key. It returns the
// final key in the chain (key itself if no edge) and the full path.
func resolveChain(d *state.Data, key string) (string, []string) {
	next := map[string]string{}
	for _, r := range d.Renames {
		next[r.OldKey] = r.NewKey
	}
	path := []string{key}
	seen := map[string]bool{key: true}
	cur := key
	for {
		n, ok := next[cur]
		if !ok {
			return cur, path
		}
		if seen[n] {
			// Defensive: cycle. Validation refuses to create cycles, but a
			// malformed frame could contain one; surface key as-is.
			return cur, path
		}
		seen[n] = true
		path = append(path, n)
		cur = n
	}
}

// currentBaselineKeys maps every historical key to its current baseline key.
func currentBaselineKeys(d *state.Data) map[string]string {
	out := map[string]string{}
	if d.Baseline == nil {
		return out
	}
	// Seed with all keys that ever appeared (from current baseline + rename
	// endpoints); the chain gives the current name deterministically.
	all := map[string]bool{}
	// current baseline keys come from the stored catalog, but chain lookup
	// works for any historical key supplied by callers; seed rename keys.
	for _, r := range d.Renames {
		all[r.OldKey] = true
		all[r.NewKey] = true
	}
	for k := range all {
		cur, _ := resolveChain(d, k)
		out[k] = cur
	}
	return out
}

// checkRenameValid validates a proposed old -> new edge against existing
// edges: no cycles, and no two old keys may converge onto one new key.
func checkRenameValid(renames []state.Rename, oldKey, newKey string) error {
	next := map[string]string{}
	inject := map[string]string{} // newKey -> oldKey
	for _, r := range renames {
		next[r.OldKey] = r.NewKey
		if prev, ok := inject[r.NewKey]; ok && prev != r.OldKey {
			// defensive: existing data already convergent
			continue
		}
		inject[r.NewKey] = r.OldKey
	}
	if oldKey == newKey {
		return &BadRequestError{Reason: "old key and new key must differ"}
	}
	if prev, ok := inject[newKey]; ok && prev != oldKey {
		return &ConflictError{Reason: "new key " + newKey + " is already the target of " + prev + "; two old keys cannot silently merge"}
	}
	if _, ok := next[newKey]; ok {
		// Traversing from newKey must not eventually reach oldKey (cycle).
		seen := map[string]bool{newKey: true}
		cur := newKey
		for {
			n, exists := next[cur]
			if !exists {
				break
			}
			if n == oldKey || seen[n] {
				return &ConflictError{Reason: "confirming this rename would form a mapping cycle"}
			}
			seen[n] = true
			cur = n
		}
	}
	return nil
}
