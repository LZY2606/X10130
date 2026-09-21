// Package store implements durable storage on the local filesystem. Every
// state change is appended as one checksummed frame; a torn tail or a bad
// checksum causes recovery to stop at the last fully valid frame and
// quarantine the remainder instead of silently resetting data.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const (
	stateFile = "state.jsonl"
	blobsDir  = "blobs"
)

// Note is one startup recovery report entry.
type Note struct {
	Level   string `json:"level"` // info | warning | error
	Detail  string `json:"detail"`
}

// Store is a frame log plus content-addressed blob directory.
type Store struct {
	dir string
	mu  sync.Mutex

	// CrashBeforeSwap, when set, makes Save write+fsync the temp file and
	// then return ErrCrashSimulation without swapping it into place.
	CrashBeforeSwap bool
}

// ErrCrashSimulation is returned by an injected crash point.
var ErrCrashSimulation = errors.New("simulated crash before atomic swap")

func New(dir string) *Store { return &Store{dir: dir} }

func (s *Store) Dir() string { return s.dir }

// Open prepares directories and returns recovery notes from the existing log.
func (s *Store) Open() ([]Note, error) {
	if err := os.MkdirAll(filepath.Join(s.dir, blobsDir), 0o755); err != nil {
		return nil, err
	}
	return s.recover()
}

type frameHeader struct {
	Seq   int64  `json:"seq"`
	SHA   string `json:"sha256"`
	Bytes int    `json:"bytes"`
}

// Save appends one frame containing dataJSON atomically. Blobs referenced by
// the frame must already exist on disk.
func (s *Store) Save(seq int64, dataJSON []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	hdr := frameHeader{Seq: seq, Bytes: len(dataJSON)}
	sum := sha256.Sum256(dataJSON)
	hdr.SHA = hex.EncodeToString(sum[:])
	hb, err := json.Marshal(&hdr)
	if err != nil {
		return err
	}
	frame := append(append(hb, '\n'), dataJSON...)

	final := filepath.Join(s.dir, stateFile)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, frame, 0o644); err != nil {
		return err
	}
	if err := fsyncFile(tmp); err != nil {
		return err
	}
	if s.CrashBeforeSwap {
		return ErrCrashSimulation
	}
	if err := os.Rename(tmp, final); err != nil {
		return err
	}
	return fsyncDir(s.dir)
}

// BlobPath is the on-disk location of a content-addressed blob.
func (s *Store) BlobPath(id string) string {
	return filepath.Join(s.dir, blobsDir, id)
}

// HasBlob reports whether a raw-file blob exists.
func (s *Store) HasBlob(id string) bool {
	_, err := os.Stat(s.BlobPath(id))
	return err == nil
}

// WriteBlob stores raw bytes as a content-addressed file. Re-writing the
// same id is a no-op so repeated imports keep the canonical object.
func (s *Store) WriteBlob(id string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeBlobLocked(id, data)
}

func (s *Store) writeBlobLocked(id string, data []byte) error {
	if err := os.MkdirAll(filepath.Join(s.dir, blobsDir), 0o755); err != nil {
		return err
	}
	final := s.BlobPath(id)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := fsyncFile(tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		return err
	}
	return fsyncDir(filepath.Join(s.dir, blobsDir))
}

// ReadBlob returns one blob's bytes.
func (s *Store) ReadBlob(id string) ([]byte, error) {
	return os.ReadFile(s.BlobPath(id))
}

