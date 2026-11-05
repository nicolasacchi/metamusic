package clip_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/media/clip"
	"github.com/nicolasacchi/metamusic/internal/media/clip/cliptest"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

const refFrames = 1_440_000

func baseRef() clip.RefSpec {
	return clip.RefSpec{
		MasterSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		MasterPath:   "audio/masters/01/0123.flac",
		SampleRate:   44100, Channels: 2, FromMS: 1000, ToMS: 13000,
	}
}

func TestRefKey_Golden(t *testing.T) {
	t.Parallel()
	sp := baseRef()
	doc := fmt.Sprintf(`{"channels":2,"def":"mm-refclip/1","from_ms":1000,"master_sha256":%q,"rate":44100,"tile":"tile/1","to_ms":13000,"tool":"ffmpeg n9.0.1"}`, sp.MasterSHA256)
	sum := sha256.Sum256([]byte(doc))
	if got, want := clip.RefKey(sp, "ffmpeg n9.0.1"), hex.EncodeToString(sum[:]); got != want {
		t.Errorf("RefKey = %s, want %s", got, want)
	}
}

func TestRefKey_EveryFieldMatters(t *testing.T) {
	t.Parallel()
	base := clip.RefKey(baseRef(), "t")
	if clip.RefKey(baseRef(), "t") != base {
		t.Fatal("RefKey is not deterministic")
	}
	seen := map[string]string{base: "base"}
	for name, mut := range map[string]func(*clip.RefSpec){
		"sha": func(s *clip.RefSpec) { s.MasterSHA256 = strings.Repeat("a", 64) }, "from": func(s *clip.RefSpec) { s.FromMS += 100 },
		"to": func(s *clip.RefSpec) { s.ToMS += 100 }, "rate": func(s *clip.RefSpec) { s.SampleRate = 48000 },
		"channels": func(s *clip.RefSpec) { s.Channels = 1 },
	} {
		sp := baseRef()
		mut(&sp)
		k := clip.RefKey(sp, "t")
		if prev, dup := seen[k]; dup {
			t.Errorf("%s collides with %s", name, prev)
		}
		seen[k] = name
	}
	sp := baseRef()
	sp.MasterPath = "elsewhere.flac"
	if clip.RefKey(sp, "t") != base {
		t.Error("the master path must not be part of the key")
	}
}

func TestReference_ToolVersionChangesKey(t *testing.T) {
	t.Parallel()
	if clip.RefKey(baseRef(), "ffmpeg a") == clip.RefKey(baseRef(), "ffmpeg b") {
		t.Error("the tool version does not change the key")
	}
}

func TestReference_KeyNeverCollidesWithDuelKey(t *testing.T) {
	t.Parallel()
	// A 30 s duel window and a 30 s reference window over the same master, rate, channels and tool.
	rs := clip.RefSpec{MasterSHA256: baseRef().MasterSHA256, SampleRate: 48000, Channels: 2, FromMS: 0, ToMS: 30000}
	ds := clip.Spec{MasterSHA256: rs.MasterSHA256, SampleRate: 48000, Channels: 2, W: clip.Window{StartMS: 0, EndMS: 30000}}
	if clip.RefKey(rs, "t") == clip.Key(ds, "t") {
		t.Error("reference and duel keys collide")
	}
}

func TestReference_DefaultTail20s(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ dur, from, to int64 }{
		{100000, 80000, 100000}, {12000, 0, 12000}, {20000, 0, 20000}, {20050, 0, 20000}, {20100, 100, 20100}, {1500, 0, 1500},
	} {
		if f, e := clip.RefTailWindow(c.dur); f != c.from || e != c.to {
			t.Errorf("RefTailWindow(%d) = %d..%d, want %d..%d", c.dur, f, e, c.from, c.to)
		}
	}
}

