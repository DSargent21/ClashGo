// A rolling frame store: the memory of the seeing system.
//
// One screenshot answers "what is on screen now". Debugging a bot that runs
// for minutes needs "what happened, in order" — which popup appeared when,
// whether a tap changed anything, what the screen looked like at the moment a
// state went wrong. The store keeps captured frames on disk beside a
// JSON-lines index recording, per frame, the classifier's verdict, a
// caller-supplied label, and how much the frame changed from its
// predecessor. That last number is the cheapest possible answer to "did my
// tap land": if a tap was supposed to dismiss a dialog, the frame changes; if
// it silently missed, the next frame is near-identical.
package vision

import (
	"bufio"
	"encoding/json"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gocv.io/x/gocv"
)

// DefaultStoreDir is where recordings live unless the caller says otherwise.
// Project-local and disposable: recordings are debugging output, not data.
const DefaultStoreDir = "tmp/see"

// FrameMeta is the per-frame annotation a caller supplies when recording.
type FrameMeta struct {
	State string
	Score int
	Label string
	Note  string
}

// FrameEntry is one recorded frame as stored in the index.
type FrameEntry struct {
	ID         int     `json:"id"`
	Time       string  `json:"ts"`
	File       string  `json:"file"`
	W          int     `json:"w"`
	H          int     `json:"h"`
	State      string  `json:"state"`
	Score      int     `json:"score"`
	Label      string  `json:"label"`
	Note       string  `json:"note,omitempty"`
	ChangedPct float64 `json:"changed_pct"`
}

// Line renders the one-line timeline row for this frame.
func (e FrameEntry) Line() string {
	label := e.Label
	if label == "" {
		label = "-"
	}
	return fmt.Sprintf("%5d  %s  %-14s %4d  %6.2f%%  %-12s %s",
		e.ID, e.Time, e.State, e.Score, e.ChangedPct, label, e.Note)
}

// FrameStore is an append-only recording directory.
type FrameStore struct {
	dir string

	// minFree is the headroom the store keeps free on its volume; a write that
	// would cross it fails with ErrLowDisk instead (see diskspace.go). Zero
	// disables the check.
	minFree uint64
	// freeFn is the free-space probe. It is a field so tests can pin the
	// volume's headroom without touching the real disk; nil means DiskFreeBytes.
	freeFn func(dir string) (uint64, error)
}

// OpenFrameStore prepares a recording directory (creating it if needed).
func OpenFrameStore(dir string) (*FrameStore, error) {
	if strings.TrimSpace(dir) == "" {
		dir = DefaultStoreDir
	}
	if err := os.MkdirAll(filepath.Join(dir, "frames"), 0o755); err != nil {
		return nil, fmt.Errorf("create frame store %s: %w", dir, err)
	}
	return &FrameStore{dir: dir, minFree: DefaultMinFreeBytes, freeFn: DiskFreeBytes}, nil
}

// Dir is the store's root directory.
func (s *FrameStore) Dir() string { return s.dir }

// SetMinFreeBytes sets the volume headroom the store insists on keeping. Zero
// disables the low-disk check; a recording tool should only do that when the
// operator explicitly asked for it.
func (s *FrameStore) SetMinFreeBytes(n uint64) { s.minFree = n }

// MinFreeBytes is the configured headroom floor.
func (s *FrameStore) MinFreeBytes() uint64 { return s.minFree }

// FreeBytes reports the free space on the store's volume.
func (s *FrameStore) FreeBytes() (uint64, error) {
	fn := s.freeFn
	if fn == nil {
		fn = DiskFreeBytes
	}
	return fn(s.dir)
}

func (s *FrameStore) indexPath() string { return filepath.Join(s.dir, "index.jsonl") }

// Entries reads the whole index, skipping malformed lines rather than failing:
// a half-written line from an interrupted run must not make the history
// unreadable.
func (s *FrameStore) Entries() ([]FrameEntry, error) {
	fh, err := os.Open(s.indexPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	var out []FrameEntry
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e FrameEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// Recent returns the last n entries in chronological order (all of them when
// n <= 0).
func (s *FrameStore) Recent(n int) ([]FrameEntry, error) {
	all, err := s.Entries()
	if err != nil {
		return nil, err
	}
	if n > 0 && len(all) > n {
		all = all[len(all)-n:]
	}
	return all, nil
}

// Last returns the most recent entry, or nil when the store is empty.
func (s *FrameStore) Last() (*FrameEntry, error) {
	all, err := s.Entries()
	if err != nil || len(all) == 0 {
		return nil, err
	}
	e := all[len(all)-1]
	return &e, nil
}

// ByID looks up a single recorded frame.
func (s *FrameStore) ByID(id int) (*FrameEntry, error) {
	all, err := s.Entries()
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].ID == id {
			return &all[i], nil
		}
	}
	return nil, fmt.Errorf("no recorded frame %d in %s", id, s.dir)
}