// Load reads and verifies the newest frame.
func (s *Store) Load() ([]byte, int64, error) {
	raw, err := os.ReadFile(filepath.Join(s.dir, stateFile))
	if errors.Is(err, os.ErrNotExist) || len(raw) == 0 {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	lines := strings.SplitAfter(string(raw), "\n")
	var lastData []byte
	var lastSeq int64
	pos := 0
	for pos < len(lines) {
		hdrLine := strings.TrimRight(lines[pos], "\n")
		pos++
		if hdrLine == "" {
			continue
		}
		var hdr frameHeader
		if err := json.Unmarshal([]byte(hdrLine), &hdr); err != nil {
			return nil, 0, fmt.Errorf("state log corrupt at load: %w", err)
		}
		if pos >= len(lines) {
			return nil, 0, fmt.Errorf("state log truncated at load")
		}
		data := []byte(strings.TrimRight(lines[pos], "\n"))
		pos++
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != hdr.SHA || hdr.Bytes != len(data) {
			return nil, 0, fmt.Errorf("state log checksum mismatch at load")
		}
		lastData = data
		lastSeq = hdr.Seq
	}
	return lastData, lastSeq, nil
}

func fsyncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// frameRec is one validated frame found during recovery.
type frameRec struct {
	hdr  frameHeader
	data []byte
}

// recover validates the frame log and returns the newest frame whose data
// parses, checksums match, and every referenced blob exists. Anything at or
// after the first bad frame is quarantined.
func (s *Store) recover() ([]Note, error) {
	var notes []Note
	path := filepath.Join(s.dir, stateFile)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return notes, nil
	}
	if err != nil {
		return notes, err
	}
	if len(raw) == 0 {
		return notes, nil
	}
	lines := strings.SplitAfter(string(raw), "\n")
	// data lines contain embedded JSON without newlines (json.Marshal
	// escapes newlines), so frames are exactly two physical lines.
	var frames []frameRec
	badAt := -1
	pos := 0
	idx := 0
	for pos < len(lines) {
		hdrLine := strings.TrimRight(lines[pos], "\n")
		pos++
		if hdrLine == "" {
			continue
		}
		var hdr frameHeader
		if jerr := json.Unmarshal([]byte(hdrLine), &hdr); jerr != nil {
			badAt = pos - 1
			notes = append(notes, Note{Level: "error", Detail: fmt.Sprintf("frame %d header unreadable: %v", idx, jerr)})
			break
		}
		if pos >= len(lines) {
			badAt = pos - 1
			notes = append(notes, Note{Level: "error", Detail: fmt.Sprintf("frame %d data line missing (torn tail)", idx)})
			break
		}
		dataLine := strings.TrimRight(lines[pos], "\n")
		pos++
		if hdr.Bytes != len(dataLine) {
			badAt = pos - 2
			notes = append(notes, Note{Level: "error", Detail: fmt.Sprintf("frame %d declared %d bytes, found %d", idx, hdr.Bytes, len(dataLine))})
			break
		}
		sum := sha256.Sum256([]byte(dataLine))
		if hex.EncodeToString(sum[:]) != hdr.SHA {
			badAt = pos - 2
			notes = append(notes, Note{Level: "error", Detail: fmt.Sprintf("frame %d checksum mismatch", idx)})
			break
		}
		var probe map[string]any
		if jerr := json.Unmarshal([]byte(dataLine), &probe); jerr != nil {
			badAt = pos - 2
			notes = append(notes, Note{Level: "error", Detail: fmt.Sprintf("frame %d payload not valid JSON: %v", idx, jerr)})
			break
		}
		frames = append(frames, frameRec{hdr: hdr, data: []byte(dataLine)})
		idx++
	}

	// Pick newest frame whose referenced blobs all exist.
	chosen := -1
	for i := len(frames) - 1; i >= 0; i-- {
		missing := missingBlobs(s, frames[i].data)
		if len(missing) == 0 {
			chosen = i
			break
		}
		notes = append(notes, Note{Level: "warning", Detail: fmt.Sprintf("frame seq=%d unusable, missing blobs %v; rolling back", frames[i].hdr.Seq, missing)})
	}

	goodBytes := 0
	for i := 0; i <= chosen; i++ {
		hb, _ := json.Marshal(frames[i].hdr)
		goodBytes += len(hb) + 1 + frames[i].hdr.Bytes + 1
	}

	quarantine := false
	if badAt >= 0 {
		quarantine = true
	} else if chosen < len(frames)-1 {
		quarantine = true
	} else if goodBytes != len(raw) {
		quarantine = true
	}
	if quarantine {
		if err := s.quarantine(raw, goodBytes); err != nil {
			notes = append(notes, Note{Level: "error", Detail: "failed to quarantine tail: " + err.Error()})
		} else {
			notes = append(notes, Note{Level: "warning", Detail: fmt.Sprintf("recovered to frame seq=%d; invalid tail quarantined", chosenSeq(frames, chosen))})
		}
	}
	if len(frames) > 0 && chosen >= 0 && chosen < len(frames)-1 {
		// Rewrite clean log up to chosen frame.
		var clean []byte
		for i := 0; i <= chosen; i++ {
			hb, _ := json.Marshal(frames[i].hdr)
			clean = append(clean, hb...)
			clean = append(clean, '\n')
			clean = append(clean, frames[i].data...)
			clean = append(clean, '\n')
		}
		if err := os.WriteFile(filepath.Join(s.dir, stateFile), clean, 0o644); err != nil {
			return notes, err
		}
	}
	sort.SliceStable(notes, func(i, j int) bool { return notes[i].Level < notes[j].Level })
	return notes, nil
}

func chosenSeq(frames []frameRec, chosen int) int64 {
	if chosen < 0 {
		return 0
	}
	return frames[chosen].hdr.Seq
}

func missingBlobs(s *Store, data []byte) []string {
	var d blobRefs
	if err := json.Unmarshal(data, &d); err != nil {
		return []string{"<state-json>"}
	}
	var missing []string
	for _, id := range allBlobRefs(d) {
		if !s.HasBlob(id) {
			missing = append(missing, shortenID(id))
		}
	}
	sort.Strings(missing)
	return missing
}

func shortenID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

type blobRefs struct {
	Baseline *struct {
		RawBlob string `json:"raw_blob"`
	} `json:"baseline"`
	Langs map[string]*struct {
		RawBlob string `json:"raw_blob"`
	} `json:"langs"`
	Uploads []struct {
		Blob string `json:"blob"`
	} `json:"uploads"`
	Certs []struct {
		Blob string `json:"blob"`
	} `json:"certs"`
}

func allBlobRefs(d blobRefs) []string {
	need := map[string]bool{}
	if d.Baseline != nil && d.Baseline.RawBlob != "" {
		need[d.Baseline.RawBlob] = true
	}
	for _, l := range d.Langs {
		if l != nil && l.RawBlob != "" {
			need[l.RawBlob] = true
		}
	}
	for _, u := range d.Uploads {
		if u.Blob != "" {
			need[u.Blob] = true
		}
	}
	for _, c := range d.Certs {
		if c.Blob != "" {
			need[c.Blob] = true
		}
	}
	out := make([]string, 0, len(need))
	for id := range need {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func (s *Store) quarantine(raw []byte, good int) error {
	if good >= len(raw) {
		return nil
	}
	bad := raw[good:]
	if len(strings.TrimSpace(string(bad))) == 0 {
		return nil
	}
	dir := filepath.Join(s.dir, "quarantine")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	name := fmt.Sprintf("state-tail-%d.jsonl", lenMust(filepath.Glob(filepath.Join(dir, "state-tail-*.jsonl")))+1)
	if err := os.WriteFile(filepath.Join(dir, name), bad, 0o644); err != nil {
		return err
	}
	return nil
}

func lenMust(xs []string, err error) int {
	if err != nil {
		return 0
	}
	return len(xs)
}
