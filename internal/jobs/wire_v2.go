package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/nicolasacchi/metamusic/internal/jobspec"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/redact"
)

// The mm.worker/2 half of the runner (C321, C323.5, Revision 4): which contract a job's attempts speak, the take spec built from
// the job's spec_json, and the per-kind verification of the worker's output. A plain job (kind generate, no needs, no spec) keeps
// every /1 behaviour byte for byte: wireOf answers /1 and every check below defers to the code that was here before.

// AttachSource reads the bytes of an attachment named by a job's spec at submit (jobspec.AttachRef.Source, for example
// plan:<id>/score.abc). The Desk defines its sources; a runner without one can serve no job that carries attachments.
type AttachSource interface {
	ReadAttach(ctx context.Context, ref jobspec.AttachRef) ([]byte, error)
}

// jobWire is what a job's kind, needs and spec decide about its attempts.
type jobWire struct {
	Wire  string // provider.ContractV1 or ContractV2
	Shape provider.KindShape
	Spec  jobspec.Spec
	// Kind is the /2 kind of the document ("" for a plain generate).
	Kind string
}

// wireOf derives the wire of a job (D-G1-3). Only yue2 speaks /2; a spec that does not parse is an error (the create path stored
// it canonically, so this is corruption).
func wireOf(engine, kind string, needs []string, specJSON string) (jobWire, error) {
	spec, err := jobspec.Parse([]byte(specJSON))
	if err != nil {
		return jobWire{}, err
	}
	t := provider.TakeSpec{Engine: engine, Kind: takeKindOf(kind)}
	if e := spec.Ears; e != nil {
		t.Ears = &provider.EarsSpec{Lang: e.Lang, BudgetS: e.BudgetS, Stages: e.Stages, KeepVocals: e.KeepVocals}
	}
	if p := spec.Plan; p != nil {
		t.Plan = &provider.PlanSpec{Seeds: p.Seeds, SecondsPerSeed: p.SecondsPerSeed}
	}
	w := jobWire{Wire: provider.WireFor(t, needs, false), Shape: provider.ShapeOf(t), Spec: spec, Kind: t.Kind}
	if len(spec.Attach) > 0 && w.Wire == provider.ContractV1 && (engine == "" || engine == provider.EngineYuE2) {
		w.Wire = provider.ContractV2 // attachments exist only on /2, whatever the needs say
	}
	return w, nil
}

// takeKindOf maps a job kind to the kind of a /2 document ("" for a plain generate).
func takeKindOf(kind string) string {
	switch kind {
	case KindAnalyze:
		return provider.KindAnalyze
	case KindPlan:
		return provider.KindPlan
	}
	return ""
}

// contractOf is the contract the worker's output of this job must carry.
func contractOf(engine string, j JobRow) string {
	w, err := wireOf(engine, j.Kind, j.Needs, j.SpecJSON)
	if err != nil {
		return "" // fails closed: no contract matches a job whose spec_json does not parse
	}
	return w.Wire
}

// outputProblem checks a decoded /2 output against the job's document (kind, ears, plans, null shapes, artifact names). It
// answers nothing for a /1 job. The text names fields, never values.
func (w jobWire) outputProblem(out provider.Output) (code, text string) {
	if w.Wire != provider.ContractV2 {
		return "", ""
	}
	if err := provider.CheckOutputV2(w.Shape, out); err != nil {
		return "WORKER_CONTRACT", "output does not match the /2 document of the job"
	}
	for name := range out.Artifacts {
		if _, ok := provider.ArtifactMaxBytesV2(w.Shape, name); !ok || name == provider.ResultArtifact {
			return "WORKER_CONTRACT", "output reports an artifact outside the allowlist of its kind"
		}
	}
	return "", ""
}

// artifactAllowed reports whether the output of the job may report the artifact: the engine's allowlist for a /1 job, the kind's
// allowlist for a /2 job.
func artifactAllowed(w jobWire, isGenerate bool, engine, name string) bool {
	if w.Wire == provider.ContractV2 {
		_, ok := provider.ArtifactMaxBytesV2(w.Shape, name)
		return ok
	}
	_, ok := provider.ArtifactMaxBytesFor(engine, name)
	return ok && isGenerate
}

