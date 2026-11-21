package provider

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
)

// mm.worker/2 (C321): the types, builder and parser of the second worker protocol, next to /1. Nothing here changes what /1
// sends or accepts: a TakeSpec with Wire "" or ContractV1 is built by BuildWorkerInput exactly as before, and a /1 document that
// carries a /2 key is refused by ParseWorkerInput.

// EarsIn is the ears block of a /2 input.
type EarsIn struct {
	Lang       string   `json:"lang,omitempty"` // "auto" or an ISO 639 code
	BudgetS    int      `json:"budget_s"`
	Stages     []string `json:"stages"`
	KeepVocals bool     `json:"keep_vocals,omitempty"`
}

// PlanIn is the plan block of a /2 input.
type PlanIn struct {
	Seeds          []int64 `json:"seeds"`
	SecondsPerSeed int     `json:"s_per_seed"`
}

// AttachIn is one inline file of a /2 input.
type AttachIn struct {
	SHA256     string `json:"sha256"`
	DataBase64 string `json:"data_base64"`
}

// v2OnlyKeys are the top-level keys a /1 document must not carry.
var v2OnlyKeys = []string{"kind", "ears", "plan", "attach", "fallback_after_s"}

// The bounds of the /2 blocks.
const (
	maxAttachFiles     = 16
	minFallbackAfterS  = 60
	maxFallbackAfterS  = 600
	maxSecondsPerSeed  = 360
	maxEarsStagesCount = 3
)

// capVocabulary is the frozen capability vocabulary of C322.1 (jobspec.Cap* are the same strings; provider imports no job code).
var capVocabulary = []string{"v2", "ears", "analyze", "plan", "attach", "fallback", "agent"}

var (
	attachNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	langRE       = regexp.MustCompile(`^(auto|[a-z]{2,3})$`)
	earsStages   = []string{"separate", "asr", "transcribe"}
)

// ErrAttachTooLarge is returned by the builder when the attachments together exceed MaxAttachBytes.
var ErrAttachTooLarge = errors.New("provider: attachments exceed the 1 MiB limit")

// usesV2 reports whether the take spec carries anything only /2 can express.
func usesV2(t TakeSpec) bool {
	return (t.Kind != "" && t.Kind != KindRender) || t.Ears != nil || t.Plan != nil || len(t.Attach) > 0
}

// WireFor chooses the contract of an attempt at submit (D-G1-3): /2 iff the job's needs are non-empty or the backend's open
// capabilities include fallback with an upload fallback configured (fallbackOpen); otherwise /1. A take spec that carries a /2
// feature is /2 whatever its needs say (the create path derives needs from the same spec, so this only guards a caller that
// forgot). Only yue2 speaks /2: any other engine stays on /1.
func WireFor(t TakeSpec, needs []string, fallbackOpen bool) string {
	if t.Engine != "" && t.Engine != EngineYuE2 {
		return ContractV1
	}
	if len(needs) > 0 || fallbackOpen || usesV2(t) {
		return ContractV2
	}
	return ContractV1
}

