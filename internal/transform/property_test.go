package transform_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
	"github.com/nicolasacchi/metamusic/internal/dsp/ops"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
	"github.com/nicolasacchi/metamusic/internal/dsp/safety"
	"github.com/nicolasacchi/metamusic/internal/transform"
)

// The property suite of T1 (C216.4, C224.2): random in-range requests of every registered DSP kind, run through the pure
// ops.Run (the code the child runs), must come out finite, of the closed-form length, true-peak safe, loudness-safe,
// reproducible, within the work model and (for widen) inside its correlation bounds. 12 timelines per PR; MM_PROPERTY_TIMELINES
// (weekly: 300, sharded by MM_PROPERTY_SHARD=i/N) raises the count. A failing seed replays by its subtest name.

func propertyTimelines() []int {
	n := 12
	if raceOn() { // the detector makes the DSP loops 5-10x slower (150 s for 3 timelines in a CI shard); the weekly run (no -race) keeps the full count
		n = 1
	}
	if v, err := strconv.Atoi(os.Getenv("MM_PROPERTY_TIMELINES")); err == nil && v > 0 {
		n = v
	}
	shard, of := 0, 1
	if s := os.Getenv("MM_PROPERTY_SHARD"); s != "" {
		if _, err := fmt.Sscanf(s, "%d/%d", &shard, &of); err != nil || of < 1 || shard < 0 || shard >= of {
			shard, of = 0, 1
		}
	}
	var seeds []int
	for i := 0; i < n; i++ {
		if i%of == shard {
			seeds = append(seeds, i+1)
		}
	}
	return seeds
}

const propRate = 16000

// raceOn reports whether this test binary was built with -race.
func raceOn() bool {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	for _, s := range bi.Settings {
		if s.Key == "-race" && s.Value == "true" {
			return true
		}
	}
	return false
}

// propCase is one random request: the kind's ops params, the inputs and the closed-form output length (-1 when the kind has none).
type propCase struct {
	kind      string
	params    json.RawMessage
	alignment json.RawMessage
	inputs    []ops.Input
	wantLen   int // frames, -1 = not closed-form here
	minLen    int // frames: a lower bound where there is no closed form
	rho       float64
	pair      bool
}

func uniform(r *rand.Rand, lo, hi float64) float64 { return lo + (hi-lo)*r.Float64() }
func uniformStep(r *rand.Rand, lo, hi, step float64) float64 {
	return lo + step*float64(r.IntN(int(math.Round((hi-lo)/step))+1))
}

// musical is a deterministic 16 kHz stereo input of 3-8 s: a click grid over pink noise and a tone.
func musical(r *rand.Rand, seconds float64) pcm.Buffer {
	n := dsptest.Frames(propRate, seconds)
	seed := r.Uint64()
	l, rr := dsptest.Pink(seed, n), dsptest.Pink(seed+1, n)
	tone := dsptest.Sine(propRate, uniform(r, 110, 880), uniform(r, 0.02, 0.2), seconds)
	click := dsptest.Clicks(propRate, 120, uniform(r, 0.2, 0.8), seconds)
	for i := range l {
		l[i] = l[i]*uniform(r, 0.05, 0.15) + tone[i] + click[i]
		rr[i] = rr[i]*0.1 + 0.8*tone[i] + click[i]
	}
	return dsptest.Stereo(propRate, l, rr)
}