// refMaster is a 40 s master whose every sample differs (a rising tone), so tiling and cutting are checkable.
func refMaster(t testing.TB, e *env, tools mediatool.Tools, name string, rate, channels int) clip.RefSpec {
	t.Helper()
	expr := "0.5*sin(2*PI*(200+t*40)*t)"
	if channels == 2 {
		expr += "|0.5*sin(2*PI*(300+t*30)*t)"
	}
	sp := lavfiMaster(t, e, tools, "audio/masters/"+name+".flac", expr, rate, 40)
	return clip.RefSpec{MasterSHA256: sp.MasterSHA256, MasterPath: sp.MasterPath, SampleRate: rate, Channels: channels, FromMS: 5000, ToMS: 17000}
}

func TestReference_Exactly30s_Table(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	s, tools := newReal(t, e, clip.Options{CacheBytes: 1 << 30})
	for _, rate := range []int{48000, 44100} {
		ref := refMaster(t, e, tools, fmt.Sprintf("tab%d", rate), rate, 2)
		master := decode(t, tools, e.abs(ref.MasterPath), 2)
		for _, c := range []struct{ from, to int64 }{{5000, 7000}, {5000, 7500}, {5000, 17000}, {5000, 34900}, {5000, 35000}, {0, 30000}} {
			t.Run(fmt.Sprintf("%d_%d-%d", rate, c.from, c.to), func(t *testing.T) {
				sp := ref
				sp.FromMS, sp.ToMS = c.from, c.to
				o, err := s.Reference(context.Background(), sp)
				if err != nil {
					t.Fatal(err)
				}
				p := e.abs(o.RelPath)
				if si := streamInfo(t, p); si.TotalSamples != refFrames || si.SampleRate != 48000 || si.BitsPerSample != 24 || si.Channels != 2 {
					t.Fatalf("streaminfo = %+v", si)
				}
				if o.DurationS != 30 || o.Channels != 2 || o.Key != clip.RefKey(sp, s.ToolVersion()) {
					t.Errorf("object = %+v", o.Object)
				}
				raw, _ := os.ReadFile(p)
				if sum := sha256.Sum256(raw); o.SHA256 != hex.EncodeToString(sum[:]) {
					t.Errorf("SHA256 = %s is not the file digest", o.SHA256)
				}
				if flac, err := exec.LookPath("flac"); err == nil {
					if out, err := exec.Command(flac, "--test", "--silent", p).CombinedOutput(); err != nil { //nolint:gosec // system flac on a test file
						t.Errorf("flac --test: %v: %s", err, out)
					}
				}
				x := decode(t, tools, p, 2)
				if len(x) != 2*refFrames {
					t.Fatalf("decoded %d samples", len(x))
				}
				win := (c.to - c.from) * 48 // output frames of one window
				if rate == 48000 {
					off := c.from * 48 * 2
					for i := range 2 * min(win, 4800) { // the head is the raw master window
						if x[i] != master[off+int64(i)] {
							t.Fatalf("head sample %d = %v, master %v", i, x[i], master[off+int64(i)])
						}
					}
				}
				for i := 2 * win; i < int64(len(x)); i++ { // every later frame repeats the window
					if x[i] != x[i%(2*win)] {
						t.Fatalf("sample %d = %v, tile rule says %v", i, x[i], x[i%(2*win)])
					}
				}
			})
		}
	}
}

