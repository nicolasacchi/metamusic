package acceptance_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis/ffmpeg"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/tape"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

// The media tier of the tape export: the production pipeline and the real flac and ffmpeg behind the real server, over the
// synthetic sources of FixtureL2Audio planted as the masters of the Takes of Record R1 (slots A, B, C and E; D is empty).

// l2Plant maps the Take labels of R1's playable slots to the synthetic sources that stand in for their masters.
var l2Plant = []struct{ take, source string }{{"A1", "quiet"}, {"B1", "loud"}, {"C2", "mid"}, {"E1", "lossy"}}

type l2MediaRig struct {
	*servertest.Rig
	audio lt.L2Audio
	tools mediatool.Tools
	an    *ffmpeg.FFmpeg
}

// newL2MediaRig builds the rig and plants the audio. flacBin overrides the encoder binary ("" keeps the real one).
func newL2MediaRig(t *testing.T, flacBin string) *l2MediaRig {
	t.Helper()
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	tools := testtools.Tools()
	r := l2Rig(t, servertest.Options{RealTape: true, RealTapeFLAC: flacBin})
	audio := lt.FixtureL2Audio(t, tools)
	an, err := ffmpeg.New(context.Background(), tools)
	if err != nil {
		t.Fatal(err)
	}
	// the fixture's placeholder masters and loudness are replaced by real ones: the immutability guards are dropped in this scratch rig only
	for _, tr := range []string{"tracks_frozen", "analyses_immutable"} {
		if _, err := r.Store.W.Exec(`DROP TRIGGER ` + tr); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range l2Plant {
		s := audio.Source(t, p.source)
		b, err := os.ReadFile(audio.Abs(s))
		if err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(r.DataDir, s.Rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, b, 0o444); err != nil { //nolint:gosec // 0444 is the point
			t.Fatal(err)
		}
		total := s.Samples
		if s.Lossy {
			total = int64(s.Seconds * float64(s.Rate))
		}
		id := l2Take(t, r, p.take)
		if _, err := r.Store.W.Exec(`UPDATE tracks SET master_path = ?, master_sha256 = ?, master_bytes = ?, sample_rate = ?, channels = ?, bits_per_sample = ?, total_samples = ?, duration_s = ? WHERE id = ?`,
			s.Rel, s.SHA256, s.Bytes, s.Rate, s.Channels, s.Bits, total, s.Seconds, id); err != nil {
			t.Fatal(err)
		}
		// the stored loudness is the real measurement of the planted audio
		l, err := an.Loudness(context.Background(), audio.Abs(s))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.Store.W.Exec(`UPDATE analyses SET metrics_json = json_set(json_set(metrics_json, '$.integrated_lufs', ?), '$.true_peak_dbtp', ?) WHERE track_id = ? AND analyzer = 'loudness'`,
			l.IntegratedLUFS, l.TruePeakDBTP, id); err != nil {
			t.Fatal(err)
		}
	}
	return &l2MediaRig{Rig: r, audio: audio, tools: tools, an: an}
}

// export has the operator export R1's tape and runs the real pipeline once; it returns the ready tape view.
func (m *l2MediaRig) export(t *testing.T, key string) map[string]any {
	t.Helper()
	return l2ExportTape(t, m.Rig, m.Human(t), l2ID(t, m.Rig, "records", l2R1), key)
}

// fetch downloads the tape through its signed media URL, as a player would.
func (m *l2MediaRig) fetch(t *testing.T, tp map[string]any) []byte {
	t.Helper()
	url, _ := obj(tp["download"])["url"].(string)
	if url == "" {
		t.Fatalf("the ready tape has no download link: %v", tp)
	}
	resp, err := http.Get(m.URL + url) //nolint:gosec,noctx // the rig's own loopback server
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Type"), "flac") {
		t.Fatalf("GET tape: %d %q %v", resp.StatusCode, resp.Header.Get("Content-Type"), err)
	}
	return b
}

