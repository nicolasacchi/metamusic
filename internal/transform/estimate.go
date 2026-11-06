package transform

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"path/filepath"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

// Request is the body of estimate and create (C212.2) in this package's terms: Members holds the JSON object of every member
// the caller sent (a JSON null counts as absent); exactly the member named by Kind may be present.
type Request struct {
	Kind       Kind
	Title      *string
	Rationale  *string
	Seed       *int64
	TargetLUFS *float64
	Members    map[Kind]json.RawMessage
}

// Estimate is the answer of Estimate (C212.3): free, pure, deterministic for a database state; it writes nothing.
type Estimate struct {
	Kind        Kind
	SpecHash    string
	Title       string
	CPU         CPUEstimate
	WallExpectS float64
	Output      OutputEstimate
	MemoryMB    int
	Inputs      []EstimateInput
	Resolved    json.RawMessage
	Warnings    []Warning
	Quota       Quota
	Queue       QueueEstimate
	Existing    *Existing
	Tools       ToolsEstimate
}

// CPUEstimate is the deterministic CPU model (model "cpu/1").
type CPUEstimate struct{ ExpectedMS, MaxMS, BudgetMS int64 }

// OutputEstimate is the expected output: 24-bit stereo FLAC at the first input's rate.
type OutputEstimate struct {
	Seconds    float64
	SampleRate int
	BytesEst   int64
}

// EstimateInput is one input with its alignment resolution.
type EstimateInput struct {
	Role, TrackID, Title string
	DurationS            float64
	BPM                  *BPMInfo
	Key                  *KeyInfo
	BeatsPerBar          int
}

// BPMInfo is a resolved tempo and its source.
type BPMInfo struct {
	Value      float64
	Source     string
	OctaveFold int
	Confidence *float64
}

// KeyInfo is a resolved key.
type KeyInfo struct {
	Camelot string
	PC      int
	Minor   bool
	Source  string
}

// QueueEstimate is the queue as an estimate sees it.
type QueueEstimate struct{ Running, Queued, PositionIfCreated int }

// Existing names a live or done transform of the same spec (create would reuse it).
type Existing struct {
	TransformID string
	TakeID      string // set when the transform is done
	State       string
}

// ToolsEstimate reports the tools a render needs.
type ToolsEstimate struct {
	FFmpeg, FLAC, Fluidsynth bool
	Soundfont                string // the soundfont a score render would use; empty otherwise
}

// plan is a fully built, validated and resolved transform request: everything create inserts and estimate reports.
type plan struct {
	Req         Request
	Kind        KindSpec
	Spec        Spec
	Canon       Canonical
	Title       string
	Res         resolution
	CPU         CPUPlan
	Warnings    []Warning
	InFrames    int64
	OutFrames   int64
	PrincipalKd string // human | agent: the quota pool
}

// problem helpers

func unavailable(component string) error {
	return problem.E(http.StatusServiceUnavailable, "UNAVAILABLE", component+" is not available", map[string]any{"component": component})
}

func forbidden(msg string, d map[string]any) error {
	return problem.E(http.StatusForbidden, "FORBIDDEN", msg, d)
}

func tooLarge(limit string, value, ceiling int64) error {
	return problem.E(http.StatusUnprocessableEntity, ErrTransformTooLarge, "the transform exceeds the "+limit+" limit",
		map[string]any{"limit": limit, "value": value, "max": ceiling})
}

func (s *Service) tools() ToolStatus {
	if s.o.ToolStatus == nil {
		return ToolStatus{FFmpeg: true, FLAC: true}
	}
	return s.o.ToolStatus()
}

// ready is step 1 of the create order: the service is enabled and the media tools exist.
func (s *Service) ready() error {
	if !s.Enabled() {
		return unavailable("transform")
	}
	switch t := s.tools(); {
	case !t.FFmpeg:
		return unavailable("ffmpeg")
	case !t.FLAC:
		return unavailable("flac")
	}
	return nil
}

