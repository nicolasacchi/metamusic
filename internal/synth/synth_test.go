package synth_test

import (
	"bytes"
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/analysis/ffmpeg"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/synth"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

func readInfo(t *testing.T, p string) (flacmeta.Info, int64) {
	t.Helper()
	fh, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	st, _ := fh.Stat()
	info, err := flacmeta.Read(fh, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	return info, st.Size()
}

func framesSHA(t *testing.T, p string) string {
	t.Helper()
	info, size := readInfo(t, p)
	fh, _ := os.Open(p)
	defer fh.Close()
	s, err := flacmeta.FramesSHA256(fh, size, info.FramesOffset)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSynthTone_LUFSWithinTolerance(t *testing.T) {
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	tools := testtools.Tools()
	f, err := ffmpeg.New(context.Background(), tools)
	if err != nil {
		t.Fatal(err)
	}
	for _, tone := range []synth.Tone{
		{LUFS: -14, Seconds: 3, BPM: 120},
		{LUFS: -10, Seconds: 3, BPM: 90, Variant: 1},
		{LUFS: -20, Seconds: 3},
		{LUFS: -14, Seconds: 3, BandLimitHz: 8000},
	} {
		p := filepath.Join(t.TempDir(), "t.flac")
		if err := synth.FLAC(context.Background(), tools, p, tone, synth.StyleFFmpeg); err != nil {
			t.Fatal(err)
		}
		l, err := f.Loudness(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		if math.Abs(l.IntegratedLUFS-tone.LUFS) > 0.05 {
			t.Errorf("%+v: measured %.3f", tone, l.IntegratedLUFS)
		}
		if l.SamplePeakDBFS > -0.5 {
			t.Errorf("%+v: sample peak %.2f risks clipping", tone, l.SamplePeakDBFS)
		}
		info, _ := readInfo(t, p)
		si := info.StreamInfo
		if si.SampleRate != 48000 || si.Channels != 2 || si.BitsPerSample != 24 || si.TotalSamples != 3*48000 {
			t.Errorf("%+v: stream info %+v", tone, si)
		}
	}
}

func TestSynth_Styles(t *testing.T) {
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	tools := testtools.Tools()
	d := t.TempDir()
	a, b := filepath.Join(d, "a.flac"), filepath.Join(d, "b.flac")
	tone := synth.Tone{LUFS: -14, Seconds: 2, BPM: 100}
	if err := synth.FLAC(context.Background(), tools, a, tone, synth.StyleFFmpeg); err != nil {
		t.Fatal(err)
	}
	if err := synth.FLAC(context.Background(), tools, b, tone, synth.StyleYuE2); err != nil {
		t.Fatal(err)
	}
	ia, _ := readInfo(t, a)
	ib, _ := readInfo(t, b)
	if ia.Count(flacmeta.BlockType(1)) == 0 || len(ia.Comments) == 0 {
		t.Errorf("ffmpeg style should carry padding and an encoder tag: %+v %v", ia.Blocks, ia.Comments)
	}
	if ib.Count(1) != 0 || ib.Count(3) != 0 || ib.Count(4) != 1 || ib.Vendor == "" || len(ib.Comments) != 0 {
		t.Errorf("yue2 style must keep a vendor-only VORBIS_COMMENT and have no padding, seektable or tags: %+v %q %v", ib.Blocks, ib.Vendor, ib.Comments)
	}
	if ib.StreamInfo.TotalSamples != 2*48000 || ib.StreamInfo.BitsPerSample != 24 {
		t.Errorf("%+v", ib.StreamInfo)
	}
	// variant changes the frames; same tone twice is reproducible
	c := filepath.Join(d, "c.flac")
	if err := synth.FLAC(context.Background(), tools, c, synth.Tone{LUFS: -14, Seconds: 2, BPM: 100, Variant: 1}, synth.StyleFFmpeg); err != nil {
		t.Fatal(err)
	}
	if framesSHA(t, a) == framesSHA(t, c) {
		t.Error("variant must change the audio")
	}
	a2 := filepath.Join(d, "a2.flac")
	if err := synth.FLAC(context.Background(), tools, a2, tone, synth.StyleFFmpeg); err != nil {
		t.Fatal(err)
	}
	if framesSHA(t, a) != framesSHA(t, a2) {
		t.Error("same tone must be reproducible")
	}
	if err := synth.FLAC(context.Background(), tools, filepath.Join(d, "bad.flac"), synth.Tone{LUFS: -14, Seconds: 0}, synth.StyleFFmpeg); err == nil {
		t.Error("zero duration accepted")
	}
	if err := synth.FLAC(context.Background(), tools, "relative.flac", tone, synth.StyleFFmpeg); err == nil {
		t.Error("relative dst accepted")
	}
}

// recordingFFmpeg wraps the real ffmpeg and logs argv.
func recordingFFmpeg(t *testing.T, realBin string) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	log, bin = filepath.Join(dir, "argv.log"), filepath.Join(dir, "ffmpeg")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> '" + log + "'\nexec '" + realBin + "' \"$@\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func TestSynth_UsesSwr(t *testing.T) {
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	tools := testtools.Tools()
	realBin, err := exec.LookPath(tools.FFmpeg)
	if err != nil {
		t.Fatal(err)
	}
	bin, log := recordingFFmpeg(t, realBin)
	tools.FFmpeg = bin
	p := filepath.Join(t.TempDir(), "t.flac")
	if err := synth.FLAC(context.Background(), tools, p, synth.Tone{LUFS: -14, Seconds: 2, BandLimitHz: 8000}, synth.StyleFFmpeg); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	if !bytes.Contains(b, []byte("resampler=swr")) || bytes.Contains(b, []byte("soxr")) {
		t.Fatalf("argv:\n%s", b)
	}
	// every run starts at -nostdin; the capability probes of ffmpeg.New have none and no -i
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	whitelisted, inputs := false, 0
	for j, a := range lines {
		switch a {
		case "-nostdin":
			whitelisted = false
		case "-protocol_whitelist":
			whitelisted = lines[j+1] == "file,pipe"
		case "-i", "-filter_complex":
			inputs++
			if !whitelisted {
				t.Errorf("%s at line %d without a preceding -protocol_whitelist file,pipe", a, j)
			}
		}
	}
	if inputs == 0 {
		t.Fatal("no ffmpeg run recorded")
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "/") {
			t.Errorf("bare path in argv: %q", line)
		}
	}
}

func TestSynthRetag_SameAudioID(t *testing.T) {
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	tools := testtools.Tools()
	d := t.TempDir()
	src, dst := filepath.Join(d, "s.flac"), filepath.Join(d, "r.flac")
	if err := synth.FLAC(context.Background(), tools, src, synth.Tone{LUFS: -14, Seconds: 2}, synth.StyleYuE2); err != nil {
		t.Fatal(err)
	}
	title := "Città è <&>"
	if err := synth.Retag(context.Background(), tools, src, dst, []string{"TITLE=" + title, "MM_X=1"}); err != nil {
		t.Fatal(err)
	}
	is, _ := readInfo(t, src)
	id, _ := readInfo(t, dst)
	if flacmeta.AudioID(framesSHA(t, src), is.StreamInfo.TotalSamples) != flacmeta.AudioID(framesSHA(t, dst), id.StreamInfo.TotalSamples) {
		t.Fatal("audio id changed")
	}
	if len(id.Comments) != 2 || id.Comments[0] != "TITLE="+title {
		t.Fatalf("comments %q", id.Comments)
	}
	a, _ := os.ReadFile(src)
	b, _ := os.ReadFile(dst)
	if bytes.Equal(a, b) {
		t.Fatal("bytes must differ")
	}
	// retagging again replaces tags
	dst2 := filepath.Join(d, "r2.flac")
	if err := synth.Retag(context.Background(), tools, dst, dst2, []string{"TITLE=x"}); err != nil {
		t.Fatal(err)
	}
	i2, _ := readInfo(t, dst2)
	if len(i2.Comments) != 1 {
		t.Fatalf("comments %q", i2.Comments)
	}
	if err := synth.Retag(context.Background(), tools, src, filepath.Join(d, "bad.flac"), []string{"A=b\nC=d"}); err == nil {
		t.Error("newline in a comment accepted")
	}
	if err := synth.Retag(context.Background(), tools, src, filepath.Join(d, "bad2.flac"), []string{"noequals"}); err == nil {
		t.Error("comment without = accepted")
	}
	if err := synth.Retag(context.Background(), tools, filepath.Join(d, "nope.flac"), filepath.Join(d, "bad3.flac"), nil); err == nil {
		t.Error("missing source accepted")
	}
	if _, err := os.Stat(filepath.Join(d, "bad3.flac")); err == nil {
		t.Error("failed retag left a file")
	}
}

func TestProbeFLAC(t *testing.T) {
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	tools := testtools.Tools()
	p := filepath.Join(t.TempDir(), "probe.flac")
	if err := synth.ProbeFLAC(context.Background(), tools, p); err != nil {
		t.Fatal(err)
	}
	info, _ := readInfo(t, p)
	si := info.StreamInfo
	if si.BitsPerSample != 24 || si.SampleRate != 48000 || si.Channels != 2 || si.TotalSamples != 3*48000 || info.Duration() != 3.0 {
		t.Fatalf("%+v", si)
	}
	an, err := ffmpeg.New(context.Background(), tools)
	if err != nil {
		t.Fatal(err)
	}
	l, err := an.Loudness(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(l.SamplePeakDBFS+20) > 0.1 {
		t.Errorf("sample peak %.2f dBFS, want -20", l.SamplePeakDBFS)
	}
}

func TestSynth_RejectsBadLUFS(t *testing.T) {
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	tools := testtools.Tools()
	d := t.TempDir()
	for _, l := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if err := synth.FLAC(context.Background(), tools, filepath.Join(d, "x.flac"), synth.Tone{LUFS: l, Seconds: 2}, synth.StyleFFmpeg); err == nil {
			t.Errorf("LUFS %v accepted", l)
		}
	}
	// a target that cannot be met without clipping is an error, not an off-target file
	if err := synth.FLAC(context.Background(), tools, filepath.Join(d, "loud.flac"), synth.Tone{LUFS: -2, Seconds: 2, BPM: 120}, synth.StyleFFmpeg); err == nil {
		t.Error("clipping target accepted")
	}
	if _, err := os.Stat(filepath.Join(d, "loud.flac")); err == nil {
		t.Error("failed render left a file")
	}
}

func TestEBU3341_BadCase(t *testing.T) {
	var tools mediatool.Tools
	if _, err := synth.EBU3341(context.Background(), tools, filepath.Join(t.TempDir(), "x.flac"), 0); err == nil {
		t.Fatal("case 0 accepted")
	}
}

func walkInbox(t *testing.T, root string) (files []string, modes map[string]os.FileMode) {
	t.Helper()
	modes = map[string]os.FileMode{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		files = append(files, rel)
		st, err := os.Lstat(p)
		if err != nil {
			return err
		}
		modes[rel] = st.Mode()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files, modes
}

func TestSynthArchive_MatchesC21(t *testing.T) {
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	dir := t.TempDir()
	m, err := synth.Archive(context.Background(), testtools.Tools(), dir, synth.ProfileAcceptance, 1)
	if err != nil {
		t.Fatal(err)
	}
	files, modes := walkInbox(t, dir)
	var listed []string
	for _, f := range m.Files {
		listed = append(listed, f.Path)
	}
	sortStrings(files)
	sortStrings(listed)
	if strings.Join(files, "\n") != strings.Join(listed, "\n") {
		t.Fatalf("manifest differs from disk:\ndisk:     %v\nmanifest: %v", files, listed)
	}
	want := []string{
		"music/songs.jsonl", "music/abc/syn-song-08.abc", "music/requests/syn-song-06.json",
		"music/flac/syn_song_01.flac", "music/flac/syn_song_01_s2001.flac", "music/flac/syn_song_02.flac",
		"music/flac/syn_song_02_s2002.flac", "music/flac/syn_song_03.flac", "music/flac/syn_song_04.flac",
		"music/flac/take_final.flac", "music/flac/synthetic_six.flac", "music/flac/syn_song_05.flac",
		"music/flac/syn_song_04.mp3", "music/flac/.syn_song_06.flac.Xy12Ab", "music/flac/link_to_02.flac", "music/flac/pipe.flac",
		"music/audio/syn_song_04.flac", "music/player-export/Syn Song 04.flac",
		"music/mp3/syn_song_02.mp3", "music/mp3/syn_song_03.mp3", "music/mp3/take_final.mp3", "music/mp3/orphan_take.mp3",
		"music/results/syn-song-05/audio.flac", "music/results/syn-song-05/request.json", "music/results/syn-song-05/result.json",
		"music/results/syn-song-05/config.json", "music/results/syn-song-05/plan.json", "music/results/syn-song-05/score.abc",
		"music/results/syn-song-05/semantic.npy", "music/results/syn-song-05/latent.npy",
		"music/scores/syn_song_03.abc", "music/backlog-variants.jsonl", "music/pilot.jsonl", "music/pilot.json",
		"music/index.csv", "music/playlist.json", "music/review.json", "music/old-batch.zip",
		"varianti/syn-song-01-v2.json", "unmatched/mystery.flac",
	}
	sortStrings(want)
	if strings.Join(listed, "\n") != strings.Join(want, "\n") {
		t.Fatalf("file list != C21:\n got %v\nwant %v", listed, want)
	}
	if modes["music/flac/link_to_02.flac"]&os.ModeSymlink == 0 || modes["music/flac/pipe.flac"]&os.ModeNamedPipe == 0 {
		t.Errorf("symlink/FIFO missing: %v %v", modes["music/flac/link_to_02.flac"], modes["music/flac/pipe.flac"])
	}
	if !strings.Contains(m.Mapping, "music/flac/syn_song_01.flac,syn-song-01,1001,,") || !strings.Contains(m.Mapping, "unmatched/mystery.flac,-,,,") {
		t.Errorf("mapping = %q", m.Mapping)
	}
	e := m.Expect
	got := map[string]int{"recipes": e.Recipes, "recipe_sources": e.RecipeSources, "tracks": e.Tracks, "drafts": e.Drafts,
		"backlog": e.DraftsBacklog, "fulfilled": e.DraftsFulfilled, "collections": e.Collections, "collection_drafts": e.CollectionDrafts,
		"refs": e.Refs, "siblings": e.SiblingEdges, "scores": e.Scores, "audio_files": e.AudioFiles, "audio_identities": e.AudioIdentities,
		"duplicate_groups": e.DuplicateGroups, "ignored": e.Ignored, "line_errors": e.RecipeLineErrors, "skipped": e.AudioSkipped, "exit": e.ExitCode}
	wantN := map[string]int{"recipes": 23, "recipe_sources": 28, "tracks": 9, "drafts": 23, "backlog": 14, "fulfilled": 9, "collections": 1,
		"collection_drafts": 3, "refs": 11, "siblings": 2, "scores": 2, "audio_files": 13, "audio_identities": 10, "duplicate_groups": 2,
		"ignored": 12, "line_errors": 1, "skipped": 1, "exit": 1}
	if d := cmp.Diff(wantN, got); d != "" {
		t.Errorf("expect (-want +got):\n%s", d)
	}
	if e.LoudnessSpreadLU != 10.0 {
		t.Errorf("spread = %v", e.LoudnessSpreadLU)
	}
	// the syn-song-04 copies share one audio identity; the retagged one has different bytes
	a, b := framesSHA(t, filepath.Join(dir, "music/flac/syn_song_04.flac")), framesSHA(t, filepath.Join(dir, "music/player-export/Syn Song 04.flac"))
	if a != b {
		t.Error("retagged copy changed the frames")
	}
	ba, _ := os.ReadFile(filepath.Join(dir, "music/flac/syn_song_04.flac"))
	bb, _ := os.ReadFile(filepath.Join(dir, "music/player-export/Syn Song 04.flac"))
	if bytes.Equal(ba, bb) {
		t.Error("retagged copy is byte identical")
	}
	// the lyrics edge strings survive verbatim in songs.jsonl
	songs, _ := os.ReadFile(filepath.Join(dir, "music/songs.jsonl"))
	for _, s := range []string{"è à ì", "<b>&", "< > &"} {
		if !bytes.Contains(songs, []byte(s)) {
			t.Errorf("songs.jsonl lacks %q", s)
		}
	}
	if bytes.Count(songs, []byte("\n")) != 11 || !bytes.HasSuffix(songs, []byte("\n")) {
		t.Errorf("songs.jsonl lines = %d", bytes.Count(songs, []byte("\n")))
	}
}

func TestSynthArchive_E2EProfile(t *testing.T) {
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	dir := t.TempDir()
	m, err := synth.Archive(context.Background(), testtools.Tools(), dir, synth.ProfileE2E, 1)
	if err != nil {
		t.Fatal(err)
	}
	files, modes := walkInbox(t, dir)
	if len(files) != len(m.Files) {
		t.Errorf("disk %d files, manifest %d", len(files), len(m.Files))
	}
	for p, md := range modes {
		if md&(os.ModeSymlink|os.ModeNamedPipe) != 0 || strings.HasSuffix(p, ".zip") {
			t.Errorf("e2e profile has %s (%v)", p, md)
		}
	}
	e := m.Expect
	if e.Recipes != 22 || e.RecipeSources != 22 || e.Tracks != 9 || e.Drafts != 22 || e.DraftsBacklog != 13 || e.DraftsFulfilled != 9 ||
		e.SiblingEdges != 1 || e.AudioFiles != 9 || e.AudioIdentities != 9 || e.Scores != 2 || e.TimedScores != 2 || e.LoudnessSpreadLU != 12.8 || e.ExitCode != 0 || m.Mapping != "" {
		t.Errorf("expect = %+v mapping=%q", e, m.Mapping)
	}
	if len(m.Audio) != 9 {
		t.Errorf("audio specs = %d", len(m.Audio))
	}
	if _, err := synth.Archive(context.Background(), testtools.Tools(), t.TempDir(), "nope", 1); err == nil {
		t.Error("unknown profile accepted")
	}
}

// ShortAudio shortens exactly one take and leaves every other file, recipe and expectation as the default build has it.
func TestSynthArchive_E2EShortAudio(t *testing.T) {
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	ctx := context.Background()
	long, short := t.TempDir(), t.TempDir()
	ml, err := synth.Archive(ctx, testtools.Tools(), long, synth.ProfileE2E, 1)
	if err != nil {
		t.Fatal(err)
	}
	ms, err := synth.ArchiveWith(ctx, testtools.Tools(), short, synth.ProfileE2E, 1, synth.Options{ShortAudio: true})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(ml.Expect, ms.Expect); diff != "" {
		t.Errorf("expectations differ:\n%s", diff)
	}
	if diff := cmp.Diff(ml.Files, ms.Files); diff != "" {
		t.Errorf("file lists differ:\n%s", diff)
	}
	var changed []string
	for i, a := range ml.Audio {
		if a.Seconds != ms.Audio[i].Seconds {
			changed = append(changed, a.Path)
		}
	}
	if diff := cmp.Diff([]string{"music/flac/syn_song_02.flac"}, changed); diff != "" {
		t.Errorf("shortened takes:\n%s", diff)
	}
	info, _ := readInfo(t, filepath.Join(short, "music/flac/syn_song_02.flac"))
	if d := info.Duration(); d != synth.ShortSeconds {
		t.Errorf("short take lasts %v s, want %d", d, synth.ShortSeconds)
	}
	info, _ = readInfo(t, filepath.Join(long, "music/flac/syn_song_02.flac"))
	if d := info.Duration(); d != 6 {
		t.Errorf("default take lasts %v s, want 6", d)
	}
}

func sortStrings(s []string) { sort.Strings(s) }

func TestSynthEars_RecipesBatchValid(t *testing.T) {
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	t.Parallel()
	dir := t.TempDir()
	if _, err := synth.Archive(context.Background(), testtools.Tools(), dir, synth.ProfileE2E, 1); err != nil {
		t.Fatal(err)
	}
	songs, err := os.ReadFile(filepath.Join(dir, "music/songs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(songs, []byte("\n")); n != 9 {
		t.Errorf("songs.jsonl has %d lines, want 9", n)
	}
	for _, id := range []string{"syn-ears-clip", "syn-ears-gap", "syn-ears-loop"} {
		if !bytes.Contains(songs, []byte(`"id": "`+id+`"`)) {
			t.Errorf("songs.jsonl lacks %s", id)
		}
	}
	// The M1 lines repeat syn-song-01 on purpose (a sibling pair), which the batch validator reports as
	// duplicate_id; the three ears lines must be clean on their own and add no issue to the whole file.
	lines := bytes.SplitAfter(songs, []byte("\n"))
	ears := bytes.Join(lines[6:9], nil)
	if rep := yue2.ValidateBatch(ears, nil); !rep.OK || len(rep.Issues) != 0 || rep.Requests != 3 {
		t.Errorf("ears lines report = %+v", rep)
	}
	whole := yue2.ValidateBatch(songs, os.DirFS(filepath.Join(dir, "music")))
	if whole.Requests != 9 || len(whole.Issues) != 1 || whole.Issues[0].Code != "duplicate_id" {
		t.Errorf("whole file report = %+v", whole)
	}
}

func TestSynthEars_Files(t *testing.T) {
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	t.Parallel()
	dir := t.TempDir()
	m, err := synth.Archive(context.Background(), testtools.Tools(), dir, synth.ProfileE2E, 1)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, f := range m.Files {
		have[f.Path] = true
	}
	for _, p := range []string{"music/flac/syn_ears_clip.flac", "music/flac/syn_ears_gap.flac",
		"music/results/syn-ears-loop/audio.flac", "music/results/syn-ears-loop/request.json", "music/results/syn-ears-loop/result.json",
		"music/results/syn-ears-loop/score.abc", "music/results/syn-ears-loop/semantic.npy"} {
		if !have[p] {
			t.Errorf("manifest lacks %s", p)
		}
	}
	b, err := os.ReadFile(filepath.Join(dir, "music/results/syn-ears-loop/semantic.npy"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(b, []byte("\x93NUMPY")) || !bytes.Contains(b[:128], []byte("(1, 600)")) {
		t.Errorf("semantic.npy header = %q", b[:min(len(b), 96)])
	}
}

func TestSynthArchive_AcceptanceSemanticNPYValid(t *testing.T) {
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	t.Parallel()
	dir := t.TempDir()
	if _, err := synth.Archive(context.Background(), testtools.Tools(), dir, synth.ProfileAcceptance, 1); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "music/results/syn-song-05/semantic.npy"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(b, []byte("\x93NUMPY")) {
		t.Errorf("acceptance semantic.npy is not a numpy array: %q", b[:min(len(b), 16)])
	}
}
