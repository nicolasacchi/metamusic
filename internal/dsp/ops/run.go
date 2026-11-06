package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"

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
	// Scratch, when set, lets RunTo finish the output in streaming passes whose memory does not grow with its length (24 bytes a
	// frame go to scratch spools instead). Optional.
	Scratch safety.Scratch
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
// prepared is a validated job ready to render.
type prepared struct {
	fn      RunFunc
	in      RunIn
	m       *dsp.Meter
	ctx     context.Context
	kind    string
	nInputs int
}

// prepare looks the kind up, checks the inputs and wires the meter and the progress reporting.
func prepare(ctx context.Context, j Job) (prepared, error) {
	fn, unitsFn, ok := Lookup(j.Kind)
	if !ok {
		return prepared{}, &ParamError{Field: "kind", Rule: RuleUnknownKind}
	}
	if j.Rate <= 0 || len(j.Inputs) == 0 {
		return prepared{}, &ParamError{Field: "inputs", Rule: RuleInput}
	}
	in := RunIn{Rate: j.Rate, Params: j.Params, Alignment: j.Alignment, Seed: j.Seed, TargetLUFS: j.TargetLUFS, RefLUFS: j.RefLUFS}
	var inFrames int64
	for _, x := range j.Inputs {
		b := x.Buf
		if b.Rate != j.Rate || len(b.Ch[0]) != len(b.Ch[1]) {
			return prepared{}, &ParamError{Field: "inputs", Rule: RuleChannel}
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
	return prepared{fn: fn, in: in, m: m, ctx: ctx, kind: j.Kind, nInputs: len(j.Inputs)}, nil
}

// Run renders j: it looks the kind up in the registry (no switch: a new kind is covered by registering), runs the operator, and
// for the kinds that do not finish themselves runs safety.Finish on the result and records it. The returned report has
// everything report.json needs except pcm_sha256. Errors: *ParamError for a bad job, dsp.ErrBudget, safety.ErrNonFinite and
// safety.ErrSilent, the context's error; nothing is recovered here.
func Run(ctx context.Context, j Job) (pcm.Buffer, Report, error) {
	p, err := prepare(ctx, j)
	if err != nil {
		return pcm.Buffer{}, Report{}, err
	}
	res, err := p.fn(p.ctx, p.in)
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
		if err := p.m.Add(SafetyUnits(int64(out.Frames()))); err != nil {
			return pcm.Buffer{}, Report{}, err
		}
		tpIn = safety.TruePeakDBTP(out)
		r, err := safety.Finish(&out, safety.FinishIn{TargetLUFS: j.TargetLUFS, RefLUFS: j.RefLUFS, OwnFades: ownFades[j.Kind]})
		if err != nil {
			return pcm.Buffer{}, Report{}, err
		}
		fin = &r
	}
	rep, err := buildReport(j.Kind, res.Report, p.nInputs, fin, tpIn)
	if err != nil {
		return pcm.Buffer{}, Report{}, err
	}
	rep.Frames, rep.Units = int64(out.Frames()), p.m.Used()
	return out, rep, nil
}

// RunTo is Run that writes the finished 24-bit stereo PCM to w instead of returning it, and fills Report.PCMSHA256 and Frames. With
// Job.Scratch set the finishing stage is streamed (safety.FinishStream: bit for bit the same samples and report as Run, with memory
// that does not grow with the output), and a kind with a streaming render (glacier) is rendered straight into a scratch spool
// instead of a buffer, so a 600 s output needs no more memory than its window. Nothing is written to w until the render has
// succeeded, except by the last stage's delivery, which only I/O errors or cancellation can still stop.
func RunTo(ctx context.Context, j Job, w io.Writer) (Report, error) {
	if j.Scratch == nil {
		out, rep, err := Run(ctx, j)
		if err != nil {
			return Report{}, err
		}
		sum, err := pcm.WritePCM24(w, out)
		if err != nil {
			return Report{}, err
		}
		rep.PCMSHA256 = sum
		return rep, nil
	}
	p, err := prepare(ctx, j)
	if err != nil {
		return Report{}, err
	}
	h := sha256.New()
	mw := io.MultiWriter(w, h)
	deliver := func(rate int) func(l, r []float64) error {
		return func(l, r []float64) error {
			_, err := pcm.WritePCM24(mw, frameBuffer(rate, l, r))
			return err
		}
	}
	var own json.RawMessage
	var src safety.Source
	var frames int
	if stream := streamers[j.Kind]; stream != nil {
		raw, err := j.Scratch.NewSpool()
		if err != nil {
			return Report{}, err
		}
		defer func() { _ = raw.Close() }()
		rate, n, rep, err := stream(p.ctx, p.in, func(l, r []float64) error { return safety.PutFrames(raw, l, r) })
		if err != nil {
			return Report{}, err
		}
		own, frames, src = rep, n, safety.SpoolSource(rate, n, raw)
	} else {
		res, err := p.fn(p.ctx, p.in)
		if err != nil {
			return Report{}, err
		}
		own, frames, src = res.Report, res.Buffer.Frames(), safety.BufferSource(res.Buffer)
		if selfFinishing[j.Kind] { // the operator's buffer is final
			if err := deliver(res.Buffer.Rate)(res.Buffer.Ch[0], res.Buffer.Ch[1]); err != nil {
				return Report{}, err
			}
			return finishReport(j.Kind, own, p, frames, nil, 0, h)
		}
	}
	if j.Progress != nil {
		j.Progress(renderShare, "finishing")
	}
	if err := p.m.Add(SafetyUnits(int64(frames))); err != nil {
		return Report{}, err
	}
	tpIn, err := safety.StreamTruePeakDBTP(p.ctx, src)
	if err != nil {
		return Report{}, err
	}
	fin, err := safety.FinishStream(p.ctx, src, safety.FinishIn{TargetLUFS: j.TargetLUFS, RefLUFS: j.RefLUFS, OwnFades: ownFades[j.Kind]}, j.Scratch, deliver(src.Rate()))
	if err != nil {
		return Report{}, err
	}
	return finishReport(j.Kind, own, p, frames, &fin, tpIn, h)
}

// finishReport merges the operator's report with the finish and fills the totals.
func finishReport(kind string, own json.RawMessage, p prepared, frames int, fin *safety.FinishReport, tpIn float64, h interface{ Sum([]byte) []byte }) (Report, error) {
	rep, err := buildReport(kind, own, p.nInputs, fin, tpIn)
	if err != nil {
		return Report{}, err
	}
	rep.Frames, rep.Units = int64(frames), p.m.Used()
	rep.PCMSHA256 = hex.EncodeToString(h.Sum(nil))
	return rep, nil
}
