package ops

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
)

var updateD4 = flag.Bool("update-d4", false, "rewrite testdata/d4_golden.txt (after a deliberate dsp_version bump only)")

const d4GoldenPath = "testdata/d4_golden.txt"

type d4GoldenCase struct{ kind, name, params, alignment string }

var d4GoldenCases = []d4GoldenCase{
	{KindGlacier, "default", `{"factor":10,"start_s":0.2,"length_s":1.2,"window_s":0.1,"fade_in_s":0.5,"fade_out_s":0.5}`, ``},
	{KindGlacier, "wide_window", `{"factor":12.5,"length_s":1,"window_s":0.25,"fade_in_s":0,"fade_out_s":0}`, ``},
	{KindShepard, "up", `{"direction":"up","loop_s":1,"start_s":0.1,"layers":3,"period_s":8,"duration_s":9,"bell_sigma_octaves":0.6}`, ``},
	{KindShepard, "down_bars", `{"direction":"down","loop_bars":1,"layers":4,"period_s":8,"duration_s":8}`, `{"inputs":[{"bpm":120,"beats_per_bar":4}]}`},
	{KindEnsemble, "hangar", `{"space":"hangar","wet_db":-6,"dry_db":-12,"tail_s":3}`, ``},
	{KindEnsemble, "cistern_nodry", `{"space":"cistern","wet_db":-3,"dry_db":null,"tail_s":2}`, ``},
}

func d4GoldenInput() pcm.Buffer {
	l := dsptest.Concat(dsptest.Sine(d4Rate, 196, 0.3, 1), dsptest.Pink(5, d4Rate/2), dsptest.Sine(d4Rate, 262, 0.3, 1))
	r := dsptest.Concat(dsptest.Sine(d4Rate, 247, 0.3, 1), dsptest.Pink(6, d4Rate/2), dsptest.Sine(d4Rate, 330, 0.3, 1))
	return dsptest.Stereo(d4Rate, l, r)
}

func d4Hashes(t *testing.T) map[string]string {
	t.Helper()
	got := map[string]string{}
	for _, c := range d4GoldenCases {
		run, _, ok := Lookup(c.kind)
		if !ok {
			t.Fatalf("%s is not registered", c.kind)
		}
		out, err := run(context.Background(), RunIn{Rate: d4Rate, Inputs: []pcm.Buffer{d4GoldenInput()}, Params: json.RawMessage(c.params),
			Alignment: json.RawMessage(c.alignment), Seed: 7, TargetLUFS: -16})
		if err != nil {
			t.Fatalf("%s %s: %v", c.kind, c.name, err)
		}
		sum, err := pcm.WritePCM24(io.Discard, out.Buffer)
		if err != nil {
			t.Fatal(err)
		}
		got[c.kind+" "+c.name] = strings.TrimSpace(sum)
	}
	return got
}

// TestD4_Golden pins pcm_sha256 of glacier, shepard and ensemble renders bit for bit (linux/amd64, GOAMD64=v1). Update only
// with a dsp_version bump.
func TestD4_Golden(t *testing.T) {
	dsptest.RequireGolden(t)
	got := d4Hashes(t)
	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if *updateD4 {
		var sb strings.Builder
		sb.WriteString("# pcm_sha256 of the 24-bit render of each case on a fixed synthetic input, seed 7. linux/amd64, GOAMD64=v1.\n")
		for _, k := range keys {
			sb.WriteString(k + " " + got[k] + "\n")
		}
		if err := os.WriteFile(d4GoldenPath, []byte(sb.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(d4GoldenPath)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if f := strings.Fields(line); len(f) == 3 && f[0][0] != '#' {
			want[f[0]+" "+f[1]] = f[2]
		}
	}
	if len(want) != len(got) {
		t.Errorf("golden has %d rows, the cases are %d", len(want), len(got))
	}
	for _, k := range keys {
		if got[k] != want[k] {
			t.Errorf("%s: pcm_sha256 %s, golden %s", k, got[k], want[k])
		}
	}
}

// TestD4_GoldenFMAOff re-executes the goldens with the run-time FMA paths switched off and requires the same bits.
func TestD4_GoldenFMAOff(t *testing.T) {
	dsptest.RequireGolden(t)
	dsptest.ReexecFMAOff(t, `^TestD4_Golden$`)
}

func TestD4_Registered(t *testing.T) {
	t.Parallel()
	for _, k := range []string{KindGlacier, KindShepard, KindEnsemble} {
		if _, u, ok := Lookup(k); !ok || u(48000, 48000, 48000, json.RawMessage(`{}`)) <= 0 {
			t.Errorf("%s is not registered with a units model", k)
		}
	}
}