// BuildWorkerInputV2 renders the /2 job input of a TakeSpec. BuildWorkerInput calls it for Wire == ContractV2.
func BuildWorkerInputV2(t TakeSpec) ([]byte, error) {
	if t.Engine != EngineYuE2 {
		return nil, fmt.Errorf("%w: engine %q does not speak %s", ErrWorkerInput, t.Engine, ContractV2)
	}
	uploads := make(map[string]UploadTarget, len(t.Uploads))
	for k, v := range t.Uploads {
		uploads[k] = v
	}
	in := WorkerInput{
		Contract: ContractV2, Kind: t.Kind, TakeID: t.JobID, AttemptID: t.AttemptID, Engine: t.Engine, Request: t.Request,
		TimeoutS: t.TimeoutS, UploadBudgetS: t.UploadBudgetS, DeadlineUnix: t.Deadline.Unix(), MinAudioS: t.MinAudioS,
		FallbackAfter: t.FallbackAfterS, Start: StartTarget{URL: t.StartURL}, Uploads: uploads, Expect: t.Expect, Inputs: t.Inputs,
	}
	if in.Kind == "" {
		in.Kind = KindRender
	}
	if e := t.Ears; e != nil {
		in.Ears = &EarsIn{Lang: e.Lang, BudgetS: e.BudgetS, Stages: slices.Clone(e.Stages), KeepVocals: e.KeepVocals}
		if in.Ears.Lang == "" {
			in.Ears.Lang = "auto"
		}
	}
	if p := t.Plan; p != nil {
		in.Plan = &PlanIn{Seeds: slices.Clone(p.Seeds), SecondsPerSeed: p.SecondsPerSeed}
	}
	if len(t.Attach) > 0 {
		var total int
		in.Attach = make(map[string]AttachIn, len(t.Attach))
		for name, f := range t.Attach {
			total += len(f.Data)
			in.Attach[name] = AttachIn{SHA256: f.SHA256, DataBase64: base64.StdEncoding.EncodeToString(f.Data)}
		}
		if total > MaxAttachBytes {
			return nil, ErrAttachTooLarge
		}
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxRunBody {
		return nil, fmt.Errorf("%w: document exceeds the /run body limit", ErrWorkerInput)
	}
	return raw, nil
}

// parseV2 is the production validator of mm-worker-2.input.schema.json: it accepts exactly the documents the schema accepts
// (TestWorkerSchemasV2_MatchParser) and decodes strictly. The limit the schema cannot state (the decoded size of the attachments
// together, the /run body) is checked here only.
func parseV2(raw []byte) (WorkerInput, error) {
	bad := func(field, rule string) (WorkerInput, error) {
		return WorkerInput{}, fmt.Errorf("%w: %s %s", ErrWorkerInput, field, rule)
	}
	if len(raw) > MaxRunBody {
		return bad("document", "exceeds the /run body limit")
	}
	var in WorkerInput
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return WorkerInput{}, fmt.Errorf("%w: %s", ErrWorkerInput, shapeErr(err))
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return WorkerInput{}, fmt.Errorf("%w: trailing data", ErrWorkerInput)
	}
	var top map[string]json.RawMessage
	_ = json.Unmarshal(raw, &top)
	var tree map[string]any
	_ = json.Unmarshal(raw, &tree)
	for k, v := range tree { // the schema allows no null anywhere outside the request; encoding/json reads null as absent
		if k != "request" && hasNull(v) {
			return bad(k, "must not be null")
		}
	}
	kind := KindRender
	if _, ok := top["kind"]; ok {
		kind = in.Kind
	}
	in.Kind = kind
	switch {
	case !idRE.MatchString(in.TakeID):
		return bad("take_id", "malformed")
	case !idRE.MatchString(in.AttemptID):
		return bad("attempt_id", "malformed")
	case in.Engine != EngineYuE2:
		return bad("engine", "must be "+EngineYuE2)
	case in.TimeoutS < 1 || in.TimeoutS > maxTimeoutS:
		return bad("timeout_s", "out of range")
	case in.UploadBudgetS < 1 || in.UploadBudgetS > maxTimeoutS:
		return bad("upload_budget_s", "out of range")
	case in.DeadlineUnix < 1:
		return bad("deadline_unix", "out of range")
	case in.MinAudioS < 0 || in.MinAudioS > MaxMinAudioS:
		return bad("min_audio_s", "out of range")
	case !validURL(in.Start.URL):
		return bad("start.url", "malformed")
	}
	shape := KindShape{Kind: kind}
	if err := checkKindBlocks(&in, kind, top, &shape); err != nil {
		return WorkerInput{}, err
	}
	if err := checkAttach(in.Attach); err != nil {
		return WorkerInput{}, err
	}
	if err := checkUploadsV2(in, shape, top); err != nil {
		return WorkerInput{}, err
	}
	if len(in.Expect.Weights) == 0 {
		return bad("expect.weights", "must not be empty")
	}
	for repo, rev := range in.Expect.Weights {
		if !revRE.MatchString(rev) {
			return bad("expect.weights."+repo, "must be a 40-digit hex revision")
		}
	}
	return in, nil
}

// hasNull reports whether a decoded JSON value contains a null anywhere.
func hasNull(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case map[string]any:
		for _, e := range x {
			if hasNull(e) {
				return true
			}
		}
	case []any:
		for _, e := range x {
			if hasNull(e) {
				return true
			}
		}
	}
	return false
}

