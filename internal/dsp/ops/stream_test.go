package ops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
	"github.com/nicolasacchi/metamusic/internal/dsp/safety"
)

// memScratch keeps spools in memory and counts them.
type memScratch struct{ made, closed int }

type memSpool struct {
	sc  *memScratch
	buf bytes.Buffer
}

func (m *memScratch) NewSpool() (safety.Spool, error) { m.made++; return &memSpool{sc: m}, nil }
func (s *memSpool) Write(p []byte) (int, error)       { return s.buf.Write(p) }
func (s *memSpool) Open() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.buf.Bytes())), nil
}
func (s *memSpool) Close() error { s.sc.closed++; s.buf.Reset(); return nil }

// dirScratch keeps spools as files, so the heap does not count them.
type dirScratch struct {
	dir  string
	n    int
	live int
}

type fileSpool struct {
	sc   *dirScratch
	path string
	w    *os.File
}

func (d *dirScratch) NewSpool() (safety.Spool, error) {
	d.n++
	d.live++
	p := filepath.Join(d.dir, "spool-"+string(rune('a'+d.n)))
	w, err := os.Create(p)
	if err != nil {
		return nil, err
	}
	return &fileSpool{sc: d, path: p, w: w}, nil
}
func (s *fileSpool) Write(p []byte) (int, error) { return s.w.Write(p) }
func (s *fileSpool) Open() (io.ReadCloser, error) {
	if s.w != nil {
		_ = s.w.Close()
		s.w = nil
	}
	return os.Open(s.path)
}
func (s *fileSpool) Close() error {
	if s.w != nil {
		_ = s.w.Close()
	}
	s.sc.live--
	return os.Remove(s.path)
}

type hashWriter struct{ b bytes.Buffer }

func (h *hashWriter) Write(p []byte) (int, error) { return h.b.Write(p) }

// TestRunTo_MatchesRun: through RunTo, with and without scratch, every kind produces the bytes, the hash, the frames, the units and
// the report of Run, and the golden of the dispatcher.
func TestRunTo_MatchesRun(t *testing.T) {
	d4SkipHeavy(t)
	golden := map[string]string{}
	if raw, err := os.ReadFile(d5GoldenPath); err == nil {
		for _, line := range bytes.Split(raw, []byte("\n")) {
			if f := bytes.Fields(line); len(f) == 2 && f[0][0] != '#' {
				golden[string(f[0])] = string(f[1])
			}
		}
	}
	for _, kind := range Kinds() {
		out, want, err := Run(context.Background(), d5Job(t, kind))
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		want.PCMSHA256 = d5Hash(t, out)
		wantJSON, _ := json.Marshal(want)
		for name, sc := range map[string]*memScratch{"without scratch": nil, "with scratch": {}} {
			j := d5Job(t, kind)
			if sc != nil {
				j.Scratch = sc
			}
			var w hashWriter
			got, err := RunTo(context.Background(), j, &w)
			if err != nil {
				t.Fatalf("%s %s: %v", kind, name, err)
			}
			sum := sha256.Sum256(w.b.Bytes())
			if hex.EncodeToString(sum[:]) != want.PCMSHA256 || got.PCMSHA256 != want.PCMSHA256 {
				t.Errorf("%s %s: bytes hash %x, report %s, Run %s", kind, name, sum, got.PCMSHA256, want.PCMSHA256)
			}
			if w.b.Len() != 6*int(want.Frames) {
				t.Errorf("%s %s: %d bytes for %d frames", kind, name, w.b.Len(), want.Frames)
			}
			if gotJSON, _ := json.Marshal(got); !bytes.Equal(gotJSON, wantJSON) {
				t.Errorf("%s %s: report\n got %s\nwant %s", kind, name, gotJSON, wantJSON)
			}
			if g, ok := golden[kind]; ok && g != got.PCMSHA256 {
				t.Errorf("%s %s: golden %s, got %s", kind, name, g, got.PCMSHA256)
			}
			if sc != nil && sc.made != sc.closed {
				t.Errorf("%s: %d spools made, %d released", kind, sc.made, sc.closed)
			}
		}
	}
}

// TestRunTo_GlacierStreamsFromDisk renders 600 s of glacier output (4.8 million frames) through RunTo with file scratch and
// requires the heap to stay under 64 MiB: neither the render, nor the finish, nor the delivery holds the output. The in-memory
// finish needs about 100 bytes a frame, which at this length is 480 MB, far above the bound.
func TestRunTo_GlacierStreamsFromDisk(t *testing.T) {
	d4SkipHeavy(t) // the race detector also changes what the heap counts
	const rate = 8000
	x := dsptest.Dual(rate, dsptest.Pink(1, 12*rate))
	j := Job{Kind: KindGlacier, Rate: rate, Seed: 1, Params: json.RawMessage(`{"factor":50,"length_s":12,"window_s":0.5,"fade_in_s":4,"fade_out_s":4}`),
		Inputs: []Input{{Buf: x}}, TargetLUFS: -16, Scratch: &dirScratch{dir: t.TempDir()}}
	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	w := &peakWriter{}
	rep, err := RunTo(context.Background(), j, w)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Frames != 600*rate || w.n != 6*600*rate {
		t.Fatalf("%d frames, %d bytes", rep.Frames, w.n)
	}
	if used := int64(w.peak) - int64(base.HeapAlloc); used > 64<<20 {
		t.Errorf("heap grew by %d MiB while finishing 600 s, want < 64 MiB", used>>20)
	}
	if j.Scratch.(*dirScratch).live != 0 {
		t.Error("a spool was not released")
	}
}

