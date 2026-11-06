package transform

import (
	"fmt"
	"github.com/nicolasacchi/metamusic/internal/dsp/ops"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/config"
)

// TestCost_Table pins the formula of C209.2 on three fixed inputs with NSPerUnit injected, so no test depends on hardware.
func TestCost_Table(t *testing.T) {
	t.Parallel()
	c := CostConstants{NSPerUnitMilli: 1000, DecodeMSPerS: 3, EncodeMSPerS: 10, IngestMSPerS: 30, ToolMSPerS: 40, SafetyUnitsPerFrame: 30}
	for _, tc := range []struct {
		name            string
		units, inMS, ms int64
		tool            bool
		want            int64
	}{
		// 1e9 units at 1 ns = 1.3 s with the 1.3 margin; decode 3 ms/s of 20 s; encode 10 and ingest 30 ms/s of 20 s
		{"compute plus shares", 1_000_000_000, 20_000, 20_000, false, 1300 + 60 + 200 + 600},
		{"score render adds the tool", 0, 0, 10_000, true, 100 + 300 + 400},
		{"rounds up", 1, 1, 1, false, 1 + 1 + 1 + 1},
	} {
		if got := c.ExpectedMS(tc.units, tc.inMS, tc.ms, tc.tool); got != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, got, tc.want)
		}
	}
	// the budget: min(MaxCPUS x 1000, 2 x expected + 5000)
	for _, tc := range []struct {
		exp  int64
		cap  int
		want int64
	}{{1000, 600, 7000}, {400_000, 600, 600_000}, {0, 30, 5000}} {
		if got := BudgetMS(tc.exp, tc.cap); got != tc.want {
			t.Errorf("BudgetMS(%d, %d) = %d, want %d", tc.exp, tc.cap, got, tc.want)
		}
	}
	// memory: (sum in + out x (1+k)) x 16 B / 2^20 + 64, planar float64 stereo
	if got := MemoryMB(48000*20, 48000*20, 3); got != ceilDiv((48000*20+48000*20*4)*16, 1<<20)+64 {
		t.Errorf("memory %d", got)
	}
	if got := OutBytesEst(1000); got != 3900 {
		t.Errorf("out bytes %d", got)
	}
}

// TestCost_Monotonic: more bars, layers, factor or tail never cost less, for every kind that has the knob.
func TestCost_Monotonic(t *testing.T) {
	t.Parallel()
	check := func(name string, vals []int64) {
		t.Helper()
		if !slices.IsSorted(vals) {
			t.Errorf("%s units not monotonic: %v", name, vals)
		}
	}
	var morph, shep, glac, ens []int64
	for bars := 1; bars <= 32; bars++ {
		morph = append(morph, unitsMorph(UnitsIn{Channels: 2, Rate: 48000, OutFrames: 48000 * 100, Resolved: []byte(fmt.Sprintf(`{"bars":%d,"beats_per_bar":4,"bpm_a":92,"bpm_b":128}`, bars))}))
	}
	for layers := 3; layers <= 7; layers++ {
		shep = append(shep, unitsShepard(UnitsIn{Channels: 2, Rate: 48000, OutFrames: 48000 * 60, Resolved: []byte(fmt.Sprintf(`{"layers":%d}`, layers))}))
	}
	for factor := 10; factor <= 50; factor++ {
		glac = append(glac, unitsGlacier(UnitsIn{Channels: 2, Rate: 48000, OutFrames: int64(factor) * 48000 * 10, Resolved: []byte(`{"window_s":0.5}`)}))
	}
	for tail := 0; tail <= 40; tail++ {
		ens = append(ens, unitsEnsemble(UnitsIn{Channels: 2, Rate: 48000, InFrames: 48000 * 20, OutFrames: 48000 * (20 + int64(tail)), Resolved: []byte(fmt.Sprintf(`{"space":"cathedral","tail_s":%d}`, tail))}))
	}
	check("morph bars", morph)
	check("shepard layers", shep)
	check("glacier factor", glac)
	check("ensemble tail", ens)
	// and the whole plan grows with the output: a longer glacier never costs less
	r := newRig(t)
	prev := int64(0)
	for _, f := range []string{"10", "20", "30", "50"} {
		pl := r.mustBuild(req(KindGlacier, fmt.Sprintf(`{"source_id":%q,"factor":%s,"length_s":10}`, r.tx("A"), f)))
		if pl.CPU.ExpectedMS < prev {
			t.Errorf("factor %s costs %d < %d", f, pl.CPU.ExpectedMS, prev)
		}
		prev = pl.CPU.ExpectedMS
	}
}