func buildCase(t *testing.T, kind string, r *rand.Rand) propCase {
	t.Helper()
	secs := uniform(r, 3, 6)
	c := propCase{kind: kind, wantLen: -1}
	switch kind {
	case "morph", "polytempo":
		c.pair = true
		sa, sb := uniform(r, 15, 18), uniform(r, 15, 18) // a pair needs bars of room; 16 kHz keeps it cheap
		c.inputs = []ops.Input{{Role: "a", Buf: musical(r, sa), BeatsPerBar: 4}, {Role: "b", Buf: musical(r, sb), BeatsPerBar: 4}}
		bpmA, bpmB := uniformStep(r, 110, 130, 1), uniformStep(r, 110, 130, 1)
		d := 0.1
		c.alignment, _ = json.Marshal(transform.Alignment{Inputs: []transform.InputAlign{
			{BPM: bpmA, BPMSource: "explicit", DownbeatS: &d, BeatsPerBar: 4}, {BPM: bpmB, BPMSource: "explicit", DownbeatS: &d, BeatsPerBar: 4}}})
		if kind == "morph" {
			c.params, _ = json.Marshal(map[string]any{"bars": 1 + r.IntN(2), "beats_per_bar": 4, "a_at": map[string]any{"bar": 0}, "b_at": map[string]any{"bar": 0},
				"bpm_a": bpmA, "bpm_b": bpmB, "semitones_b": uniformStep(r, -3, 3, 1), "key_policy": "to_a"})
		} else {
			c.params, _ = json.Marshal(map[string]any{"beats_per_bar": 4, "bpm_a": bpmA, "bpm_b": bpmB, "converge_bar": 4, "unison_bars": r.IntN(2),
				"pan": []string{"split", "centre"}[r.IntN(2)]})
		}
		return c
	}
	in := musical(r, secs)
	var member map[string]any
	switch kind {
	case "glacier":
		length := uniform(r, 1, math.Min(secs-0.5, 2.5))
		member = map[string]any{"source_id": "x", "factor": uniformStep(r, 10, 14, 0.5), "start_s": 0.0, "length_s": math.Round(length*10) / 10}
	case "shepard":
		member = map[string]any{"source_id": "x", "loop_s": uniformStep(r, 1, 3, 0.5), "start_s": 0.0, "layers": 3 + r.IntN(2), "period_s": 8.0,
			"duration_s": uniformStep(r, 8, 11, 1), "direction": []string{"up", "down"}[r.IntN(2)]}
	case "ensemble":
		member = map[string]any{"source_id": "x", "space": []string{"cathedral", "cistern", "hangar", "canyon"}[r.IntN(4)], "wet_db": uniformStep(r, -12, -3, 1), "tail_s": uniformStep(r, 0, 2, 0.5)}
	case "master":
		member = map[string]any{"source_id": "x", "preset": []string{"neutral", "warm", "bright", "punch"}[r.IntN(4)], "comp_amount": uniformStep(r, 0, 1, 0.05)}
	case "binaural":
		member = map[string]any{"source_id": "x", "model": []string{"hrtf", "parametric"}[r.IntN(2)], "preset": []string{"studio", "wide", "near"}[r.IntN(3)],
			"room": []string{"none", "studio", "hall"}[r.IntN(3)], "room_mix_db": -12 - r.IntN(20), "tail_s": uniformStep(r, 0, 2, 0.5)}
	case "widen":
		c.rho = uniform(r, -0.8, 0.99)
		l, rr := dsptest.Correlated(r.Uint64(), dsptest.Frames(propRate, secs), uniform(r, 0.05, 0.2), c.rho)
		in = dsptest.Stereo(propRate, l, rr)
		member = map[string]any{"source_id": "x", "width": uniformStep(r, 1, 2, 0.05), "decorrelate": uniformStep(r, 0, 0.5, 0.05)}
	default:
		t.Fatalf("the property suite does not know kind %q: add a generator for it", kind)
	}
	raw, _ := json.Marshal(member)
	ks, ok := transform.LookupKind(transform.Kind(kind))
	if !ok {
		t.Fatalf("kind %s is registered with ops but not with the transform package", kind)
	}
	resolved, err := ks.Validate(raw)
	if err != nil {
		t.Fatalf("%s: the generator produced a request the validator refuses: %v: %s", kind, err, raw)
	}
	c.params = resolved
	c.inputs = []ops.Input{{Role: "source", Buf: in, BeatsPerBar: 4}}
	c.minLen = in.Frames()
	switch kind {
	case "master", "widen":
		c.wantLen = in.Frames()
	case "glacier":
		var p struct{ Factor, LengthS float64 }
		_ = json.Unmarshal(resolved, &struct {
			Factor  *float64 `json:"factor"`
			LengthS *float64 `json:"length_s"`
		}{&p.Factor, &p.LengthS})
		c.wantLen = ops.GlacierLen(p.LengthS, p.Factor, propRate)
	case "shepard":
		var p struct {
			D float64 `json:"duration_s"`
		}
		_ = json.Unmarshal(resolved, &p)
		c.wantLen = dsptest.Frames(propRate, p.D)
	}
	return c
}

