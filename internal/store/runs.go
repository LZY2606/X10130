package store

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
)

// updateRuns maintains the single stable validation run for the current
// parser+baseline+mapping configuration.
//
//   - A parser/baseline/mapping change permanently invalidates every run.
//   - A target-language move invalidates the config run and names only the
//     changed languages; unaffected languages are not named.
//   - Returning to an identical configuration reactivates the stored run and
//     points the new snapshot at it, so old success is never cleared.
func (s *Store) updateRuns(now int64) {
	st := s.state
	cur := st.Snapshots[st.Seq]

	rid := configRunID(cur)
	run, exists := st.Runs[rid]
	if !exists {
		run = &Run{
			ID: rid, ParserVersion: cur.ParserVersion,
			BaselineFP: cur.LangFPs[cur.Baseline], BaselineName: cur.Baseline,
			MappingSig: cur.MappingSig, LangFPs: map[string]string{},
			Status: "current", Created: now,
		}
		st.Runs[rid] = run
	}

	prev := st.Snapshots[st.Seq-1]
	if prev != nil && configRunID(prev) == rid {
		switch {
		case cur.ParserVersion != prev.ParserVersion:
			run.Status = "invalid"
			run.InvalidReason = "parser version changed: " + prev.ParserVersion + " -> " + cur.ParserVersion
		case cur.Baseline != prev.Baseline:
			run.Status = "invalid"
			run.InvalidReason = "baseline language changed"
		case cur.LangFPs[cur.Baseline] != prev.LangFPs[prev.Baseline]:
			run.Status = "invalid"
			run.InvalidReason = "baseline catalog version changed"
		case cur.MappingSig != prev.MappingSig:
			run.Status = "invalid"
			run.InvalidReason = "rename mapping changed"
		default:
			var changed []string
			for name := range cur.LangFPs {
				if name == cur.Baseline {
					continue
				}
				oldFP, existed := prev.LangFPs[name]
				// Only an existing language moving to a new version
				// invalidates; importing an additional language does not.
				if existed && oldFP != cur.LangFPs[name] {
					changed = append(changed, name)
				}
			}
			if len(changed) > 0 && run.Status == "current" {
				run.Status = "invalid"
				run.InvalidReason = "target versions changed: " + joinSorted(changed)
			}
		}
	} else if prev != nil && configRunID(prev) != rid {
		// Entering a (possibly previously seen) configuration. If every
		// recorded invalidation reason still matches current reality, leave
		// the explicit status; a run created fresh is current.
	}

	run.Seq = cur.Seq
	run.LangFPs = map[string]string{}
	for k, v := range cur.LangFPs {
		run.LangFPs[k] = v
	}
	run.IssueCount = len(cur.Issues)
	st.Runs[rid] = run
	cur.RunID = rid
}

func configRunID(sn *Snapshot) string {
	h := sha256.New()
	h.Write([]byte(sn.ParserVersion))
	h.Write([]byte{0})
	h.Write([]byte(sn.Baseline))
	h.Write([]byte{0})
	h.Write([]byte(sn.LangFPs[sn.Baseline]))
	h.Write([]byte{0})
	h.Write([]byte(sn.MappingSig))
	return "run-" + hex.EncodeToString(h.Sum(nil))[:16]
}

func joinSorted(in []string) string {
	cp := append([]string{}, in...)
	sort.Strings(cp)
	out := ""
	for i, s := range cp {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}
