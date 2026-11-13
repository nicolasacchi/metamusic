package ops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"runtime"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
)

// TestRunTo_HifiStreamsFromDisk renders 150 s through master, widen and binaural with file scratch and requires the heap to stay
// near the input (16 bytes a frame, which the job holds), not at the 100 bytes a frame of the in-memory finish and the operators'
// own copies. The input is a decorrelated pair so widen really renders.
func TestRunTo_HifiStreamsFromDisk(t *testing.T) {
	d4SkipHeavy(t) // the race detector also changes what the heap counts
	const rate, secs = 8000, 150
	n := secs * rate
	x := dsptest.Dual(rate, dsptest.Pink(1, n))
	x.Ch[1] = dsptest.Pink(2, n)
	inBytes := int64(16 * n)
	for _, kind := range []string{KindMaster, KindWiden, KindBinaural} {
		j := Job{Kind: kind, Rate: rate, Seed: 1, Params: json.RawMessage(`{}`), Inputs: []Input{{Buf: x}}, TargetLUFS: -16, Scratch: &dirScratch{dir: t.TempDir()}}
		runtime.GC()
		var base runtime.MemStats
		runtime.ReadMemStats(&base)
		w := &liveWriter{}
		j.Progress = func(int, string) { w.sample(16) } // the render phase, through the meter's context poll
		rep, err := RunTo(context.Background(), j, w)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if rep.Frames < int64(n) || int64(w.n) != 6*rep.Frames {
			t.Fatalf("%s: %d frames, %d bytes", kind, rep.Frames, w.n)
		}
		if used := int64(w.peak) - int64(base.HeapAlloc); used > 24<<20 {
			t.Errorf("%s: heap grew by %d MiB beyond the %d MiB input while finishing, want < 24 MiB", kind, used>>20, inBytes>>20)
		}
		if j.Scratch.(*dirScratch).live != 0 {
			t.Errorf("%s: a spool was not released", kind)
		}
	}
}

// liveWriter counts bytes and samples the heap on live data only: a collection comes first, so the garbage of the block convolutions
// and spool buffers (the collector lets a heap double before it runs) does not count. It is what a memory cap sees under GOMEMLIMIT.
type liveWriter struct {
	n     int
	calls int
	peak  uint64
}

func (w *liveWriter) sample(every int) {
	if w.calls++; w.calls%every == 0 {
		runtime.GC()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		w.peak = max(w.peak, ms.HeapAlloc)
	}
}

func (w *liveWriter) Write(b []byte) (int, error) {
	w.n += len(b)
	w.sample(8)
	return len(b), nil
}

// hifiPair is a stereo signal that is neither mono nor hard-panned: the right channel is partly the left and partly its own noise, with
// a click in the middle (an edge for the filters and the convolutions to carry across blocks).
func hifiPair(rate, n int, mix float64) pcm.Buffer {
	l, o := dsptest.Pink(11, n), dsptest.Pink(12, n)
	r := make([]float64, n)
	for i := range r {
		r[i] = mix*l[i] + (1-mix)*o[i]
	}
	l[n/2] = 0.9
	return pcm.Buffer{Rate: rate, Ch: [2][]float64{l, r}}
}

