package librarytest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Fixed ids of the M2c rows (C66).
const (
	TrackRefFlac = "01900000-0000-7000-8000-000000000106" // reference FLAC: stub recipe, lossless master, mp3_320 + peaks
	TrackLossy   = "01900000-0000-7000-8000-000000000107" // lossy reference: MP3 master, peaks only
	RefFlacTitle = "Synthetic Reference"
	LossyTitle   = "Synthetic Lossy"
	// LossyDuration and LossyBytes give the lossy fixture master a bitrate of round(bytes*8/dur/1000) = 128 kbps.
	LossyDuration = 8.0
	lossyBytes    = 128000 // 8 s at 128 kbps
)

type refFixture struct {
	id, title, audioKey string
	lossy               bool
}

var refFixtures = []refFixture{
	{TrackRefFlac, RefFlacTitle, "fixture-ref-flac", false},
	{TrackLossy, LossyTitle, "mp3:fixture-lossy", true},
}

// refFiles derives the deterministic files of reference fixture i.
func refFiles(i int, r refFixture) (master, mp3, peaks fxFile) {
	pk := analysis.Peaks{DurationS: 6,
		Overview: analysis.Waveform{Version: 2, Channels: 1, SampleRate: 48000, SamplesPerPixel: 141, Bits: 8, Length: 4, Data: []int8{-64, 64, -32, 32, -16, 16, -8, 8}},
		Detail:   analysis.Waveform{Version: 2, Channels: 1, SampleRate: 48000, SamplesPerPixel: 2400, Bits: 8, Length: 2, Data: []int8{-100, 100, -50, 50}}}
	pb, _ := json.Marshal(pk)
	pb = append(pb, '\n')
	ks := sha(pb)
	peaks = fxFile{fmt.Sprintf("derived/%s/%s.peaks.json", ks[:2], ks), pb}
	if r.lossy {
		// bytes are a stand-in for MP3 data: tests that parse them build their own.
		data := make([]byte, lossyBytes)
		copy(data, fmt.Sprintf("fixture-lossy-master-%d", i))
		return fxFile{"audio/masters/synthetic_lossy__0__20261001-1200.mp3", data}, fxFile{}, peaks
	}
	m := []byte(fmt.Sprintf("fixture-ref-master-%d", i))
	p := []byte(fmt.Sprintf("fixture-ref-mp3-%d", i))
	ps := sha(p)
	return fxFile{"audio/masters/synthetic_reference__0__20261001-1200.flac", m}, fxFile{fmt.Sprintf("derived/%s/%s.mp3", ps[:2], ps), p}, peaks
}

func writeRefFiles(tb testing.TB, dataDir string) {
	tb.Helper()
	for i, r := range refFixtures {
		m, p, k := refFiles(i, r)
		for _, f := range []fxFile{m, p, k} {
			if f.path == "" {
				continue
			}
			full := filepath.Join(dataDir, f.path)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				tb.Fatal(err)
			}
			if err := os.WriteFile(full, f.data, 0o644); err != nil {
				tb.Fatal(err)
			}
		}
	}
}

// addM2c inserts the two reference tracks (a reference FLAC and a lossy
// reference) through the real write path.
func addM2c(tb testing.TB, st *store.Store, now time.Time, fx Fixtures) Fixtures {
	tb.Helper()
	ctx := context.Background()
	n := 0x900
	svc := library.New(st, nil, func() time.Time { return now })
	svc.UseIDFunc(func() string { n++; return fmt.Sprintf("01900000-0000-7000-8000-%012x", n) })
	for i, r := range refFixtures {
		tx := begin(tb, st)
		slug := yue2.Slug(r.title)
		rid, _, err := svc.InsertReferenceRecipe(ctx, tx, library.NewReferenceRecipe{Title: r.title, Slug: slug, AudioKey: r.audioKey})
		check(tb, err)
		fx.Recipes = append(fx.Recipes, rid)
		master, mp3, peaks := refFiles(i, r)
		msha := sha(master.data)
		nt := library.NewTake{
			ID: r.id, Title: r.title, Slug: slug, RecipeID: rid, Reference: true, ImportRule: library.RuleReference, PrincipalID: "cli",
			DurationS: 6, SampleRate: 48000, BitsPerSample: 24, Channels: 2, TotalSamples: 6 * 48000,
			MasterPath: master.path, MasterSHA256: msha, MasterBytes: int64(len(master.data)),
			SourceSHA256: sha([]byte(fmt.Sprintf("fixture-ref-source-%d", i))), FramesSHA256: sha([]byte(fmt.Sprintf("fixture-ref-frames-%d", i))),
			AudioMD5: fmt.Sprintf("%032x", 0x100+i), ImportRunID: RunID, RecordedAt: now.Add(-12 * time.Hour),
			Loudness:    analysis.Loudness{IntegratedLUFS: -13.5, LRA: 4, LRALowLUFS: -18, LRAHighLUFS: -14, TruePeakDBTP: -1.0, SamplePeakDBFS: -1.2, ShortTermMaxLUFS: -12, ShortTerm1Hz: []float64{-13.5, -13}},
			Spectrum:    analysis.Spectrum{BandwidthHz: bptrInt(16000), RefDB: -40, ThresholdDB: -110, Frames: 30},
			ToolVersion: "ffmpeg n9.0.1", ReplayGainDB: -4.5,
			Renditions: []library.NewRendition{{Kind: "peaks", SHA256: sha(peaks.data), Bytes: int64(len(peaks.data)), Path: peaks.path, Params: map[string]any{"format": analysis.PeaksFormat}}},
		}
		if r.lossy {
			nt.Lossy = true
			nt.DurationS, nt.SampleRate, nt.BitsPerSample, nt.Channels = LossyDuration, 44100, 16, 2
			nt.TotalSamples = int64(LossyDuration * 44100)
			nt.SourceSHA256, nt.FramesSHA256, nt.AudioMD5 = msha, msha, "00000000000000000000000000000000"
			nt.Spectrum.BandwidthHz = bptrInt(16000)
		} else {
			nt.Renditions = append(nt.Renditions, library.NewRendition{Kind: "mp3_320", SHA256: sha(mp3.data), Bytes: int64(len(mp3.data)), Path: mp3.path,
				Params: map[string]any{"encoder": "libmp3lame", "bitrate": "320k", "sample_rate": 48000, "gain_db": -0.6, "ffmpeg": "n9.0.1"}})
		}
		check(tb, svc.InsertTake(ctx, tx, nt))
		check(tb, tx.Commit())
		fx.Tracks = append(fx.Tracks, r.id)
	}
	return fx
}

// ApplyM2c adds the two M2c rows to a library that already holds Fixture's rows (servertest uses it).
func ApplyM2c(tb testing.TB, st *store.Store, now time.Time) {
	tb.Helper()
	addM2c(tb, st, now, Fixtures{})
}

func bptrInt(v int) *int { return &v }