// checkKindBlocks validates the request, ears, plan, inputs and min_audio_s of the document's kind and fills the shape.
func checkKindBlocks(in *WorkerInput, kind string, top map[string]json.RawMessage, shape *KindShape) error {
	bad := func(field, rule string) error { return fmt.Errorf("%w: %s %s", ErrWorkerInput, field, rule) }
	_, hasRequest := top["request"]
	_, hasMin := top["min_audio_s"]
	switch kind {
	case KindRender:
		if !isNonEmptyObject(in.Request) {
			return bad("request", "must be a non-empty object")
		}
		if in.Plan != nil {
			return bad("plan", "is only part of kind plan")
		}
		if in.Inputs != nil {
			return bad("inputs", "is only part of kind analyze for yue2")
		}
	case KindAnalyze:
		switch {
		case hasRequest:
			return bad("request", "is not part of kind analyze")
		case in.Ears == nil:
			return bad("ears", "is required for kind analyze")
		case in.Plan != nil:
			return bad("plan", "is only part of kind plan")
		case hasMin:
			return bad("min_audio_s", "is only part of kind render")
		}
		if err := checkAnalyzeInputs(in.Inputs); err != nil {
			return err
		}
	case KindPlan:
		switch {
		case !isNonEmptyObject(in.Request):
			return bad("request", "must be a non-empty object")
		case in.Plan == nil:
			return bad("plan", "is required for kind plan")
		case in.Ears != nil:
			return bad("ears", "is not part of kind plan")
		case in.Inputs != nil:
			return bad("inputs", "is only part of kind analyze for yue2")
		case len(in.Attach) > 0:
			return bad("attach", "is not part of kind plan")
		case hasMin:
			return bad("min_audio_s", "is only part of kind render")
		}
	default:
		return bad("kind", "must be render, analyze or plan")
	}
	if e := in.Ears; e != nil {
		if err := checkEars(e, top["ears"]); err != nil {
			return err
		}
		shape.Ears, shape.KeepVocals = true, e.KeepVocals
	}
	if p := in.Plan; p != nil {
		if err := checkPlan(p); err != nil {
			return err
		}
		shape.Seeds = len(p.Seeds)
	}
	return nil
}

func checkAnalyzeInputs(inputs map[string]InputRef) error {
	bad := func(field, rule string) error { return fmt.Errorf("%w: %s %s", ErrWorkerInput, field, rule) }
	r, ok := inputs[InputSrcAudio]
	switch {
	case !ok || len(inputs) != 1:
		return bad("inputs", "must hold exactly src_audio for kind analyze")
	case !validURL(r.URL):
		return bad("inputs.src_audio.url", "malformed")
	case !sha256RE.MatchString(r.SHA256):
		return bad("inputs.src_audio.sha256", "must be 64 hex digits")
	case r.Bytes < 1 || r.Bytes > maxInputBytes:
		return bad("inputs.src_audio.bytes", "out of range")
	}
	return nil
}

func checkEars(e *EarsIn, rawEars json.RawMessage) error {
	bad := func(field, rule string) error { return fmt.Errorf("%w: ears.%s %s", ErrWorkerInput, field, rule) }
	var keys map[string]json.RawMessage
	_ = json.Unmarshal(rawEars, &keys)
	if _, ok := keys["lang"]; ok && !langRE.MatchString(e.Lang) {
		return bad("lang", "must be auto or a language code")
	}
	if _, ok := keys["budget_s"]; !ok || e.BudgetS < 1 || e.BudgetS > maxTimeoutS {
		return bad("budget_s", "is required and must be in range")
	}
	if n := len(e.Stages); n < 1 || n > maxEarsStagesCount {
		return bad("stages", "must hold one to three stages")
	}
	seen := map[string]bool{}
	for _, st := range e.Stages {
		if !slices.Contains(earsStages, st) || seen[st] {
			return bad("stages", "must be distinct stages of separate, asr, transcribe")
		}
		seen[st] = true
	}
	return nil
}

func checkPlan(p *PlanIn) error {
	bad := func(field, rule string) error { return fmt.Errorf("%w: plan.%s %s", ErrWorkerInput, field, rule) }
	if n := len(p.Seeds); n < 1 || n > MaxPlanSeeds {
		return bad("seeds", "must hold 1 to 16 seeds")
	}
	seen := map[int64]bool{}
	for _, s := range p.Seeds {
		if seen[s] {
			return bad("seeds", "must be distinct")
		}
		seen[s] = true
	}
	if p.SecondsPerSeed < 1 || p.SecondsPerSeed > maxSecondsPerSeed {
		return bad("s_per_seed", "out of range")
	}
	return nil
}

func checkAttach(att map[string]AttachIn) error {
	bad := func(field, rule string) error { return fmt.Errorf("%w: attach%s %s", ErrWorkerInput, field, rule) }
	if len(att) > maxAttachFiles {
		return bad("", "holds more than 16 files")
	}
	total := 0
	for name, f := range att {
		switch {
		case !attachNameRE.MatchString(name):
			return bad("", "has a malformed file name")
		case !sha256RE.MatchString(f.SHA256):
			return bad("."+name+".sha256", "must be 64 hex digits")
		}
		data, err := base64.StdEncoding.DecodeString(f.DataBase64)
		if err != nil || strings.ContainsAny(f.DataBase64, "\r\n") { // the decoder skips line breaks; the schema does not
			return bad("."+name+".data_base64", "is not base64")
		}
		total += len(data)
	}
	if total > MaxAttachBytes {
		return bad("", "exceeds 1 MiB decoded")
	}
	return nil
}