func (c propCase) job(seed int64) ops.Job {
	return ops.Job{Kind: c.kind, Rate: propRate, Seed: seed, Params: c.params, Alignment: c.alignment, Inputs: c.inputs, TargetLUFS: -16, Meter: &dsp.Meter{Max: 1 << 50}}
}

// ops kinds the suite runs: every DSP kind the transform package knows. The ops registry decides, so a new kind that registers
// without a generator fails buildCase loudly instead of being skipped.
func propertyKinds() []string {
	var out []string
	for _, k := range ops.Kinds() {
		if _, ok := transform.LookupKind(transform.Kind(k)); ok {
			out = append(out, k)
		}
	}
	return out
}

// TestProperty_AnyTransformIsSafe: see the header of this file.
func TestProperty_AnyTransformIsSafe(t *testing.T) {
	if testing.Short() {
		t.Skip("property suite renders every kind; runs in make test and CI")
	}
	kinds := propertyKinds()
	if len(kinds) < 7 {
		t.Fatalf("only %v are registered: the five T1a kinds, master and widen are expected", kinds)
	}
	for _, kind := range kinds {
		for _, seed := range propertyTimelines() {
			t.Run(fmt.Sprintf("%s/seed=%d", kind, seed), func(t *testing.T) {
				r := rand.New(rand.NewPCG(uint64(seed), 0x7e57))
				c := buildCase(t, kind, r)
				gor := runtime.NumGoroutine()
				out1, rep1, err := ops.Run(context.Background(), c.job(int64(seed)))
				if err != nil {
					t.Fatalf("run: %v (params %s)", err, c.params)
				}
				if g := runtime.NumGoroutine(); g != gor {
					t.Errorf("a goroutine was started: %d -> %d", gor, g)
				}
				for ch := range out1.Ch {
					for i, v := range out1.Ch[ch] {
						if math.IsNaN(v) || math.IsInf(v, 0) {
							t.Fatalf("non-finite sample %d of channel %d", i, ch)
						}
					}
				}
				if c.wantLen >= 0 && out1.Frames() != c.wantLen {
					t.Errorf("length %d frames, closed form %d", out1.Frames(), c.wantLen)
				}
				if tp := safety.TruePeakDBTP(out1); tp > -0.95 {
					t.Errorf("true peak %.3f dBTP > -0.95", tp)
				}
				raw, _ := json.Marshal(rep1)
				var doc struct {
					Safety struct {
						Effective float64 `json:"target_lufs_effective"`
						LufsOut   float64 `json:"lufs_out"`
						Limiter   struct {
							Engaged float64 `json:"engaged_fraction"`
						} `json:"limiter"`
					} `json:"safety"`
					Hifi struct {
						Widen *struct {
							RhoIn  float64 `json:"rho_in"`
							RhoOut float64 `json:"rho_out"`
						} `json:"widen"`
					} `json:"hifi"`
				}
				if err := json.Unmarshal(raw, &doc); err != nil {
					t.Fatal(err)
				}
				if d := math.Abs(doc.Safety.LufsOut - doc.Safety.Effective); d > 1.5 && doc.Safety.Limiter.Engaged <= 0.1 {
					t.Errorf("loudness %.2f LUFS, effective target %.2f, limiter %.2f", doc.Safety.LufsOut, doc.Safety.Effective, doc.Safety.Limiter.Engaged)
				}
				// the work model: the meter stays within 10 % of the registered model, and so far inside the budget, which is
				// 2 x the expected CPU plus 5 s (C209.3)
				model, ok := ops.Units(kind, totalFrames(c.inputs), int64(out1.Frames()), propRate, c.params)
				if !ok || float64(rep1.Units) > 1.1*float64(model) {
					t.Errorf("metered %d units, model %d (ok=%v)", rep1.Units, model, ok)
				}
				if c.wantLen < 0 && out1.Frames() < max(c.minLen, 1) {
					t.Errorf("output of %d frames is shorter than its input (%d) or empty", out1.Frames(), c.minLen)
				}
				if kind == "widen" && doc.Hifi.Widen != nil {
					if floor := math.Min(0.30, doc.Hifi.Widen.RhoIn); doc.Hifi.Widen.RhoOut < floor-1e-9 {
						t.Errorf("rho_out %.3f below min(0.30, rho_in %.3f)", doc.Hifi.Widen.RhoOut, doc.Hifi.Widen.RhoIn)
					}
				}
				// the same job again: the same audio, bit for bit
				out2, _, err := ops.Run(context.Background(), c.job(int64(seed)))
				if err != nil {
					t.Fatal(err)
				}
				var b1, b2 bytes.Buffer
				s1, _ := pcm.WritePCM24(&b1, out1)
				s2, _ := pcm.WritePCM24(&b2, out2)
				if s1 != s2 {
					t.Errorf("pcm_sha256 differs across two runs: %s != %s", s1, s2)
				}
			})
		}
	}
}

