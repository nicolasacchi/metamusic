package importer

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/media/ingest"
)

// IngestLossy is the fake of ingest.Pipeline.IngestLossy (M2c): it records the request next to the
// FLAC ones, honours the fail hook and the delay, and answers the lossy result shape of C60
// (master = source bytes, no MP3 rendition, peaks only, zero audio_md5).
func (f *fakeIngester) IngestLossy(ctx context.Context, r ingest.Request) (ingest.Result, error) {
	f.mu.Lock()
	f.requests = append(f.requests, r)
	f.running++
	f.peak = max(f.peak, f.running)
	fail := f.fail
	f.mu.Unlock()
	defer func() { f.mu.Lock(); f.running--; f.mu.Unlock() }()
	if len(r.Artifacts) > 0 {
		return ingest.Result{}, fmt.Errorf("lossy ingest takes no artifacts")
	}
	if fail != nil {
		if err := fail(r); err != nil {
			return ingest.Result{}, err
		}
	}
	n, err := io.Copy(io.Discard, r.Source)
	if err != nil {
		return ingest.Result{}, err
	}
	tail := r.TrackID[len(r.TrackID)-12:]
	master := fmt.Sprintf("audio/masters/%s__%d__%s.mp3", r.Slug, r.Seed, tail)
	pksha := shaOf("peaks:" + r.SourceSHA256)
	return ingest.Result{
		MasterRelPath: master, MasterSHA256: r.SourceSHA256, FramesSHA256: r.SourceSHA256, AudioMD5: strings.Repeat("0", 32), MasterBytes: n,
		Peaks:       ingest.Rendition{Kind: "peaks", RelPath: fmt.Sprintf("derived/%s/%s.peaks.json", pksha[:2], pksha), SHA256: pksha, Bytes: 50, Params: map[string]any{"format": "mm.peaks/1"}},
		ToolVersion: "ffmpeg fake", Created: []string{master},
		Lossy: &ingest.LossyInfo{SampleRate: 44100, Channels: 2, TotalSamples: 88200, DurationS: 2.0},
	}, nil
}

// mp3Bytes is a minimal valid MPEG-1 layer III stream: three 128 kbps / 44.1 kHz stereo frame headers
// (417 bytes each) whose padding bytes depend on n, so different n give different audio identities.
func mp3Bytes(n int) string {
	frame := make([]byte, 417)
	copy(frame, []byte{0xFF, 0xFB, 0x90, 0x00})
	for i := 4; i < len(frame); i++ {
		frame[i] = byte(n + i)
	}
	return string(frame) + string(frame) + string(frame)
}

// mp3 writes a settled standalone MP3 with a valid header.
func (e *env) mp3(rel string, n int) { e.t.Helper(); e.put(rel, mp3Bytes(n)) }

// trackBy returns the stored track whose slug is slug (the first by recording time).
func (e *env) trackBySlug(slug string) trackRow {
	e.t.Helper()
	for _, tr := range e.tracks() {
		if tr.Slug == slug {
			return tr
		}
	}
	e.t.Fatalf("no track with slug %q", slug)
	return trackRow{}
}

// mappingTemplate renders mapping.todo.csv for inv.
func mappingTemplate(t *testing.T, inv *Inventory) string {
	t.Helper()
	var sb strings.Builder
	if err := WriteMappingTemplate(&sb, inv); err != nil {
		t.Fatal(err)
	}
	return sb.String()
}

func planHas(inv *Inventory, sub string) bool {
	for _, p := range inv.PlanEntries() {
		if strings.Contains(p, sub) {
			return true
		}
	}
	return false
}

func withRefFlags(unmatched, lossy bool) func(*Options) {
	return func(o *Options) { o.ReferenceUnmatched, o.ReferenceLossy = unmatched, lossy }
}
