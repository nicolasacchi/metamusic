package ops

import (
	"context"
	"encoding/json"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
	"github.com/nicolasacchi/metamusic/internal/dsp/safety"
)

// RuleUnknownKind is the rule of the ParamError for a kind nothing registered.
const RuleUnknownKind = "unknown_kind"

// Input is one decoded input of a job: a stereo buffer at the job rate and the alignment numbers of the spec for it (the
// operators read the frozen alignment object itself; these repeat it for callers that only need a number).
type Input struct {
	Role        string
	Buf         pcm.Buffer
	BPM         float64
	BeatsPerBar int
	KeyShiftS   float64
	DownbeatS   *float64
}

// Job is one render (C205): the kind, the resolved params and alignment of the spec, the decoded inputs, the loudness targets
// and the work meter.
type Job struct {
	Kind       string
	Rate       int
	Seed       int64
	Params     json.RawMessage
	Alignment  json.RawMessage
	Inputs     []Input
	TargetLUFS float64
	RefLUFS    float64
	Meter      *dsp.Meter
	// Progress receives permille 0..999 and the phase ("rendering", "finishing"), monotone. It is called from inside the work
	// meter's context poll, so the caller throttles it. Optional.
	Progress func(permille int, phase string)
}

// Rendering owns permille 0..renderShare, finishing the rest.
const renderShare = 950

// progressCtx reports progress from the context poll of the work meter (every 4096 units): it adds nothing to the operators and
// needs no goroutine or clock. The denominator is the registered model of the job, so it is an estimate and is clamped below
// the finishing share.
type progressCtx struct {
	context.Context
	m      *dsp.Meter
	expect int64
	last   int
	fn     func(int, string)
}

func (c *progressCtx) Err() error {
	if err := c.Context.Err(); err != nil {
		return err
	}
	if c.expect > 0 {
		if p := int(min(int64(renderShare-1), int64(renderShare)*c.m.Used()/c.expect)); p > c.last {
			c.last = p
			c.fn(p, "rendering")
		}
	}
	return nil
}

// Run renders j: it looks the kind up in the registry (no switch: a new kind is covered by registering), runs the operator, and
// for the kinds that do not finish themselves runs safety.Finish on the result and records it. The returned report has
// everything report.json needs except pcm_sha256. Errors: *ParamError for a bad job, dsp.ErrBudget, safety.ErrNonFinite and
// safety.ErrSilent, the context's error; nothing is recovered here.
func Run(ctx context.Context, j Job) (pcm.Buffer, Report, error) {
	fn, unitsFn, ok := Lookup(j.Kind)
	if !ok {
		return pcm.Buffer{}, Report{}, &ParamError{Field: "kind", Rule: RuleUnknownKind}
	}
	if j.Rate <= 0 || len(j.Inputs) == 0 {
		return pcm.Buffer{}, Report{}, &ParamError{Field: "inputs", Rule: RuleInput}
	}
	in := RunIn{Rate: j.Rate, Params: j.Params, Alignment: j.Alignment, Seed: j.Seed, TargetLUFS: j.TargetLUFS, RefLUFS: j.RefLUFS}
	var inFrames int64
	for _, x := range j.Inputs {
		b := x.Buf
		if b.Rate != j.Rate || len(b.Ch[0]) != len(b.Ch[1]) {
			return pcm.Buffer{}, Report{}, &ParamError{Field: "inputs", Rule: RuleChannel}
		}
		in.Inputs = append(in.Inputs, b)
		inFrames += int64(b.Frames())
	}
	m := j.Meter
	if m == nil {
		m = &dsp.Meter{} // unlimited, but the report still counts the units
	}
	in.Meter = m
	if m.Ctx == nil {
		c := ctx
		if j.Progress != nil {
			c = &progressCtx{Context: ctx, m: m, expect: unitsFn(inFrames, inFrames, j.Rate, j.Params), fn: j.Progress}
		}
		m.Ctx = c
		ctx = c
	}
	res, err := fn(ctx, in)
	if err != nil {
		return pcm.Buffer{}, Report{}, err
	}
	out := res.Buffer
	var fin *safety.FinishReport
	var tpIn float64
	if !selfFinishing[j.Kind] {
		if j.Progress != nil {
			j.Progress(renderShare, "finishing")
		}
		if err := m.Add(SafetyUnits(int64(out.Frames()))); err != nil {
			return pcm.Buffer{}, Report{}, err
		}
		tpIn = safety.TruePeakDBTP(out)
		r, err := safety.Finish(&out, safety.FinishIn{TargetLUFS: j.TargetLUFS, RefLUFS: j.RefLUFS, OwnFades: ownFades[j.Kind]})
		if err != nil {
			return pcm.Buffer{}, Report{}, err
		}
		fin = &r
	}
	rep, err := buildReport(j.Kind, res.Report, len(j.Inputs), fin, tpIn)
	if err != nil {
		return pcm.Buffer{}, Report{}, err
	}
	rep.Frames, rep.Units = int64(out.Frames()), m.Used()
	return out, rep, nil
}
