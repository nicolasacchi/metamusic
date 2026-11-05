package ops

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"runtime"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
)

func d4Glacier(t testing.TB, x pcm.Buffer, params string, seed int64, m *dsp.Meter) pcm.Buffer {
	t.Helper()
	out, err := runGlacier(context.Background(), RunIn{Rate: x.Rate, Inputs: []pcm.Buffer{x}, Params: json.RawMessage(params), Seed: seed, Meter: m})
	if err != nil {
		t.Fatal(err)
	}
	return out.Buffer
}

const d4GlacierParams = `{"factor":10,"start_s":0,"length_s":2,"window_s":0.5,"fade_in_s":0,"fade_out_s":0}`

func TestGlacier_Len(t *testing.T) {
	t.Parallel()
	x := dsptest.Dual(d4Rate, dsptest.Sine(d4Rate, 440, 0.5, 2))
	out := d4Glacier(t, x, d4GlacierParams, 1, nil)
	if out.Frames() != 320000 || len(out.Ch[1]) != 320000 || out.Rate != d4Rate {
		t.Errorf("frames %d/%d rate %d, want 320000 at 16000", out.Frames(), len(out.Ch[1]), out.Rate)
	}
	if got := GlacierLen(2, 10, d4Rate); got != 320000 {
		t.Errorf("GlacierLen = %d", got)
	}
	// round(length_s * rate * factor), not the rounded excerpt times factor
	if got := GlacierLen(1.0001, 12.5, 44100); got != roundHalfUp(1.0001*44100*12.5) {
		t.Errorf("GlacierLen = %d", got)
	}
	// An odd length: the output length is exact.
	out = d4Glacier(t, x, `{"factor":10.33,"length_s":0.77,"window_s":0.1,"fade_in_s":0,"fade_out_s":0}`, 1, nil)
	if want := GlacierLen(0.77, 10.33, d4Rate); out.Frames() != want {
		t.Errorf("frames %d, want %d", out.Frames(), want)
	}
}

func TestGlacier_Sine(t *testing.T) {
	t.Parallel()
	x := dsptest.Dual(d4Rate, dsptest.Sine(d4Rate, 440, 0.5, 2))
	out := d4Glacier(t, x, d4GlacierParams, 1, nil)
	if f := dsptest.PeakFreq(out.Ch[0], d4Rate); math.Abs(f-440) > 3 {
		t.Errorf("peak %.1f Hz, want 440 +- 3", f)
	}
	body := out.Ch[0][8192 : len(out.Ch[0])-8192]
	if d := d4DB(dsptest.RMS(body), dsptest.RMS(x.Ch[0])); math.Abs(d) > 1 {
		t.Errorf("RMS differs by %.2f dB from the source, want within 1 dB", d)
	}
}

func TestGlacier_SeedChangesAudio(t *testing.T) {
	t.Parallel()
	x := dsptest.Dual(d4Rate, dsptest.Pink(3, d4Rate))
	p := `{"factor":10,"length_s":1,"window_s":0.1,"fade_in_s":0,"fade_out_s":0}`
	a, b, c := d4Glacier(t, x, p, 1, nil), d4Glacier(t, x, p, 2, nil), d4Glacier(t, x, p, 1, nil)
	same := func(u, v pcm.Buffer) bool {
		for i := range u.Ch[0] {
			if u.Ch[0][i] != v.Ch[0][i] || u.Ch[1][i] != v.Ch[1][i] {
				return false
			}
		}
		return true
	}
	if same(a, b) {
		t.Error("another seed gave the same audio")
	}
	if !same(a, c) {
		t.Error("the same seed gave other audio")
	}
}

func TestGlacier_Decorrelated(t *testing.T) {
	t.Parallel()
	// a mono source (both channels equal): the phases are drawn per channel, so the output channels are not
	x := dsptest.Dual(d4Rate, dsptest.Noise(5, d4Rate, 0.2))
	out := d4Glacier(t, x, `{"factor":10,"length_s":1,"window_s":0.1,"fade_in_s":0,"fade_out_s":0}`, 4, nil)
	if rho := d4Pearson(out.Ch[0], out.Ch[1]); math.Abs(rho) >= 0.2 {
		t.Errorf("|rho(L,R)| = %.3f, want < 0.2", rho)
	}
}

func TestGlacier_FadesAndExcerpt(t *testing.T) {
	t.Parallel()
	x := dsptest.Dual(d4Rate, dsptest.Sine(d4Rate, 330, 0.5, 4))
	out := d4Glacier(t, x, `{"factor":10,"start_s":1,"length_s":1,"window_s":0.1,"fade_in_s":1,"fade_out_s":2}`, 9, nil)
	n := out.Frames()
	if n != 160000 {
		t.Fatalf("frames %d", n)
	}
	if math.Abs(out.Ch[0][0]) > 1e-3 || math.Abs(out.Ch[0][n-1]) > 1e-3 {
		t.Errorf("ends not faded: %v %v", out.Ch[0][0], out.Ch[0][n-1])
	}
	// the fade-in rises over its second and the body between the fades is at full level
	early, mid := dsptest.RMS(out.Ch[0][:1600]), dsptest.RMS(out.Ch[0][60000:100000])
	if early > 0.2*mid {
		t.Errorf("first 100 ms at %.3f of the body %.3f: the fade-in is missing", early, mid)
	}
	// default excerpt: without length_s the whole input after start (< 30 s) is scanned
	out = d4Glacier(t, x, `{"factor":10,"start_s":3,"window_s":0.1,"fade_in_s":0,"fade_out_s":0}`, 9, nil)
	if out.Frames() != 160000 {
		t.Errorf("default excerpt: %d frames, want 160000 (the last second)", out.Frames())
	}
	// an excerpt that runs past the end is cut there
	out = d4Glacier(t, x, `{"factor":10,"start_s":3,"length_s":5,"window_s":0.1}`, 9, nil)
	if out.Frames() != 160000 {
		t.Errorf("cut excerpt: %d frames", out.Frames())
	}
}

