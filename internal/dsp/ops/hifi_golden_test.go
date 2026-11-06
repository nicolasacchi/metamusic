package ops

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
	"github.com/nicolasacchi/metamusic/internal/dsp/safety"
)

var updateHifi = flag.Bool("update-hifi", false, "rewrite testdata/hifi_{master,widen}.sha256 (after a deliberate dsp_version bump only)")

// hifiGoldenCase is one golden row: a kind, a name and the resolved params; the input is the same synthetic pink pair for all.
type hifiGoldenCase struct{ kind, name, params string }

var hifiGoldenCases = []hifiGoldenCase{
	{KindMaster, "neutral", `{"preset":"neutral"}`},
	{KindMaster, "warm", `{"preset":"warm"}`},
	{KindMaster, "bright", `{"preset":"bright"}`},
	{KindMaster, "punch", `{"preset":"punch"}`},
	{KindMaster, "warm_ceiling3", `{"preset":"warm","ceiling_dbtp":-3}`},
	{KindWiden, "default", `{}`},
	{KindWiden, "wide2", `{"width":2,"decorrelate":0,"bass_mono_hz":0}`},
	{KindWiden, "bass250", `{"width":1.5,"decorrelate":0.2,"bass_mono_hz":250}`},
	{KindWiden, "mono_decorrelate", `{"width":1.3,"decorrelate":0.3,"bass_mono_hz":120}`},
}

func hifiGoldenInput(name string) pcm.Buffer {
	if name == "mono_decorrelate" {
		return dsptest.Dual(testRate, dsptest.Pink(77, dsptest.Frames(testRate, 2)))
	}
	b := dsptest.PinkStereo(testRate, 76, 2, 0.6)
	l, _ := safety.LUFS(b)
	g := dsptest.DBToLin(-24 - l)
	for c := range 2 {
		b.Ch[c] = dsptest.Scale(b.Ch[c], g)
	}
	return b
}

// hifiHashes renders every golden case through the registry and returns pcm_sha256 per "kind name".
func hifiHashes(t *testing.T) map[string]string {
	t.Helper()
	got := map[string]string{}
	for _, c := range hifiGoldenCases {
		run, _, ok := Lookup(c.kind)
		if !ok {
			t.Fatalf("%s is not registered", c.kind)
		}
		b := hifiGoldenInput(c.name)
		out, err := run(context.Background(), RunIn{Rate: b.Rate, Inputs: []pcm.Buffer{b}, Params: json.RawMessage(c.params), TargetLUFS: -16, RefLUFS: -24})
		if err != nil {
			t.Fatalf("%s %s: %v", c.kind, c.name, err)
		}
		sum, err := pcm.WritePCM24(io.Discard, out.Buffer)
		if err != nil {
			t.Fatal(err)
		}
		got[c.kind+" "+c.name] = strings.TrimSpace(sum)
		_, _ = hex.DecodeString(sum) // pcm_sha256 is lower-case hex
	}
	return got
}

// TestHifi_Golden pins pcm_sha256 of every master preset and widen parameter set bit for bit (linux/amd64, GOAMD64=v1).
func TestHifi_Golden(t *testing.T) {
	dsptest.RequireGolden(t)
	got := hifiHashes(t)
	for _, kind := range []string{KindMaster, KindWiden} {
		path := "testdata/hifi_" + kind + ".sha256"
		var names []string
		for _, c := range hifiGoldenCases {
			if c.kind == kind {
				names = append(names, c.name)
			}
		}
		if *updateHifi {
			var sb strings.Builder
			sb.WriteString("# pcm_sha256 of the " + kind + " operator on a fixed synthetic input (C224.1). linux/amd64, GOAMD64=v1.\n")
			for _, n := range names {
				sb.WriteString(n + " " + got[kind+" "+n] + "\n")
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
		if len(want) != len(names) {
			t.Errorf("%s has %d rows, want %d", path, len(want), len(names))
		}
		for _, n := range names {
			if got[kind+" "+n] != want[n] {
				t.Errorf("%s %s: hash %s, golden %s", kind, n, got[kind+" "+n], want[n])
			}
		}
	}
}

// TestHifi_GoldenFMAOff re-executes the goldens under GODEBUG=cpu.fma=off and requires the same bits.
func TestHifi_GoldenFMAOff(t *testing.T) {
	dsptest.RequireGolden(t)
	dsptest.ReexecFMAOff(t, `^TestHifi_Golden$`)
}

// TestHifi_Registered: both kinds are in the registry with their units model, so the dispatcher, the determinism sweep and the
// property suite that iterate Kinds() cover them.
func TestHifi_Registered(t *testing.T) {
	t.Parallel()
	kinds := Kinds()
	for _, k := range []string{KindMaster, KindWiden} {
		if !slices.Contains(kinds, k) {
			t.Errorf("%s not registered (Kinds = %v)", k, kinds)
		}
		if _, u, ok := Lookup(k); !ok || u(48000, 48000, 48000, nil) <= SafetyUnits(48000) {
			t.Errorf("%s units model missing or not above the safety stage", k)
		}
	}
}