// principalPool maps a principal to its quota pool (D-T1-10): agents draw from the agent pool, every human-like principal (the
// operator, a paired device, the CLI) from the human pool.
func principalPool(p auth.Principal) (string, error) {
	if p.Kind == auth.KindAnonymous || p.Key() == "" {
		return "", forbidden("a transform needs an authenticated principal", nil)
	}
	return QuotaKind(p), nil
}

// build runs steps 2 to 5 of the create order (C210.1) over q, in order, and stops at the first refusal: the request shape
// and parameter ranges, the inputs (404, RECIPE_UNKNOWN, LOSSY_MASTER, the binaural chain), the alignment, the limits.
func (s *Service) build(ctx context.Context, q queryer, p auth.Principal, req Request) (*plan, error) {
	pool, err := principalPool(p)
	if err != nil {
		return nil, err
	}
	// 2. body shape and ranges
	if !req.Kind.Valid() {
		return nil, invalidErr(&RuleError{Field: "kind", Rule: RuleRange})
	}
	members := 0
	for k, raw := range req.Members {
		if string(raw) != "null" && len(raw) > 0 {
			members++
			if k != req.Kind {
				return nil, invalidErr(&RuleError{Field: string(k), Rule: RuleKindParamsMismatch})
			}
		}
	}
	if members != 1 || len(req.Members[req.Kind]) == 0 {
		return nil, invalidErr(&RuleError{Field: string(req.Kind), Rule: RuleKindParamsMismatch})
	}
	ks, ok := LookupKind(req.Kind)
	if !ok {
		if req.Kind == KindScoreRender {
			return nil, unavailable("soundfont")
		}
		return nil, unavailable("transform")
	}
	if t := req.TargetLUFS; t != nil && (*t < -30 || *t > -12 || math.IsNaN(*t)) {
		return nil, invalidErr(&RuleError{Field: "target_lufs", Rule: RuleRange})
	}
	if r := req.Rationale; r != nil && (utf8.RuneCountInString(*r) < 1 || utf8.RuneCountInString(*r) > MaxRationaleRunes) {
		return nil, invalidErr(&RuleError{Field: "rationale", Rule: RuleRange})
	}
	if t := req.Title; t != nil && (utf8.RuneCountInString(*t) < 1 || utf8.RuneCountInString(*t) > MaxTitleRunes) {
		return nil, invalidErr(&RuleError{Field: "title", Rule: RuleRange})
	}
	if sd := req.Seed; sd != nil && (*sd < 0 || *sd >= seedLimit) {
		return nil, invalidErr(&RuleError{Field: "seed", Rule: RuleRange})
	}
	validated, err := ks.Validate(req.Members[req.Kind])
	if err != nil {
		return nil, asProblem(err)
	}

	// 3. inputs exist and qualify
	refs := inputIDs(validated)
	if len(refs) > MaxInputs {
		return nil, invalidErr(&RuleError{Field: "inputs", Rule: RuleRange})
	}
	infos := make([]inputInfo, 0, len(refs))
	for _, r := range refs {
		in, err := s.loadInput(ctx, q, r)
		if err != nil {
			return nil, err
		}
		infos = append(infos, in)
	}
	var chain []Kind
	if len(infos) > 0 && (ks.Warnings != nil || req.Kind == KindBinaural) {
		if chain, err = chainKinds(ctx, q, infos[0].Ref.TrackID); err != nil {
			return nil, err
		}
	}
	if req.Kind == KindBinaural {
		for _, k := range chain {
			if k == KindBinaural { // the HRTF is never applied twice (C218.5)
				return nil, invalidErr(&RuleError{Field: "source_id", Rule: RuleAlreadyBinaural})
			}
		}
	}

	// 4. alignment
	res := resolution{}
	if len(infos) > 0 {
		if res, err = s.resolve(ctx, q, req.Kind, validated, infos); err != nil {
			return nil, asProblem(err)
		}
	} else {
		res.Params = validated
	}

	// the spec
	rate := 48000
	if len(infos) > 0 {
		rate = infos[0].Rate
	}
	pl := &plan{Req: req, Kind: ks, Res: res, PrincipalKd: pool}
	m := paramsMap(res.Params)
	spec := Spec{V: 1, Kind: req.Kind, DSPVersion: DSPVersion, SampleRate: rate, Params: res.Params, Alignment: res.Align}
	for _, in := range res.Inputs {
		spec.Inputs = append(spec.Inputs, in.Ref)
	}
	if usesSeed(req.Kind) {
		switch {
		case req.Seed != nil:
			spec.Seed = *req.Seed
		case len(infos) > 0:
			spec.Seed = DefaultSeed(infos[0].Ref.TrackID)
		}
	}
	spec.TargetLUFS = targetLUFS(req, m, res.Inputs)
	if pl.Canon, err = Canonicalize(spec); err != nil {
		return nil, err
	}
	spec.ID = pl.Canon.ID
	pl.Spec = spec

	primary, secondary := "", ""
	if len(res.Inputs) > 0 {
		primary = res.Inputs[0].Title
	}
	if len(res.Inputs) > 1 {
		secondary = res.Inputs[1].Title
	}
	pl.Title = DefaultTitle(req.Title, primary, ks.Title(TitleIn{Primary: primary, Secondary: secondary, Resolved: res.Params}))

	// cost, memory, output
	var inMS int64
	for _, in := range res.Inputs {
		inMS += framesMS(in.Frames, in.Rate)
		fr := in.Frames
		if in.Rate != rate && in.Rate > 0 { // a second input at another rate is resampled to the output rate (D-T1-19)
			fr = int64(math.Round(float64(in.Frames) * float64(rate) / float64(in.Rate)))
		}
		pl.InFrames += fr
	}
	pl.OutFrames = int64(math.Ceil(res.OutS * float64(rate)))
	pl.CPU = planCPU(DefaultCosts, ks, UnitsIn{InFrames: pl.InFrames, OutFrames: pl.OutFrames, Channels: 2, Rate: rate, Resolved: res.Params}, inMS, s.o.Config.TransformMaxCPUS)

	// warnings: from resolution, then the kind's own (rows only, no audio)
	pl.Warnings = append(pl.Warnings, res.Warnings...)
	if ks.Warnings != nil {
		pl.Warnings = append(pl.Warnings, ks.Warnings(WarnIn{Resolved: res.Params, Chain: chain})...)
	}
	pl.Warnings = dedupWarnings(pl.Warnings)

	// 5. limits
	if err := s.checkLimits(pl); err != nil {
		return nil, err
	}
	return pl, nil
}

