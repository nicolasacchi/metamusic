package clip_test

import (
	"bytes"
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/media/clip"
	"github.com/nicolasacchi/metamusic/internal/media/clip/cliptest"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/synth"
)

const clickExpr = "if(lt(mod(n,24000),48),0.9,0)"

// clickMaster is a 40 s click track (a 1 ms click every 0.5 s) at the given rate.
func clickMaster(t testing.TB, e *env, tools mediatool.Tools, name string, rate, channels int) clip.Spec {
	t.Helper()
	expr := clickExpr
	if rate != 48000 {
		expr = "if(lt(mod(n,22050),44),0.9,0)"
	}
	if channels == 2 {
		expr += "|" + expr
	}
	return lavfiMaster(t, e, tools, "audio/masters/"+name+".flac", expr, rate, 40)
}

func newReal(t testing.TB, e *env, o clip.Options) (*clip.Service, mediatool.Tools) {
	t.Helper()
	tools, an := realTools(t)
	return clip.New(e.root, e.dir, tools, an, nil, nil, o), tools
}

func TestClip_SampleExactWindow(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	s, tools := newReal(t, e, clip.Options{CacheBytes: 1 << 30})
	sp := clickMaster(t, e, tools, "click", 48000, 2)
	o, err := s.Ensure(context.Background(), sp)
	if err != nil {
		t.Fatal(err)
	}
	si := streamInfo(t, e.abs(o.RelPath))
	if si.TotalSamples != 1_440_000 {
		t.Fatalf("TotalSamples = %d, want 1440000", si.TotalSamples)
	}
	x := decode(t, tools, e.abs(o.RelPath), 2)
	if len(x) != 2*1_440_000 {
		t.Fatalf("decoded %d samples", len(x))
	}
	var onsets []int
	for i := 1; i < 1_440_000; i++ {
		if math.Abs(float64(x[2*i])) > 0.5 && math.Abs(float64(x[2*(i-1)])) <= 0.5 {
			onsets = append(onsets, i)
		}
	}
	// clicks sit at 24000·k; click 0 is inside the fade-in (and its first sample is 0)
	var ks []int
	for _, on := range onsets {
		k := (on + 12000) / 24000
		if d := on - 24000*k; d < -2 || d > 2 {
			t.Errorf("click onset %d is %d samples off the grid", on, d)
		}
		ks = append(ks, k)
	}
	for k := 1; k <= 59; k++ {
		found := false
		for _, g := range ks {
			found = found || g == k
		}
		if !found {
			t.Errorf("click %d missing (onsets %v...)", k, ks[:min(len(ks), 5)])
			break
		}
	}
}

