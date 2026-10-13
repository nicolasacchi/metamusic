package importer

import (
	"context"
	"fmt"
	"io"
	"strings"

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