// targetLUFS is the requested target of D-T1-14: an explicit one, else the primary parent's measured loudness, else -16,
// clamped to [-30, -12] and rounded to 0.1 LU so the spec is stable.
func targetLUFS(req Request, m map[string]json.RawMessage, in []inputInfo) float64 {
	t := TargetLUFSDefault
	switch {
	case req.TargetLUFS != nil:
		t = *req.TargetLUFS
	case rawFloat(m, "target_lufs") != nil:
		t = *rawFloat(m, "target_lufs")
	case len(in) > 0 && in[0].LUFS != nil:
		t = *in[0].LUFS
	}
	return round(math.Min(-12, math.Max(-30, t)), 1)
}

// checkLimits is step 5: input and output length, CPU, memory and disk, in that order (C209.3, C209.4).
func (s *Service) checkLimits(pl *plan) error {
	cfg := s.o.Config
	for _, in := range pl.Res.Inputs {
		if in.DurationS > MaxInputS {
			return tooLarge("input_seconds", int64(math.Ceil(in.DurationS)), MaxInputS)
		}
	}
	if pl.Res.OutS > float64(cfg.TransformMaxOutS) {
		return tooLarge("output_seconds", int64(math.Ceil(pl.Res.OutS)), int64(cfg.TransformMaxOutS))
	}
	if pl.CPU.ExpectedMS > int64(cfg.TransformMaxCPUS)*1000 {
		return tooLarge("cpu_seconds", ceilDiv(pl.CPU.ExpectedMS, 1000), int64(cfg.TransformMaxCPUS))
	}
	if pl.CPU.MemMB > int64(cfg.TransformMaxMemMB) {
		return tooLarge("memory_mb", pl.CPU.MemMB, int64(cfg.TransformMaxMemMB))
	}
	if s.o.FreeBytes != nil && cfg.DataDir != "" {
		need := DiskBytesNeeded(pl.Spec.Kind, pl.OutFrames)
		for _, dir := range []string{cfg.DataDir, filepath.Join(cfg.DataDir, "incoming")} {
			free, err := s.o.FreeBytes(dir)
			if err != nil {
				continue // an unreadable work dir is the runner's problem, not a limit
			}
			if int64(free) < need { //nolint:gosec // free space fits int64
				return tooLarge("disk", need, int64(min(free, math.MaxInt64))) //nolint:gosec // clamped to int64
			}
		}
	}
	return nil
}

