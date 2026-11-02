package librarytest

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/mediatool"
)

// L3AudioSource is one synthetic master of FixtureL3Audio. Rel is relative to L3Audio.Dir.
type L3AudioSource struct {
	Name    string
	Rel     string
	Kind    string  // "key" | "dense" | "pad" | "quiet" | "clicks120"
	Tonic   int     // key sources: the tonic pitch class (C = 0)
	Minor   bool    // key sources
	Cents   float64 // key sources: A4 = 440 Hz shifted by this many cents
	Seconds float64
}

// L3Audio is the audio side of the L3 fixture (C136, media tier): files of known key and known energy ordering for the
// Ears analyzers, which decode them through the real ffmpeg path.
type L3Audio struct {
	Dir     string
	Sources []L3AudioSource
}

// Abs is the absolute path of a source.
func (a L3Audio) Abs(s L3AudioSource) string { return filepath.Join(a.Dir, s.Rel) }

// Source returns the source called name, failing the test when there is none.
func (a L3Audio) Source(t testing.TB, name string) L3AudioSource {
	t.Helper()
	for _, s := range a.Sources {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no L3 audio source %q", name)
	return L3AudioSource{}
}

const (
	l3AudioRate    = 22050 // mono 16-bit WAV, encoded to FLAC; the analyzers decode it back down to 11 025 Hz
	l3AudioKeySecs = 40
	l3AudioEnSecs  = 30
)

// L3KeyNames lists the 24 keys of the fixture in order: tonic 0..11, major then minor.
func L3KeyNames() []string {
	maj := []string{"C", "Db", "D", "Eb", "E", "F", "F#", "G", "Ab", "A", "Bb", "B"}
	minor := []string{"C", "C#", "D", "Eb", "E", "F", "F#", "G", "G#", "A", "Bb", "B"}
	var out []string
	for t := range 12 {
		out = append(out, maj[t]+" major", minor[t]+" minor")
	}
	return out
}

// FixtureL3Audio renders, into a fresh t.TempDir(), 48 key sources (the 24 keys, each plain and detuned by -35 cents: 40 s of
// I-IV-V-I triads, 6 partials at 1/n), and four energy sources of 30 s: dense (5 loud noise bursts per second), pad (a
// slow chord), quiet (the pad at about -57 dBFS peak-RMS) and clicks120 (2 bursts per second). All are mono 22.05 kHz 16-bit FLAC,
// synthetic, mode 0444. tools must hold a working ffmpeg; the caller skips or fails the test without it.
func FixtureL3Audio(t testing.TB, tools mediatool.Tools) L3Audio {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "masters", "l3"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := L3Audio{Dir: dir}
	for tonic := range 12 {
		for _, minor := range []bool{false, true} {
			for _, cents := range []float64{0, -35} {
				suffix := "plain"
				if cents != 0 {
					suffix = "detuned"
				}
				name := fmt.Sprintf("key-%02d-%s-%s", tonic, map[bool]string{false: "major", true: "minor"}[minor], suffix)
				a.Sources = append(a.Sources, L3AudioSource{Name: name, Rel: filepath.Join("masters", "l3", name+".flac"), Kind: "key", Tonic: tonic, Minor: minor, Cents: cents, Seconds: l3AudioKeySecs})
			}
		}
	}
	for _, e := range []string{"dense", "pad", "quiet", "clicks120"} {
		a.Sources = append(a.Sources, L3AudioSource{Name: e, Rel: filepath.Join("masters", "l3", e+".flac"), Kind: e, Seconds: l3AudioEnSecs})
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	errs := make([]error, len(a.Sources))
	for i, s := range a.Sources {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			errs[i] = renderL3Source(ctx, tools, dir, s)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("%s: %v", a.Sources[i].Name, err)
		}
	}
	return a
}

func renderL3Source(ctx context.Context, tools mediatool.Tools, dir string, s L3AudioSource) error {
	var x []float64
	switch s.Kind {
	case "key":
		x = l3Triads(s.Tonic, s.Minor, s.Cents, s.Seconds)
	case "dense":
		x = l3Clicks(s.Seconds, 5, 0.7, 3)
	case "clicks120":
		x = l3Clicks(s.Seconds, 2, 0.6, 4)
	case "pad":
		x = l3Pad(s.Seconds, 0.95)
	case "quiet":
		x = l3Pad(s.Seconds, 0.006)
	default:
		return fmt.Errorf("unknown kind %q", s.Kind)
	}
	wav := filepath.Join(dir, "masters", "l3", s.Name+".wav")
	if err := writeL3WAV(wav, x); err != nil {
		return err
	}
	defer func() { _ = os.Remove(wav) }()
	out := filepath.Join(dir, s.Rel)
	args := []string{"-nostdin", "-hide_banner", "-v", "error", "-i", "file:" + wav, "-map_metadata", "-1", "-c:a", "flac", "-f", "flac", "file:" + out}
	if _, err := mediatool.Run(ctx, tools.FFmpeg, args, mediatool.RunOpts{Timeout: time.Minute, MaxStdout: 1 << 10}); err != nil {
		return err
	}
	return os.Chmod(out, 0o444)
}

// l3Triads is a I-IV-V-I progression (2.5 s per chord, 10 s cycle repeated) of harmonic-rich triads: 6 partials at 1/n.
func l3Triads(tonic int, minor bool, cents, seconds float64) []float64 {
	third := 4
	chords := [4][3]int{{0, third, 7}, {5, 5 + third, 12}, {7, 11, 14}, {0, third, 7}}
	if minor {
		third = 3
		chords = [4][3]int{{0, third, 7}, {5, 8, 12}, {7, 11, 14}, {0, third, 7}}
	}
	root := 45 + ((tonic-9)%12+12)%12 // A2 .. G#3
	chordN := int(2.5 * l3AudioRate)
	fade := l3AudioRate / 33
	cycle := make([]float64, 4*chordN)
	for i := range cycle {
		pos := i % chordN
		env := 1.0
		if pos < fade {
			env = float64(pos) / float64(fade)
		} else if chordN-pos < fade {
			env = float64(chordN-pos) / float64(fade)
		}
		tm := float64(i) / l3AudioRate
		var v float64
		for _, semi := range chords[i/chordN] {
			f := 440 * math.Pow(2, (float64(root+semi-69)+cents/100)/12)
			for h := 1; h <= 6; h++ {
				v += math.Sin(2*math.Pi*f*float64(h)*tm) / float64(h)
			}
		}
		cycle[i] = 0.08 * env * v
	}
	x := make([]float64, int(seconds*l3AudioRate))
	for i := range x {
		x[i] = cycle[i%len(cycle)]
	}
	return x
}

type l3RNG uint64

func (l *l3RNG) next() float64 {
	*l = *l*6364136223846793005 + 1442695040888963407
	return float64(uint64(*l)>>11)/float64(1<<53)*2 - 1
}

// l3Clicks is a 60 ms decaying noise burst perSec times a second over a faint noise floor.
func l3Clicks(seconds, perSec, amp float64, seed uint64) []float64 {
	r := l3RNG(seed)
	x := make([]float64, int(seconds*l3AudioRate))
	period, burst := int(l3AudioRate/perSec), l3AudioRate*6/100
	for i := range x {
		v := 0.002 * r.next()
		if pos := i % period; pos < burst {
			v += amp * math.Exp(-float64(pos)/(0.012*l3AudioRate)) * r.next()
		}
		x[i] = v
	}
	return x
}

// l3Pad is a slowly swelling chord (A3, E4, A4 and their 2nd to 4th partials at 1/n) of sines at the given peak amplitude.
func l3Pad(seconds, amp float64) []float64 {
	x := make([]float64, int(seconds*l3AudioRate))
	for i := range x {
		tm := float64(i) / l3AudioRate
		env := 0.85 + 0.15*math.Sin(2*math.Pi*tm/8)
		var v float64
		for _, f := range []float64{220, 330, 440} {
			for h := 1; h <= 4; h++ {
				v += math.Sin(2*math.Pi*f*float64(h)*tm) / float64(h)
			}
		}
		x[i] = amp * env * v / 6
	}
	return x
}

// writeL3WAV writes mono 16-bit samples at l3AudioRate.
func writeL3WAV(path string, x []float64) error {
	n := len(x)
	b := make([]byte, 44+2*n)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+2*n)) //nolint:gosec // bounded by the fixture length
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], l3AudioRate)
	binary.LittleEndian.PutUint32(b[28:], l3AudioRate*2)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(2*n)) //nolint:gosec // bounded by the fixture length
	for i, v := range x {
		s := int16(math.Round(math.Max(-1, math.Min(1, v)) * 32767))
		binary.LittleEndian.PutUint16(b[44+2*i:], uint16(s)) //nolint:gosec // two's complement sample
	}
	return os.WriteFile(path, b, 0o600)
}