// peakWriter counts bytes and samples the heap now and then.
type peakWriter struct {
	n     int
	calls int
	peak  uint64
}

func (p *peakWriter) Write(b []byte) (int, error) {
	p.n += len(b)
	if p.calls++; p.calls%256 == 0 {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		p.peak = max(p.peak, ms.HeapAlloc)
	}
	return len(b), nil
}

type failWriter struct{ after int }

func (f *failWriter) Write(b []byte) (int, error) {
	if f.after -= len(b); f.after < 0 {
		return 0, errors.New("disk full")
	}
	return len(b), nil
}

type failScratch struct{}

func (failScratch) NewSpool() (safety.Spool, error) { return nil, errors.New("no space") }

func TestRunTo_Errors(t *testing.T) {
	d4SkipHeavy(t)
	// a writer that fails ends the run with its error and releases every spool
	for _, kind := range []string{KindGlacier, KindEnsemble, KindMaster} {
		sc := &memScratch{}
		j := d5Job(t, kind)
		j.Scratch = sc
		if _, err := RunTo(context.Background(), j, &failWriter{after: 1000}); err == nil {
			t.Errorf("%s: a failing writer was accepted", kind)
		}
		if sc.made != sc.closed {
			t.Errorf("%s: %d spools made, %d released", kind, sc.made, sc.closed)
		}
	}
	j := d5Job(t, KindGlacier)
	if _, err := RunTo(context.Background(), j, &failWriter{after: 1000}); err == nil {
		t.Error("without scratch: a failing writer was accepted")
	}
	// a scratch that cannot make a spool
	for _, kind := range []string{KindGlacier, KindEnsemble} {
		j := d5Job(t, kind)
		j.Scratch = failScratch{}
		if _, err := RunTo(context.Background(), j, io.Discard); err == nil {
			t.Errorf("%s: a failing scratch was accepted", kind)
		}
	}
	// bad jobs, a budget, a silent source and a cancelled context all fail before anything is delivered
	var pe *ParamError
	j = d5Job(t, KindGlacier)
	j.Kind = "nope"
	j.Scratch = &memScratch{}
	if _, err := RunTo(context.Background(), j, io.Discard); !errors.As(err, &pe) {
		t.Errorf("unknown kind: %v", err)
	}
	j = d5Job(t, KindGlacier)
	j.Scratch = &memScratch{}
	j.Params = json.RawMessage(`{"factor":"x"}`)
	if _, err := RunTo(context.Background(), j, io.Discard); !errors.As(err, &pe) {
		t.Errorf("bad params: %v", err)
	}
	j = d5Job(t, KindGlacier)
	j.Scratch = &memScratch{}
	j.Meter = &dsp.Meter{Max: 1000}
	var w bytes.Buffer
	if _, err := RunTo(context.Background(), j, &w); !errors.Is(err, dsp.ErrBudget) || w.Len() != 0 {
		t.Errorf("budget: %v, %d bytes written", err, w.Len())
	}
	// room for the render but not for the finishing stage
	_, rep, err := Run(context.Background(), d5Job(t, KindEnsemble))
	if err != nil {
		t.Fatal(err)
	}
	j = d5Job(t, KindEnsemble)
	j.Scratch = &memScratch{}
	j.Meter = &dsp.Meter{Max: rep.Units - SafetyUnits(rep.Frames)/2}
	w.Reset()
	if _, err := RunTo(context.Background(), j, &w); !errors.Is(err, dsp.ErrBudget) || w.Len() != 0 {
		t.Errorf("budget without room for safety: %v, %d bytes written", err, w.Len())
	}
	j = d5Job(t, KindGlacier)
	j.Scratch = &memScratch{}
	silent := dsptest.Dual(d4Rate, dsptest.Silence(d4Rate))
	j.Inputs = []Input{{Buf: silent}}
	j.Rate = d4Rate
	w.Reset()
	if _, err := RunTo(context.Background(), j, &w); !errors.Is(err, safety.ErrSilent) || w.Len() != 0 {
		t.Errorf("silent source: %v, %d bytes written", err, w.Len())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	j = d5Job(t, KindEnsemble)
	j.Scratch = &memScratch{}
	if _, err := RunTo(ctx, j, io.Discard); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
	// progress reaches finishing on the streamed path too
	var got []int
	j = d5Job(t, KindGlacier)
	j.Scratch = &memScratch{}
	j.Progress = func(p int, _ string) { got = append(got, p) }
	if _, err := RunTo(context.Background(), j, io.Discard); err != nil || len(got) == 0 || got[len(got)-1] != renderShare {
		t.Errorf("progress %v (%v)", got, err)
	}
}
