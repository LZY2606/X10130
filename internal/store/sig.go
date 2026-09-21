package store

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"msgcatalog/internal/icu"
)

const parserVersionConst = icu.ParserVersion

// MappingSignature is a stable hash of the confirmed rename edges.
func MappingSignature(edges []Edge) string {
	cp := make([]Edge, len(edges))
	copy(cp, edges)
	sort.Slice(cp, func(a, b int) bool {
		if cp[a].OldKey != cp[b].OldKey {
			return cp[a].OldKey < cp[b].OldKey
		}
		return cp[a].NewKey < cp[b].NewKey
	})
	h := sha256.New()
	for _, e := range cp {
		h.Write([]byte(e.OldKey))
		h.Write([]byte{0})
		h.Write([]byte(e.NewKey))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// SnapshotID identifies one committed state.
func SnapshotID(seq int64, langFPs map[string]string, mappingSig string) string {
	h := sha256.New()
	names := make([]string, 0, len(langFPs))
	for n := range langFPs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		h.Write([]byte(n))
		h.Write([]byte{0})
		h.Write([]byte(langFPs[n]))
		h.Write([]byte{0})
	}
	h.Write([]byte(mappingSig))
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:12]) + fmtSeq(seq)
}

func fmtSeq(seq int64) string {
	const hexd = "0123456789abcdef"
	out := make([]byte, 16)
	for i := 15; i >= 0; i-- {
		out[i] = hexd[seq&0xf]
		seq >>= 4
	}
	return "-" + string(out)
}