func TestClip_24bit48kNoTags(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	s, tools := newReal(t, e, clip.Options{})
	sp := clickMaster(t, e, tools, "tags", 48000, 2)
	o, err := s.Ensure(context.Background(), sp)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(e.abs(o.RelPath))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	st, _ := f.Stat()
	info, err := flacmeta.Read(f, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	if si := info.StreamInfo; si.BitsPerSample != 24 || si.SampleRate != 48000 || si.Channels != 2 {
		t.Errorf("streaminfo = %+v", si)
	}
	if len(info.Comments) != 0 {
		t.Errorf("comments = %v, want none (vendor only)", info.Comments)
	}
	raw, _ := os.ReadFile(e.abs(o.RelPath))
	for _, bad := range []string{"TITLE", "MM_", "ARTIST", "ENCODER"} {
		if bytes.Contains(raw[:min(len(raw), 4096)], []byte(bad)) {
			t.Errorf("clip header contains %q", bad)
		}
	}
	if o.Channels != 2 || o.DurationS != 30 || o.Bytes != st.Size() || o.Key != strings.TrimSuffix(filepath.Base(o.RelPath), ".flac") {
		t.Errorf("object = %+v", o)
	}
}

func TestClip_Resample44k(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	s, tools := newReal(t, e, clip.Options{})
	sp := clickMaster(t, e, tools, "r44", 44100, 2)
	o, err := s.Ensure(context.Background(), sp)
	if err != nil {
		t.Fatal(err)
	}
	si := streamInfo(t, e.abs(o.RelPath))
	if d := int64(si.TotalSamples) - 1_440_000; d < -1 || d > 1 || si.SampleRate != 48000 {
		t.Errorf("streaminfo = %+v", si)
	}
}

func TestClip_Mono(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	s, tools := newReal(t, e, clip.Options{})
	sp := clickMaster(t, e, tools, "mono", 48000, 1)
	o, err := s.Ensure(context.Background(), sp)
	if err != nil {
		t.Fatal(err)
	}
	if si := streamInfo(t, e.abs(o.RelPath)); si.Channels != 1 || o.Channels != 1 {
		t.Errorf("channels = %d / %d, want 1", si.Channels, o.Channels)
	}
}

func TestClip_FadesApplied(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	s, tools := newReal(t, e, clip.Options{})
	sp := lavfiMaster(t, e, tools, "audio/masters/dc.flac", "0.25|0.25", 48000, 40)
	o, err := s.Ensure(context.Background(), sp)
	if err != nil {
		t.Fatal(err)
	}
	x := decode(t, tools, e.abs(o.RelPath), 2)
	n := len(x)
	for _, v := range []float32{x[0], x[1], x[n-2], x[n-1]} {
		if math.Abs(float64(v)) >= 1e-3 {
			t.Errorf("edge sample %v not faded out", v)
		}
	}
	if mid := x[n/2]; math.Abs(float64(mid)-0.25) > 1e-3 {
		t.Errorf("mid sample = %v, want 0.25", mid)
	}
}

func TestClip_Deterministic(t *testing.T) {
	t.Parallel()
	tools, an := realTools(t)
	var keys [2]string
	var data [2][]byte
	for i := range keys {
		e := newEnv(t)
		sp := lavfiMaster(t, e, tools, "audio/masters/det.flac", "0.3*sin(2*PI*330*t)|0.2*sin(2*PI*441*t)", 48000, 40)
		sp.GainCDB = -350
		s := clip.New(e.root, e.dir, tools, an, nil, nil, clip.Options{})
		o, err := s.Ensure(context.Background(), sp)
		if err != nil {
			t.Fatal(err)
		}
		keys[i] = o.Key
		data[i], _ = os.ReadFile(e.abs(o.RelPath))
	}
	if keys[0] != keys[1] || !bytes.Equal(data[0], data[1]) {
		t.Errorf("renders differ: keys %v, equal bytes %v", keys, bytes.Equal(data[0], data[1]))
	}
}

// loggingFFmpeg wraps the real ffmpeg in a script that appends a line per invocation.
func loggingFFmpeg(t testing.TB, realBin string) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "calls.log")
	bin = filepath.Join(dir, "ffmpeg")
	script := "#!/bin/sh\necho call >> '" + log + "'\nexec '" + realBin + "' \"$@\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func calls(t testing.TB, log string) int {
	t.Helper()
	b, _ := os.ReadFile(log)
	return strings.Count(string(b), "call")
}

func TestClip_CacheHitRunsNoTool(t *testing.T) {
	t.Parallel()
	rt, _ := realTools(t)
	bin, log := loggingFFmpeg(t, rt.FFmpeg)
	tools := mediatool.Tools{FFmpeg: bin}
	e := newEnv(t)
	sp := lavfiMaster(t, e, rt, "audio/masters/hit.flac", "0.3*sin(2*PI*330*t)|0.2*sin(2*PI*441*t)", 48000, 40)
	an := mustAnalyzer(t, tools)
	s := clip.New(e.root, e.dir, tools, an, nil, nil, clip.Options{})
	before := calls(t, log)
	o1, err := s.Ensure(context.Background(), sp)
	if err != nil {
		t.Fatal(err)
	}
	if got := calls(t, log) - before; got != 1 {
		t.Fatalf("first Ensure ran %d tool calls, want 1", got)
	}
	o2, err := s.Ensure(context.Background(), sp)
	if err != nil {
		t.Fatal(err)
	}
	if got := calls(t, log) - before; got != 1 || o1 != o2 {
		t.Errorf("second Ensure: %d tool calls, objects equal %v", got-1, o1 == o2)
	}
}

