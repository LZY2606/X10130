package state

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Issue codes.
const (
	IssueMissing            = "missing_key"
	IssueExtra              = "extra_key"
	IssuePlaceholderMissing = "placeholder_mismatch"
	IssueTypeDrift          = "type_drift"
	IssuePlural             = "plural_categories"
	IssueTags               = "tags_unbalanced"
)

// Issue is one validation finding for a target language.
type Issue struct {
	ID          string   `json:"id"`
	Code        string   `json:"code"`
	Language    string   `json:"language"`
	Key         string   `json:"key"`
	Placeholder string   `json:"placeholder,omitempty"`
	Side        string   `json:"side,omitempty"` // baseline | target
	Detail      string   `json:"detail"`
	Expected    []string `json:"expected,omitempty"`
	Actual      []string `json:"actual,omitempty"`
}

// IssueIdentity builds the stable identifier used to attach exemptions. It
// deliberately excludes descriptive text so re-runs with identical meaning
// resolve to the same identity.
func IssueIdentity(i Issue) string {
	parts := []string{i.Code, i.Language, i.Key}
	switch i.Code {
	case IssueTypeDrift:
		parts = append(parts, i.Placeholder, joinOrDash(i.Expected), joinOrDash(i.Actual))
	case IssuePlaceholderMissing:
		parts = append(parts, joinOrDash(i.Expected), joinOrDash(i.Actual))
	case IssuePlural:
		parts = append(parts, i.Placeholder, joinOrDash(i.Expected), joinOrDash(i.Actual))
	case IssueTags:
		parts = append(parts, i.Side)
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:16])
}

func joinOrDash(xs []string) string {
	if len(xs) == 0 {
		return "-"
	}
	return strings.Join(xs, ",")
}

// WithID returns the issue with its stable ID filled in.
func (i Issue) WithID() Issue {
	i.ID = IssueIdentity(i)
	return i
}