// tableCounts counts every table so a test can prove a call wrote nothing.
func (r *rig) tableCounts() map[string]int {
	r.t.Helper()
	rows, err := r.st.R.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		r.t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		names = append(names, n)
	}
	_ = rows.Close()
	out := map[string]int{}
	for _, n := range names {
		out[n] = r.count(`SELECT COUNT(*) FROM "` + n + `"`)
	}
	return out
}

// TestEstimate_WritesNothing: an estimate leaves the row count of every table unchanged and never wakes the runner.
func TestEstimate_WritesNothing(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	before := r.tableCounts()
	for _, q := range []Request{glacier(r.tx("A")), r.morphAB(""), req(KindEnsemble, fmt.Sprintf(`{"source_id":%q}`, r.tx("A")))} {
		if _, err := r.svc.Estimate(t.Context(), agentA, q); err != nil {
			t.Fatal(err)
		}
	}
	after := r.tableCounts()
	for n, c := range before {
		if after[n] != c {
			t.Errorf("table %s: %d rows -> %d", n, c, after[n])
		}
	}
	if r.wakes.Load() != 0 {
		t.Error("an estimate woke the runner")
	}
}

// TestEstimate_Deterministic: the same database state gives the same estimate, byte for byte.
func TestEstimate_Deterministic(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a, err := r.svc.Estimate(t.Context(), human, r.morphAB(""))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := r.svc.Estimate(t.Context(), human, r.morphAB(""))
	if !reflect.DeepEqual(a, b) {
		t.Errorf("estimates differ:\n%+v\n%+v", a, b)
	}
}

// TestEstimate_Content: lengths, CPU, memory, bytes, inputs, queue, quota, existing and tools of an estimate.
func TestEstimate_Content(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	e, err := r.svc.Estimate(t.Context(), agentA, glacier(r.tx("A")))
	if err != nil {
		t.Fatal(err)
	}
	// glacier of the 20 s fixture take: length 20 s x factor 20 = 400 s at the take's rate
	if e.Output.Seconds != 400 || e.Output.SampleRate != 48000 || e.Kind != KindGlacier {
		t.Errorf("output %+v", e.Output)
	}
	if e.CPU.ExpectedMS <= 0 || e.CPU.MaxMS != BudgetMS(e.CPU.ExpectedMS, 600) || e.CPU.BudgetMS != e.CPU.MaxMS {
		t.Errorf("cpu %+v", e.CPU)
	}
	if e.Output.BytesEst != OutBytesEst(400*48000) || e.MemoryMB != int(MemoryMB(20*48000, 400*48000, 1)) {
		t.Errorf("bytes %d memory %d", e.Output.BytesEst, e.MemoryMB)
	}
	if !strings.HasPrefix(e.Title, "Synthetic Take XA — glacier ×20") || len(e.Inputs) != 1 || e.Inputs[0].Role != "source" || e.Inputs[0].DurationS != 20 {
		t.Errorf("title %q inputs %+v", e.Title, e.Inputs)
	}
	if e.Queue != (QueueEstimate{Running: 0, Queued: 0, PositionIfCreated: 1}) || e.Existing != nil {
		t.Errorf("queue %+v existing %+v", e.Queue, e.Existing)
	}
	if e.Quota.Kind != "agent" || e.Quota.CapMS != 1_800_000 || e.Quota.UsedMS != 0 || e.Quota.ReservedMS != 0 {
		t.Errorf("quota %+v", e.Quota)
	}
	if !e.Tools.FFmpeg || !e.Tools.FLAC || e.Tools.Fluidsynth {
		t.Errorf("tools %+v", e.Tools)
	}
	if h, _ := r.svc.Estimate(t.Context(), human, glacier(r.tx("A"))); h.Quota.Kind != "human" || h.Quota.CapMS != 14_400_000 {
		t.Errorf("human quota %+v", h.Quota)
	}
	// once created, the estimate sees it as existing and the queue as one longer
	if _, err := r.svc.Create(t.Context(), human, glacier(r.tx("A")), nil); err != nil {
		t.Fatal(err)
	}
	e, _ = r.svc.Estimate(t.Context(), agentA, glacier(r.tx("A")))
	if e.Existing == nil || e.Existing.State != StateQueued || e.Existing.TakeID != "" || e.Queue.Queued != 1 || e.Queue.PositionIfCreated != 2 {
		t.Errorf("existing %+v queue %+v", e.Existing, e.Queue)
	}
	if e.Quota.ReservedMS != 0 { // the live transform is the human's: the agent pool is separate
		t.Errorf("the human's transform reserved from the agent pool: %+v", e.Quota)
	}
	if h, _ := r.svc.Estimate(t.Context(), human, glacier(r.tx("A"))); h.Quota.ReservedMS == 0 {
		t.Errorf("a live transform reserved nothing of its own pool: %+v", h.Quota)
	}
	// a morph reports its inputs with the resolved tempo and key
	e, err = r.svc.Estimate(t.Context(), human, r.morphAB(""))
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Inputs) != 2 || e.Inputs[0].BPM == nil || e.Inputs[0].BPM.Value != 92 || e.Inputs[0].BPM.Source != SrcDeclared || e.Inputs[1].Role != "b" || e.Inputs[0].BeatsPerBar != 4 {
		t.Errorf("inputs %+v", e.Inputs)
	}
	if !slices.Contains(codes(e.Warnings), WarnBeatPhasePending) || !slices.Contains(codes(e.Warnings), WarnLargeTempoChange) {
		t.Errorf("warnings %v", codes(e.Warnings))
	}
	for _, w := range e.Warnings {
		if w.Message == "" {
			t.Errorf("warning %s has no message", w.Code)
		}
	}
	if !strings.HasPrefix(e.Title, "Synthetic Take XA — → Synthetic Take XB morph") {
		t.Errorf("title %q", e.Title)
	}
	// ensemble warns about the full mix
	e, _ = r.svc.Estimate(t.Context(), human, req(KindEnsemble, fmt.Sprintf(`{"source_id":%q}`, r.tx("A"))))
	if !slices.Contains(codes(e.Warnings), WarnEnsembleFullMix) || math.Abs(e.Output.Seconds-(20+float64(ensembleTailFrames(paramsMap(e.Resolved), 48000))/48000)) > 1e-6 {
		t.Errorf("ensemble %v %v", codes(e.Warnings), e.Output.Seconds)
	}
}

