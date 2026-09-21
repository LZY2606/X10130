// Package catalog defines the parsed structure of ICU-style catalog messages.
package catalog

// Node kinds.
const (
	KindLiteral    = "literal"
	KindArgument   = "argument"
	KindPlural     = "plural"
	KindSelect     = "select"
	KindTag        = "tag"
)

// Node is one parsed fragment of a message.
type Node struct {
	Kind     string  `json:"kind"`
	Text     string  `json:"text,omitempty"`
	Name     string  `json:"name,omitempty"`
	Type     string  `json:"type,omitempty"`
	Style    string  `json:"style,omitempty"`
	Offset   int     `json:"offset,omitempty"`
	Tag      string  `json:"tag,omitempty"`
	SelfClose bool   `json:"selfClose,omitempty"`
	Branches []Branch `json:"branches,omitempty"`
	Children []Node   `json:"children,omitempty"`
}

// Branch is one plural/select case.
type Branch struct {
	Case     string `json:"case"`
	Children []Node `json:"children"`
}

// Parsed is the result of parsing one message.
type Parsed struct {
	Nodes       []Node     `json:"nodes"`
	Placeholders []Placeholder `json:"placeholders"`
	Tags        []TagUse   `json:"tags"`
	Errors      []string   `json:"errors,omitempty"`
}

// Placeholder describes an ICU argument used in a message.
type Placeholder struct {
	Name string `json:"name"`
	Type string `json:"type"` // "", number/date/time/plural/select...
}

// TagUse is one occurrence of a rich-text tag.
type TagUse struct {
	Name      string `json:"name"`
	Open      bool   `json:"open"`
	Close     bool   `json:"close"`
	SelfClose bool   `json:"selfClose"`
}
