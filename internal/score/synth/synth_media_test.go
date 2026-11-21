package synth

// go-media tier: these run the real fluidsynth against a generated test soundfont (dsptest.WriteSF2). They skip when
// fluidsynth is absent unless MM_REQUIRE_MEDIA_TOOLS=1 (CI), and under -short.

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
	"github.com/nicolasacchi/metamusic/internal/score"
	"github.com/nicolasacchi/metamusic/internal/score/smf"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

// requireFluidsynth skips when fluidsynth is absent. CI does not install fluidsynth until DESK-T3 adds it to the apt steps of
// .github/workflows/ci.yml, so MM_REQUIRE_MEDIA_TOOLS=1 must not fail these tests yet; MM_REQUIRE_FLUIDSYNTH=1 does. DESK-T3
// sets that variable beside the apt line (or replaces this helper by testtools.Require(t, "fluidsynth")).
func requireFluidsynth(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("media tools: skipped under -short")
	}
	bin := testtools.Tools().Fluidsynth
	if _, err := exec.LookPath(bin); err != nil {
		if os.Getenv("MM_REQUIRE_FLUIDSYNTH") == "1" {
			t.Fatal("fluidsynth required (MM_REQUIRE_FLUIDSYNTH=1) but not found")
		}
		t.Skip("media tool \"fluidsynth\" not found")
	}
	return bin
}

// rig is a fluidsynth adapter, a generated soundfont and a work directory.
type rig struct {
	f    *Fluidsynth
	dir  string
	sf   string
	last int
}

func newRig(t *testing.T, o dsptest.SF2Options) *rig {
	t.Helper()
	f, err := NewFluidsynth(context.Background(), requireFluidsynth(t))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	sf := filepath.Join(dir, "test.sf2")
	if err := dsptest.WriteSF2(sf, o); err != nil {
		t.Fatal(err)
	}
	return &rig{f: f, dir: dir, sf: sf}
}

// render writes the SMF of s with the tail and renders it; it returns the mono samples and the rate.
func (g *rig) render(t *testing.T, s *score.Score, tail float64, timeout time.Duration) ([]float64, int, string) {
	t.Helper()
	g.last++
	mid := filepath.Join(g.dir, "s.mid")
	out := filepath.Join(g.dir, "o"+string(rune('a'+g.last))+".wav")
	if err := os.WriteFile(mid, smf.Write(s, tail), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := g.f.Render(context.Background(), Request{SMF: mid, Soundfont: g.sf, Out: out, Timeout: timeout}); err != nil {
		t.Fatalf("render: %v", err)
	}
	x, rate := readWAV(t, out)
	return x, rate, out
}

// readWAV reads an IEEE-float or PCM16/24 WAV (also WAVE_FORMAT_EXTENSIBLE) and returns the channel mean.
func readWAV(t *testing.T, path string) ([]float64, int) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		t.Fatalf("not a WAV (%d bytes)", len(b))
	}
	var tag, ch, bits, rate int
	var data []byte
	for p := 12; p+8 <= len(b); {
		id, n := string(b[p:p+4]), int(binary.LittleEndian.Uint32(b[p+4:]))
		body := b[p+8 : min(p+8+n, len(b))]
		switch id {
		case "fmt ":
			tag, ch = int(binary.LittleEndian.Uint16(body)), int(binary.LittleEndian.Uint16(body[2:]))
			rate, bits = int(binary.LittleEndian.Uint32(body[4:])), int(binary.LittleEndian.Uint16(body[14:]))
			if tag == 0xFFFE && len(body) >= 26 {
				tag = int(binary.LittleEndian.Uint16(body[24:]))
			}
		case "data":
			data = body
		}
		p += 8 + n + n%2
	}
	if ch < 1 || len(data) == 0 {
		t.Fatalf("no audio in WAV: ch=%d data=%d", ch, len(data))
	}
	bps := bits / 8
	frames := len(data) / (bps * ch)
	x := make([]float64, frames)
	for i := range x {
		var sum float64
		for c := 0; c < ch; c++ {
			o := (i*ch + c) * bps
			switch {
			case tag == 3 && bits == 32:
				sum += float64(math.Float32frombits(binary.LittleEndian.Uint32(data[o:])))
			case tag == 1 && bits == 16:
				sum += float64(int16(binary.LittleEndian.Uint16(data[o:]))) / 32768
			case tag == 1 && bits == 24:
				v := int32(data[o]) | int32(data[o+1])<<8 | int32(int8(data[o+2]))<<16
				sum += float64(v) / 8388608
			default:
				t.Fatalf("unsupported WAV format tag %d, %d bits", tag, bits)
			}
		}
		x[i] = sum / float64(ch)
	}
	return x, rate
}

func quarterNotes(pitches []int, dur int64) *score.Score {
	v := score.Voice{ID: "A", Name: "A", Program: 0, Channel: 0}
	for i, p := range pitches {
		v.Notes = append(v.Notes, score.Note{Pitch: p, Vel: 100, Tick: int64(i) * 480, Dur: dur})
	}
	return &score.Score{PPQ: 480, Tempos: []score.Tempo{{Tick: 0, USPerQ: 500000}}, Meters: []score.Meter{{Tick: 0, Num: 4, Den: 4}}, Voices: []score.Voice{v}}
}

func window(x []float64, rate int, from, to float64) []float64 {
	a, b := int(from*float64(rate)), min(int(to*float64(rate)), len(x))
	if a >= b {
		return nil
	}
	return x[a:b]
}