// Add writes a frame and appends its index entry, computing the change
// percentage against the previous frame. It returns the stored entry.
//
// It fails with ErrLowDisk (writing nothing) when the volume is at or below the
// store's free-space floor: a recorder that keeps writing on a filling disk is
// how a review store turns into a machine-wide outage.
func (s *FrameStore) Add(f Frame, meta FrameMeta) (FrameEntry, error) {
	if f.Empty() {
		return FrameEntry{}, fmt.Errorf("refusing to record an empty frame")
	}
	if free, err := s.FreeBytes(); err == nil && s.minFree > 0 && free < s.minFree {
		return FrameEntry{}, fmt.Errorf("%w (%d MiB free in %s, floor %d MiB)",
			ErrLowDisk, free>>20, s.dir, s.minFree>>20)
	}
	prev, err := s.Last()
	if err != nil {
		return FrameEntry{}, err
	}
	var changedPct float64
	if prev != nil {
		if pf, err := s.Load(*prev); err == nil {
			changedPct = Diff(pf, f, DefaultDiffThreshold).Pct
		}
	}
	id := 1
	if prev != nil {
		id = prev.ID + 1
	}
	now := time.Now()
	name := fmt.Sprintf("%05d_%s_%s.png", id, now.Format("150405"), sanitizeLabel(meta.Label))
	rel := filepath.Join("frames", name)
	if err := WritePNG(filepath.Join(s.dir, rel), f); err != nil {
		return FrameEntry{}, err
	}
	entry := FrameEntry{
		ID: id, Time: now.Format("15:04:05"), File: rel,
		W: f.W, H: f.H,
		State: meta.State, Score: meta.Score, Label: meta.Label, Note: meta.Note,
		ChangedPct: changedPct,
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return FrameEntry{}, err
	}
	fh, err := os.OpenFile(s.indexPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return FrameEntry{}, err
	}
	defer fh.Close()
	if _, err := fh.Write(append(line, '\n')); err != nil {
		return FrameEntry{}, err
	}
	return entry, nil
}

// Load reads a recorded frame back into memory.
func (s *FrameStore) Load(e FrameEntry) (Frame, error) {
	path := e.File
	if !filepath.IsAbs(path) {
		path = filepath.Join(s.dir, e.File)
	}
	return LoadPNG(path)
}

// LoadPNG reads any screenshot into a Frame.
func LoadPNG(path string) (Frame, error) {
	m := gocv.IMRead(path, gocv.IMReadColor)
	if m.Empty() {
		return Frame{}, fmt.Errorf("cannot read image %s", path)
	}
	defer m.Close()
	return FrameFromMat(m).Copy(), nil
}

// Prune keeps only the newest `keep` frames, deleting the rest so a long
// recording session cannot fill the disk.
func (s *FrameStore) Prune(keep int) (int, error) {
	if keep <= 0 {
		return 0, nil
	}
	all, err := s.Entries()
	if err != nil {
		return 0, err
	}
	if len(all) <= keep {
		return 0, nil
	}
	drop, remain := all[:len(all)-keep], all[len(all)-keep:]
	for _, e := range drop {
		os.Remove(filepath.Join(s.dir, e.File))
	}
	fh, err := os.Create(s.indexPath())
	if err != nil {
		return 0, err
	}
	defer fh.Close()
	w := bufio.NewWriter(fh)
	for _, e := range remain {
		line, err := json.Marshal(e)
		if err != nil {
			continue
		}
		w.Write(line)
		w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		return 0, err
	}
	return len(drop), nil
}

// Copy repacks a frame into a contiguous buffer, dropping any row padding.
// It is what makes a Frame safe to keep after its Mat is released, and what
// PNG writing needs.
func (f Frame) Copy() Frame {
	out := Frame{W: f.W, H: f.H, Stride: f.W * 3, Pix: make([]byte, f.W*3*f.H)}
	if f.Empty() {
		return out
	}
	for y := 0; y < f.H; y++ {
		copy(out.Pix[y*out.Stride:(y+1)*out.Stride], f.Pix[y*f.Stride:y*f.Stride+f.W*3])
	}
	return out
}

// Crop returns a sub-frame as a contiguous copy. Rectangles outside the frame
// are clipped.
func (f Frame) Crop(r image.Rectangle) Frame {
	r = r.Intersect(image.Rect(0, 0, f.W, f.H))
	if r.Empty() {
		return Frame{}
	}
	out := Frame{W: r.Dx(), H: r.Dy(), Stride: r.Dx() * 3, Pix: make([]byte, r.Dx()*3*r.Dy())}
	for y := 0; y < r.Dy(); y++ {
		src := (r.Min.Y+y)*f.Stride + r.Min.X*3
		copy(out.Pix[y*out.Stride:(y+1)*out.Stride], f.Pix[src:src+r.Dx()*3])
	}
	return out
}

// WritePNG encodes a frame to disk.
func WritePNG(path string, f Frame) error {
	c := f.Copy()
	m, err := gocv.NewMatFromBytes(c.H, c.W, gocv.MatTypeCV8UC3, c.Pix)
	if err != nil {
		return fmt.Errorf("mat from frame: %w", err)
	}
	defer m.Close()
	if !gocv.IMWrite(path, m) {
		return fmt.Errorf("cannot write %s", path)
	}
	return nil
}

// sanitizeLabel keeps a label safe for a filename.
func sanitizeLabel(s string) string {
	if s == "" {
		return "frame"
	}
	var sb strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			sb.WriteRune(r)
		default:
			sb.WriteByte('-')
		}
	}
	out := strings.Trim(sb.String(), "-")
	if out == "" {
		return "frame"
	}
	if len(out) > 24 {
		out = out[:24]
	}
	return out
}

// Timeline renders a table of recorded frames, oldest first.
func Timeline(entries []FrameEntry) string {
	if len(entries) == 0 {
		return "(no recorded frames)\n"
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%5s  %-8s  %-14s %4s  %7s  %-12s %s\n",
		"id", "time", "state", "score", "changed", "label", "note"))
	for _, e := range entries {
		sb.WriteString(e.Line())
		sb.WriteByte('\n')
	}
	return sb.String()
}

// SortEntries orders entries by ID ascending (the index is append-ordered,
// but a hand-edited file should not confuse the reader).
func SortEntries(entries []FrameEntry) []FrameEntry {
	out := append([]FrameEntry(nil), entries...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