func TestGlacier_Errors(t *testing.T) {
	t.Parallel()
	x := dsptest.Dual(d4Rate, dsptest.Sine(d4Rate, 330, 0.5, 2))
	for name, c := range map[string]struct {
		params, field string
	}{
		"factor low":  {`{"factor":9}`, "factor"},
		"factor high": {`{"factor":51}`, "factor"},
		"window":      {`{"window_s":3}`, "window_s"},
		"fade in":     {`{"fade_in_s":31}`, "fade_in_s"},
		"fade out":    {`{"fade_out_s":-1}`, "fade_out_s"},
		"start":       {`{"start_s":-1}`, "start_s"},
		"length":      {`{"length_s":0}`, "length_s"},
		"past end":    {`{"start_s":2}`, "start_s"},
		"type":        {`{"factor":"x"}`, "params"},
	} {
		_, err := runGlacier(context.Background(), RunIn{Rate: d4Rate, Inputs: []pcm.Buffer{x}, Params: json.RawMessage(c.params)})
		var pe *ParamError
		if !errors.As(err, &pe) || pe.Field != c.field {
			t.Errorf("%s: err = %v, want ParamError on %s", name, err, c.field)
		}
	}
	if _, err := runGlacier(context.Background(), RunIn{Rate: d4Rate, Params: json.RawMessage(`{}`)}); err == nil {
		t.Error("no input accepted")
	}
	if _, err := ParseGlacier(nil); err != nil {
		t.Errorf("empty params: %v", err)
	}
	// a zero rate on the input takes the job's
	y := x.Clone()
	y.Rate = 0
	if out, err := runGlacier(context.Background(), RunIn{Rate: d4Rate, Inputs: []pcm.Buffer{y}, Params: json.RawMessage(d4GlacierParams)}); err != nil || out.Buffer.Rate != d4Rate {
		t.Errorf("rate fallback: %v, %d", err, out.Buffer.Rate)
	}
}

func TestGlacier_MeterContextAndSink(t *testing.T) {
	t.Parallel()
	x := dsptest.Dual(d4Rate, dsptest.Sine(d4Rate, 330, 0.5, 2))
	m := &dsp.Meter{}
	d4Glacier(t, x, d4GlacierParams, 1, m)
	want := GlacierUnits(320000, d4Rate, 0.5)
	if m.Used() != want {
		t.Errorf("meter used %d, GlacierUnits %d", m.Used(), want)
	}
	if _, u, ok := Lookup(KindGlacier); !ok || u(0, 320000, d4Rate, json.RawMessage(d4GlacierParams)) != want || u(0, 320000, d4Rate, json.RawMessage(`{`)) != want {
		t.Error("registered units differ from GlacierUnits")
	}
	_, err := runGlacier(context.Background(), RunIn{Rate: d4Rate, Inputs: []pcm.Buffer{x}, Params: json.RawMessage(d4GlacierParams), Meter: &dsp.Meter{Max: want / 3}})
	if !errors.Is(err, dsp.ErrBudget) {
		t.Errorf("budget: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runGlacier(ctx, RunIn{Rate: d4Rate, Inputs: []pcm.Buffer{x}, Params: json.RawMessage(d4GlacierParams)}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancel: %v", err)
	}
	stop := errors.New("stop")
	p, _ := ParseGlacier(json.RawMessage(d4GlacierParams))
	blocks := 0
	if err := Glacier(context.Background(), x, p, 1, nil, func(l, r []float64) error { blocks++; return stop }); !errors.Is(err, stop) || blocks != 1 {
		t.Errorf("sink error: %v after %d blocks", err, blocks)
	}
	var rep struct {
		Glacier struct {
			OutFrames int `json:"out_frames"`
		} `json:"glacier"`
	}
	out, _ := runGlacier(context.Background(), RunIn{Rate: d4Rate, Inputs: []pcm.Buffer{x}, Params: json.RawMessage(d4GlacierParams)})
	if err := json.Unmarshal(out.Report, &rep); err != nil || rep.Glacier.OutFrames != 320000 {
		t.Errorf("report %s: %v", out.Report, err)
	}
	bad := p
	bad.Factor = 1
	if err := Glacier(context.Background(), x, bad, 1, nil, nil); err == nil {
		t.Error("Glacier accepted factor 1")
	}
}

// TestOps_GlacierStreamingMemory renders 900 s of output and requires the heap to stay under 64 MiB: the output is handed to
// the sink block by block and never held.
func TestOps_GlacierStreamingMemory(t *testing.T) {
	const rate = 4000
	x := dsptest.Dual(rate, dsptest.Pink(1, 18*rate))
	p, err := ParseGlacier(json.RawMessage(`{"factor":50,"length_s":18,"window_s":0.5,"fade_in_s":4,"fade_out_s":4}`))
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	var peak uint64
	total, calls := 0, 0
	var sum float64
	err = Glacier(context.Background(), x, p, 1, nil, func(l, r []float64) error {
		total += len(l)
		sum += l[0] + r[len(r)-1]
		if calls++; calls%64 == 0 {
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			peak = max(peak, ms.HeapAlloc)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 900*rate {
		t.Fatalf("streamed %d samples, want %d", total, 900*rate)
	}
	if used := int64(peak) - int64(base.HeapAlloc); used > 64<<20 {
		t.Errorf("heap grew by %d MiB while rendering 900 s, want < 64 MiB", used>>20)
	}
	_ = sum
}