func TestClip_ConcurrentSameKey(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	sp := e.fakeMaster(t, "conc")
	var renders atomic.Int32
	gate := make(chan struct{})
	s := clip.New(e.root, e.dir, mediatool.Tools{}, cliptest.NewAnalyzer(), nil, nil, clip.Options{
		Render: func(ctx context.Context, m, out string, sp clip.Spec) error {
			renders.Add(1)
			<-gate
			return cliptest.Render(ctx, m, out, sp)
		}})
	var wg sync.WaitGroup
	objs := make([]clip.Object, 8)
	errs := make([]error, 8)
	for i := range objs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			objs[i], errs[i] = s.Ensure(context.Background(), sp)
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()
	for i := range objs {
		if errs[i] != nil || objs[i] != objs[0] {
			t.Fatalf("caller %d: %+v, %v (want %+v)", i, objs[i], errs[i], objs[0])
		}
	}
	if n := renders.Load(); n != 1 {
		t.Errorf("renders = %d, want 1", n)
	}
	if got := len(e.cachedPaths(t)); got != 1 {
		t.Errorf("cache files = %d", got)
	}
}

func TestClip_CancelledCallerDoesNotFailWaiters(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	sp := e.fakeMaster(t, "cancel")
	var renders atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	s := clip.New(e.root, e.dir, mediatool.Tools{}, cliptest.NewAnalyzer(), nil, nil, clip.Options{
		Render: func(ctx context.Context, m, out string, sp clip.Spec) error {
			if renders.Add(1) == 1 {
				close(started)
			}
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			return cliptest.Render(ctx, m, out, sp)
		}})
	ctx1, cancel1 := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := s.Ensure(ctx1, sp); first <- err }()
	<-started
	second := make(chan error, 1)
	var obj clip.Object
	go func() { var err error; obj, err = s.Ensure(context.Background(), sp); second <- err }()
	time.Sleep(20 * time.Millisecond)
	cancel1()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("first caller: %v, want context.Canceled", err)
	}
	close(release)
	if err := <-second; err != nil {
		t.Fatalf("waiter: %v", err)
	}
	if _, err := os.Stat(e.abs(obj.RelPath)); err != nil {
		t.Errorf("clip file: %v", err)
	}
	if n := renders.Load(); n != 1 {
		t.Errorf("renders = %d, want 1", n)
	}
}

