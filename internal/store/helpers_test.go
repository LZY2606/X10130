package store

import (
	"encoding/json"
	"sort"
)

func marshalCatalog(m map[string]string) []byte {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	type msg struct {
		Key  string `json:"key"`
		Text string `json:"text"`
	}
	var out struct {
		Messages []msg `json:"messages"`
	}
	for _, k := range keys {
		out.Messages = append(out.Messages, msg{Key: k, Text: m[k]})
	}
	b, _ := json.Marshal(out)
	return b
}