func (m *l2MediaRig) decodeS32(t *testing.T, path string) []byte {
	t.Helper()
	args := []string{"-nostdin", "-hide_banner", "-v", "error", "-i", "file:" + path, "-map", "0:a:0", "-ac", "2", "-ar", "48000", "-f", "s32le", "pipe:1"}
	out, err := mediatool.Run(context.Background(), m.tools.FFmpeg, args, mediatool.RunOpts{Timeout: time.Minute, MaxStdout: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func le32(b []byte, i int) int32 {
	return int32(uint32(b[4*i]) | uint32(b[4*i+1])<<8 | uint32(b[4*i+2])<<16 | uint32(b[4*i+3])<<24)
}

// TestAcceptance_L2_TapeExport: the whole export through the real server and the production pipeline (media tier). Subtests are
// the verifications V1-V8 of C128.6, read from the file the signed link serves.
func TestAcceptance_L2_TapeExport(t *testing.T) {
	m := newL2MediaRig(t, "")
	before := snapshotMasters(t, m.Rig)
	tp := m.export(t, "media-tape")
	data := m.fetch(t, tp)
	path := filepath.Join(t.TempDir(), "tape.flac")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := flacmeta.Read(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	items, _ := tp["items"].([]any)
	chapters, _ := tp["chapters"].([]any)
	if len(items) != 4 || len(chapters) != 4 {
		t.Fatalf("the tape has %d items and %d chapters, want 4 and 4 (the empty slot is skipped)", len(items), len(chapters))
	}

	t.Run("V1_container", func(t *testing.T) {
		if _, err := mediatool.Run(context.Background(), m.tools.FLAC, []string{"--silent", "--test", path}, mediatool.RunOpts{Timeout: time.Minute, MaxStdout: 1 << 10}); err != nil {
			t.Fatal("flac --test:", err)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != tp["sha256"] || float64(len(data)) != tp["bytes"].(float64) {
			t.Error("the served file is not the file the tape row describes")
		}
		if fn := obj(tp["download"])["filename"].(string); !strings.HasSuffix(fn, ".flac") || strings.ContainsAny(fn, `/\"`) {
			t.Errorf("download filename %q", fn)
		}
	})
	var sum int64
	t.Run("V2_samples", func(t *testing.T) {
		for i, c := range chapters {
			ch := obj(c)
			n := int64(ch["samples"].(float64))
			sum += n
			if ch["index"] != float64(i+1) {
				t.Errorf("chapter %d index %v", i+1, ch["index"])
			}
			src := l2Plant[i].source
			s := m.audio.Source(t, src)
			if s.Rate == 48000 && s.Channels == 2 && !s.Lossy && n != s.Samples {
				t.Errorf("%s: %d samples, the source has %d", src, n, s.Samples)
			}
			if d := math.Abs(float64(n) - s.Seconds*48000); d > 48000*0.1 {
				t.Errorf("%s: %d samples is %.0f off the source length", src, n, d)
			}
		}
		si := info.StreamInfo
		if si.SampleRate != 48000 || si.BitsPerSample != 24 || si.Channels != 2 || int64(si.TotalSamples) != sum {
			t.Errorf("stream info %+v, chapters sum %d", si, sum)
		}
		if info.Count(flacmeta.BlockSeekTable) != 1 {
			t.Error("no seek table")
		}
	})
	t.Run("V3_gapless", func(t *testing.T) {
		var want []byte
		for i, it := range items {
			s := m.audio.Source(t, l2Plant[i].source)
			pcm := m.decodeS32(t, m.audio.Abs(s))
			v := make([]int32, len(pcm)/4)
			for k := range v {
				v[k] = le32(pcm, k) >> 8
			}
			cdb := int(math.Round(obj(it)["gain_db"].(float64) * 100))
			if clipped := tape.ApplyGain(v, v, cdb); clipped != 0 {
				t.Fatalf("item %d clips", i+1)
			}
			for _, x := range v {
				want = append(want, 0, byte(x), byte(x>>8), byte(x>>16))
			}
		}
		if got := m.decodeS32(t, path); sha256.Sum256(got) != sha256.Sum256(want) {
			t.Errorf("the decoded tape differs from the concatenation of the gained sources (%d vs %d bytes)", len(got), len(want))
		}
	})
	t.Run("V4_chapters", func(t *testing.T) {
		cue, ok, err := flacmeta.ReadCuesheet(bytes.NewReader(data), int64(len(data)), info)
		if err != nil || !ok || len(cue.Tracks) != 4 || cue.LeadOut != uint64(sum) {
			t.Fatal(cue, ok, err)
		}
		pc, err := tape.ParseChapterComments(info.Comments)
		if err != nil || len(pc) != 4 {
			t.Fatal(pc, err)
		}
		preview := obj(obj(m.Human(t).OK(t, "GET", "/api/records/"+l2ID(t, m.Rig, "records", l2R1)+"?include=preview", nil))["preview"])
		var start int64
		for i, c := range chapters {
			ch := obj(c)
			if int64(cue.Tracks[i].Offset) != start || pc[i].MS != start*1000/48000 || int64(ch["start_ms"].(float64)) != pc[i].MS {
				t.Errorf("chapter %d: block %d, comment %d ms, view %v ms, want sample %d", i+1, cue.Tracks[i].Offset, pc[i].MS, ch["start_ms"], start)
			}
			if want := obj(preview["chapters"].([]any)[i])["name"]; pc[i].Name != want {
				t.Errorf("chapter %d is named %q in the file and %q in the publish preview", i+1, pc[i].Name, want)
			}
			start += int64(ch["samples"].(float64))
		}
	})
	t.Run("V5_loudness", func(t *testing.T) {
		var start int64
		for i, c := range chapters {
			ch := obj(c)
			n := int64(ch["samples"].(float64))
			it := obj(items[i])
			pred, ok := it["predicted_lufs"].(float64)
			if st := it["status"]; !ok || st == "cut_clamped" || st == "boost_clamped" {
				start += n
				continue
			}
			seg := filepath.Join(t.TempDir(), "seg.wav")
			args := []string{"-nostdin", "-hide_banner", "-v", "error", "-i", "file:" + path, "-af", fmt.Sprintf("atrim=start_sample=%d:end_sample=%d", start, start+n),
				"-c:a", "pcm_s24le", "-f", "wav", "file:" + seg}
			if _, err := mediatool.Run(context.Background(), m.tools.FFmpeg, args, mediatool.RunOpts{Timeout: time.Minute, MaxStdout: 1 << 10}); err != nil {
				t.Fatal(err)
			}
			l, err := m.an.Loudness(context.Background(), seg)
			if err != nil {
				t.Fatal(err)
			}
			tol := 0.5
			if it["lossy"] == true {
				tol = 0.7
			}
			if d := math.Abs(l.IntegratedLUFS - pred); d > tol {
				t.Errorf("chapter %d (%v): %.2f LUFS, predicted %.2f", i+1, it["status"], l.IntegratedLUFS, pred)
			}
			start += n
		}
	})
	t.Run("V6_lossy_label", func(t *testing.T) {
		joined := strings.Join(info.Comments, "\n")
		for _, w := range []string{"MM_CONTAINS_LOSSY_SOURCES=true", "Contains lossy sources (1 of 4 tracks)", "CHAPTER004NAME=Synthetic Take E1" + tape.LossySuffix} {
			if !strings.Contains(joined, w) {
				t.Errorf("tag %q missing", w)
			}
		}
		pc, err := tape.ParseChapterComments(info.Comments)
		if err != nil || len(pc) != 4 {
			t.Fatal(pc, err)
		}
		for i, c := range pc {
			if got := strings.HasSuffix(c.Name, tape.LossySuffix); got != (i == 3) {
				t.Errorf("chapter %d %q: lossy label = %v", i+1, c.Name, got)
			}
		}
		if tp["contains_lossy"] != true || tp["lossy_sources"] != float64(1) {
			t.Errorf("view: contains_lossy %v lossy_sources %v", tp["contains_lossy"], tp["lossy_sources"])
		}
	})
	t.Run("V7_tags_and_cover", func(t *testing.T) {
		joined := strings.Join(info.Comments, "\n")
		for _, w := range []string{"TITLE=" + l2R1, "ALBUM=" + l2R1, "ARTIST=metamusic", "MM_TAPE_VERSION=", "MM_RECORD_ID=" + l2ID(t, m.Rig, "records", l2R1), "REPLAYGAIN_ALBUM_GAIN="} {
			if !strings.Contains(joined, w) {
				t.Errorf("tag %q missing", w)
			}
		}
		pics, err := flacmeta.ReadPictures(bytes.NewReader(data), int64(len(data)), info)
		if err != nil || len(pics) != 1 || pics[0].W != 512 || pics[0].H != 512 || pics[0].Type != 3 {
			t.Fatalf("picture: %v %+v", err, pics)
		}
		for _, c := range info.Comments {
			if strings.HasPrefix(c, "MM_") && strings.Contains(c, "Synthetic Curation") {
				t.Errorf("a Muse string is in tag %q", c)
			}
		}
	})
	t.Run("V8_masters_untouched", func(t *testing.T) {
		after := snapshotMasters(t, m.Rig)
		for id, b := range before {
			if a := after[id]; a != b {
				t.Errorf("master of %s changed by the export: %+v -> %+v", id, b, a)
			}
		}
		planted := 0
		for _, p := range l2Plant {
			if b := before[l2Take(t, m.Rig, p.take)]; b.Exists && b.Mode == 0o444 {
				planted++
			}
		}
		if planted != len(l2Plant) {
			t.Errorf("%d of %d planted masters are on disk with mode 0444", planted, len(l2Plant))
		}
	})
	// the tape row, the link and the quota bookkeeping: the same snapshot is one tape (reused)
	t.Run("reuse", func(t *testing.T) {
		again := obj(m.Human(t).OK(t, "POST", "/api/records/"+l2ID(t, m.Rig, "records", l2R1)+"/tapes", map[string]any{"accept_warnings": true}, "Idempotency-Key", "media-tape-2"))
		if again["id"] != tp["id"] || again["reused"] != true {
			t.Errorf("a second export of the same snapshot: %v", again)
		}
	})
}

// TestTape_DeterministicFile: the same snapshot rendered twice, the second time after the first tape expired, is the same file
// byte for byte (same sha256 and size): the key addresses the bytes.
func TestTape_DeterministicFile(t *testing.T) {
	m := newL2MediaRig(t, "")
	first := m.export(t, "det-1")
	a := m.fetch(t, first)
	if n, err := m.Services.Tapes.Sweep(context.Background(), -time.Hour); err != nil || n != 1 {
		t.Fatalf("sweep expired %d tapes (%v), want 1", n, err)
	}
	second := m.export(t, "det-2")
	if second["id"] == first["id"] {
		t.Fatal("the second export reused the expired tape")
	}
	b := m.fetch(t, second)
	if !bytes.Equal(a, b) || first["sha256"] != second["sha256"] || first["bytes"] != second["bytes"] {
		t.Errorf("two renders of one snapshot differ: %v vs %v (%d vs %d bytes)", first["sha256"], second["sha256"], len(a), len(b))
	}
}

// TestTape_FlacVersionOnce: `flac --version` is asked once when the services are built (it is part of the tape key), never per
// export: two exports and a sweep later the wrapper has seen exactly one --version call and several encodes and tests.
func TestTape_FlacVersionOnce(t *testing.T) {
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	shim := filepath.Join(dir, "flac")
	script := "#!/bin/sh\necho \"$@\" >> '" + log + "'\nexec '" + testtools.Tools().FLAC + "' \"$@\"\n"
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil { //nolint:gosec // a test wrapper must be executable
		t.Fatal(err)
	}
	m := newL2MediaRig(t, shim)
	calls := func() (version, other int) {
		b, _ := os.ReadFile(log)
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			switch {
			case line == "":
			case strings.Contains(line, "--version"):
				version++
			default:
				other++
			}
		}
		return version, other
	}
	if v, o := calls(); v != 1 || o != 0 {
		t.Fatalf("after Build: %d --version calls and %d others, want 1 and 0", v, o)
	}
	m.export(t, "ver-1")
	if _, err := m.Services.Tapes.Sweep(context.Background(), -time.Hour); err != nil {
		t.Fatal(err)
	}
	m.export(t, "ver-2")
	v, o := calls()
	if v != 1 {
		t.Errorf("%d --version calls after two exports, want exactly 1", v)
	}
	if o < 2 {
		t.Errorf("only %d other flac calls: the wrapper was not behind the pipeline", o)
	}
}

// TestImmutableMastersUntouched_Tape: a tape export leaves every master's recorded hash, size and file (content, mtime, mode
// 0444) exactly as it was (invariant 7); the export reads masters and writes only under derived/tapes and derived/covers.
func TestImmutableMastersUntouched_Tape(t *testing.T) {
	m := newL2MediaRig(t, "")
	before := snapshotMasters(t, m.Rig)
	m.export(t, "imm-tape")
	after := snapshotMasters(t, m.Rig)
	if len(after) != len(before) || len(before) == 0 {
		t.Fatalf("master set %d -> %d", len(before), len(after))
	}
	onDisk := 0
	for id, b := range before {
		if a := after[id]; a != b {
			t.Errorf("master of %s changed: %+v -> %+v", id, b, a)
		}
		if b.Exists {
			onDisk++
			if b.Mode != 0o444 {
				t.Errorf("master of %s has mode %v, want 0444", id, b.Mode)
			}
		}
	}
	if onDisk < len(l2Plant) {
		t.Errorf("%d masters on disk, want at least the %d planted", onDisk, len(l2Plant))
	}
}
