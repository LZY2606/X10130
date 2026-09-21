package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"msgcheck/internal/state"
)

// requestFingerprint hashes request content as canonical JSON. Any map keys
// are sorted, so caller JSON key ordering cannot break idempotency.
func requestFingerprint(v any) string {
	b, err := json.Marshal(canonical(v))
	if err != nil {
		// fallback: best-effort marshal
		b, _ = json.Marshal(v)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

func canonical(v any) any {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(t))
		for _, k := range keys {
			out[k] = canonical(t[k])
		}
		return out
	case []any:
		for i := range t {
			t[i] = canonical(t[i])
		}
		return t
	default:
		return v
	}
}

// beginOp enforces idempotency semantics.
//
// - existing op id + same content: returns stored response (caller replays).
// - existing op id + different content: ConflictError.
// - new op id: nil (proceed).
func (a *App) beginOpLocked(kind, opID, reqFP string) (json.RawMessage, error) {
	if opID == "" {
		return nil, nil
	}
	if op, ok := a.data.Ops[opID]; ok {
		if op.Request != reqFP {
			return nil, &ConflictError{Reason: "operation id " + opID + " was already submitted with different content", Current: a.pinLocked()}
		}
		return op.Response, nil
	}
	return nil, nil
}

// finishOp records a successfully applied operation.
func (a *App) finishOpLocked(kind, opID, reqFP string, resp any) {
	if opID == "" {
		return
	}
	rb, _ := json.Marshal(resp)
	a.data.Ops[opID] = &state.Operation{
		ID: opID, Kind: kind, Request: reqFP, At: a.now(),
		Response: rb, Snapshot: a.pinLocked().Snapshot,
	}
}

// Operation returns the stored result for an id.
func (a *App) Operation(id string) (*state.Operation, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	op, ok := a.data.Ops[id]
	return op, ok
}
