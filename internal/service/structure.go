package service

import (
	"messagecatalog/internal/catalog"
	"messagecatalog/internal/icu"
)

func icuParse(text string) (*icu.Message, error) {
	return icu.Parse(catalog.NormalizeText(text))
}

// ArgView is the JSON-friendly placeholder structure.
type ArgView struct {
	Name  string         `json:"name"`
	Type  string         `json:"type"`
	Style string         `json:"style,omitempty"`
	Cases map[string]any `json:"cases,omitempty"`
}

// MsgView is the parsed tree shown in side-by-side view.
type MsgView struct {
	Placeholders []ArgView `json:"placeholders"`
	Tags         []icu.Tag `json:"tags"`
}

func structureOf(m *icu.Message) MsgView {
	return MsgView{Placeholders: argsOf(m), Tags: m.AllTags()}
}

func argsOf(m *icu.Message) []ArgView {
	var out []ArgView
	for _, a := range m.Args {
		av := ArgView{Name: a.Name, Type: a.Type, Style: a.Style}
		if len(a.Cases) > 0 {
			av.Cases = map[string]any{}
			for _, c := range a.Cases {
				av.Cases[c.Key] = MsgView{
					Placeholders: argsOf(c.Message),
					Tags:         c.Message.AllTags(),
				}
			}
		}
		out = append(out, av)
	}
	return out
}

// rootAliasesFor returns keys that share the mapping root with k.
func rootAliasesFor(mapping map[string]string, k string) []string {
	root := resolveRoot(mapping, k)
	seen := map[string]bool{root: true}
	for o, n := range mapping {
		if resolveRoot(mapping, o) == root {
			seen[o] = true
			seen[n] = true
		}
	}
	out := []string{}
	for x := range seen {
		out = append(out, x)
	}
	return out
}
