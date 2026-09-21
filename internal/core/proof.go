package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// Proof is the machine-readable release attestation. It contains no
// wall-clock fields: regenerating it while the state and the expiry
// decisions are unchanged yields byte-identical output.
type Proof struct {
	Schema              string           `json:"schema"`
	Snapshot            int64            `json:"snapshot"`
	ParserVersion       string           `json:"parserVersion"`
	Base                ProofCatalog     `json:"base"`
	Targets             []ProofCatalog   `json:"targets"`
	InputFingerprints   []string         `json:"inputFingerprints"`
	MappingsHash        string           `json:"mappingsHash"`
	Results             []ProofResult    `json:"results"`
	UnexemptedPerLocale map[string]int   `json:"unexemptedPerLocale"`
	UnexemptedTotal     int              `json:"unexemptedTotal"`
	Exemptions          []ProofExemption `json:"exemptions"`
}

// ProofCatalog identifies one catalog version in a proof.
type ProofCatalog struct {
	Locale      string `json:"locale"`
	Version     string `json:"version"`
	Fingerprint string `json:"fingerprint"`
}

// ProofResult ties a proof to the exact validation result it used.
type ProofResult struct {
	Locale   string `json:"locale"`
	ResultID string `json:"resultId"`
}

// ProofExemption records one exemption decision inside a proof.
type ProofExemption struct {
	IssueID  string `json:"issueId"`
	Reason   string `json:"reason"`
	Deadline int64  `json:"deadline"`
	Expired  bool   `json:"expired"`
}

// BuildProof renders the deterministic proof bytes for the current state.
// nowUnix is only used to decide exemption expiry; it is not embedded.
func BuildProof(st *State, nowUnix int64) (id string, data []byte, err error) {
	baseLocale := BaseLocale(st)
	p := Proof{
		Schema:              "release-proof/v1",
		Snapshot:            st.Seq,
		ParserVersion:       "",
		Targets:             []ProofCatalog{},
		InputFingerprints:   []string{},
		MappingsHash:        MappingsHash(st.Mappings),
		Results:             []ProofResult{},
		UnexemptedPerLocale: map[string]int{},
		Exemptions:          []ProofExemption{},
	}
	fpSet := map[string]bool{}
	addCatalog := func(locale string) {
		ls := st.Locales[locale]
		ver := st.Versions[ls.CurrentVersion]
		pc := ProofCatalog{Locale: locale, Version: ver.ID, Fingerprint: ver.Fingerprint}
		if locale == baseLocale {
			p.Base = pc
		} else {
			p.Targets = append(p.Targets, pc)
		}
		fpSet[ver.Fingerprint] = true
	}
	if baseLocale != "" {
		addCatalog(baseLocale)
	}
	for _, locale := range SortedKeys(st.Locales) {
		if locale != baseLocale {
			addCatalog(locale)
		}
	}
	for fp := range fpSet {
		p.InputFingerprints = append(p.InputFingerprints, fp)
	}
	sort.Strings(p.InputFingerprints)

	current := CurrentResultIDs(st)
	for _, locale := range SortedKeys(current) {
		r := st.Results[current[locale]]
		if r == nil {
			continue
		}
		p.ParserVersion = r.ParserVersion
		p.Results = append(p.Results, ProofResult{Locale: locale, ResultID: r.ID})
		count := 0
		for _, is := range r.Issues {
			ex := findExemption(st, is.ID)
			if ex != nil && ExemptionActive(ex, nowUnix) {
				continue
			}
			count++
		}
		p.UnexemptedPerLocale[locale] = count
		p.UnexemptedTotal += count
	}
	for _, id := range SortedKeys(st.Exemptions) {
		e := st.Exemptions[id]
		p.Exemptions = append(p.Exemptions, ProofExemption{
			IssueID:  e.IssueID,
			Reason:   e.Reason,
			Deadline: e.Deadline,
			Expired:  !ExemptionActive(e, nowUnix),
		})
	}
	data, err = json.MarshalIndent(p, "", "  ")
	if err != nil {
		return "", nil, err
	}
	data = append(data, '\n')
	sum := sha256.Sum256(data)
	return "p_" + hex.EncodeToString(sum[:])[:16], data, nil
}

func findExemption(st *State, issueID string) *Exemption {
	for _, e := range st.Exemptions {
		if e.IssueID == issueID {
			return e
		}
	}
	return nil
}