// kindRequired lists the artifacts the finished output must report: the engine's own required set for a generate job, the kind's
// extra ones for a /2 job (provider.ExtraRequiredV2). The result is sorted and without duplicates.
func (w jobWire) kindRequired(isGenerate bool, engineRequired []string, out provider.Output) []string {
	var names []string
	if isGenerate {
		names = append(names, engineRequired...)
	}
	if w.Wire == provider.ContractV2 {
		names = append(names, provider.ExtraRequiredV2(w.Shape, out)...)
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// applyWire fills the /2 fields of a take spec from the job (C323.5): kind, ears, plan, attachments and the signed input of the
// spec's pins. A job on /1 is left exactly as it was. Errors never carry a URL.
func (r *Runner) applyWire(ctx context.Context, c *claim, spec *provider.TakeSpec) error {
	w, err := wireOf(c.job.Engine, c.job.Kind, c.job.Needs, c.job.SpecJSON)
	if err != nil {
		return fmt.Errorf("jobs: spec_json: %w", err)
	}
	if w.Wire != provider.ContractV2 {
		return nil
	}
	spec.Wire, spec.Kind = provider.ContractV2, w.Kind
	if e := w.Spec.Ears; e != nil {
		spec.Ears = &provider.EarsSpec{Lang: e.Lang, BudgetS: e.BudgetS, Stages: slices.Clone(e.Stages), KeepVocals: e.KeepVocals}
	}
	if p := w.Spec.Plan; p != nil {
		spec.Plan = &provider.PlanSpec{Seeds: slices.Clone(p.Seeds), SecondsPerSeed: p.SecondsPerSeed}
	}
	if w.Kind != "" {
		spec.MinAudioS = 0 // min_audio_s is part of kind render only
	}
	if w.Kind == provider.KindAnalyze {
		spec.Request = nil // the analyzed audio is the input; the recipe's request is not sent
	}
	if in := w.Spec.Inputs; in != nil {
		pm, ok := r.o.Inputs.(PinMinter)
		if !ok {
			return errors.New("jobs: the job pins input audio but no pin minter is wired")
		}
		inputs, err := pm.MintPins(ctx, *in, time.Unix(c.exp, 0).UTC())
		if err != nil {
			return fmt.Errorf("jobs: mint pinned inputs: %w", redactedErr{err})
		}
		spec.Inputs = inputs
	}
	if len(w.Spec.Attach) > 0 {
		if r.o.Attach == nil {
			return errors.New("jobs: the job carries attachments but no attachment source is wired")
		}
		spec.Attach = make(map[string]provider.AttachFile, len(w.Spec.Attach))
		for _, ref := range w.Spec.Attach {
			data, err := r.o.Attach.ReadAttach(ctx, ref)
			if err != nil {
				return fmt.Errorf("jobs: read attachment %s: %s", ref.Name, redact.Error(err))
			}
			sum := sha256.Sum256(data)
			if hex.EncodeToString(sum[:]) != ref.SHA256 {
				return fmt.Errorf("jobs: attachment %s changed since the estimate", ref.Name)
			}
			spec.Attach[ref.Name] = provider.AttachFile{SHA256: ref.SHA256, Data: data}
		}
	}
	// A document the worker would refuse (BAD_INPUT) must never be sent: it would close the capability gate (C322.5) for a fault
	// of this job. The check runs the builder and the parser the worker mirrors, on the spec as it will be submitted.
	if raw, err := provider.BuildWorkerInputV2(*spec); err != nil {
		return fmt.Errorf("jobs: the /2 document cannot be built: %w", err)
	} else if _, err := provider.ParseWorkerInput(raw); err != nil {
		return fmt.Errorf("jobs: the /2 document is invalid: %w", err)
	}
	return nil
}

// redactedErr keeps the chain of an error (errors.Is) while its text passes through redact.
type redactedErr struct{ err error }

func (e redactedErr) Error() string { return redact.Error(e.err) }
func (e redactedErr) Unwrap() error { return e.err }