// checkUploadsV2 holds the uploads of the document to the allowlist of its shape and requires its mandatory targets, then checks
// the reserved fallback fields: a target's fallback and the document's fallback_after_s come together or not at all.
func checkUploadsV2(in WorkerInput, shape KindShape, top map[string]json.RawMessage) error {
	bad := func(field, rule string) error { return fmt.Errorf("%w: %s %s", ErrWorkerInput, field, rule) }
	for _, a := range ArtifactsV2(shape) {
		if _, ok := in.Uploads[a.Name]; a.Required && !ok {
			return bad("uploads."+a.Name, "missing")
		}
	}
	anyFallback := false
	for name, t := range in.Uploads {
		limit, ok := ArtifactMaxBytesV2(shape, name)
		switch {
		case !ok:
			return bad("uploads."+name, "is not an allowlisted artifact of this kind")
		case !validURL(t.URL):
			return bad("uploads."+name+".url", "malformed")
		case t.MaxBytes < 1 || t.MaxBytes > limit:
			return bad("uploads."+name+".max_bytes", "out of range")
		}
		if t.Fallback != nil {
			anyFallback = true
			if !validURL(t.Fallback.URL) {
				return bad("uploads."+name+".fallback.url", "malformed")
			}
		}
	}
	_, hasAfter := top["fallback_after_s"]
	switch {
	case anyFallback && !hasAfter:
		return bad("fallback_after_s", "is required when an upload carries a fallback")
	case !anyFallback && hasAfter:
		return bad("fallback_after_s", "needs an upload with a fallback")
	case hasAfter && (in.FallbackAfter < minFallbackAfterS || in.FallbackAfter > maxFallbackAfterS):
		return bad("fallback_after_s", "out of range")
	}
	return nil
}

// checkOutputKeys applies the version rules of DecodeOutput: a /1 output must not carry a /2 key, and a /2 output names a known
// kind and a well-formed ears block and plan list, and has the kind's null shape of audio_seconds and truncated.
func checkOutputKeys(raw []byte, o Output) error {
	bad := func(field, rule string) error { return fmt.Errorf("%w: output: %s %s", ErrWorkerInput, field, rule) }
	var top map[string]json.RawMessage
	_ = json.Unmarshal(raw, &top)
	if o.Contract != ContractV2 {
		for _, k := range []string{"kind", "ears", "plans", "caps"} {
			if _, ok := top[k]; ok {
				return bad(k, "is not part of mm.worker/1")
			}
		}
		return nil
	}
	if e := o.Ears; e != nil && !slices.Contains([]string{"ok", "error", "skipped"}, e.Status) {
		return bad("ears.status", "must be ok, error or skipped")
	}
	for _, k := range []string{"kind", "ears", "plans", "caps"} {
		if v, ok := top[k]; ok && bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return bad(k, "must not be null")
		}
	}
	for i, p := range o.Plans {
		switch {
		case p.Index < 0 || p.Index >= MaxPlanSeeds || i >= MaxPlanSeeds:
			return bad("plans.index", "out of range")
		case p.Status == "ok" && p.Bundle != PlanBundleName(p.Index):
			return bad("plans.bundle", "must name the plan's tar")
		case p.Status == "error" && (p.Code == "" || p.Bundle != ""):
			return bad("plans", "an error entry needs a code and no bundle")
		case p.Status != "ok" && p.Status != "error":
			return bad("plans.status", "must be ok or error")
		}
	}
	seenCap := map[string]bool{}
	for _, c := range o.Caps {
		if !slices.Contains(capVocabulary, c) || seenCap[c] {
			return bad("caps", "must be distinct capabilities of the vocabulary")
		}
		seenCap[c] = true
	}
	ok := o.Status == "ok"
	switch o.Kind {
	case KindRender:
		switch {
		case len(o.Plans) > 0:
			return bad("plans", "is only part of kind plan")
		case ok && (o.AudioSeconds == nil || o.Truncated == nil):
			return bad("audio_seconds", "and truncated are required on an ok render")
		}
	case KindAnalyze:
		switch {
		case len(o.Plans) > 0 || o.Truncated != nil:
			return bad("plans", "and truncated are not part of kind analyze")
		case ok && (o.AudioSeconds == nil || o.Ears == nil || o.Ears.Status != "ok"):
			return bad("audio_seconds", "and an ok ears block are required on an ok analyze")
		}
	case KindPlan:
		switch {
		case o.Ears != nil || o.AudioSeconds != nil || o.Truncated != nil:
			return bad("ears", "audio_seconds and truncated are not part of kind plan")
		case ok && !slices.ContainsFunc(o.Plans, func(p PlanOut) bool { return p.Status == "ok" }):
			return bad("plans", "needs an ok entry on an ok plan output")
		}
	default:
		return bad("kind", "must be render, analyze or plan")
	}
	return nil
}
