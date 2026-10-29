package tape_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/analysis/ffmpeg"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/cover"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/tape"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

// realRig is the media-tier setup: the synthetic sources of FixtureL2Audio, their measured levels, a real mosaic cover in the
// data directory and a Job over all of them.
type realRig struct {
	audio  librarytest.L2Audio
	tools  mediatool.Tools
	root   *os.Root
	an     *ffmpeg.FFmpeg
	items  []tape.PlannedItem
	plans  []tape.GainPlan
	target float64
	job    tape.Job
	coverP []byte
}

func newRealRig(t *testing.T, order ...string) *realRig {
	t.Helper()
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	tools := testtools.Tools()
	a := librarytest.FixtureL2Audio(t, tools)
	root, err := os.OpenRoot(a.Dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	an, err := ffmpeg.New(context.Background(), tools)
	if err != nil {
		t.Fatal(err)
	}
	r := &realRig{audio: a, tools: tools, root: root, an: an}
	var ms []tape.Measure
	for _, n := range order {
		s := a.Source(t, n)
		l, err := an.Loudness(context.Background(), a.Abs(s))
		if err != nil {
			t.Fatal(err)
		}
		ms = append(ms, tape.Measure{LUFS: l.IntegratedLUFS, TruePeakDBTP: l.TruePeakDBTP, OK: true})
		r.items = append(r.items, tape.PlannedItem{
			Item: tape.Item{SlotID: "slot-" + n, TakeID: "take-" + n, Position: len(r.items) + 1, Title: "Title " + n, MasterRel: s.Rel, MasterSHA256: s.SHA256,
				MasterBytes: s.Bytes, SampleRate: s.Rate, Bits: s.Bits, Channels: s.Channels, Lossy: s.Lossy, DurationS: s.Seconds, Measure: ms[len(ms)-1]},
			Resampled: s.Rate != 48000, Upmixed: s.Channels == 1,
		})
	}
	r.target, r.plans = tape.PlanGains(ms, nil)
	for i := range r.items {
		r.items[i].Gain = r.plans[i]
	}
	// a real mosaic through the cover service, so the pipeline reads the file where the service wrote it
	in := cover.Input{Kind: cover.KindRecord, ID: "rec-1", Seed: 7, Parts: []cover.Part{{RootID: "r1", Seed: 1}, {RootID: "r2", Seed: 2}, {RootID: "r3", Seed: 3}}}
	obj, key, err := cover.New(root, nil, nil).EnsureMosaic(context.Background(), in, 512)
	if err != nil {
		t.Fatal(err)
	}
	if obj.RelPath != tape.CoverRel(key) {
		t.Fatalf("cover layout drifted: service %q, tape %q", obj.RelPath, tape.CoverRel(key))
	}
	if r.coverP, err = root.ReadFile(obj.RelPath); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, it := range r.items {
		names = append(names, tape.ChapterName(it.Item.Title, "", it.Item.Lossy))
	}
	snap := tape.Snapshot{RecordID: "rec-1", Title: "Real Record", Description: "Synthetic liner.", Items: nil,
		Tags: []flacmeta.Comment{{Key: "DATE", Value: "2026-10-01"}, {Key: "GENRE", Value: "synthetic"}}}
	r.job = tape.Job{Snapshot: snap, Items: r.items, TargetLUFS: r.target, Chapters: names, CoverKey: key,
		Tags: tape.BuildTags(tape.TagsInput{Snapshot: snap, Items: r.items, TargetLUFS: r.target, IncludeLiner: true})}
	return r
}

func (r *realRig) pipeline() tape.Pipeline {
	return tape.NewPipeline(tape.PipelineOptions{Tools: r.tools, Data: r.root, DataDir: r.audio.Dir, Version: "test"})
}

// decodeS32 decodes any file to raw 48 kHz stereo s32le in memory with ffmpeg, the way a listener's player would.
func decodeS32(t *testing.T, tools mediatool.Tools, path string, extra ...string) []byte {
	t.Helper()
	args := append([]string{"-nostdin", "-hide_banner", "-v", "error", "-i", "file:" + path, "-map", "0:a:0"}, extra...)
	args = append(args, "-ac", "2", "-ar", "48000", "-f", "s32le", "pipe:1")
	out, err := mediatool.Run(context.Background(), tools.FFmpeg, args, mediatool.RunOpts{Timeout: time.Minute, MaxStdout: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func le32(b []byte, i int) int32 {
	return int32(uint32(b[4*i]) | uint32(b[4*i+1])<<8 | uint32(b[4*i+2])<<16 | uint32(b[4*i+3])<<24)
}

func TestPipeline_Real_Verified(t *testing.T) {
	r := newRealRig(t, "quiet", "loud", "lossy", "cd", "mono", "mid")
	res, err := r.pipeline().Render(context.Background(), r.job, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Cleanup()

	// V1 container
	if _, err := mediatool.Run(context.Background(), r.tools.FLAC, []string{"--silent", "--test", res.Path}, mediatool.RunOpts{Timeout: time.Minute, MaxStdout: 1 << 10}); err != nil {
		t.Fatal("flac --test:", err)
	}
	b, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != res.SHA256 || int64(len(b)) != res.Bytes {
		t.Fatal("result hash or size is not the file's")
	}
	info, err := flacmeta.Read(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}

	// V2 samples
	si := info.StreamInfo
	var sum int64
	for i, c := range res.Chapters {
		sum += c.Samples
		if c.StartSample != sum-c.Samples || c.Index != i+1 {
			t.Fatalf("chapter %d: %+v", i, c)
		}
	}
	if si.SampleRate != 48000 || si.BitsPerSample != 24 || si.Channels != 2 || int64(si.TotalSamples) != sum || res.TotalSamples != sum {
		t.Fatalf("%+v total %d", si, sum)
	}
	for i, it := range r.items {
		s := r.audio.Source(t, strings.TrimPrefix(it.Item.SlotID, "slot-"))
		if s.Rate == 48000 && s.Channels == 2 && !s.Lossy && res.Chapters[i].Samples != s.Samples {
			t.Errorf("%s: %d samples, source has %d", s.Name, res.Chapters[i].Samples, s.Samples)
		}
		if d := math.Abs(float64(res.Chapters[i].Samples) - s.Seconds*48000); d > 48000*0.1 {
			t.Errorf("%s: %d samples is %.0f off %.0f s", s.Name, res.Chapters[i].Samples, d, s.Seconds)
		}
	}
	if info.Count(flacmeta.BlockSeekTable) != 1 {
		t.Error("no seek table")
	}

	// V3 gapless: the tape's decode equals the concatenation of the independently decoded and gained sources
	var want []byte
	for i, it := range r.items {
		pcm := decodeS32(t, r.tools, r.audio.Abs(r.audio.Source(t, strings.TrimPrefix(it.Item.SlotID, "slot-"))))
		v := make([]int32, len(pcm)/4)
		for k := range v {
			v[k] = le32(pcm, k) >> 8
		}
		if clipped := tape.ApplyGain(v, v, r.plans[i].GainCDB); clipped != 0 {
			t.Fatalf("item %d clips", i)
		}
		if int64(len(v)/2) != res.Chapters[i].Samples {
			t.Fatalf("item %d: independent decode has %d frames, chapter %d", i, len(v)/2, res.Chapters[i].Samples)
		}
		for _, x := range v {
			want = append(want, 0, byte(x), byte(x>>8), byte(x>>16)) // the 24-bit value left-aligned in 32 bits
		}
	}
	got := decodeS32(t, r.tools, res.Path)
	if sha256.Sum256(got) != sha256.Sum256(want) {
		t.Fatalf("decoded tape differs from the concatenation (%d vs %d bytes)", len(got), len(want))
	}

	// V4 chapters in three forms
	cue, ok, err := flacmeta.ReadCuesheet(bytes.NewReader(b), int64(len(b)), info)
	if err != nil || !ok || len(cue.Tracks) != len(r.items) || cue.LeadOut != uint64(sum) {
		t.Fatal(cue, ok, err)
	}
	pc, err := tape.ParseChapterComments(info.Comments)
	if err != nil || len(pc) != len(r.items) {
		t.Fatal(pc, err)
	}
	var cueText string
	for _, c := range info.Comments {
		if v, ok := strings.CutPrefix(c, "CUESHEET="); ok {
			cueText = v
		}
	}
	pt, err := tape.ParseCueText(cueText)
	if err != nil || len(pt) != len(r.items) {
		t.Fatal(pt, err)
	}
	for i, ch := range res.Chapters {
		if int64(cue.Tracks[i].Offset) != ch.StartSample || pc[i].MS != ch.StartMS || pc[i].Name != r.job.Chapters[i] {
			t.Errorf("chapter %d disagrees: block %d, comment %d %q", i, cue.Tracks[i].Offset, pc[i].MS, pc[i].Name)
		}
		if exact := float64(ch.StartSample) * 75 / 48000; exact-float64(pt[i].Frames) >= 1 || exact < float64(pt[i].Frames) {
			t.Errorf("chapter %d text frames %d vs %.2f", i, pt[i].Frames, exact)
		}
	}

	// V5 loudness: each chapter within 0.5 LU of its prediction, post-gain spread of the unclamped within 0.5 LU
	var lo, hi = math.Inf(1), math.Inf(-1)
	for i, ch := range res.Chapters {
		p := r.plans[i]
		if p.PredictedLUFS == nil || p.Status == tape.GainCutClamped || p.Status == tape.GainBoostClamped {
			continue
		}
		seg := filepath.Join(t.TempDir(), "seg.wav")
		args := []string{"-nostdin", "-hide_banner", "-v", "error", "-i", "file:" + res.Path, "-af", fmt.Sprintf("atrim=start_sample=%d:end_sample=%d", ch.StartSample, ch.StartSample+ch.Samples),
			"-c:a", "pcm_s24le", "-f", "wav", "file:" + seg}
		if _, err := mediatool.Run(context.Background(), r.tools.FFmpeg, args, mediatool.RunOpts{Timeout: time.Minute, MaxStdout: 1 << 10}); err != nil {
			t.Fatal(err)
		}
		l, err := r.an.Loudness(context.Background(), seg)
		if err != nil {
			t.Fatal(err)
		}
		tol := 0.5
		if r.items[i].Item.Lossy {
			tol = 0.7
		}
		if d := math.Abs(l.IntegratedLUFS - *p.PredictedLUFS); d > tol {
			t.Errorf("chapter %d (%s): %.2f LUFS, predicted %.2f", i+1, p.Status, l.IntegratedLUFS, *p.PredictedLUFS)
		}
		lo, hi = math.Min(lo, l.IntegratedLUFS), math.Max(hi, l.IntegratedLUFS)
	}
	t.Logf("gains %v target %.1f; post-gain spread of the unclamped %.2f LU", gainsOf(r.plans), r.target, hi-lo)

	// V6 lossy label and V7 tags and cover
	joined := strings.Join(info.Comments, "\n")
	for _, w := range []string{"MM_CONTAINS_LOSSY_SOURCES=true", "Contains lossy sources (1 of 6 tracks)", "MM_SOURCES=lossless=5;lossy=1;resampled=2;upmixed=1",
		"MM_TAPE_VERSION=mm-tape/1", "MM_RECORD_ID=rec-1", "ALBUM=Real Record", "REPLAYGAIN_ALBUM_GAIN=", "REPLAYGAIN_ALBUM_PEAK=", "DATE=2026-10-01"} {
		if !strings.Contains(joined, w) {
			t.Errorf("tag %q missing", w)
		}
	}
	if !strings.Contains(joined, "CHAPTER003NAME=Title lossy [lossy source]") || strings.Contains(joined, "CHAPTER001NAME=Title quiet [lossy") {
		t.Error("lossy suffix is not on exactly the lossy item")
	}
	pics, err := flacmeta.ReadPictures(bytes.NewReader(b), int64(len(b)), info)
	if err != nil || len(pics) != 1 || !bytes.Equal(pics[0].Data, r.coverP) || pics[0].W != 512 || pics[0].H != 512 || pics[0].Type != 3 {
		t.Fatalf("picture: %v %+v", err, pics)
	}
	if res.PeakSample <= 0 || res.PeakSample > 1 {
		t.Errorf("peak %v", res.PeakSample)
	}

	// V8 determinism: the same job rendered again is the same file
	res2, err := r.pipeline().Render(context.Background(), r.job, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Cleanup()
	if res2.SHA256 != res.SHA256 || res2.Bytes != res.Bytes {
		t.Errorf("not deterministic: %s vs %s", res.SHA256, res2.SHA256)
	}

	// the reference tool agrees with Go about the new blocks
	out, err := mediatool.Run(context.Background(), r.tools.Metaflac, []string{"--list", "--block-type=CUESHEET", res.Path}, mediatool.RunOpts{Timeout: time.Minute, MaxStdout: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "type: 5 (CUESHEET)") || !strings.Contains(string(out), "lead-in: 0") || !strings.Contains(string(out), "number of tracks: 7") {
		t.Errorf("metaflac:\n%s", out)
	}
}

func gainsOf(p []tape.GainPlan) []string {
	var out []string
	for _, g := range p {
		out = append(out, fmt.Sprintf("%d/%s", g.GainCDB, g.Status))
	}
	return out
}

func TestPipeline_Real_ClippedAborts(t *testing.T) {
	// a forced +20 dB on the loud (-12 LUFS) source certainly leaves 24 bits
	r2 := newRealRig(t, "loud")
	r2.job.Items[0].Gain = tape.GainPlan{GainCDB: 2000, Status: tape.GainMatched}
	_, err := r2.pipeline().Render(context.Background(), r2.job, nil)
	var re *tape.RenderError
	if !errorsAs(err, &re) || re.Code != tape.ErrClipped {
		t.Fatalf("err %v", err)
	}
	if ents, _ := os.ReadDir(filepath.Join(r2.audio.Dir, "incoming")); len(ents) != 0 {
		t.Fatalf("leftovers: %v", ents)
	}
}

func TestPipeline_Real_SourceChanged(t *testing.T) {
	r := newRealRig(t, "mid")
	r.job.Items[0].Item.MasterBytes++
	_, err := r.pipeline().Render(context.Background(), r.job, nil)
	var re *tape.RenderError
	if !errorsAs(err, &re) || re.Code != tape.ErrSourceChanged {
		t.Fatalf("err %v", err)
	}
}

func TestPipeline_Real_CancelKillsTools(t *testing.T) {
	r := newRealRig(t, "quiet", "loud", "mid")
	ctx, cancel := context.WithCancel(context.Background())
	start := time.Now()
	_, err := r.pipeline().Render(ctx, r.job, func(p tape.Progress) {
		if p.Phase == "decode" && p.Item == 2 { // the encoder runs and the first item is in
			cancel()
		}
	})
	if !errorsIs(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if time.Since(start) > 15*time.Second {
		t.Fatalf("cancel took %v", time.Since(start))
	}
	if ents, _ := os.ReadDir(filepath.Join(r.audio.Dir, "incoming")); len(ents) != 0 {
		t.Fatalf("leftovers: %v", ents)
	}
}

// TestFlacmeta_RealFiles: the reference tool accepts every rewritten file whatever block mix Rewrite produced.
func TestFlacmeta_RealFiles(t *testing.T) {
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	tools := testtools.Tools()
	a := librarytest.FixtureL2Audio(t, tools)
	src := a.Abs(a.Source(t, "mid"))
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	info, err := flacmeta.Read(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	vc := flacmeta.VorbisComment("metamusic test", []flacmeta.Comment{{Key: "TITLE", Value: "Zażółć · 夜 a=b"}, {Key: "EMPTY", Value: ""}})
	cue := flacmeta.CuesheetBlock(flacmeta.Cuesheet{Tracks: []flacmeta.CueTrack{
		{Offset: 0, Number: 1, Indexes: []flacmeta.CueIndex{{Number: 1}}}, {Offset: 48000, Number: 2, Indexes: []flacmeta.CueIndex{{Number: 1}}},
	}, LeadOut: info.StreamInfo.TotalSamples})
	pic := flacmeta.PictureBlock(flacmeta.Picture{Type: 3, MIME: "image/png", W: 1, H: 1, Depth: 8, Data: []byte("not really a png")})
	for name, rep := range map[string]flacmeta.Replace{
		"all":         {Vorbis: vc, Cuesheet: cue, Picture: pic},
		"keep":        {},
		"drop-all":    {Vorbis: []byte{}, Cuesheet: []byte{}, Picture: []byte{}},
		"only-cue":    {Vorbis: []byte{}, Cuesheet: cue, Picture: []byte{}},
		"only-vorbis": {Vorbis: vc, Cuesheet: []byte{}, Picture: []byte{}},
	} {
		t.Run(name, func(t *testing.T) {
			dst := filepath.Join(t.TempDir(), "out.flac")
			f, err := os.Create(dst)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := flacmeta.Rewrite(f, bytes.NewReader(b), int64(len(b)), info, rep); err != nil {
				t.Fatal(err)
			}
			f.Close()
			if _, err := mediatool.Run(context.Background(), tools.FLAC, []string{"--silent", "--test", dst}, mediatool.RunOpts{Timeout: time.Minute, MaxStdout: 1 << 10}); err != nil {
				t.Fatalf("flac --test: %v", err)
			}
			if _, err := mediatool.Run(context.Background(), tools.Metaflac, []string{"--list", dst}, mediatool.RunOpts{Timeout: time.Minute, MaxStdout: 1 << 20}); err != nil {
				t.Fatalf("metaflac --list: %v", err)
			}
			nb, _ := os.ReadFile(dst)
			ni, err := flacmeta.Read(bytes.NewReader(nb), int64(len(nb)))
			if err != nil || ni.StreamInfo != info.StreamInfo {
				t.Fatal(err)
			}
			a, _ := flacmeta.FramesSHA256(bytes.NewReader(b), int64(len(b)), info.FramesOffset)
			c, _ := flacmeta.FramesSHA256(bytes.NewReader(nb), int64(len(nb)), ni.FramesOffset)
			if a != c {
				t.Fatal("frames changed")
			}
		})
	}
	_ = cmp.Diff
}

// TestPerf_TapeRender: 12 x 240 s of 48 kHz stereo (a 48 minute tape) within a wall budget and a bounded heap.
func TestPerf_TapeRender(t *testing.T) {
	if testing.Short() {
		t.Skip("perf")
	}
	testtools.Require(t, "ffmpeg", "flac")
	tools := testtools.Tools()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := root.MkdirAll("masters", 0o755); err != nil {
		t.Fatal(err)
	}
	const n, secs = 12, 240
	var items []tape.PlannedItem
	var names []string
	for i := range n {
		rel := fmt.Sprintf("masters/p%02d.flac", i)
		args := []string{"-nostdin", "-hide_banner", "-v", "error", "-f", "lavfi", "-i", fmt.Sprintf("sine=f=%d:r=48000:d=%d", 220+i*20, secs), "-ac", "2",
			"-c:a", "flac", "-sample_fmt", "s32", "-bits_per_raw_sample", "24", "-f", "flac", "file:" + filepath.Join(dir, rel)}
		if _, err := mediatool.Run(context.Background(), tools.FFmpeg, args, mediatool.RunOpts{Timeout: 2 * time.Minute, MaxStdout: 1 << 10}); err != nil {
			t.Fatal(err)
		}
		st, _ := root.Stat(rel)
		items = append(items, tape.PlannedItem{Item: tape.Item{SlotID: fmt.Sprint("s", i), TakeID: fmt.Sprint("t", i), MasterRel: rel, MasterBytes: st.Size(), DurationS: secs},
			Gain: tape.GainPlan{GainCDB: -300, Status: tape.GainMatched}})
		names = append(names, fmt.Sprint("Perf ", i))
	}
	job := tape.Job{Items: items, TargetLUFS: -14, Chapters: names, Tags: []flacmeta.Comment{{Key: "TITLE", Value: "perf"}}}
	p := tape.NewPipeline(tape.PipelineOptions{Tools: tools, Data: root, DataDir: dir, Version: "test"})
	start := time.Now()
	peakHeap := sampleHeap(func() {
		res, err := p.Render(context.Background(), job, nil)
		if err != nil {
			t.Fatal(err)
		}
		res.Cleanup()
	})
	wall := time.Since(start)
	t.Logf("12 x 240 s rendered in %v, peak heap %d MiB", wall.Round(time.Millisecond), peakHeap>>20)
	if wall > 3*time.Minute {
		t.Errorf("wall %v over the 3 minute budget", wall)
	}
	if peakHeap > 256<<20 {
		t.Errorf("heap peaked at %d MiB, over 256", peakHeap>>20)
	}
}

// TestTape_ChaptersVisibleToFFmpeg is the step-0 spike log of C128.6: it asserts nothing about the players; it logs what
// ffmpeg's FLAC demuxer reports (-f ffmetadata) and what metaflac lists for a 3-chapter tape, so the three encodings can be
// compared by eye. Only the build of the tape itself must succeed.
func TestTape_ChaptersVisibleToFFmpeg(t *testing.T) {
	r := newRealRig(t, "quiet", "mid", "loud")
	res, err := r.pipeline().Render(context.Background(), r.job, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Cleanup()
	out, err := mediatool.Run(context.Background(), r.tools.FFmpeg, []string{"-nostdin", "-hide_banner", "-v", "error", "-i", "file:" + res.Path, "-f", "ffmetadata", "pipe:1"},
		mediatool.RunOpts{Timeout: time.Minute, MaxStdout: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("ffmpeg -f ffmetadata:\n%s", out)
	hasChapterSection := bytes.Contains(out, []byte("[CHAPTER]"))
	t.Logf("ffmpeg demuxer exposes [CHAPTER] entries: %v", hasChapterSection)
	for _, bt := range []string{"CUESHEET", "VORBIS_COMMENT"} {
		l, err := mediatool.Run(context.Background(), r.tools.Metaflac, []string{"--list", "--block-type=" + bt, res.Path}, mediatool.RunOpts{Timeout: time.Minute, MaxStdout: 1 << 20})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("metaflac --list --block-type=%s:\n%s", bt, l)
	}
	l, err := mediatool.Run(context.Background(), r.tools.Metaflac, []string{"--export-cuesheet-to=-", res.Path}, mediatool.RunOpts{Timeout: time.Minute, MaxStdout: 1 << 20})
	t.Logf("metaflac --export-cuesheet-to=- (err %v):\n%s", err, l)

	// each encoding alone: which one does ffmpeg's demuxer turn into [CHAPTER] entries, and does it carry the names?
	b, _ := os.ReadFile(res.Path)
	info, _ := flacmeta.Read(bytes.NewReader(b), int64(len(b)))
	var base, chap, text []flacmeta.Comment
	for _, c := range info.Comments {
		k, v, _ := strings.Cut(c, "=")
		switch {
		case strings.HasPrefix(k, "CHAPTER"):
			chap = append(chap, flacmeta.Comment{Key: k, Value: v})
		case k == "CUESHEET":
			text = append(text, flacmeta.Comment{Key: k, Value: v})
		default:
			base = append(base, flacmeta.Comment{Key: k, Value: v})
		}
	}
	blockOnly := flacmeta.CuesheetBlock(tape.CuesheetOf(res.Chapters))
	for _, v := range []struct {
		name string
		rep  flacmeta.Replace
	}{
		{"binary CUESHEET block only", flacmeta.Replace{Vorbis: flacmeta.VorbisComment("v", base), Cuesheet: blockOnly}},
		{"CHAPTERnnn comments only", flacmeta.Replace{Vorbis: flacmeta.VorbisComment("v", append(append([]flacmeta.Comment{}, base...), chap...)), Cuesheet: []byte{}}},
		{"text CUESHEET comment only", flacmeta.Replace{Vorbis: flacmeta.VorbisComment("v", append(append([]flacmeta.Comment{}, base...), text...)), Cuesheet: []byte{}}},
	} {
		dst := filepath.Join(t.TempDir(), "v.flac")
		f, _ := os.Create(dst)
		if _, err := flacmeta.Rewrite(f, bytes.NewReader(b), int64(len(b)), info, v.rep); err != nil {
			t.Fatal(err)
		}
		f.Close()
		o, err := mediatool.Run(context.Background(), r.tools.FFmpeg, []string{"-nostdin", "-hide_banner", "-v", "error", "-i", "file:" + dst, "-f", "ffmetadata", "pipe:1"},
			mediatool.RunOpts{Timeout: time.Minute, MaxStdout: 1 << 20})
		if err != nil {
			t.Fatal(err)
		}
		var starts []string
		for _, line := range strings.Split(string(o), "\n") {
			if strings.HasPrefix(line, "START=") || strings.HasPrefix(line, "TIMEBASE=") || strings.HasPrefix(line, "title=") {
				starts = append(starts, line)
			}
		}
		t.Logf("ffmpeg demuxer, %s: %d [CHAPTER] entries %v", v.name, strings.Count(string(o), "[CHAPTER]"), starts)
	}
}