func totalFrames(in []ops.Input) int64 {
	var n int64
	for _, x := range in {
		n += int64(x.Buf.Frames())
	}
	return n
}

// TestProperty_SpecCanonical: the spec hash does not depend on whether defaults are spelled out or omitted, and any change of a
// parameter or of a parent's master changes it.
func TestProperty_SpecCanonical(t *testing.T) {
	r := newRig(t, false)
	est := func(q transform.Request) transform.Estimate {
		t.Helper()
		e, err := r.svc.Estimate(context.Background(), human, q)
		if err != nil {
			t.Fatalf("estimate: %v", err)
		}
		return e
	}
	req := func(k transform.Kind, member map[string]any) transform.Request {
		raw, _ := json.Marshal(member)
		return transform.Request{Kind: k, Members: map[transform.Kind]json.RawMessage{k: raw}}
	}
	a := r.fx["A"]
	rng := rand.New(rand.NewPCG(7, 7))
	for i := 0; i < 12; i++ {
		factor := uniformStep(rng, 10, 30, 0.5)
		base := req(transform.KindGlacier, map[string]any{"source_id": a, "factor": factor})
		e1 := est(base)
		// the same request with every default spelled out
		var spelled map[string]any
		if err := json.Unmarshal(e1.Resolved, &spelled); err != nil {
			t.Fatal(err)
		}
		spelled["source_id"] = a
		e2 := est(req(transform.KindGlacier, spelled))
		if e1.SpecHash != e2.SpecHash {
			t.Fatalf("defaults spelled out change the hash (factor %v): %s != %s\nresolved %s", factor, e1.SpecHash, e2.SpecHash, e1.Resolved)
		}
		// a changed parameter changes it
		changed := req(transform.KindGlacier, map[string]any{"source_id": a, "factor": factor + 0.5})
		if est(changed).SpecHash == e1.SpecHash {
			t.Errorf("factor %v and %v hash alike", factor, factor+0.5)
		}
		other := req(transform.KindGlacier, map[string]any{"source_id": a, "factor": factor, "window_s": 1.0 + rng.Float64()*0.4})
		if est(other).SpecHash == e1.SpecHash {
			t.Errorf("a changed window_s did not change the hash")
		}
	}
	// another seed, another kind, another parent: all different
	g := req(transform.KindGlacier, map[string]any{"source_id": a})
	seed := int64(9)
	gs := g
	gs.Seed = &seed
	m := req(transform.KindMaster, map[string]any{"source_id": a})
	gb := req(transform.KindGlacier, map[string]any{"source_id": r.fx["B"]})
	hashes := map[string]string{"g": est(g).SpecHash, "seed": est(gs).SpecHash, "master": est(m).SpecHash, "parentB": est(gb).SpecHash}
	seen := map[string]string{}
	for k, h := range hashes {
		if prev, dup := seen[h]; dup {
			t.Errorf("%s and %s share spec hash %s", k, prev, h)
		}
		seen[h] = k
	}
	// a parent's master sha is part of the spec (the frozen-master trigger is dropped in this scratch library only)
	before := est(g).SpecHash
	r.exec(`DROP TRIGGER tracks_frozen`)
	r.exec(`UPDATE tracks SET master_sha256 = ? WHERE id = ?`, strings.Repeat("ab", 32), a)
	if after := est(g).SpecHash; after == before {
		t.Error("a changed parent master did not change the spec hash")
	}
}

