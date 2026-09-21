package catalog

import (
	"bytes"
	"encoding/json"
)

// canonicalJSON marshals v deterministically (no whitespace, stable key order).
func canonicalJSON(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	// Re-encode through a generic value to normalize number formatting etc.
	var anyVal any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&anyVal); err != nil {
		return b, nil
	}
	return json.Marshal(anyVal)
}
