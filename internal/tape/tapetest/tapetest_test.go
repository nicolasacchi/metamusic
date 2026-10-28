package tapetest_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/tape"
	"github.com/nicolasacchi/metamusic/internal/tape/tapetest"
)

func job() tape.Job {
	return tape.Job{
		Items: []tape.PlannedItem{
			{Item: tape.Item{SlotID: "s1", TakeID: "t1", DurationS: 2}},
			{Item: tape.Item{SlotID: "s2", TakeID: "t2", DurationS: 0.5}},
		},
		Chapters: []string{"One", "Two [lossy source]"},
	}
}

// TestFakePipeline_ParseableAndDeterministic: the fake file is a FLAC flacmeta reads, with the planned sample count, and
// the same job gives the same bytes; the chapters follow the cumulative counts.
func TestFakePipeline_ParseableAndDeterministic(t *testing.T) {
	t.Parallel()
	p := tapetest.NewFakePipeline(t.TempDir())
	var phases []string
	a, err := p.Render(context.Background(), job(), func(pr tape.Progress) { phases = append(phases, pr.Phase) })
	if err != nil {
		t.Fatal(err)
	}
	defer a.Cleanup()
	b, err := p.Render(context.Background(), job(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Cleanup()
	if a.SHA256 != b.SHA256 || a.Bytes != b.Bytes {
		t.Errorf("the same job rendered two files: %s vs %s", a.SHA256, b.SHA256)
	}
	body, err := os.ReadFile(a.Path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := flacmeta.Read(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("flacmeta.Read: %v", err)
	}
	want := uint64(2*tape.Rate + tape.Rate/2)
	if info.StreamInfo.TotalSamples != want || info.StreamInfo.SampleRate != tape.Rate || info.StreamInfo.BitsPerSample != tape.Bits || info.StreamInfo.Channels != 2 {
		t.Errorf("streaminfo = %+v, want %d samples at 48000/24/2", info.StreamInfo, want)
	}
	if int64(want) != a.TotalSamples || len(a.Chapters) != 2 || a.Chapters[1].StartSample != 2*tape.Rate || a.Chapters[1].StartMS != 2000 {
		t.Errorf("result = %d samples, chapters %+v", a.TotalSamples, a.Chapters)
	}
	if !slices.Contains(info.Comments, "CHAPTER002NAME=Two [lossy source]") {
		t.Errorf("comments = %v", info.Comments)
	}
	if len(phases) == 0 || len(p.Jobs()) != 2 {
		t.Errorf("phases %v, jobs %d", phases, len(p.Jobs()))
	}
	a.Cleanup()
	if _, err := os.Stat(a.Path); !os.IsNotExist(err) {
		t.Error("Cleanup must remove the file")
	}
}

// TestFakePipeline_ErrAndCancel: a configured error and a cancelled context fail the render without a file.
func TestFakePipeline_ErrAndCancel(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	p := &tapetest.FakePipeline{Dir: t.TempDir(), Err: boom}
	if _, err := p.Render(context.Background(), job(), nil); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tapetest.NewFakePipeline(t.TempDir()).Render(ctx, job(), nil); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled render: %v", err)
	}
}

// TestFakeSourceAndLoudness: fixed answers; an unknown Record has no snapshot and an unlisted Take is unmeasured.
func TestFakeSourceAndLoudness(t *testing.T) {
	t.Parallel()
	s := tapetest.NewFakeSource()
	s.Set(tape.Snapshot{RecordID: "r1", Title: "Synthetic Record 001"})
	if snap, err := s.Snapshot(context.Background(), "r1", tape.SnapOptions{}); err != nil || snap.Title != "Synthetic Record 001" {
		t.Errorf("snapshot = %+v, %v", snap, err)
	}
	if _, err := s.Snapshot(context.Background(), "nope", tape.SnapOptions{}); !errors.Is(err, tapetest.ErrNoSnapshot) {
		t.Errorf("unknown record: %v", err)
	}
	l := tapetest.NewFakeLoudness()
	l.Set("t1", tape.Measure{LUFS: -14, TruePeakDBTP: -3, OK: true})
	m, err := l.Ensure(context.Background(), []string{"t1", "t2"})
	if err != nil || len(m) != 1 || !m["t1"].OK {
		t.Errorf("measures = %+v, %v", m, err)
	}
}