// TestHifiStream_MatchesInMemory: master, widen, binaural and ensemble finished through RunTo with scratch (the render in blocks into a spool,
// safety.FinishStream, the report measured on the delivered blocks) give the bytes, the report and the units of the in-memory Run,
// with block sizes that cut the filters, the all-pass rings, the correlation segments and the convolution partitions in awkward
// places. The cases cover each branch: filters only, compressor, the guard lowering the strength, the guard passing through, a
// room with a long tail, no room, the parametric model.
func TestHifiStream_MatchesInMemory(t *testing.T) {
	d4SkipHeavy(t)
	const rate = 8000
	n := 4*4096 + 777 // not a multiple of any block: the convolution's last partition is partial
	cases := []struct {
		name, kind, params string
		mix                float64
	}{
		{"master neutral", KindMaster, `{}`, 0.5},
		{"master warm compressed", KindMaster, `{"preset":"warm","comp_amount":0.8,"low_cut_hz":30,"target_lufs":-20}`, 0.5},
		{"master no stages", KindMaster, `{"tilt_db":0,"comp_amount":0,"low_cut_hz":0}`, 0.5},
		{"widen default", KindWiden, `{}`, 0.9},
		{"widen strong", KindWiden, `{"width":2,"decorrelate":0.5,"bass_mono_hz":250}`, 0.97},
		{"widen no all-pass", KindWiden, `{"width":1.6,"decorrelate":0,"bass_mono_hz":0}`, 0.9},
		{"widen guard lowers the strength", KindWiden, `{"width":2,"decorrelate":0.5,"bass_mono_hz":60}`, 0.4},
		{"widen already wide", KindWiden, `{}`, 0.0},
		{"widen neutral", KindWiden, `{"width":1,"decorrelate":0,"bass_mono_hz":0}`, 0.9},
		{"ensemble default", KindEnsemble, `{}`, 0.5},
		{"ensemble cut tail", KindEnsemble, `{"space":"hangar","tail_s":1.5,"wet_db":-3}`, 0.5},
		{"ensemble wet only", KindEnsemble, `{"dry_db":null,"tail_s":2}`, 0.5},
		{"ensemble no room", KindEnsemble, `{"tail_s":0}`, 0.5},
		{"binaural default", KindBinaural, `{}`, 0.5},
		{"binaural hall long tail", KindBinaural, `{"room":"hall","tail_s":4,"room_mix_db":-18}`, 0.5},
		{"binaural dry", KindBinaural, `{"room":"none"}`, 0.5},
		{"binaural parametric", KindBinaural, `{"model":"parametric","preset":"wide","room":"studio","tail_s":0.2}`, 0.5},
	}
	for _, c := range cases {
		x := hifiPair(rate, n, c.mix)
		job := func() Job {
			return Job{Kind: c.kind, Rate: rate, Seed: 3, Params: json.RawMessage(c.params), Inputs: []Input{{Buf: x}}, TargetLUFS: -16, RefLUFS: -20, Meter: &dsp.Meter{}}
		}
		out, want, err := Run(context.Background(), job())
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		var wantPCM bytes.Buffer
		sum, err := pcm.WritePCM24(&wantPCM, out)
		if err != nil {
			t.Fatal(err)
		}
		want.PCMSHA256 = sum
		wantJSON, _ := json.Marshal(want)
		for _, block := range []int{997, 4096, 5000} {
			old := hifiBlock
			hifiBlock = block
			sc := &memScratch{}
			j := job()
			j.Scratch = sc
			var w hashWriter
			got, err := RunTo(context.Background(), j, &w)
			hifiBlock = old
			if err != nil {
				t.Fatalf("%s block %d: %v", c.name, block, err)
			}
			if !bytes.Equal(w.b.Bytes(), wantPCM.Bytes()) {
				t.Errorf("%s block %d: the streamed bytes differ from the in-memory render (%x vs %x)", c.name, block, sha256.Sum256(w.b.Bytes()), sha256.Sum256(wantPCM.Bytes()))
			}
			if gotJSON, _ := json.Marshal(got); !bytes.Equal(gotJSON, wantJSON) {
				t.Errorf("%s block %d: report\n got %s\nwant %s", c.name, block, gotJSON, wantJSON)
			}
			if sc.made != sc.closed || sc.made == 0 {
				t.Errorf("%s block %d: %d spools made, %d released", c.name, block, sc.made, sc.closed)
			}
		}
	}
}

// TestHifiStream_D5JobsAcrossBlocks: the dispatcher's own jobs of every kind (the d5 golden inputs: pairs with alignment, a shepard, a
// glacier) are the same through RunTo at block sizes that cut the verbatim parts of a morph, as through Run.
func TestHifiStream_D5JobsAcrossBlocks(t *testing.T) {
	d4SkipHeavy(t)
	for _, kind := range Kinds() {
		out, want, err := Run(context.Background(), d5Job(t, kind))
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		want.PCMSHA256 = d5Hash(t, out)
		wantJSON, _ := json.Marshal(want)
		for _, block := range []int{997, 5000} {
			old := hifiBlock
			hifiBlock = block
			j := d5Job(t, kind)
			j.Scratch = &memScratch{}
			var w hashWriter
			got, err := RunTo(context.Background(), j, &w)
			hifiBlock = old
			if err != nil {
				t.Fatalf("%s block %d: %v", kind, block, err)
			}
			if gotJSON, _ := json.Marshal(got); !bytes.Equal(gotJSON, wantJSON) {
				t.Errorf("%s block %d: report\n got %s\nwant %s", kind, block, gotJSON, wantJSON)
			}
		}
	}
}
