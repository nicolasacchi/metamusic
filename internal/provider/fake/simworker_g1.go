package fake

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"sync"

	"github.com/nicolasacchi/metamusic/internal/provider"
)

// The mm.worker/2 half of the simulated worker (C321, C329): it speaks /2 for yue2, answers the kinds and stages that have a hook
// and refuses the others with KIND_UNAVAILABLE before any render. Tracks add their hooks from their own simworker_<track>.go
// (init-time RegisterSimKind) or per worker through SimWorkerOptions.Kinds; this file owns only the table and the plumbing.

// The hook names: the Tier B stage of a render, and the two kinds that are not a render.
const (
	SimKindEars    = "ears"
	SimKindAnalyze = provider.KindAnalyze
	SimKindPlan    = provider.KindPlan
)

// SimArtifact is one file a stage hook produces; it is uploaded before mm-result.json under the name it carries.
type SimArtifact struct {
	Name string
	Body []byte
}

// SimKindResult is what a stage hook returns.
type SimKindResult struct {
	Artifacts []SimArtifact
	// Ears is the stage outcome; nil means {status: ok}. A hook that fails returns an error instead (see SimKindHook).
	Ears *provider.EarsOut
	// Plans is the per-seed outcome of the plan kind.
	Plans []provider.PlanOut
	// AudioSeconds is the analyzed audio's length (analyze); zero takes the worker's AudioSeconds.
	AudioSeconds float64
}

// SimKindHook runs one stage. For "ears" a returned error never fails the take (ears.status = error, code EARS_FAILED); for
// "analyze" it fails the run with ANALYZE_FAILED; for "plan" the run fails with PLANS_FAILED when no seed produced a bundle.
type SimKindHook func(ctx context.Context, w *SimWorker, in provider.WorkerInput) (SimKindResult, error)

var (
	simKindMu sync.RWMutex
	simKinds  = map[string]SimKindHook{}
)

// RegisterSimKind registers the process-wide hook of a stage ("ears", "analyze" or "plan"). It is meant for init functions and
// panics on a repeated or unknown name, so two tracks cannot silently shadow each other.
func RegisterSimKind(name string, h SimKindHook) {
	if !slices.Contains([]string{SimKindEars, SimKindAnalyze, SimKindPlan}, name) || h == nil {
		panic("fake: RegisterSimKind: unknown stage or nil hook: " + name)
	}
	simKindMu.Lock()
	defer simKindMu.Unlock()
	if _, dup := simKinds[name]; dup {
		panic("fake: RegisterSimKind: stage registered twice: " + name)
	}
	simKinds[name] = h
}

// hook finds the hook of a stage: the worker's own table first, then the process-wide one.
func (w *SimWorker) hook(name string) SimKindHook {
	if h, ok := w.o.Kinds[name]; ok {
		return h
	}
	simKindMu.RLock()
	defer simKindMu.RUnlock()
	return simKinds[name]
}

// caps is the capability list the worker reports in its /2 outputs: v2 always, and one per registered stage.
func (w *SimWorker) caps() []string {
	caps := []string{"v2"}
	for _, c := range [][2]string{{SimKindEars, "ears"}, {SimKindAnalyze, "analyze"}, {SimKindPlan, "plan"}} {
		if w.hook(c[0]) != nil {
			caps = append(caps, c[1])
		}
	}
	slices.Sort(caps)
	return caps
}

func (s *Session) contract() string {
	if s.in.Contract != "" {
		return s.in.Contract
	}
	return provider.Contract
}

// checkKindV2 is step 1c: the kind and every requested stage must exist in this image.
func (s *Session) checkKindV2() (code, msg string) {
	switch {
	case s.in.Kind == provider.KindAnalyze && s.w.hook(SimKindAnalyze) == nil:
		return "KIND_UNAVAILABLE", "kind analyze is not available in this image"
	case s.in.Kind == provider.KindPlan && s.w.hook(SimKindPlan) == nil:
		return "KIND_UNAVAILABLE", "kind plan is not available in this image"
	case s.in.Ears != nil && s.in.Kind == provider.KindRender && s.w.hook(SimKindEars) == nil:
		return "KIND_UNAVAILABLE", "the ears stage is not available in this image"
	}
	return "", ""
}

// checkAttach is step 3b: every attachment must match its sha256 (the parser validated its encoding).
func (s *Session) checkAttach() (code, msg string) {
	for name, f := range s.in.Attach {
		data, err := decodeAttach(f.DataBase64)
		sum := sha256.Sum256(data)
		if err != nil || hex.EncodeToString(sum[:]) != f.SHA256 {
			return "ATTACH_MISMATCH", "attachment " + name + " does not match its sha256"
		}
	}
	return "", ""
}

// stageV2 runs the stage of the document's kind and fills the /2 blocks of the output. A non-nil Result is a failed run.
func (s *Session) stageV2(ctx context.Context, arts []artifact, out *provider.Output) ([]artifact, *Result) {
	out.Kind = s.in.Kind
	out.Caps = s.w.caps()
	add := func(r SimKindResult) {
		for _, a := range r.Artifacts {
			arts = append(arts, artifact{a.Name, a.Body})
		}
	}
	switch s.in.Kind {
	case provider.KindRender:
		if s.in.Ears == nil {
			return arts, nil
		}
		r, err := s.w.hook(SimKindEars)(ctx, s.w, s.in)
		if err != nil { // a Tier B failure never fails the take (C321.4)
			out.Ears = &provider.EarsOut{Status: "error", Code: "EARS_FAILED"}
			return arts, nil
		}
		out.Ears = earsOf(r)
		add(r)
	case provider.KindAnalyze:
		r, err := s.w.hook(SimKindAnalyze)(ctx, s.w, s.in)
		if err != nil {
			res := s.fail("ANALYZE_FAILED", "the analysis failed", true)
			return nil, &res
		}
		out.Ears = earsOf(r)
		if out.Ears.Status != "ok" {
			res := s.fail("ANALYZE_FAILED", "the analysis did not complete", true)
			return nil, &res
		}
		out.Truncated = nil
		secs := r.AudioSeconds
		if secs <= 0 {
			secs = s.w.o.AudioSeconds
		}
		n := json.Number(strconv.FormatFloat(secs, 'f', -1, 64))
		out.AudioSeconds = &n
		add(r)
	case provider.KindPlan:
		r, err := s.w.hook(SimKindPlan)(ctx, s.w, s.in)
		if err != nil || !slices.ContainsFunc(r.Plans, func(p provider.PlanOut) bool { return p.Status == "ok" }) {
			res := s.fail("PLANS_FAILED", "every plan seed failed", true)
			return nil, &res
		}
		out.Truncated, out.AudioSeconds = nil, nil
		out.Plans = r.Plans
		add(r)
	}
	return arts, nil
}

func earsOf(r SimKindResult) *provider.EarsOut {
	if r.Ears != nil {
		return r.Ears
	}
	return &provider.EarsOut{Status: "ok"}
}

func decodeAttach(s string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, errors.New("fake: attachment is not base64")
	}
	return b, nil
}
