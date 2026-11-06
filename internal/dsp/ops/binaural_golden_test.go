package ops

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
	"github.com/nicolasacchi/metamusic/internal/dsp/safety"
)

var updateBinaural = flag.Bool("update-binaural", false, "rewrite testdata/hifi_binaural.sha256 (after a deliberate dsp_version bump only)")

// binauralGoldenCases are the rows of C221.7: model/preset/room, each at a different rate so the table is used as stored, resampled
// down and resampled up. The input is the same synthetic pink pair, scaled to -24 LUFS.
var binauralGoldenCases = []struct {
	name   string
	rate   int
	params string
}{
	{"hrtf_studio_none", 48000, `{"model":"hrtf","preset":"studio","room":"none"}`},
	{"hrtf_wide_studio", 44100, `{"model":"hrtf","preset":"wide","room":"studio"}`},
	{"parametric_studio_hall", 16000, `{"model":"parametric","preset":"studio","room":"hall","tail_s":1}`},
}

func binauralGoldenInput(rate int) pcm.Buffer {
	b := dsptest.PinkStereo(rate, 81, 1.5, 0.4)
	l, _ := safety.LUFS(b)
	g := dsptest.DBToLin(-24 - l)
	for c := range 2 {
		b.Ch[c] = dsptest.Scale(b.Ch[c], g)
	}
	return b
}

func binauralHashes(t *testing.T) map[string]string {
	t.Helper()
	run, _, ok := Lookup(KindBinaural)
	if !ok {
		t.Fatal("binaural is not registered")
	}
	got := map[string]string{}
	for _, c := range binauralGoldenCases {
		b := binauralGoldenInput(c.rate)
		out, err := run(context.Background(), RunIn{Rate: c.rate, Inputs: []pcm.Buffer{b}, Params: json.RawMessage(c.params), Seed: 77, TargetLUFS: -16, RefLUFS: -24})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		sum, err := pcm.WritePCM24(io.Discard, out.Buffer)
		if err != nil {
			t.Fatal(err)
		}
		got[c.name] = strings.TrimSpace(sum)
	}
	return got
}

// TestBinaural_Golden pins pcm_sha256 of the three C221.7 rows bit for bit (linux/amd64, GOAMD64=v1).
func TestBinaural_Golden(t *testing.T) {
	dsptest.RequireGolden(t)
	got := binauralHashes(t)
	const path = "testdata/hifi_binaural.sha256"
	if *updateBinaural {
		var sb strings.Builder
		sb.WriteString("# pcm_sha256 of the binaural operator on a fixed synthetic input (C224.1). linux/amd64, GOAMD64=v1.\n")
		for _, c := range binauralGoldenCases {
			sb.WriteString(c.name + " " + got[c.name] + "\n")
		}
		if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0][0] != '#' {
			want[f[0]] = f[1]
		}
	}
	if len(want) != len(binauralGoldenCases) {
		t.Errorf("%s has %d rows, want %d", path, len(want), len(binauralGoldenCases))
	}
	for _, c := range binauralGoldenCases {
		if got[c.name] != want[c.name] {
			t.Errorf("%s: hash %s, golden %s", c.name, got[c.name], want[c.name])
		}
	}
}

// TestBinaural_GoldenFMAOff re-executes the golden under GODEBUG=cpu.fma=off and requires the same bits.
func TestBinaural_GoldenFMAOff(t *testing.T) {
	dsptest.RequireGolden(t)
	dsptest.ReexecFMAOff(t, `^TestBinaural_Golden$`)
}

// TestBinaural_Deterministic: two runs of every golden row are bit-identical.
func TestBinaural_Deterministic(t *testing.T) {
	dsptest.RequireGolden(t)
	a, b := binauralHashes(t), binauralHashes(t)
	for k, v := range a {
		if b[k] != v {
			t.Errorf("%s: two runs differ", k)
		}
	}
}