func dedupWarnings(in []Warning) []Warning {
	seen := map[[2]string]bool{}
	out := make([]Warning, 0, len(in))
	for _, w := range in {
		k := [2]string{w.Code, w.Field}
		if seen[k] {
			continue
		}
		seen[k] = true
		if w.Message == "" {
			w.Message = warningText[w.Code]
		}
		out = append(out, w)
	}
	return out
}

// ---- warnings of the T1a kinds, from the resolved parameters alone ----

func warnPair(in WarnIn) []Warning {
	m := paramsMap(in.Resolved)
	out := []Warning{{Code: WarnBeatPhasePending}}
	if a, b := paramFloat(m, "bpm_a", 0), paramFloat(m, "bpm_b", 0); a > 0 && b > 0 && math.Abs(dsp.Log(b/a)) > 0.25 {
		out = append(out, Warning{Code: WarnLargeTempoChange})
	}
	if s := rawFloat(m, "semitones_b"); s != nil && math.Abs(*s) > 6 {
		out = append(out, Warning{Code: WarnHeavyPitchShift, Field: "semitones_b"})
	}
	for _, n := range []string{"downbeat_a_s", "downbeat_b_s"} {
		if paramString(m, n) == "auto" {
			out = append(out, Warning{Code: WarnDownbeatAuto, Field: n})
		}
	}
	return out
}

func warnShepard(WarnIn) []Warning { return []Warning{{Code: WarnBeatPhasePending}} }

func warnEnsemble(WarnIn) []Warning { return []Warning{{Code: WarnEnsembleFullMix}} }

// warningText is the human sentence of each estimate-time warning code.
var warningText = map[string]string{
	WarnTempoMeasuredLowConfidence: "the tempo was measured with low confidence; pass the tempo explicitly if it sounds wrong",
	WarnTempoOctaveFolded:          "a measured tempo was read an octave away to sit near its partner",
	WarnKeyUnknownAssumedNone:      "the key of an input is unknown; no key shift is applied",
	WarnLargeTempoChange:           "the tempo change is large; expect audible stretching",
	WarnHeavyPitchShift:            "a pitch shift beyond six semitones sounds processed",
	WarnEnsembleFullMix:            "an ensemble of a full mix stacks the drums and bass too; use a voice-only take",
	WarnBeatPhasePending:           "beat phase is measured inside the render",
	WarnDownbeatAuto:               "the downbeat is estimated by a heuristic that cannot work for four-on-the-floor",
	WarnSectionUnavailable:         "the section has no time on this take",
	WarnStartDefaulted:             "no timed section was found; the start defaults to 0",
	WarnSoundfontDefaultUsed:       "the first configured soundfont is used",
	WarnBinauralHeadphonesOnly:     "binaural copies are for headphones only",
	WarnBinauralMonoIncompatible:   "a binaural copy folds down to mono poorly",
	WarnBinauralParametricBasic:    "the parametric model lateralises but does not externalise",
	WarnElevationIgnored:           "the parametric model ignores elevation",
	WarnAfterBinaural:              "the source is already a binaural copy; widening or mastering breaks the image",
}

