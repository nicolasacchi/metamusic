package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/redact"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/transform"
)

// trErr maps a transform service error to the problem model (C212.6): a *problem.Error keeps its status, code and details (a
// full queue also answers Retry-After), anything else is a 500 whose cause the Huma error hook logs through redact.
func trErr(err error) error {
	var pe *problem.Error
	if errors.As(err, &pe) && pe.Code == transform.ErrQueueFull {
		e := Err(pe.Status, pe.Code, redact.String(pe.Message), pe.Details)
		return huma.ErrorWithHeaders(e, http.Header{"Retry-After": []string{"5"}})
	}
	return m2bErr(err, taskT1B4)
}

// transformRequestOf turns the wire request into the service's: the JSON object of every member sent (a JSON null is absent).
func transformRequestOf(b TransformRequest) (transform.Request, error) {
	req := transform.Request{Kind: transform.Kind(b.Kind), Title: b.Title, Rationale: b.Rationale, Seed: b.Seed, TargetLUFS: b.TargetLUFS,
		Members: map[transform.Kind]json.RawMessage{}}
	add := func(k transform.Kind, v any, set bool) error {
		if !set {
			return nil
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		req.Members[k] = raw
		return nil
	}
	for _, m := range []struct {
		k   transform.Kind
		v   any
		set bool
	}{
		{transform.KindMorph, b.Morph, b.Morph != nil}, {transform.KindShepard, b.Shepard, b.Shepard != nil},
		{transform.KindGlacier, b.Glacier, b.Glacier != nil}, {transform.KindPolytempo, b.Polytempo, b.Polytempo != nil},
		{transform.KindEnsemble, b.Ensemble, b.Ensemble != nil}, {transform.KindScoreRender, b.ScoreRender, b.ScoreRender != nil},
		{transform.KindMaster, b.Master, b.Master != nil}, {transform.KindWiden, b.Widen, b.Widen != nil},
		{transform.KindBinaural, b.Binaural, b.Binaural != nil},
	} {
		if err := add(m.k, m.v, m.set); err != nil {
			return transform.Request{}, err
		}
	}
	return req, nil
}

const zeroUSD = "0.000000"

func transformQuotaOf(q transform.Quota) TransformQuota {
	return TransformQuota{PrincipalKind: q.Kind, CapMS: q.CapMS, UsedMS: q.UsedMS, ReservedMS: q.ReservedMS, ResetsAt: store.FormatTime(q.ResetsAt)}
}

func transformWarnings(in []transform.Warning) []TransformWarning {
	out := make([]TransformWarning, 0, len(in))
	for _, w := range in {
		out = append(out, TransformWarning{Code: w.Code, Field: w.Field, Message: redact.String(w.Message)})
	}
	return out
}

func transformEstimateFrom(e transform.Estimate) TransformEstimate {
	out := TransformEstimate{
		Kind: string(e.Kind), SpecHash: e.SpecHash, Title: e.Title, Cost: TransformCost{USD: zeroUSD},
		CPU:         TransformCPU{ExpectedMS: e.CPU.ExpectedMS, MaxMS: e.CPU.MaxMS, BudgetMS: e.CPU.BudgetMS, Model: "cpu/1"},
		WallExpectS: e.WallExpectS,
		Output:      TransformOutputEstimate{Seconds: e.Output.Seconds, SampleRate: e.Output.SampleRate, Bits: 24, Channels: 2, BytesEst: e.Output.BytesEst},
		MemoryMB:    e.MemoryMB, Inputs: make([]TransformEstimateInput, 0, len(e.Inputs)), Resolved: RawRequest(e.Resolved),
		Warnings: transformWarnings(e.Warnings), Quota: transformQuotaOf(e.Quota),
		Queue: TransformQueueEstimate{Running: e.Queue.Running, Queued: e.Queue.Queued, PositionIfCreated: e.Queue.PositionIfCreated},
		Tools: TransformTools{FFmpeg: e.Tools.FFmpeg, FLAC: e.Tools.FLAC, Fluidsynth: e.Tools.Fluidsynth, Soundfont: nonEmpty(e.Tools.Soundfont)},
	}
	if len(out.Resolved) == 0 {
		out.Resolved = RawRequest("{}")
	}
	for _, in := range e.Inputs {
		ei := TransformEstimateInput{Role: in.Role, TrackID: in.TrackID, Title: in.Title, DurationS: in.DurationS, BeatsPerBar: in.BeatsPerBar}
		if b := in.BPM; b != nil {
			ei.BPM = &TransformBPM{Value: b.Value, Source: b.Source, OctaveFold: b.OctaveFold, Confidence: b.Confidence}
		}
		if k := in.Key; k != nil {
			ei.Key = &TransformKey{Camelot: nonEmpty(k.Camelot), PC: k.PC, Minor: k.Minor, Source: k.Source}
		}
		out.Inputs = append(out.Inputs, ei)
	}
	if x := e.Existing; x != nil {
		out.Existing = &TransformExisting{TransformID: x.TransformID, TakeID: nonEmpty(x.TakeID), State: x.State}
	}
	return out
}

func transformViewFrom(viewer auth.Principal, v transform.View) TransformView {
	out := TransformView{
		ID: v.ID, TakeID: v.ID, Kind: string(v.Kind), State: v.State, Phase: v.Phase, ProgressPermille: v.ProgressPermille, Title: v.Title,
		SpecHash: v.SpecHash, Reused: v.Reused, Restored: v.Restored, Principal: RefFor(viewer, v.PrincipalID, v.PrincipalKind, v.PrincipalName),
		Rationale: v.Rationale, Inputs: make([]TransformInputRef, 0, len(v.Inputs)), Cost: TransformCost{USD: zeroUSD},
		CPU:              TransformCPUUse{ExpectedMS: v.ExpectedMS, MaxMS: v.MaxMS, UsedMS: v.UsedMS},
		OutputSecondsEst: v.OutputSecondsEst, Warnings: transformWarnings(v.Warnings),
		CancelRequestedAt: v.CancelRequestedAt, CreatedAt: v.CreatedAt, StartedAt: v.StartedAt, FinishedAt: v.FinishedAt,
	}
	for _, in := range v.Inputs {
		out.Inputs = append(out.Inputs, TransformInputRef{Role: in.Role, TrackID: in.TrackID, Title: in.Title})
	}
	if e := v.Error; e != nil {
		out.Error = &TransformError{Code: e.Code, Message: redact.String(e.Message)}
	}
	return out
}

func transformCapabilitiesFrom(c transform.Capabilities) TransformCapabilities {
	out := TransformCapabilities{
		Enabled: c.Enabled, Kinds: make([]TransformKindCaps, 0, len(c.Kinds)), DSPVersion: c.DSPVersion,
		Limits: TransformLimits{MaxInputs: c.Limits.MaxInputs, MaxInputS: c.Limits.MaxInputS, MaxOutputS: c.Limits.MaxOutputS,
			MaxMemMB: c.Limits.MaxMemMB, MaxCPUS: c.Limits.MaxCPUS, Concurrent: c.Limits.Concurrent},
		Quota: transformQuotaOf(c.Quota), Queue: TransformQueueCaps{Running: c.Queue.Running, Queued: c.Queue.Queued, Max: c.Queue.Max},
		Tools: TransformToolCaps{FFmpeg: c.Tools.FFmpeg, FLAC: c.Tools.FLAC,
			Fluidsynth: TransformFluidsynth{Available: c.Tools.Fluidsynth, Version: nonEmpty(c.Tools.FluidsynthVersion)}},
		Soundfonts: make([]TransformSoundfont, 0, len(c.Soundfonts)),
	}
	for _, k := range c.Kinds {
		kc := TransformKindCaps{Kind: string(k.Kind), Summary: k.Summary, Inputs: k.Inputs, Params: make([]TransformParamCap, 0, len(k.Params)),
			BlindSpots: append([]string{}, k.BlindSpots...), Example: RawRequest(k.Example), Listening: nonEmpty(k.Listening), Dataset: nonEmpty(k.Dataset)}
		if kc.Example == nil {
			kc.Example = RawRequest("{}")
		}
		for _, p := range k.Params {
			kc.Params = append(kc.Params, TransformParamCap{Name: p.Name, Type: p.Type, Default: p.Default, Min: p.Min, Max: p.Max,
				Enum: append([]string{}, p.Enum...), Required: p.Required})
		}
		out.Kinds = append(out.Kinds, kc)
	}
	for _, s := range c.Soundfonts {
		out.Soundfonts = append(out.Soundfonts, TransformSoundfont{Name: s.Name, SHA12: s.SHA12, Bytes: s.Bytes})
	}
	return out
}

// encodeTransformCreate is the encoder create hands the service: the exact body and status, built before commit so the
// idempotency row stores what the caller sees.
func encodeTransformCreate(viewer auth.Principal) func(transform.CreateResult) (int, []byte, error) {
	return func(c transform.CreateResult) (int, []byte, error) {
		b, err := EncodeResponse(transformViewFrom(viewer, c.View))
		return c.Status, b, err
	}
}

// registerTransformT1 registers the seven transform operations (C212.1). All are generic MCP operations (D-T1-21, operator
// decision of 2026-11-03: no dedicated tool, tools/list unchanged) and none is human-only. Every handler checks its dependency
// (503 UNAVAILABLE {component: "transform"}); the service owns every other refusal.
func registerTransformT1(a huma.API, d Deps) {
	Register(a, withBody(huma.Operation{
		OperationID: "estimate_transform", Method: "POST", Path: "/api/transforms/estimate",
		Summary: "Estimate a transform",
		Description: "Free and pure: CPU time, output length and size, memory, the resolved tempo and key of each input, warnings, quota and queue position. " +
			"Costs $0 and writes nothing; it needs write scope because it previews quota and queue state of your principal kind.",
	}, bodyTransform), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/tracks/:id"},
		func(ctx context.Context, in *estimateTransformIn) (*transformEstimateOut, error) {
			if d.Transform == nil {
				return nil, unavailable("transform")
			}
			req, err := transformRequestOf(in.Body)
			if err != nil {
				return nil, err
			}
			e, err := d.Transform.Estimate(ctx, auth.FromContext(ctx), req)
			if err != nil {
				return nil, trErr(err)
			}
			return &transformEstimateOut{Body: transformEstimateFrom(e)}, nil
		})

	Register(a, withBody(huma.Operation{
		OperationID: "create_transform", Method: "POST", Path: "/api/transforms",
		Summary: "Create a transform",
		Description: "Queues a CPU transform of one or two Takes (morph, shepard, glacier, polytempo, ensemble, score_render, master, widen, binaural). " +
			"The result is an ordinary Take. The same spec returns the existing transform (reused). Spends no money; limited by a daily CPU pool per principal kind.",
	}, bodyTransform), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/tracks/:id", Idempotency: "create", Rate: "heavy"},
		func(ctx context.Context, in *createTransformIn) (*createTransformOut, error) {
			if d.Transform == nil {
				return nil, unavailable("transform")
			}
			req, err := transformRequestOf(in.Body)
			if err != nil {
				return nil, err
			}
			p := auth.FromContext(ctx)
			res, err := d.Transform.Create(ctx, p, req, encodeTransformCreate(p))
			if err != nil {
				return nil, trErr(err)
			}
			return &createTransformOut{Status: res.Status, Body: transformViewFrom(p, res.View)}, nil
		})

	Register(a, huma.Operation{
		OperationID: "get_transform", Method: "GET", Path: "/api/transforms/{id}",
		Summary:     "Get a transform",
		Description: "State, phase, progress, inputs, CPU use, warnings and error of one transform; its id is the id of its Take.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/transforms"},
		func(ctx context.Context, in *getTransformIn) (*transformOut, error) {
			if d.Transform == nil {
				return nil, unavailable("transform")
			}
			p := auth.FromContext(ctx)
			v, err := d.Transform.Get(ctx, p, in.ID)
			if err != nil {
				return nil, trErr(err)
			}
			return &transformOut{Body: transformViewFrom(p, v)}, nil
		})

	Register(a, huma.Operation{
		OperationID: "list_transforms", Method: "GET", Path: "/api/transforms",
		Summary:     "List transforms",
		Description: "Transforms newest first, filtered by state, kind or an input Take.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/transforms"},
		func(ctx context.Context, in *listTransformsIn) (*transformPageOut, error) {
			if d.Transform == nil {
				return nil, unavailable("transform")
			}
			p := auth.FromContext(ctx)
			pg, err := d.Transform.List(ctx, p, transform.ListFilter{State: in.State, Kind: transform.Kind(in.Kind), TrackID: in.TrackID,
				Limit: in.Limit, Cursor: in.Cursor})
			if err != nil {
				return nil, trErr(err)
			}
			out := TransformPage{Items: make([]TransformView, 0, len(pg.Items)), NextCursor: nonEmpty(pg.NextCursor)}
			for _, v := range pg.Items {
				out.Items = append(out.Items, transformViewFrom(p, v))
			}
			return &transformPageOut{Body: out}, nil
		})

	Register(a, huma.Operation{
		OperationID: "wait_for_transform", Method: "GET", Path: "/api/transforms/{id}/wait",
		Summary:     "Wait for a transform",
		Description: "Long-polls up to 55 s until the state differs from since_state or the transform is terminal.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/tracks/:id"},
		func(ctx context.Context, in *waitForTransformIn) (*transformWaitOut, error) {
			if d.Transform == nil {
				return nil, unavailable("transform")
			}
			p := auth.FromContext(ctx)
			w, err := d.Transform.Wait(ctx, p, in.ID, in.SinceState, time.Duration(in.TimeoutS)*time.Second)
			if err != nil {
				return nil, trErr(err)
			}
			return &transformWaitOut{Body: TransformWait{Transform: transformViewFrom(p, w.View), Changed: w.Changed, TimedOut: w.TimedOut}}, nil
		})

	Register(a, huma.Operation{
		OperationID: "cancel_transform", Method: "POST", Path: "/api/transforms/{id}/cancel",
		Summary:     "Cancel a transform",
		Description: "Cancels a queued or running transform; an agent cancels only its own, a human any.",
	}, Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/transforms"},
		func(ctx context.Context, in *cancelTransformIn) (*transformOut, error) {
			if d.Transform == nil {
				return nil, unavailable("transform")
			}
			p := auth.FromContext(ctx)
			if err := d.Transform.Cancel(ctx, p, in.ID); err != nil {
				return nil, trErr(err)
			}
			v, err := d.Transform.Get(ctx, p, in.ID)
			if err != nil {
				return nil, trErr(err)
			}
			return &transformOut{Body: transformViewFrom(p, v)}, nil
		})

	Register(a, huma.Operation{
		OperationID: "get_transform_capabilities", Method: "GET", Path: "/api/transforms/capabilities",
		Summary: "Get transform capabilities",
		Description: "The kinds with their parameter tables, defaults, ranges and blind spots, the limits, your daily CPU quota, the queue and the tools of the host. " +
			"Generated from the tables that validate requests.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/tracks/:id"},
		func(ctx context.Context, in *struct{}) (*transformCapabilitiesOut, error) {
			if d.Transform == nil {
				return nil, unavailable("transform")
			}
			c, err := d.Transform.Capabilities(ctx, auth.FromContext(ctx))
			if err != nil {
				return nil, trErr(err)
			}
			return &transformCapabilitiesOut{Body: transformCapabilitiesFrom(c)}, nil
		})
}