func TestReference_Mono(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	s, tools := newReal(t, e, clip.Options{})
	ref := refMaster(t, e, tools, "mono", 48000, 1)
	o, err := s.Reference(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if si := streamInfo(t, e.abs(o.RelPath)); si.Channels != 1 || si.TotalSamples != refFrames || o.Channels != 1 {
		t.Errorf("streaminfo = %+v, object %+v", si, o.Object)
	}
}

func TestReference_Deterministic(t *testing.T) {
	t.Parallel()
	for _, rate := range []int{48000, 44100} {
		e1, e2 := newEnv(t), newEnv(t)
		s1, tools := newReal(t, e1, clip.Options{})
		s2, _ := newReal(t, e2, clip.Options{})
		r1 := refMaster(t, e1, tools, "det", rate, 2)
		r2 := refMaster(t, e2, tools, "det", rate, 2)
		a, err := s1.Reference(context.Background(), r1)
		if err != nil {
			t.Fatal(err)
		}
		b, err := s2.Reference(context.Background(), r2)
		if err != nil {
			t.Fatal(err)
		}
		again, err := s1.Reference(context.Background(), r1)
		if err != nil {
			t.Fatal(err)
		}
		if a.SHA256 == "" || a.SHA256 != b.SHA256 || a.SHA256 != again.SHA256 {
			t.Errorf("rate %d: sha256 %s / %s / %s differ", rate, a.SHA256, b.SHA256, again.SHA256)
		}
	}
}

func TestReference_CacheHitAndEviction(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	s, tools := newReal(t, e, clip.Options{CacheBytes: 1 << 30})
	ref := refMaster(t, e, tools, "hit", 48000, 2)
	first, err := s.Reference(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	// A service whose ffmpeg cannot run answers from the cache.
	cold := clip.New(e.root, e.dir, mediatool.Tools{FFmpeg: filepath.Join(t.TempDir(), "missing")}, mustAnalyzer(t, tools), nil, nil, clip.Options{CacheBytes: 1 << 30})
	hit, err := cold.Reference(context.Background(), ref)
	if err != nil {
		t.Fatalf("cache hit: %v", err)
	}
	if hit.SHA256 != first.SHA256 || hit.Key != first.Key || hit.Bytes != first.Bytes {
		t.Errorf("hit %+v differs from %+v", hit, first)
	}
	if n := len(e.workDirs(t)); n != 0 {
		t.Errorf("%d work dirs left behind", n)
	}
	// Under a tiny cap the next render evicts the oldest unpinned file, the reference clip first (it is the oldest).
	old := first.RelPath
	if err := os.Chtimes(e.abs(old), mustTime(2020), mustTime(2020)); err != nil {
		t.Fatal(err)
	}
	small := clip.New(e.root, e.dir, tools, mustAnalyzer(t, tools), nil, nil, clip.Options{CacheBytes: first.Bytes / 2})
	other := ref
	other.ToMS = 9000
	if _, err := small.Reference(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(e.abs(old)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the oldest reference clip survived eviction: %v", err)
	}
	// Pinned keys survive (the open-duel mechanism covers reference clips too).
	pinned := clip.New(e.root, e.dir, tools, mustAnalyzer(t, tools), nil, nil, clip.Options{CacheBytes: 10,
		Pinned: func(context.Context) (map[string]struct{}, error) {
			return map[string]struct{}{clip.RefKey(other, small.ToolVersion()): {}}, nil
		}})
	third := ref
	third.ToMS = 11000
	if _, err := pinned.Reference(context.Background(), third); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(e.abs(relOf(clip.RefKey(other, small.ToolVersion())))); err != nil {
		t.Errorf("the pinned reference clip was evicted: %v", err)
	}
}

func TestReference_Concurrent_OneRender(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	s, tools := newReal(t, e, clip.Options{})
	ref := refMaster(t, e, tools, "conc", 48000, 2)
	res := make(chan string, 4)
	for range 4 {
		go func() {
			o, err := s.Reference(context.Background(), ref)
			if err != nil {
				res <- err.Error()
				return
			}
			res <- o.SHA256
		}()
	}
	first := <-res
	for range 3 {
		if got := <-res; got != first {
			t.Errorf("concurrent results differ: %s vs %s", got, first)
		}
	}
	if n := len(e.cachedPaths(t)); n != 1 {
		t.Errorf("%d cached files, want 1", n)
	}
}

func TestReference_SilentRefused(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	logf := filepath.Join(t.TempDir(), "argv.log")
	bin := filepath.Join(t.TempDir(), "ffmpeg")
	writeFakeTool(t, bin, "echo called > '"+logf+"'\nexit 1\n")
	an := cliptest.NewAnalyzer()
	sp := e.fakeMaster(t, "silent")
	an.SetMeasure(sp.MasterSHA256, clip.Measure{IntegratedLUFS: -70, TruePeakDBTP: -90})
	s := clip.New(e.root, e.dir, mediatool.Tools{FFmpeg: bin}, an, nil, nil, clip.Options{})
	_, err := s.Reference(context.Background(), clip.RefSpec{MasterSHA256: sp.MasterSHA256, MasterPath: sp.MasterPath, SampleRate: 48000, Channels: 2, FromMS: 1000, ToMS: 9000})
	var pe *problem.Error
	if !errors.As(err, &pe) || pe.Code != "CLIP_REFERENCE_SILENT" || pe.Status != 422 {
		t.Fatalf("err = %v, want 422 CLIP_REFERENCE_SILENT", err)
	}
	if _, err := os.Stat(logf); err == nil {
		t.Error("ffmpeg ran for a silent window")
	}
	if n := len(e.cachedPaths(t)) + len(e.workDirs(t)); n != 0 {
		t.Errorf("%d files left behind", n)
	}
}

func TestReference_SilentRefused_RealFFmpeg(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	s, tools := newReal(t, e, clip.Options{})
	sp := lavfiMaster(t, e, tools, "audio/masters/silence.flac", "0|0", 48000, 20)
	_, err := s.Reference(context.Background(), clip.RefSpec{MasterSHA256: sp.MasterSHA256, MasterPath: sp.MasterPath, SampleRate: 48000, Channels: 2, FromMS: 1000, ToMS: 9000})
	var pe *problem.Error
	if !errors.As(err, &pe) || pe.Code != "CLIP_REFERENCE_SILENT" {
		t.Fatalf("err = %v, want CLIP_REFERENCE_SILENT", err)
	}
}

func TestReference_Validation_Table(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	s, tools := newReal(t, e, clip.Options{})
	ok := refMaster(t, e, tools, "val", 48000, 2) // a 40 s master
	for _, c := range []struct {
		name string
		mut  func(*clip.RefSpec)
		code string
	}{
		{"too short", func(r *clip.RefSpec) { r.ToMS = r.FromMS + 1900 }, "CLIP_REFERENCE_WINDOW"},
		{"too long", func(r *clip.RefSpec) { r.ToMS = r.FromMS + 30100 }, "CLIP_REFERENCE_WINDOW"},
		{"off grid from", func(r *clip.RefSpec) { r.FromMS = 5050 }, "CLIP_REFERENCE_WINDOW"},
		{"off grid to", func(r *clip.RefSpec) { r.ToMS = 17050 }, "CLIP_REFERENCE_WINDOW"},
		{"negative", func(r *clip.RefSpec) { r.FromMS, r.ToMS = -1000, 5000 }, "CLIP_REFERENCE_WINDOW"},
		{"inverted", func(r *clip.RefSpec) { r.FromMS, r.ToMS = 9000, 5000 }, "CLIP_REFERENCE_WINDOW"},
		{"past the end of the master", func(r *clip.RefSpec) { r.FromMS, r.ToMS = 30000, 42000 }, "CLIP_REFERENCE_WINDOW"},
		{"three channels", func(r *clip.RefSpec) { r.Channels = 3 }, "CLIP_UNSUPPORTED_CHANNELS"},
		{"96 kHz", func(r *clip.RefSpec) { r.SampleRate = 96000 }, "CLIP_UNSUPPORTED_RATE"},
		{"32 kHz", func(r *clip.RefSpec) { r.SampleRate = 32000 }, "CLIP_UNSUPPORTED_RATE"},
	} {
		t.Run(c.name, func(t *testing.T) {
			sp := ok
			c.mut(&sp)
			_, err := s.Reference(context.Background(), sp)
			var pe *problem.Error
			if !errors.As(err, &pe) || pe.Code != c.code || pe.Status != 422 {
				t.Fatalf("err = %v, want 422 %s", err, c.code)
			}
		})
	}
	if _, err := s.Reference(context.Background(), clip.RefSpec{}); err == nil {
		t.Error("an empty spec is accepted")
	}
	if n := len(e.cachedPaths(t)) + len(e.workDirs(t)); n != 0 {
		t.Errorf("%d files left behind by refused requests", n)
	}
}

// TestArgv_OnlyWorkPaths_Reference runs the render against a fake ffmpeg that logs its argv.
func TestArgv_OnlyWorkPaths_Reference(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		rate   int
		from   int64
		to     int64
		wantAF string
	}{
		{"48k 12s", 48000, 5000, 17000,
			"atrim=start_sample=240000:end_sample=816000,asetpts=PTS-STARTPTS,aloop=loop=-1:size=576000:start=0,atrim=end_sample=1440000,asetpts=PTS-STARTPTS"},
		{"44.1k 2s", 44100, 5000, 7000,
			"atrim=start_sample=220500:end_sample=308700,asetpts=PTS-STARTPTS,aresample=48000:resampler=swr,atrim=end_sample=96000,apad=whole_len=96000,aloop=loop=-1:size=96000:start=0,atrim=end_sample=1440000,asetpts=PTS-STARTPTS"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			logf := filepath.Join(t.TempDir(), "argv.log")
			bin := filepath.Join(t.TempDir(), "ffmpeg")
			writeFakeTool(t, bin, "{ printf '%s\\n' \"$@\"; } > '"+logf+"'\nexit 1\n")
			m := e.fakeMaster(t, "refargv")
			sp := clip.RefSpec{MasterSHA256: m.MasterSHA256, MasterPath: m.MasterPath, SampleRate: c.rate, Channels: 2, FromMS: c.from, ToMS: c.to}
			s := clip.New(e.root, e.dir, mediatool.Tools{FFmpeg: bin}, cliptest.NewAnalyzer(), nil, nil, clip.Options{})
			if _, err := s.Reference(context.Background(), sp); err == nil {
				t.Fatal("the fake ffmpeg fails; Reference must too")
			}
			b, err := os.ReadFile(logf)
			if err != nil {
				t.Fatal(err)
			}
			args := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
			var in string
			for i, a := range args {
				if a == "-i" {
					in = args[i+1]
				}
			}
			out := args[len(args)-1]
			wd := "file:" + e.dir + "/incoming/clip-"
			if !strings.HasPrefix(in, wd) || !strings.HasSuffix(in, "/master.flac") || !strings.HasPrefix(out, wd) || !strings.HasSuffix(out, "/clip.flac") {
				t.Fatalf("paths: in %q out %q", in, out)
			}
			want := []string{"-nostdin", "-hide_banner", "-nostats", "-v", "error", "-protocol_whitelist", "file,pipe", "-i", in, "-map", "0:a:0",
				"-map_metadata", "-1", "-fflags", "+bitexact", "-flags:a", "+bitexact", "-af", c.wantAF,
				"-c:a", "flac", "-sample_fmt", "s32", "-bits_per_raw_sample", "24", "-compression_level", "5", "-f", "flac", out}
			if !slices.Equal(args, want) {
				t.Errorf("argv =\n%q\nwant\n%q", args, want)
			}
			if pw, ii := slices.Index(args, "-protocol_whitelist"), slices.Index(args, "-i"); pw < 0 || pw > ii {
				t.Error("-protocol_whitelist must precede -i")
			}
			for _, a := range args {
				if strings.HasPrefix(a, "/") {
					t.Errorf("bare absolute path in argv: %q", a)
				}
			}
			if n := len(e.cachedPaths(t)) + len(e.workDirs(t)); n != 0 {
				t.Errorf("%d files left behind by a failed render", n)
			}
		})
	}
}

func relOf(key string) string { return "derived/clips/" + key[:2] + "/" + key + ".flac" }

func mustTime(year int) time.Time { return time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC) }