func measureClip(t testing.TB, tools mediatool.Tools, path string, samples int64) clip.Measure {
	t.Helper()
	m, err := mustAnalyzer(t, tools).WindowLoudness(context.Background(), path, 0, samples)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestClip_LoudnessMatched(t *testing.T) {
	t.Parallel()
	tools, an := realTools(t)
	e := newEnv(t)
	var specs [2]clip.Spec
	for i, lufs := range []float64{-10, -20} {
		rel := "audio/masters/lm" + itoa(i) + ".flac"
		if err := os.MkdirAll(filepath.Dir(e.abs(rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := synth.FLAC(context.Background(), tools, e.abs(rel), synth.Tone{LUFS: lufs, Seconds: 40, BPM: 120, Variant: i}, synth.StyleFFmpeg); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(e.abs(rel))
		specs[i] = e.putMasterBytes(rel, b, 48000, 2)
		specs[i].W = clip.Window{StartMS: 0, EndMS: 30000}
	}
	s := clip.New(e.root, e.dir, tools, an, nil, nil, clip.Options{})
	var ms [2]clip.Measure
	for i := range specs {
		m, err := s.Measure(context.Background(), specs[i])
		if err != nil {
			t.Fatal(err)
		}
		ms[i] = m
	}
	ga, gb, target, status := clip.MatchGains(ms[0], ms[1])
	if status != "matched" || target == nil {
		t.Fatalf("status = %q", status)
	}
	for i, g := range []int{ga, gb} {
		specs[i].GainCDB = g
		o, err := s.Ensure(context.Background(), specs[i])
		if err != nil {
			t.Fatal(err)
		}
		if m := measureClip(t, tools, e.abs(o.RelPath), 1_440_000); math.Abs(m.IntegratedLUFS-(-20)) > 0.2 {
			t.Errorf("clip %d measures %.2f LUFS, want -20.0 +- 0.2", i, m.IntegratedLUFS)
		}
	}
	if len(e.workDirs(t)) != 0 {
		t.Error("work dirs left behind")
	}
}

func TestClip_TargetAttenuateOnly(t *testing.T) {
	t.Parallel()
	tools, an := realTools(t)
	e := newEnv(t)
	rel := "audio/masters/ta.flac"
	if err := os.MkdirAll(filepath.Dir(e.abs(rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := synth.FLAC(context.Background(), tools, e.abs(rel), synth.Tone{LUFS: -20, Seconds: 40, BPM: 120}, synth.StyleFFmpeg); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(e.abs(rel))
	sp := e.putMasterBytes(rel, b, 48000, 2)
	sp.W = clip.Window{StartMS: 0, EndMS: 30000}
	s := clip.New(e.root, e.dir, tools, an, nil, nil, clip.Options{})
	m, err := s.Measure(context.Background(), sp)
	if err != nil {
		t.Fatal(err)
	}
	g, status := clip.TargetGain(m, -30)
	if status != "matched" {
		t.Fatalf("status = %q", status)
	}
	sp.GainCDB = g
	o, err := s.Ensure(context.Background(), sp)
	if err != nil {
		t.Fatal(err)
	}
	if got := measureClip(t, tools, e.abs(o.RelPath), 1_440_000); math.Abs(got.IntegratedLUFS-(-30)) > 0.2 {
		t.Errorf("clip measures %.2f LUFS, want -30.0 +- 0.2", got.IntegratedLUFS)
	}
	if g, status := clip.TargetGain(m, -10); g != 0 || status != "limited" {
		t.Errorf("TargetGain(-10) = %d, %q, want 0, limited", g, status)
	}
}

func TestClip_MasterUntouched(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	sp := e.fakeMaster(t, "frozen")
	fail := errors.New("boom")
	var failing atomic.Bool
	s := clip.New(e.root, e.dir, mediatool.Tools{}, cliptest.NewAnalyzer(), nil, nil, clip.Options{
		Render: func(ctx context.Context, m, out string, sp clip.Spec) error {
			if failing.Load() {
				return fail
			}
			return cliptest.Render(ctx, m, out, sp)
		}})
	before, _ := os.Stat(e.abs(sp.MasterPath))
	check := func(when string) {
		t.Helper()
		b, _ := os.ReadFile(e.abs(sp.MasterPath))
		st, err := os.Stat(e.abs(sp.MasterPath))
		if err != nil {
			t.Fatal(err)
		}
		if got := sha256Hex(b); got != sp.MasterSHA256 || st.Mode().Perm() != 0o444 || !st.ModTime().Equal(before.ModTime()) || nlink(st) != 1 {
			t.Errorf("%s: master changed (sha ok %v, mode %v, nlink %d)", when, got == sp.MasterSHA256, st.Mode().Perm(), nlink(st))
		}
	}
	if _, err := s.Ensure(context.Background(), sp); err != nil {
		t.Fatal(err)
	}
	check("after render")
	if _, err := s.Measure(context.Background(), sp); err != nil {
		t.Fatal(err)
	}
	check("after measure")
	failing.Store(true)
	sp.GainCDB = -100
	if _, err := s.Ensure(context.Background(), sp); err == nil {
		t.Fatal("failing render succeeded")
	}
	check("after failed render")
}

func TestClip_WorkDirRemoved(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	sp := e.fakeMaster(t, "work")
	var failing atomic.Bool
	s := clip.New(e.root, e.dir, mediatool.Tools{}, cliptest.NewAnalyzer(), nil, nil, clip.Options{
		Render: func(ctx context.Context, m, out string, sp clip.Spec) error {
			if failing.Load() {
				return errors.New("render failed")
			}
			return cliptest.Render(ctx, m, out, sp)
		}})
	if _, err := s.Ensure(context.Background(), sp); err != nil {
		t.Fatal(err)
	}
	if d := e.workDirs(t); len(d) != 0 {
		t.Errorf("work dirs after success: %v", d)
	}
	failing.Store(true)
	sp.GainCDB = -50
	_, err := s.Ensure(context.Background(), sp)
	var pe *problem.Error
	if !errors.As(err, &pe) || pe.Status != 500 || pe.Code != "INTERNAL" {
		t.Errorf("err = %v, want 500 INTERNAL", err)
	}
	if d := e.workDirs(t); len(d) != 0 {
		t.Errorf("work dirs after failure: %v", d)
	}
}

func TestClip_RejectsBadInput(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	good := e.fakeMaster(t, "bad")
	s := clip.New(e.root, e.dir, mediatool.Tools{}, cliptest.NewAnalyzer(), nil, nil, clip.Options{Render: cliptest.Render})
	for name, mut := range map[string]func(*clip.Spec){
		"channels": func(sp *clip.Spec) { sp.Channels = 6 },
		"rate":     func(sp *clip.Spec) { sp.SampleRate = 0 },
		"gain up":  func(sp *clip.Spec) { sp.GainCDB = 1 },
		"gain low": func(sp *clip.Spec) { sp.GainCDB = clip.MaxCutCDB - 1 },
		"window":   func(sp *clip.Spec) { sp.W = clip.Window{StartMS: 5000, EndMS: 5000} },
		"grid":     func(sp *clip.Spec) { sp.W.StartMS = 5050 },
	} {
		sp := good
		mut(&sp)
		for _, call := range []func() error{
			func() error { _, err := s.Ensure(context.Background(), sp); return err },
			func() error { _, err := s.Measure(context.Background(), sp); return err },
		} {
			var pe *problem.Error
			if err := call(); !errors.As(err, &pe) {
				t.Errorf("%s: err = %v, want *problem.Error", name, err)
			} else if name == "channels" && (pe.Status != 422 || pe.Code != "CLIP_UNSUPPORTED_CHANNELS") {
				t.Errorf("channels: %v", pe)
			}
		}
	}
	missing := good
	missing.MasterPath = "audio/masters/gone.flac"
	if _, err := s.Ensure(context.Background(), missing); !errors.Is(err, library.ErrMediaUnavailable) {
		t.Errorf("missing master: %v, want ErrMediaUnavailable", err)
	}
	// a render that writes a wrong stream is refused and not cached
	bad := clip.New(e.root, e.dir, mediatool.Tools{}, cliptest.NewAnalyzer(), nil, nil, clip.Options{
		Render: func(_ context.Context, _, out string, _ clip.Spec) error {
			return os.WriteFile(out, []byte("not flac"), 0o600)
		}})
	var pe *problem.Error
	if _, err := bad.Ensure(context.Background(), good); !errors.As(err, &pe) || pe.Status != 500 {
		t.Errorf("invalid render: %v", err)
	}
	if len(e.cachedPaths(t)) != 0 {
		t.Error("an invalid clip was cached")
	}
	// the analyzer failing is a 500, not a leaked tool error
	badAn := clip.New(e.root, e.dir, mediatool.Tools{}, failingAnalyzer{}, nil, nil, clip.Options{})
	if _, err := badAn.Measure(context.Background(), good); !errors.As(err, &pe) || pe.Status != 500 {
		t.Errorf("analyzer failure: %v", err)
	}
	if got := badAn.ToolVersion(); got != "ffmpeg fail" {
		t.Errorf("ToolVersion = %q", got)
	}
}

type failingAnalyzer struct{}

func (failingAnalyzer) ToolVersion() string { return "ffmpeg fail" }
func (failingAnalyzer) WindowLoudness(context.Context, string, int64, int64) (clip.Measure, error) {
	return clip.Measure{}, errors.New("secret path /data/x failed")
}
