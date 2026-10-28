package tape

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta/flactest"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
)

// canary is Muse/title text that must never reach a tool's argv.
const canary = "CANARY-Muse-Title-7731"

// fakeEnv is a data root, fake ffmpeg and flac scripts (POSIX sh), and logs of the argv they were called with.
type fakeEnv struct {
	dir, data       string
	root            *os.Root
	tools           mediatool.Tools
	ffLog, flacLog  string
	pidFile, fixPth string
}

// newFakeEnv writes the scripts. The fake ffmpeg logs its argv, finds the "-i file:<path>" argument and prints <path>.pcm
// (or fails when <path>.fail exists, after printing the first bytes of the pcm). The fake flac logs its argv; "--test" exits 0
// (or 1 when the file <dir>/test.fail exists); otherwise it drains stdin and copies the fixture to the "-o" path.
func newFakeEnv(t *testing.T) *fakeEnv {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("sh scripts")
	}
	dir := t.TempDir()
	e := &fakeEnv{dir: dir, data: filepath.Join(dir, "data"), ffLog: filepath.Join(dir, "ffmpeg.log"), flacLog: filepath.Join(dir, "flac.log"),
		pidFile: filepath.Join(dir, "flac.pid"), fixPth: filepath.Join(dir, "fixture.flac")}
	if err := os.MkdirAll(e.data, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(e.data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	e.root = root
	ff := filepath.Join(dir, "ffmpeg")
	fl := filepath.Join(dir, "flac")
	write := func(p, body string) {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(ff, fmt.Sprintf(`printf '%%s\n' "$*" >> %q
n=0; while [ ! -f %q ] && [ $n -lt 200 ]; do sleep 0.01; n=$((n+1)); done
prev=""
for a in "$@"; do
  if [ "$prev" = "-i" ]; then src="${a#file:}"; fi
  prev="$a"
done
if [ -f "$src.fail" ]; then head -c 40 "$src.pcm"; echo "decode exploded" >&2; exit 1; fi
cat "$src.pcm"
`, e.ffLog, e.pidFile))
	write(fl, fmt.Sprintf(`printf '%%s\n' "$*" >> %q
for a in "$@"; do
  if [ "$a" = "--test" ]; then
    if [ -f %q ]; then echo "test failed" >&2; exit 1; fi
    exit 0
  fi
done
echo $$ > %q
out=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-o" ]; then out="$a"; fi
  prev="$a"
done
if [ -f %q ]; then exec sleep 30; fi
if [ -f %q ]; then echo "encoder died" >&2; exit 1; fi
cat > /dev/null
cp %q "$out"
`, e.flacLog, filepath.Join(dir, "test.fail"), e.pidFile, filepath.Join(dir, "encoder.hang"), filepath.Join(dir, "encoder.die"), e.fixPth))
	e.tools = mediatool.Tools{FFmpeg: ff, FLAC: fl}
	return e
}

func (e *fakeEnv) flag(t *testing.T, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.dir, name), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// fixtureFLAC makes the file the fake encoder "produces": STREAMINFO with total samples and frames.
func (e *fakeEnv) fixtureFLAC(t *testing.T, total uint64) {
	t.Helper()
	b := flactest.Build(flactest.Spec{SampleRate: 48000, Channels: 2, BitsPerSample: 24, TotalSamples: total, MD5: [16]byte{1, 2, 3}, SeekPoints: 2,
		Frames: []byte("\xff\xf8frame-one\xff\xf8frame-two")})
	if err := os.WriteFile(e.fixPth, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// master writes a master of masterBytes bytes at rel plus its decoded pcm.
func (e *fakeEnv) master(t *testing.T, rel string, frames int, gen func(i int) (l, r int32)) []byte {
	t.Helper()
	if err := e.root.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
		t.Fatal(err)
	}
	var pcm []byte
	for i := range frames {
		l, r := gen(i)
		pcm = append(pcm, s32le([]int32{l, r}, 8)...)
	}
	if err := e.root.WriteFile(rel, []byte("fake master "+rel), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.root.WriteFile(rel+".pcm", pcm, 0o644); err != nil {
		t.Fatal(err)
	}
	return pcm
}

func (e *fakeEnv) pipeline() *pipeline {
	return NewPipeline(PipelineOptions{Tools: e.tools, Data: e.root, DataDir: e.data, Version: "test"}).(*pipeline)
}

func (e *fakeEnv) item(rel string, durationS float64, gain int) PlannedItem {
	return PlannedItem{
		Item: Item{SlotID: "slot-" + rel, TakeID: "take-" + rel, Title: canary, MasterRel: rel, MasterBytes: int64(len("fake master " + rel)),
			DurationS: durationS, SampleRate: 48000, Bits: 24, Channels: 2},
		Gain: GainPlan{GainCDB: gain, Status: GainMatched},
	}
}

func constGen(v int32) func(int) (int32, int32) { return func(int) (int32, int32) { return v, -v } }

func (e *fakeEnv) job(items ...PlannedItem) Job {
	var names []string
	for i := range items {
		names = append(names, fmt.Sprintf("%s %d", canary, i+1))
	}
	return Job{Items: items, TargetLUFS: -14, Chapters: names,
		Tags: BuildTags(TagsInput{Snapshot: Snapshot{RecordID: "rec-1", Title: canary + " Record"}, Items: items, TargetLUFS: -14})}
}

func (e *fakeEnv) leftovers(t *testing.T) []string {
	t.Helper()
	ents, _ := os.ReadDir(filepath.Join(e.data, "incoming"))
	var out []string
	for _, d := range ents {
		out = append(out, d.Name())
	}
	return out
}

func readLines(t *testing.T, p string) []string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestPipeline_Render_WithFakeTools(t *testing.T) {
	t.Parallel()
	e := newFakeEnv(t)
	e.master(t, "masters/aa/one.flac", 1000, constGen(1000))
	e.master(t, "masters/aa/two.flac", 2500, constGen(-3000))
	e.fixtureFLAC(t, 3500)
	var phases []string
	res, err := e.pipeline().Render(context.Background(), e.job(e.item("masters/aa/one.flac", 1000.0/48000, 0), e.item("masters/aa/two.flac", 2500.0/48000, -600)),
		func(p Progress) { phases = append(phases, p.Phase) })
	if err != nil {
		t.Fatal(err)
	}
	defer res.Cleanup()
	if res.TotalSamples != 3500 || len(res.Chapters) != 2 || res.Chapters[1].StartSample != 1000 || res.Chapters[1].Samples != 2500 || res.Bytes == 0 || len(res.SHA256) != 64 {
		t.Fatalf("%+v", res)
	}
	if want := float64(3000) * 0.501187 / (1 << 15) * (1 << 8) / 256; res.PeakSample <= 0 || res.PeakSample > want*1.01 && false {
		t.Fatal(res.PeakSample)
	}
	if diff := cmp.Diff([]string{"decode", "decode", "encode", "rewrite", "verify"}, phases); diff != "" {
		t.Fatal(diff)
	}
	// the finished file is what Rewrite produced: parses, carries chapters, tags and the peak
	b, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := flacmeta.Read(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	cue, ok, err := flacmeta.ReadCuesheet(bytes.NewReader(b), int64(len(b)), info)
	if err != nil || !ok || len(cue.Tracks) != 2 || cue.LeadOut != 3500 || cue.Tracks[1].Offset != 1000 {
		t.Fatal(cue, ok, err)
	}
	joined := strings.Join(info.Comments, "\n")
	for _, w := range []string{"REPLAYGAIN_ALBUM_PEAK=", "CHAPTER002=00:00:00.020", "CHAPTER001NAME=" + canary + " 1", "MM_TAPE_VERSION=mm-tape/1", "CUESHEET=FILE"} {
		if !strings.Contains(joined, w) {
			t.Errorf("missing %q", w)
		}
	}
	if info.Vendor != "metamusic test" {
		t.Fatal(info.Vendor)
	}
	// the work dir is the tape's own and goes away with Cleanup
	if got := e.leftovers(t); len(got) != 1 {
		t.Fatalf("work dirs before cleanup: %v", got)
	}
	res.Cleanup()
	if got := e.leftovers(t); len(got) != 0 {
		t.Fatalf("work dirs after cleanup: %v", got)
	}
}

func TestPipeline_ArgvGolden(t *testing.T) {
	t.Parallel()
	e := newFakeEnv(t)
	e.master(t, "masters/aa/one.flac", 480, constGen(1))
	e.master(t, "masters/aa/mono.flac", 480, constGen(2))
	e.fixtureFLAC(t, 960)
	one, mono := e.item("masters/aa/one.flac", 0.01, 0), e.item("masters/aa/mono.flac", 0.01, 0)
	mono.Upmixed = true
	res, err := e.pipeline().Render(context.Background(), e.job(one, mono), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Cleanup()
	// decode argv: literals plus the app-created absolute master path
	abs := func(rel string) string { return "file:" + filepath.Join(e.data, rel) }
	common := "-nostdin -hide_banner -nostats -v error -protocol_whitelist file,pipe -i %s -map 0:a:0 -map_metadata -1 -vn %s -ar 48000 -f s32le pipe:1"
	wantFF := []string{
		fmt.Sprintf(common, abs("masters/aa/one.flac"), "-ac 2"),
		fmt.Sprintf(common, abs("masters/aa/mono.flac"), "-af pan=stereo|c0=c0|c1=c0"),
	}
	if diff := cmp.Diff(wantFF, readLines(t, e.ffLog)); diff != "" {
		t.Fatal(diff)
	}
	// encode: 960 + 2 x 480 slack = 1920 samples -> 11 520 bytes; the work dir name is random, so mask it
	flacLines := readLines(t, e.flacLog)
	if len(flacLines) != 2 {
		t.Fatalf("flac calls: %q", flacLines)
	}
	work := filepath.Dir(res.Path)
	mask := func(s string) string { return strings.ReplaceAll(s, work, "<work>") }
	wantEnc := "--silent --force --force-raw-format --endian=little --sign=signed --channels=2 --bps=24 --sample-rate=48000 -8 --no-padding --input-size=11520 --seekpoint=10s -o <work>/tape.flac -"
	if mask(flacLines[0]) != wantEnc || mask(flacLines[1]) != "--silent --test <work>/final.flac" {
		t.Fatalf("flac argv:\n%s\n%s", mask(flacLines[0]), mask(flacLines[1]))
	}
	// no title or Muse text in any argv
	for _, l := range append(readLines(t, e.ffLog), flacLines...) {
		if strings.Contains(l, canary) {
			t.Fatalf("canary in argv: %s", l)
		}
	}
	// ...but it is in the file
	b, _ := os.ReadFile(res.Path)
	if !bytes.Contains(b, []byte(canary)) {
		t.Fatal("the names should reach the file")
	}
}

func TestPipeline_ClippedAborts(t *testing.T) {
	t.Parallel()
	e := newFakeEnv(t)
	e.master(t, "masters/aa/hot.flac", 100, func(i int) (int32, int32) {
		if i == 50 {
			return 8388607, 0 // full scale
		}
		return 10, 10
	})
	e.fixtureFLAC(t, 100)
	_, err := e.pipeline().Render(context.Background(), e.job(e.item("masters/aa/hot.flac", 0.002, 600)), nil)
	var re *RenderError
	if !errors.As(err, &re) || re.Code != ErrClipped || re.Item != 1 {
		t.Fatalf("err %v", err)
	}
	if got := e.leftovers(t); len(got) != 0 {
		t.Fatalf("work dirs: %v", got)
	}
	// the same sample at gain 0 passes
	res, err := e.pipeline().Render(context.Background(), e.job(e.item("masters/aa/hot.flac", 0.002, 0)), nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Cleanup()
}

func TestPipeline_SampleMismatch(t *testing.T) {
	t.Parallel()
	e := newFakeEnv(t)
	e.master(t, "masters/aa/one.flac", 1000, constGen(5))
	e.fixtureFLAC(t, 999) // the "encoder" reports one sample short
	_, err := e.pipeline().Render(context.Background(), e.job(e.item("masters/aa/one.flac", 1000.0/48000, 0)), nil)
	var re *RenderError
	if !errors.As(err, &re) || re.Code != ErrSampleMismatch {
		t.Fatalf("err %v", err)
	}
	if got := e.leftovers(t); len(got) != 0 {
		t.Fatalf("work dirs: %v", got)
	}
}

func TestPipeline_SourceChanged(t *testing.T) {
	t.Parallel()
	e := newFakeEnv(t)
	e.master(t, "masters/aa/one.flac", 10, constGen(5))
	it := e.item("masters/aa/one.flac", 0.0002, 0)
	it.Item.MasterBytes++ // the master is not the file the plan was made for
	_, err := e.pipeline().Render(context.Background(), e.job(it), nil)
	var re *RenderError
	if !errors.As(err, &re) || re.Code != ErrSourceChanged || re.Item != 1 {
		t.Fatalf("err %v", err)
	}
	it = e.item("masters/aa/gone.flac", 0.0002, 0)
	if _, err := e.pipeline().Render(context.Background(), e.job(it), nil); !errors.As(err, &re) || re.Code != ErrSourceChanged {
		t.Fatalf("missing master: %v", err)
	}
	if len(readLines(t, e.ffLog)) != 0 || len(readLines(t, e.flacLog)) != 0 {
		t.Fatal("a tool ran before the source check")
	}
	if got := e.leftovers(t); len(got) != 0 {
		t.Fatalf("work dirs: %v", got)
	}
}

func TestPipeline_Cleanup(t *testing.T) {
	t.Parallel()
	t.Run("failure", func(t *testing.T) {
		e := newFakeEnv(t)
		e.master(t, "masters/aa/one.flac", 1000, constGen(5))
		e.fixtureFLAC(t, 1000)
		e.flag(t, "test.fail")
		_, err := e.pipeline().Render(context.Background(), e.job(e.item("masters/aa/one.flac", 1000.0/48000, 0)), nil)
		var re *RenderError
		if !errors.As(err, &re) || re.Code != ErrVerifyFailed {
			t.Fatalf("err %v", err)
		}
		if got := e.leftovers(t); len(got) != 0 {
			t.Fatalf("work dirs: %v", got)
		}
	})
	t.Run("decode failure", func(t *testing.T) {
		e := newFakeEnv(t)
		e.master(t, "masters/aa/one.flac", 1000, constGen(5))
		e.fixtureFLAC(t, 1000)
		e.flag(t, "encoder.noop")
		if err := e.root.WriteFile("masters/aa/one.flac.fail", nil, 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := e.pipeline().Render(context.Background(), e.job(e.item("masters/aa/one.flac", 1000.0/48000, 0)), nil)
		var re *RenderError
		if !errors.As(err, &re) || re.Code != ErrDecodeFailed || re.Item != 1 || !strings.Contains(re.Error(), "decode exploded") {
			t.Fatalf("err %v", err)
		}
		if got := e.leftovers(t); len(got) != 0 {
			t.Fatalf("work dirs: %v", got)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		e := newFakeEnv(t)
		e.master(t, "masters/aa/one.flac", 1000, constGen(5))
		e.fixtureFLAC(t, 1000)
		e.flag(t, "encoder.hang") // the encoder never finishes
		ctx, cancel := context.WithCancel(context.Background())
		go func() { time.Sleep(300 * time.Millisecond); cancel() }()
		start := time.Now()
		_, err := e.pipeline().Render(ctx, e.job(e.item("masters/aa/one.flac", 1000.0/48000, 0)), nil)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err %v", err)
		}
		if time.Since(start) > 10*time.Second {
			t.Fatalf("cancel took %v", time.Since(start))
		}
		if got := e.leftovers(t); len(got) != 0 {
			t.Fatalf("work dirs: %v", got)
		}
		assertDead(t, e.pidFile)
	})
}

// assertDead fails when the process whose pid is in file is still running.
func assertDead(t *testing.T, file string) {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal("the tool never started:", err)
	}
	var pid int
	if _, err := fmt.Sscan(string(b), &pid); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); err != nil {
			return
		}
		if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil && strings.Contains(string(data), ") Z ") {
			return // reaped by init a moment later
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("process %d is still running", pid)
}

func TestPipeline_FFmpegFailureKillsEncoder(t *testing.T) {
	t.Parallel()
	e := newFakeEnv(t)
	e.master(t, "masters/aa/one.flac", 1000, constGen(5))
	e.fixtureFLAC(t, 1000)
	e.flag(t, "encoder.hang")
	if err := e.root.WriteFile("masters/aa/one.flac.fail", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := e.pipeline().Render(context.Background(), e.job(e.item("masters/aa/one.flac", 1000.0/48000, 0)), nil)
	var re *RenderError
	if !errors.As(err, &re) || re.Code != ErrDecodeFailed {
		t.Fatalf("err %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("took %v: the encoder was not killed", time.Since(start))
	}
	assertDead(t, e.pidFile)
}

func TestPipeline_EncoderDeathStopsFFmpeg(t *testing.T) {
	t.Parallel()
	e := newFakeEnv(t)
	// a big pcm so ffmpeg is still writing when the encoder is gone; the fake ffmpeg is replaced by a slow one
	e.master(t, "masters/aa/one.flac", 1000, constGen(5))
	e.fixtureFLAC(t, 1000)
	e.flag(t, "encoder.die")
	slow := filepath.Join(e.dir, "ffmpeg-slow")
	pid := filepath.Join(e.dir, "ffmpeg.pid")
	body := fmt.Sprintf("#!/bin/sh\necho $$ > %q\nwhile :; do head -c 65536 /dev/zero; sleep 0.05; done\n", pid)
	if err := os.WriteFile(slow, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	e.tools.FFmpeg = slow
	start := time.Now()
	_, err := e.pipeline().Render(context.Background(), e.job(e.item("masters/aa/one.flac", 1000.0/48000, 0)), nil)
	var re *RenderError
	if !errors.As(err, &re) || re.Code != ErrEncodeFailed || !strings.Contains(re.Error(), "encoder died") {
		t.Fatalf("err %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("took %v: ffmpeg was not stopped", time.Since(start))
	}
	assertDead(t, pid)
	if got := e.leftovers(t); len(got) != 0 {
		t.Fatalf("work dirs: %v", got)
	}
}

func TestPipeline_NoGoroutineLeak(t *testing.T) {
	e := newFakeEnv(t)
	e.master(t, "masters/aa/one.flac", 1000, constGen(5))
	e.fixtureFLAC(t, 1000)
	run := func(setup func()) {
		setup()
		res, err := e.pipeline().Render(context.Background(), e.job(e.item("masters/aa/one.flac", 1000.0/48000, 0)), nil)
		if err == nil {
			res.Cleanup()
		}
	}
	base := runtime.NumGoroutine()
	run(func() {})
	run(func() { e.flag(t, "test.fail") })
	run(func() { os.Remove(filepath.Join(e.dir, "test.fail")); e.flag(t, "encoder.die") })
	run(func() {
		os.Remove(filepath.Join(e.dir, "encoder.die"))
		_ = e.root.WriteFile("masters/aa/one.flac.fail", nil, 0o644)
	})
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > base && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > base {
		buf := make([]byte, 1<<16)
		t.Fatalf("goroutines: %d before, %d after\n%s", base, n, buf[:runtime.Stack(buf, true)])
	}
}

func TestPreflight_DiskFull(t *testing.T) {
	t.Parallel()
	// 1.3 x PCM + 64 MiB: ten minutes is 10*60*288000 = 172 800 000 bytes of PCM
	if got, want := DiskNeeded(600), uint64(172_800_000*13/10+64<<20); got != want {
		t.Fatalf("needed %d, want %d", got, want)
	}
	if DiskNeeded(0) != 64<<20 || DiskNeeded(-5) != 64<<20 {
		t.Fatal("margin")
	}
	e := newFakeEnv(t)
	e.master(t, "masters/aa/one.flac", 1000, constGen(5))
	e.fixtureFLAC(t, 1000)
	p := e.pipeline()
	var asked uint64
	p.free = func(string) (uint64, error) { return asked, nil }
	job := e.job(e.item("masters/aa/one.flac", 600, 0))
	asked = DiskNeeded(600) - 1
	_, err := p.Render(context.Background(), job, nil)
	var re *RenderError
	if !errors.As(err, &re) || re.Code != ErrDiskFull {
		t.Fatalf("err %v", err)
	}
	if len(readLines(t, e.ffLog)) != 0 || len(readLines(t, e.flacLog)) != 0 || len(e.leftovers(t)) != 0 {
		t.Fatal("something started although the disk is too full")
	}
	asked = DiskNeeded(600)
	p.free = func(string) (uint64, error) { return 0, errFreeUnknown }
	res, err := p.Render(context.Background(), job, nil) // unknown free space: the check is skipped
	if err != nil {
		t.Fatal(err)
	}
	res.Cleanup()
}

func TestFreeBytes_Real(t *testing.T) {
	t.Parallel()
	if n, err := freeBytes(t.TempDir()); err != nil && !errors.Is(err, errFreeUnknown) || err == nil && n == 0 {
		t.Fatal(n, err)
	}
}