// ---- queue, quota, existing ----

func (s *Service) queueState(ctx context.Context, q queryer) (running, queued int, err error) {
	rows, err := q.QueryContext(ctx, `SELECT state, COUNT(*) FROM transforms WHERE state IN ('queued','running') GROUP BY state`)
	if err != nil {
		return 0, 0, fmt.Errorf("transform: queue: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return 0, 0, err
		}
		if st == StateRunning {
			running = n
		} else {
			queued = n
		}
	}
	return running, queued, rows.Err()
}

func (s *Service) existing(ctx context.Context, q queryer, specHash string) (*Existing, error) {
	var id, st string
	err := q.QueryRowContext(ctx, `SELECT id, state FROM transforms WHERE spec_hash = ? AND state IN ('queued','running','done')`, specHash).Scan(&id, &st)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("transform: existing: %w", err)
	}
	e := &Existing{TransformID: id, State: st}
	if st == StateDone {
		e.TakeID = id
	}
	return e, nil
}

// Estimate answers estimate_transform (D-T1-6, C209.5): the same build as create, over the reader pool, writing nothing, reading
// no audio, costing nothing.
func (s *Service) Estimate(ctx context.Context, p auth.Principal, req Request) (Estimate, error) {
	if err := s.ready(); err != nil {
		return Estimate{}, err
	}
	q := queryer(s.o.Store.R)
	pl, err := s.build(ctx, q, p, req)
	if err != nil {
		return Estimate{}, err
	}
	run, queued, err := s.queueState(ctx, q)
	if err != nil {
		return Estimate{}, err
	}
	quota, err := s.QuotaOf(ctx, q, pl.PrincipalKd, s.o.Now())
	if err != nil {
		return Estimate{}, err
	}
	ex, err := s.existing(ctx, q, pl.Canon.SpecHash)
	if err != nil {
		return Estimate{}, err
	}
	ts := s.tools()
	e := Estimate{
		Kind: pl.Spec.Kind, SpecHash: pl.Canon.SpecHash, Title: pl.Title,
		CPU:         CPUEstimate{ExpectedMS: pl.CPU.ExpectedMS, MaxMS: pl.CPU.MaxMS, BudgetMS: pl.CPU.MaxMS},
		WallExpectS: math.Ceil(float64(pl.CPU.ExpectedMS)/100) / 10,
		Output:      OutputEstimate{Seconds: pl.Res.OutS, SampleRate: pl.Spec.SampleRate, BytesEst: pl.CPU.OutBytes},
		MemoryMB:    int(pl.CPU.MemMB), Resolved: pl.Res.Params, Warnings: pl.Warnings, Quota: quota,
		Queue:    QueueEstimate{Running: run, Queued: queued, PositionIfCreated: run + queued + 1},
		Existing: ex,
		Tools:    ToolsEstimate{FFmpeg: ts.FFmpeg, FLAC: ts.FLAC, Fluidsynth: ts.Fluidsynth},
	}
	for _, in := range pl.Res.Inputs {
		ei := EstimateInput{Role: in.Ref.Role, TrackID: in.Ref.TrackID, Title: in.Title, DurationS: in.DurationS, BeatsPerBar: in.Align.BeatsPerBar}
		if in.HasBPM {
			ei.BPM = &BPMInfo{Value: in.Align.BPM, Source: in.Align.BPMSource, OctaveFold: in.Align.OctaveFold, Confidence: in.Confidence}
		}
		if in.HasKey {
			ei.Key = &KeyInfo{Camelot: in.Camelot, PC: in.Align.KeyPC, Minor: in.Align.KeyMinor, Source: in.Align.KeySource}
		}
		e.Inputs = append(e.Inputs, ei)
	}
	return e, nil
}