// TestProperty_TransformsNeverSpend (TestGuards_TransformsNeverSpend, as a property): a random sequence of estimates, creates,
// reuses, cancels, runs and refusals, by a human and by agents, adds no row to any money, job or backend table, and no estimate
// or view ever shows a cost (the wire types carry the constant 0; the REST side is TestAcceptance_T1_NoSpendRows).
func TestProperty_TransformsNeverSpend(t *testing.T) {
	r := newRig(t, false)
	tables := []string{"ledger", "batches", "jobs", "estimates", "provider_state", "spend_caps"}
	snap := func() string {
		var out []string
		for _, tb := range tables {
			out = append(out, fmt.Sprintf("%s=%d", tb, r.count(`SELECT COUNT(*) FROM `+tb)))
		}
		return strings.Join(out, " ")
	}
	before := snap()
	rng := rand.New(rand.NewPCG(11, 13))
	principals := []auth.Principal{human, agentA, agentB}
	labels := []string{"A", "B", "C"}
	member := func(k transform.Kind) map[string]any {
		a, b := r.fx[labels[rng.IntN(3)]], r.fx[labels[rng.IntN(2)]]
		switch k {
		case transform.KindGlacier:
			return map[string]any{"source_id": a, "factor": uniformStep(rng, 10, 30, 1)}
		case transform.KindMaster:
			return map[string]any{"source_id": a, "preset": []string{"neutral", "warm", "bright", "punch"}[rng.IntN(4)]}
		case transform.KindWiden:
			return map[string]any{"source_id": a, "width": uniformStep(rng, 1, 2, 0.05)}
		case transform.KindEnsemble:
			return map[string]any{"source_id": a, "tail_s": uniformStep(rng, 0, 10, 1)}
		default:
			return map[string]any{"track_a_id": r.fx["A"], "track_b_id": b, "downbeat_a_s": 0.1, "downbeat_b_s": 0.1}
		}
	}
	kinds := []transform.Kind{transform.KindGlacier, transform.KindMaster, transform.KindWiden, transform.KindEnsemble, transform.KindMorph}
	var live []string
	for step := 0; step < 60; step++ {
		p := principals[rng.IntN(len(principals))]
		k := kinds[rng.IntN(len(kinds))]
		raw, _ := json.Marshal(member(k))
		q := transform.Request{Kind: k, Members: map[transform.Kind]json.RawMessage{k: raw}}
		switch rng.IntN(4) {
		case 0:
			if e, err := r.svc.Estimate(context.Background(), p, q); err == nil && e.CPU.ExpectedMS <= 0 {
				t.Errorf("estimate without a CPU model: %+v", e.CPU)
			}
		case 1, 2:
			if res, err := r.svc.Create(context.Background(), p, q, nil); err == nil {
				live = append(live, res.View.ID)
			}
		default:
			if len(live) > 0 {
				_ = r.svc.Cancel(context.Background(), human, live[rng.IntN(len(live))])
			}
			r.runOne()
		}
		if now := snap(); now != before {
			t.Fatalf("step %d: money or backend tables moved: %s -> %s", step, before, now)
		}
	}
	if r.count(`SELECT COUNT(*) FROM transforms`) == 0 {
		t.Error("the sequence created nothing: the test is vacuous")
	}
}
