package service

import (
	"time"

	"messagecatalog/internal/catalog"
	"messagecatalog/internal/store"
)

// invalidateAfterImport marks affected existing validation results as
// superseded/invalidated. Only languages depending on the changed catalog are
// affected; other languages keep their successful results.
func (s *Service) invalidateAfterImport(st *store.State, lang, role, newVersionID, newFingerprint string) {
	now := time.Now().UTC()
	if role == roleBaseline {
		// Baseline changed: every validation depending on the previous baseline
		// version or parser version is affected.
		for i := range st.Validations {
			v := &st.Validations[i]
			if v.Invalidated {
				continue
			}
			if v.BaseVersionID != newVersionID {
				v.Invalidated = true
				v.InvalidReason = "baseline catalog changed"
				v.Superseded = true
				v.SupersededBy = ""
				_ = now
			}
		}
		// Stale proofs no longer describe the latest state.
		for i := range st.Proofs {
			st.Proofs[i].Current = false
		}
		return
	}
	// Target changed: only that language's validations are affected.
	for i := range st.Validations {
		v := &st.Validations[i]
		if v.Invalidated {
			continue
		}
		if v.TargetLang == lang && v.TargVersionID != newVersionID {
			v.Invalidated = true
			v.InvalidReason = "target catalog changed"
			v.Superseded = true
		}
	}
	for i := range st.Proofs {
		st.Proofs[i].Current = false
	}
}

// InvalidateParser marks results produced by older parser versions.
func (s *Service) InvalidateParser() error {
	return s.Store.Commit(func(st *store.State) error {
		for i := range st.Validations {
			v := &st.Validations[i]
			if !v.Invalidated && v.ParserVersion != catalog.ParserVersion {
				v.Invalidated = true
				v.InvalidReason = "parser version changed from " + v.ParserVersion
			}
		}
		for i := range st.Proofs {
			st.Proofs[i].Current = false
		}
		return nil
	})
}

// mappingChanged invalidates all validations (all depend on baseline mapping).
func mappingChanged(st *store.State) {
	for i := range st.Validations {
		v := &st.Validations[i]
		if !v.Invalidated {
			v.Invalidated = true
			v.InvalidReason = "rename mapping changed"
			v.Superseded = true
		}
	}
	for i := range st.Proofs {
		st.Proofs[i].Current = false
	}
}