// TestEstimate_Limits: each limit of C209.3/C209.4 answers 422 TRANSFORM_TOO_LARGE naming it.
func TestEstimate_Limits(t *testing.T) {
	t.Parallel()
	limit := func(t *testing.T, err error, name string) {
		t.Helper()
		pe := wantProblem(t, err, 422, ErrTransformTooLarge)
		if pe.Details["limit"] != name {
			t.Errorf("limit %v, want %s", pe.Details["limit"], name)
		}
	}
	big := func(r *rig) Request {
		return req(KindGlacier, fmt.Sprintf(`{"source_id":%q,"factor":50,"length_s":20}`, r.tx("A")))
	}
	t.Run("output_seconds", func(t *testing.T) {
		r := newRig(t, func(c *config.Config, _ *Options) { c.TransformMaxOutS = 60 })
		_, err := r.svc.Estimate(t.Context(), human, req(KindShepard, fmt.Sprintf(`{"source_id":%q,"loop_s":2,"duration_s":120}`, r.tx("A"))))
		limit(t, err, "output_seconds")
	})
	t.Run("cpu_seconds", func(t *testing.T) {
		r := newRig(t, func(c *config.Config, _ *Options) { c.TransformMaxOutS = 3600; c.TransformMaxCPUS = 30 })
		_, err := r.svc.Estimate(t.Context(), human, big(r))
		limit(t, err, "cpu_seconds")
	})
	t.Run("memory_mb", func(t *testing.T) {
		r := newRig(t, func(c *config.Config, _ *Options) { c.TransformMaxOutS = 3600; c.TransformMaxMemMB = 256 })
		_, err := r.svc.Estimate(t.Context(), human, big(r))
		limit(t, err, "memory_mb")
	})
	t.Run("input_seconds", func(t *testing.T) {
		r := newRig(t)
		r.unfrozen(func() { r.exec(`UPDATE tracks SET duration_s = 700 WHERE id = ?`, r.tx("A")) })
		_, err := r.svc.Estimate(t.Context(), human, req(KindGlacier, fmt.Sprintf(`{"source_id":%q,"length_s":10}`, r.tx("A"))))
		limit(t, err, "input_seconds")
	})
	t.Run("disk", func(t *testing.T) {
		r := newRig(t, func(_ *config.Config, o *Options) { o.FreeBytes = func(string) (uint64, error) { return 1 << 20, nil } })
		_, err := r.svc.Estimate(t.Context(), human, glacier(r.tx("A"))) // 400 s of output is 1.5 MB x 3
		limit(t, err, "disk")
		r2 := newRig(t, func(_ *config.Config, o *Options) { o.FreeBytes = func(string) (uint64, error) { return 1 << 40, nil } })
		if _, err := r2.svc.Estimate(t.Context(), human, glacier(r2.tx("A"))); err != nil {
			t.Fatal(err)
		}
	})
}

