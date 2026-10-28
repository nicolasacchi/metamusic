// Package tapetest is TEST-ONLY: fakes for the tape service's collaborators so the API, the rigs and lane T2 run without
// ffmpeg or the flac encoder. Import it only from _test.go files and servertest (see TestNoTestHelpersInBinary).
package tapetest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/nicolasacchi/metamusic/internal/media/flacmeta/flactest"
	"github.com/nicolasacchi/metamusic/internal/tape"
)

// ErrNoSnapshot is what FakeSource answers for a Record it has no snapshot for.
var ErrNoSnapshot = errors.New("tapetest: no snapshot for this record")

// FakePipeline is a tape.Pipeline that renders no audio: it writes a small, header-valid and deterministic FLAC (STREAMINFO
// with the planned sample count, a VORBIS_COMMENT with the job's tags and chapter names, no frames) into Dir and reports the
// chapters the cumulative sample counts imply, at tape.Rate. The same Job gives the same bytes. It is safe for concurrent use.
type FakePipeline struct {
	// Dir receives the rendered files; each Render makes a fresh sub-directory that Result.Cleanup removes.
	Dir string
	// Err, when set, makes Render fail with it (a failed tape).
	Err error
	// Progress lists the phases reported, in order (default: encode, verify).
	Phases []string

	mu   sync.Mutex
	n    int
	jobs []tape.Job
}

// NewFakePipeline returns a FakePipeline writing under dir.
func NewFakePipeline(dir string) *FakePipeline { return &FakePipeline{Dir: dir} }

// Jobs returns the jobs rendered so far, oldest first.
func (p *FakePipeline) Jobs() []tape.Job {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]tape.Job(nil), p.jobs...)
}

// Render implements tape.Pipeline.
func (p *FakePipeline) Render(ctx context.Context, j tape.Job, progress func(tape.Progress)) (tape.Result, error) {
	p.mu.Lock()
	p.n++
	n := p.n
	p.jobs = append(p.jobs, j)
	p.mu.Unlock()
	if p.Err != nil {
		return tape.Result{}, p.Err
	}
	if err := ctx.Err(); err != nil {
		return tape.Result{}, err
	}
	phases := p.Phases
	if len(phases) == 0 {
		phases = []string{"encode", "verify"}
	}
	for _, ph := range phases {
		if progress != nil {
			progress(tape.Progress{Phase: ph, Item: len(j.Items), Items: len(j.Items)})
		}
	}
	var chapters []tape.Chapter
	var total int64
	for i, it := range j.Items {
		samples := int64(it.Item.DurationS*tape.Rate + 0.5)
		chapters = append(chapters, tape.Chapter{Index: i + 1, SlotID: it.Item.SlotID, TakeID: it.Item.TakeID, StartSample: total, Samples: samples, StartMS: total * 1000 / tape.Rate})
		total += samples
	}
	comments := make([]string, 0, len(j.Tags)+len(j.Chapters))
	for _, c := range j.Tags {
		comments = append(comments, c.Key+"="+c.Value)
	}
	for i, name := range j.Chapters {
		comments = append(comments, fmt.Sprintf("CHAPTER%03dNAME=%s", i+1, name))
	}
	body := flactest.Build(flactest.Spec{SampleRate: tape.Rate, Channels: 2, BitsPerSample: tape.Bits, TotalSamples: uint64(total),
		Vendor: "tapetest", Comments: comments, SeekPoints: 1, PaddingBytes: 0})
	dir, err := os.MkdirTemp(p.Dir, fmt.Sprintf("tape-%d-", n))
	if err != nil {
		return tape.Result{}, err
	}
	path := filepath.Join(dir, "final.flac")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return tape.Result{}, err
	}
	sum := sha256.Sum256(body)
	return tape.Result{Path: path, SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(body)), TotalSamples: total, Chapters: chapters,
		PeakSample: 0.5, Cleanup: func() { _ = os.RemoveAll(dir) }}, nil
}

// FakeSource is a tape.Source over fixed snapshots, keyed by Record id.
type FakeSource struct {
	mu    sync.Mutex
	snaps map[string]tape.Snapshot
	// Calls counts Snapshot calls.
	Calls int
}

// NewFakeSource returns an empty FakeSource.
func NewFakeSource() *FakeSource { return &FakeSource{snaps: map[string]tape.Snapshot{}} }

// Set stores the snapshot returned for its RecordID.
func (s *FakeSource) Set(snap tape.Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snaps[snap.RecordID] = snap
}

// Snapshot implements tape.Source.
func (s *FakeSource) Snapshot(_ context.Context, recordID string, _ tape.SnapOptions) (tape.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Calls++
	snap, ok := s.snaps[recordID]
	if !ok {
		return tape.Snapshot{}, ErrNoSnapshot
	}
	return snap, nil
}

// FakeLoudness is a tape.Loudness over fixed measures, keyed by Take id; an unlisted Take is unmeasured.
type FakeLoudness struct {
	mu sync.Mutex
	m  map[string]tape.Measure
}

// NewFakeLoudness returns an empty FakeLoudness.
func NewFakeLoudness() *FakeLoudness { return &FakeLoudness{m: map[string]tape.Measure{}} }

// Set lists the measure of a Take.
func (l *FakeLoudness) Set(takeID string, m tape.Measure) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.m[takeID] = m
}

// Ensure implements tape.Loudness.
func (l *FakeLoudness) Ensure(_ context.Context, takeIDs []string) (map[string]tape.Measure, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[string]tape.Measure, len(takeIDs))
	for _, id := range takeIDs {
		if m, ok := l.m[id]; ok {
			out[id] = m
		}
	}
	return out, nil
}
