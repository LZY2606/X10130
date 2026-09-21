// Package store is a local, crash-safe persistence layer. It uses an
// append-only, CRC-framed WAL as the source of truth plus content-addressed
// blobs for byte-exact uploaded files and release proofs. Every mutation is
// one WAL event; blobs are written and fsynced before the referencing event,
// so a crash can never leave a viewable record whose bytes are missing.
package store

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

)

var (
	ErrConflict       = errors.New("version conflict")
	ErrBadRequest     = errors.New("bad request")
	ErrIdemConflict   = errors.New("idempotency key reused with different content")
	ErrReadOnly       = errors.New("store is read-only after recovery from corruption")
	ErrNotFound       = errors.New("not found")
)

type Clock func() time.Time

type RecoveryReport struct {
	Healthy bool     `json:"healthy"`
	Notes   []string `json:"notes"`
	Quarantined int   `json:"quarantined_bytes"`
}

type Store struct {
	mu      sync.Mutex
	dir     string
	f       *os.File
	state   *State
	clock   Clock
	readOnly bool
	recovery RecoveryReport

	// Fault injection: crash after N fsyncs (0 disables).
	faultCrashAfter int
	fsyncCount      int
}

// Event is the WAL frame payload.
type Event struct {
	Type        string          `json:"type"`
	At          int64           `json:"at"`
	Import      *ImportEvent    `json:"import,omitempty"`
	Rename      *RenameEvent    `json:"rename,omitempty"`
	Exemption   *ExemptionEvent `json:"exemption,omitempty"`
	Batch       *BatchEvent     `json:"batch,omitempty"`
	Proof       *ProofEvent     `json:"proof,omitempty"`
}

type ImportEvent struct {
	Language    string           `json:"language"`
	AsBaseline  bool             `json:"as_baseline"`
	Version     *VersionEntry    `json:"version"`
	Raw         RawRef           `json:"raw"`
	OpKey       string           `json:"op_key"`
	ReqHash     string           `json:"req_hash"`
	NewBaseline string           `json:"new_baseline,omitempty"`
	Proposal    *RenameProposal  `json:"proposal,omitempty"`
}

type RenameEvent struct {
	OpKey   string `json:"op_key"`
	ReqHash string `json:"req_hash"`
	Edges   []Edge `json:"edges"`
	Resolve string `json:"resolve,omitempty"`
}

type ExemptionEvent struct {
	OpKey      string `json:"op_key"`
	ReqHash    string `json:"req_hash"`
	IssueID    string `json:"issue_id"`
	Reason     string `json:"reason"`
	Deadline   int64  `json:"deadline"`
	BaseSeq    int64  `json:"base_seq"`
	Revoke     bool   `json:"revoke"`
}

type BatchEvent struct {
	Batch *BatchFix `json:"batch"`
}

type ProofEvent struct {
	Proof *ProofRecord `json:"proof"`
	OpKey string       `json:"op_key,omitempty"`
}

func Open(dir string, clock Clock) (*Store, error) {
	if clock == nil {
		clock = time.Now
	}
	if err := os.MkdirAll(filepath.Join(dir, "blobs", "raw"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "blobs", "proof"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "quarantine"), 0o755); err != nil {
		return nil, err
	}
	st := &Store{
		dir:   dir,
		clock: clock,
		state: newState(),
		recovery: RecoveryReport{Healthy: true},
	}
	if err := st.replay(); err != nil {
		return nil, err
	}
	if err := st.verifyBlobs(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "wal.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	st.f = f
	return st, nil
}

func newState() *State {
	return &State{
		Langs:      map[string]*LangState{},
		Versions:   map[string]*VersionEntry{},
		Proposals:  map[string]RenameProposal{},
		Exemptions: map[string]Exemption{},
		Batches:    map[string]*BatchFix{},
		Runs:       map[string]*Run{},
		Idem:       map[string]IdemRecord{},
		Proofs:     map[string]ProofRecord{},
		ProofOps:   map[string]string{},
		Snapshots:  map[int64]*Snapshot{},
	}
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f != nil {
		return s.f.Close()
	}
	return nil
}

func (s *Store) Recovery() RecoveryReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recovery
}

func (s *Store) now() int64 { return s.clock().Unix() }

// ---- framing ----
// Frame: 8-byte big-endian payload length, JSON payload, 4-byte CRC32 of
// (length bytes + payload).

func writeFrame(w io.Writer, payload []byte) error {
	var lenb [8]byte
	n := uint64(len(payload))
	for i := 7; i >= 0; i-- {
		lenb[i] = byte(n)
		n >>= 8
	}
	var crcb [4]byte
	crc := crc32ieee(append(lenb[:], payload...))
	c := crc
	for i := 3; i >= 0; i-- {
		crcb[i] = byte(c)
		c >>= 8
	}
	if _, err := w.Write(lenb[:]); err != nil {
		return err
	}
	if _, err := w.Write(payload); err != nil {
		return err
	}
	_, err := w.Write(crcb[:])
	return err
}

// readFrames returns payloads up to the last fully valid frame and the count
// of unusable trailing bytes.
func readFrames(data []byte) (payloads [][]byte, trailing int, err error) {
	pos := 0
	for pos+12 <= len(data) {
		var n uint64
		for i := 0; i < 8; i++ {
			n = n<<8 | uint64(data[pos+i])
		}
		if n > uint64(len(data)) || pos+12+int(n) > len(data) {
			return payloads, len(data) - pos, nil
		}
		payload := data[pos+8 : pos+8+int(n)]
		var crc uint32
		for i := 0; i < 4; i++ {
			crc = crc<<8 | uint32(data[pos+8+int(n)+i])
		}
		if crc32ieee(data[pos:pos+8+int(n)]) != crc {
			return payloads, len(data) - pos, nil
		}
		payloads = append(payloads, append([]byte(nil), payload...))
		pos += 12 + int(n)
	}
	return payloads, len(data) - pos, nil
}

func crc32ieee(b []byte) uint32 {
	// IEEE 802.3 polynomial, table-driven.
	var table [256]uint32
	for i := 0; i < 256; i++ {
		c := uint32(i)
		for j := 0; j < 8; j++ {
			if c&1 != 0 {
				c = 0xEDB88320 ^ (c >> 1)
			} else {
				c >>= 1
			}
		}
		table[i] = c
	}
	var crc uint32 = 0xFFFFFFFF
	for _, v := range b {
		crc = table[byte(crc)^v] ^ (crc >> 8)
	}
	return crc ^ 0xFFFFFFFF
}