// TestEstimate_RefusesWhatCreateRefuses: estimate and create share one build, so they cannot disagree.
func TestEstimate_RefusesWhatCreateRefuses(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	for name, q := range map[string]Request{
		"unknown take":   glacier("nope"),
		"reference":      glacier(r.fx.Parts[0]),
		"bad range":      req(KindGlacier, fmt.Sprintf(`{"source_id":%q,"factor":9}`, r.tx("A"))),
		"unresolved":     req(KindMorph, fmt.Sprintf(`{"track_a_id":%q,"track_b_id":%q}`, r.tx("A"), r.tx("B"))),
		"kind mismatch":  {Kind: KindGlacier, Members: map[Kind]jsonRaw{KindShepard: jsonRaw(`{"source_id":"x"}`)}},
		"no member":      {Kind: KindGlacier, Members: map[Kind]jsonRaw{}},
		"two members":    {Kind: KindGlacier, Members: map[Kind]jsonRaw{KindGlacier: jsonRaw(`{"source_id":"x"}`), KindShepard: jsonRaw(`{"source_id":"x"}`)}},
		"unknown kind":   {Kind: "reverb", Members: map[Kind]jsonRaw{"reverb": jsonRaw(`{}`)}},
		"bad target":     {Kind: KindGlacier, TargetLUFS: f64(-5), Members: map[Kind]jsonRaw{KindGlacier: jsonRaw(fmt.Sprintf(`{"source_id":%q}`, r.tx("A")))}},
		"long rationale": {Kind: KindGlacier, Rationale: ptrStr(strings.Repeat("x", 1001)), Members: map[Kind]jsonRaw{KindGlacier: jsonRaw(fmt.Sprintf(`{"source_id":%q}`, r.tx("A")))}},
	} {
		_, e1 := r.svc.Estimate(t.Context(), human, q)
		_, e2 := r.svc.Create(t.Context(), human, q, nil)
		if e1 == nil || e2 == nil || e1.Error() != e2.Error() {
			t.Errorf("%s: estimate %v, create %v", name, e1, e2)
		}
	}
}

// TestCost_B2bMeasured pins the constants and estimates recalibrated from other lanes' measurements (T1-B2b).
func TestCost_B2bMeasured(t *testing.T) {
	t.Parallel()
	// safety.Finish: about 700 units per frame, measured by H1 (the plan's 30 was 24x too low)
	if DefaultCosts.SafetyUnitsPerFrame != 700 || DefaultCosts.SafetyUnitsPerFrame != ops.SafetyUnits(1) {
		t.Errorf("SafetyUnitsPerFrame = %d", DefaultCosts.SafetyUnitsPerFrame)
	}
	// glacier holds the whole output for safety.Finish
	if ks, _ := LookupKind(KindGlacier); ks.MemFactor < 1 {
		t.Errorf("glacier MemFactor = %v", ks.MemFactor)
	}
	// the cathedral tail is D4's bound: pre-delay + 70/60 of the decay + onset, at least the measured ~11.6 s effective tail
	// rounded down, and never the old rt60 x 70/60 = 10.5 s
	m := paramsMap([]byte(`{"space":"cathedral","tail_s":40}`))
	tail := float64(ensembleTailFrames(m, 48000)) / 48000
	if tail < 10.6 || tail > 15 {
		t.Errorf("cathedral tail %.2f s", tail)
	}
	if got := ensembleTailFrames(paramsMap([]byte(`{"space":"cathedral","tail_s":3}`)), 48000); got != 3*48000 {
		t.Errorf("tail_s cut: %d", got)
	}
	// ensemble and glacier units are the operators' own prices plus safety
	u := UnitsIn{InFrames: 48000 * 20, OutFrames: 48000 * 400, Channels: 2, Rate: 48000, Resolved: []byte(`{"window_s":0.5}`)}
	if got, want := unitsGlacier(u), ops.GlacierUnits(u.OutFrames, 48000, 0.5)+700*u.OutFrames; got != want {
		t.Errorf("glacier units %d, want %d", got, want)
	}
	u = UnitsIn{InFrames: 48000 * 20, OutFrames: 48000 * 32, Channels: 2, Rate: 48000, Resolved: []byte(`{"space":"cathedral","tail_s":20}`)}
	if unitsEnsemble(u) < ops.EnsembleUnits(u.InFrames, 48000, ops.EnsembleParams{Space: "cathedral", TailS: 20})+700*u.OutFrames {
		t.Error("ensemble units below the operator's price plus safety")
	}
	// a glacier estimate now carries the whole-output memory
	r := newRig(t)
	e, err := r.svc.Estimate(t.Context(), human, glacier(r.tx("A")))
	if err != nil {
		t.Fatal(err)
	}
	if want := int(MemoryMB(20*48000, 400*48000, 1)); e.MemoryMB != want {
		t.Errorf("glacier memory %d, want %d", e.MemoryMB, want)
	}
}