func TestFluidsynth_HelpListsEveryFlag(t *testing.T) {
	requireFluidsynth(t)
	out, err := os.ReadFile("testdata/fluidsynth-help.txt")
	if err != nil {
		t.Skip("testdata/fluidsynth-help.txt not recorded yet (DESK-T3 records it from the image's fluidsynth)")
	}
	for _, flag := range []string{"-q", "-n", "-i", "-o", "-g", "-r", "-R", "-C", "-T", "-O", "-F"} {
		if !strings.Contains(string(out), flag) {
			t.Errorf("recorded help does not mention %s", flag)
		}
	}
}

func TestSynth_TestSoundfont_Pitch(t *testing.T) {
	g := newRig(t, dsptest.SF2Options{})
	// A4 C5 E5 A5, eighth notes on quarter-note onsets at 120 BPM: silence between notes makes the onsets measurable
	s := quarterNotes([]int{69, 72, 76, 81}, 240)
	x, rate, _ := g.render(t, s, 0, 30*time.Second)
	peak := dsptest.PeakAbs(x)
	if peak < 0.01 {
		t.Fatalf("silent render (peak %g)", peak)
	}
	thr := peak * dsptest.DBToLin(-30)
	for i, want := range []float64{440, 523.2511, 659.2551, 880} {
		onset := float64(i) * 0.5
		mid := window(x, rate, onset+0.0625, onset+0.1875)
		got := dsptest.PeakFreq(mid, rate)
		cents := 1200 * math.Log2(got/want)
		if math.Abs(cents) > 15 {
			t.Errorf("note %d: %.2f Hz, want %.2f (%.1f cents)", i, got, want, cents)
		}
		first := -1
		for j, v := range window(x, rate, onset-0.05, onset+0.3) {
			if math.Abs(v) >= thr {
				first = j
				break
			}
		}
		if first < 0 {
			t.Fatalf("note %d: no onset", i)
		}
		at := onset - 0.05 + float64(first)/float64(rate)
		if math.Abs(at-onset) > 0.025 {
			t.Errorf("note %d: onset at %.4f s, want %.4f +- 25 ms", i, at, onset)
		}
	}
}

func TestSynth_Deterministic(t *testing.T) {
	g := newRig(t, dsptest.SF2Options{})
	s := quarterNotes([]int{60, 64, 67, 72}, 400)
	_, _, p1 := g.render(t, s, 1, 30*time.Second)
	_, _, p2 := g.render(t, s, 1, 30*time.Second)
	a, _ := os.ReadFile(p1)
	b, _ := os.ReadFile(p2)
	if len(a) == 0 || string(a) != string(b) {
		t.Errorf("two renders differ (%d and %d bytes)", len(a), len(b))
	}
}

func TestSynth_TailRendered(t *testing.T) {
	g := newRig(t, dsptest.SF2Options{Release: time.Second})
	s := quarterNotes([]int{69}, 480) // one note, 1.0 s
	const tail = 1.0
	x, rate, _ := g.render(t, s, tail, 30*time.Second)
	if got, want := float64(len(x))/float64(rate), 1.0+tail; math.Abs(got-want) > 0.010 {
		t.Errorf("output is %.3f s, want %.3f +- 10 ms (score seconds + tail)", got, want)
	}
	rms := dsptest.RMS(window(x, rate, 1.0, 1.0+tail))
	if db := dsptest.DBFS(rms); db < -60 {
		t.Errorf("release tail RMS %.1f dBFS, want above -60", db)
	}
}

func TestSynth_DryByDefault(t *testing.T) {
	g := newRig(t, dsptest.SF2Options{}) // instant release
	s := quarterNotes([]int{69}, 480)
	x, rate, _ := g.render(t, s, 1, 30*time.Second)
	if peak := dsptest.PeakAbs(window(x, rate, 0.1, 0.9)); peak < 0.01 {
		t.Fatalf("the note is silent (peak %g)", peak)
	}
	if db := dsptest.DBFS(dsptest.RMS(window(x, rate, 1.05, 2.0))); db >= -60 {
		t.Errorf("energy after the last note + 50 ms is %.1f dBFS, want below -60 (reverb or chorus leaked in)", db)
	}
}

func TestSynth_NoUserConfig(t *testing.T) {
	g := newRig(t, dsptest.SF2Options{})
	s := quarterNotes([]int{69, 72}, 400)
	_, _, clean := g.render(t, s, 0.5, 30*time.Second)

	decoy := t.TempDir()
	hostile := "gain 0.001\nset synth.gain 0.001\nset synth.sample-rate 8000\nset synth.polyphony 1\n"
	for _, f := range []string{filepath.Join(decoy, ".fluidsynth"), filepath.Join(decoy, "fluidsynth.conf")} {
		if err := os.WriteFile(f, []byte(hostile), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(decoy, ".config", "fluidsynth"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", decoy)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(decoy, ".config"))
	_, _, hostileOut := g.render(t, s, 0.5, 30*time.Second)
	a, _ := os.ReadFile(clean)
	b, _ := os.ReadFile(hostileOut)
	if len(a) == 0 || string(a) != string(b) {
		t.Errorf("a decoy user config changed the render (%d vs %d bytes)", len(a), len(b))
	}
}

func TestSynth_Timeout(t *testing.T) {
	g := newRig(t, dsptest.SF2Options{})
	mid := filepath.Join(g.dir, "s.mid")
	if err := os.WriteFile(mid, smf.Write(quarterNotes([]int{69}, 480), 0), 0o644); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := g.f.Render(context.Background(), Request{SMF: mid, Soundfont: g.sf, Out: filepath.Join(g.dir, "o.wav"), Timeout: time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want a deadline error, got %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("a 1 ms timeout took %v: the process was not killed promptly", time.Since(start))
	}
}
