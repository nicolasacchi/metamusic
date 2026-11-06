package runpod

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
)

// The ACE-Step canary (C155.4, E2a part). It is the same scripted REST agent as M3's: estimate, create, wait, assert,
// discard. Every case is operator-typed spend bounded by --max-usd; `--dry-run` prints the worst case and spends nothing.
// The P0b measurements the operator folded into the first canary (peak VRAM per component, seed reproducibility, the
// LM's gpu_memory_utilization) are recorded outputs of the cases, never gates.

// EngineACEStep is the --engine value of the ACE-Step canary.
const EngineACEStep = acestep.Name

// The ACE-Step cases that are not M3's (terminate, timeout and kill-switch are reused). Order is C155.4's.
const (
	CaseT2M       CanaryCase = "t2m"
	CaseT2MLong   CanaryCase = "t2m-long"
	CaseReference CanaryCase = "reference"
	CaseRepaint   CanaryCase = "repaint"
	CaseInitFail  CanaryCase = "init-fail"
	CaseFirstPull CanaryCase = "first-pull"
)

// ErrInitFailConfirm reports that the init-fail case needs the operator's confirmation that the endpoint carries a wrong revision.
var ErrInitFailConfirm = errors.New("canary: the init-fail case needs --init-fail-confirm: first run `metamusic runpod provision --engine acestep15 --activate --max 1 --init-fail`, which sets a deliberately wrong MM_ACESTEP_MAIN_REVISION")

// part names the E2 delivery part of a case: "a" (E2-X4a) or "b" (E2-X4b, the only gate for the E2b flags).
type part string

// ---- stages and cases ----------------------------------------------------------------------------------------------

// aceStage is one take of a case: what it renders, how it is budgeted and what it must show.
type aceStage struct {
	label string
	// budget (the dry run quotes exactly these)
	audioS      int
	class       string // acestep.ClassTurbo | ClassBase
	extraLoadS  int    // seconds on top of the class's seed load (LRC alignment, input fetch)
	maxRuntimeS int    // 0 = the server's MM_ACESTEP_MAX_RUNTIME (BudgetParams.MaxRuntimeS)
	// run
	item func(s *aceState, id string) map[string]any // nil: compose_new from req
	req  func(id string) acestep.Request             // the compose_new request
	keep bool                                        // the take is a source of a later stage
	// want is the exact expected length in milliseconds of an edit (repaint: the source's; extend: source + extension).
	want func(s *aceState) (int64, bool)
	// skipWithSource: the stage renders (or needs more than) the source that --source-track replaces; it is skipped then.
	skipWithSource bool
}

// aceCaseDef is one case: its stages and the cross-stage evaluation after the last one.
type aceCaseDef struct {
	name     CanaryCase
	part     part
	stages   func(o CanaryOptions) []aceStage
	finalize func(c *canary, s *aceState, meas map[string]any)
	// proposal turns a passing run into the proposed features patch (only passing cases flip a flag).
	proposal func(o CanaryOptions, s *aceState, rep *CanaryReport, p *ProposedPatch)
}

// aceState carries what the stages of one case learn from each other.
type aceState struct {
	src    string // the source track id (--source-track, or the case's own source stage)
	srcAud string // its audio_s when this case rendered it
	by     map[string]*stageResult
	own    []string // track ids this case created (discarded at the end unless --keep)
}

type stageResult struct {
	label    string
	trackID  string
	audioS   string
	execMS   *int64
	coldS    *int64
	wallS    int64
	sha      map[string]string // artifact name -> sha256 (never printed whole)
	names    []string
	bytes    map[string]int64
	gpuClass string
}

// CanaryStage is the printed observation of one stage (no hashes in full, no ids of the provider).
type CanaryStage struct {
	Label       string   `json:"label"`
	JobID       string   `json:"job_id,omitempty"`
	BatchID     string   `json:"batch_id,omitempty"`
	FinalState  string   `json:"final_state,omitempty"`
	AudioS      string   `json:"audio_s,omitempty"`
	ExecutionMS *int64   `json:"execution_ms,omitempty"`
	ColdStartS  *int64   `json:"cold_start_s,omitempty"`
	WallS       int64    `json:"wall_s"`
	Artifacts   []string `json:"artifacts,omitempty"`
	GPUClass    string   `json:"gpu_class,omitempty"`
}

// day is the day stamp of the synthetic request ids.
func (c *canary) day() time.Time { return c.o.Day }

var aceCases []aceCaseDef

func init() {
	aceCases = append(aceCases, aceCasesA()...)
	aceCases = append(aceCases, aceCasesB()...)
}

func aceCaseByName(n CanaryCase) (aceCaseDef, bool) {
	for _, d := range aceCases {
		if d.name == n {
			return d, true
		}
	}
	return aceCaseDef{}, false
}

// ACEStepCanaryCases lists the cases of a part ("a", "b" or "" for both) in C155.4 order.
func ACEStepCanaryCases(p string) []CanaryCase {
	var out []CanaryCase
	for _, d := range aceCases {
		if p == "" || string(d.part) == p {
			out = append(out, d.name)
		}
	}
	return out
}

// ParseACEStepCase validates a --case value for --engine acestep15.
func ParseACEStepCase(s string) (CanaryCase, error) {
	if _, ok := aceCaseByName(CanaryCase(s)); ok {
		return CanaryCase(s), nil
	}
	names := make([]string, 0, len(aceCases))
	for _, d := range aceCases {
		names = append(names, string(d.name))
	}
	return "", fmt.Errorf("runpod: canary: unknown case %q for acestep15 (want %s)", s, strings.Join(names, ", "))
}

// ---- requests ------------------------------------------------------------------------------------------------------

// aceRequest is the synthetic text2music request of the canary: instrumental, a fixed seed and the LM's thinking on
// (the LM path is what the P0b memory measurements need).
func aceRequest(id string, audioS int, seed int64) acestep.Request {
	return acestep.Request{ID: id, Task: acestep.TaskText2Music, Caption: "instrumental, ambient, slow piano and pads, 80 BPM",
		Instrumental: true, VocalLanguage: "unknown", DurationDS: audioS * 10, Seed: seed, Thinking: true,
		ParamsVersion: acestep.CurrentParamsVersion(), Steps: acestep.TurboSteps}
}

func composeItem(r acestep.Request) (map[string]any, error) {
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	if _, _, issues := acestep.Parse(raw); acestep.HasErrors(issues) {
		return nil, fmt.Errorf("canary: the synthetic request %q is invalid (%s)", r.ID, issues[0].Code)
	}
	return map[string]any{"mode": "compose_new", "engine": EngineACEStep, "recipe": json.RawMessage(raw), "title": r.ID}, nil
}

// editItem is an edit mode item over the state's source track.
func editItem(mode string, edit map[string]any, seed int64) func(*aceState, string) map[string]any {
	return func(s *aceState, _ string) map[string]any {
		it := map[string]any{"mode": mode, "engine": EngineACEStep, "parent_track_id": s.src, "edit": edit}
		if seed != 0 { // complete and stems render the parent's seed: no seeds field
			it["seeds"] = []int64{seed}
		}
		return it
	}
}

func (c *canary) estimateBody(spec CanarySpec) map[string]any {
	if spec.Item != nil {
		return map[string]any{"engine": spec.Engine, "items": []any{spec.Item}}
	}
	return map[string]any{
		"engine": "yue2",
		"items":  []any{map[string]any{"mode": "compose_new", "recipe": spec.Recipe, "title": spec.RequestID}},
	}
}

// ---- provider status per backend ----------------------------------------------------------------------------------

type cBackend struct {
	cProvider
	Engine string `json:"engine"`
}

type cProviderAll struct {
	cProvider
	Backends []cBackend `json:"backends"`
}

// cTimings is the job view's timings object: the two timings the canary has always read and timings.extra, the worker's
// numeric measurements (peak_vram_mb_<component>, lm_gpu_memory_utilization_milli, ...) for the P0b recorded outputs.
type cTimings struct {
	ExecutionMS *int64                 `json:"execution_ms"`
	ColdStartS  *int64                 `json:"cold_start_s"`
	Extra       map[string]json.Number `json:"extra"`
}

// readProvider reads /api/provider. For --engine acestep15 it returns the ACE-Step backend's entry of backends[]; the
// default object is the yue2 backend's and is returned as before otherwise.
func (c *canary) readProvider(ctx context.Context) (cProvider, error) {
	var all cProviderAll
	if err := c.do(ctx, http.MethodGet, "/api/provider", nil, nil, &all); err != nil {
		return cProvider{}, err
	}
	if c.o.Engine != EngineACEStep {
		return all.cProvider, nil
	}
	for _, b := range all.Backends {
		if b.Engine == EngineACEStep {
			return b.cProvider, nil
		}
	}
	return cProvider{}, errors.New("canary: the server shows no acestep15 backend (MM_ACESTEP=1 and RUNPOD_ACESTEP_ENDPOINT_ID are needed)")
}

// ---- running a case -----------------------------------------------------------------------------------------------

func (c *canary) runACEStep(ctx context.Context) (*CanaryReport, error) {
	def, ok := aceCaseByName(c.o.Case)
	if !ok {
		_, err := ParseACEStepCase(string(c.o.Case))
		return nil, err
	}
	c.rep.Engine = EngineACEStep
	if def.name == CaseInitFail && !c.o.InitFailConfirm {
		return nil, ErrInitFailConfirm
	}
	if c.o.N < 1 {
		c.o.N = 1
	}
	stages := def.stages(c.o)
	st := &aceState{src: c.o.SourceTrack, by: map[string]*stageResult{}}
	agg := map[string]any{}
	defer func() { c.discardOwn(st) }()
	for i, sg := range stages {
		if sg.skipWithSource && c.o.SourceTrack != "" {
			continue
		}
		last := i == len(stages)-1
		spec, err := c.aceSpec(def, sg, st)
		if err != nil {
			return nil, err
		}
		c.rep.RequestID = spec.RequestID
		c.prefix = ""
		if len(stages) > 1 {
			c.prefix = sg.label + ":"
		}
		c.keepStage, c.skipIdle = !last, !last
		began := c.o.Clock.Now()
		rep, err := c.run(ctx, spec)
		var re *restError
		if errors.As(err, &re) && re.Status == http.StatusUnprocessableEntity && re.Code == "INVALID_ITEM" {
			// the engine's own guards (exceeds_max_runtime, range and format rules) refuse before any money is reserved
			c.rep.Measured = agg
			return c.finish("refused", ExitRefused, "the server refused the %s stage before reserving anything: %s", sg.label, re.Message), nil
		}
		if err != nil {
			return nil, err
		}
		res := c.stageResult(sg.label, c.o.Clock.Now().Sub(began))
		st.by[sg.label] = res
		for k, v := range c.rep.Measured {
			agg[sg.label+"."+k] = v
		}
		c.rep.Stages = append(c.rep.Stages, c.stageView(res))
		if res.trackID != "" {
			if !last { // the last stage's track is discarded by the stage itself (unless --keep)
				st.own = append(st.own, res.trackID)
			}
			if sg.keep {
				st.src, st.srcAud = res.trackID, res.audioS
			}
		}
		if rep.Outcome != "pass" {
			c.rep.Measured = agg
			return rep, nil
		}
	}
	c.prefix, c.keepStage = "", false
	if def.finalize != nil {
		def.finalize(c, st, agg)
	}
	c.rep.Measured = agg
	for _, ck := range c.rep.Checks {
		if !ck.OK {
			return c.finish("fail", ExitFail, "check failed: %s (%s)", ck.Name, ck.Note), nil
		}
	}
	rep := c.finish("pass", ExitPass, "")
	if def.proposal != nil {
		p := &ProposedPatch{Features: map[string]any{}}
		def.proposal(c.o, st, rep, p)
		rep.Proposed = p
	}
	return rep, nil
}

// aceSpec builds the CanarySpec of a stage: the request id is unique per day, case, stage and sequence number.
func (c *canary) aceSpec(def aceCaseDef, sg aceStage, st *aceState) (CanarySpec, error) {
	id := fmt.Sprintf("mm-canary-%s-%s-%s-%d", c.day().UTC().Format("20060102"), def.name, sg.label, c.o.N)
	var item map[string]any
	if sg.item != nil {
		if st.src == "" {
			return CanarySpec{}, errors.New("canary: this stage needs a source track and none was rendered")
		}
		item = sg.item(st, id)
	} else {
		var err error
		if item, err = composeItem(sg.req(id)); err != nil {
			return CanarySpec{}, err
		}
	}
	spec := CanarySpec{Case: def.name, RequestID: id, Engine: EngineACEStep, Item: item, ExpectedAudioS: sg.audioS}
	switch def.name {
	case CaseInitFail:
		spec.driver = initFailDriver
	case CaseTerminate, CaseTimeout, CaseKillSwitch:
		// M3's assertions apply as they are
	default:
		sg := sg
		spec.assert = func(c *canary, prov cProvider, job cJob, meas map[string]any) {
			c.assertStage(sg, st, prov, job, meas)
		}
	}
	return spec, nil
}

// stageResult reads the stage that just ended from the canary's last job view.
func (c *canary) stageResult(label string, wall time.Duration) *stageResult {
	j := c.lastJob
	r := &stageResult{label: label, execMS: j.Timings.ExecutionMS, coldS: j.Timings.ColdStartS, wallS: int64(wall / time.Second),
		sha: map[string]string{}, bytes: map[string]int64{}}
	if j.TrackID != nil {
		r.trackID = *j.TrackID
	}
	if j.AudioS != nil {
		r.audioS = j.AudioS.String()
	}
	for _, a := range j.Artifacts {
		r.sha[a.Name], r.bytes[a.Name] = a.SHA256, a.Bytes
		r.names = append(r.names, a.Name)
	}
	slices.Sort(r.names)
	if j.Env != nil {
		r.gpuClass = j.Env.GPUClass
	}
	return r
}

func (c *canary) stageView(r *stageResult) CanaryStage {
	return CanaryStage{Label: r.label, JobID: c.rep.JobID, BatchID: c.rep.BatchID, FinalState: c.rep.FinalState, AudioS: r.audioS,
		ExecutionMS: r.execMS, ColdStartS: r.coldS, WallS: r.wallS, Artifacts: r.names, GPUClass: r.gpuClass}
}

// discardOwn discards the tracks a multi-stage case created (unless --keep). The source track of --source-track is
// the operator's and is never touched.
func (c *canary) discardOwn(st *aceState) {
	if c.o.Keep {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, id := range st.own {
		if id == c.o.SourceTrack {
			continue
		}
		_ = c.do(ctx, http.MethodPatch, "/api/tracks/"+id, nil, map[string]string{"status": "discarded"}, nil)
	}
}

// ---- assertions ---------------------------------------------------------------------------------------------------

// assertStage is the common outcome of a completed render: clean, every artifact verified, the length within
// +-0.5 s of the expectation (text2music) and the observations of C155.4 recorded.
func (c *canary) assertStage(sg aceStage, st *aceState, prov cProvider, job cJob, meas map[string]any) {
	ok := c.check("completed", job.State == "completed" && job.Outcome != nil && *job.Outcome == "clean", "state %s", job.State)
	if !ok {
		return
	}
	verified := len(job.Artifacts) > 0
	for _, a := range job.Artifacts {
		verified = verified && a.Verified
	}
	c.check("artifacts_verified", verified, "%d artifacts", len(job.Artifacts))
	c.recordRender(job, meas)
	if sg.item == nil {
		c.checkLength(job, int64(sg.audioS)*1000, 500, "text2music")
	} else if sg.want != nil {
		if ms, ok := sg.want(st); ok {
			c.checkLength(job, ms, 10, "exact")
		}
	}
	c.recordP0b(job, meas)
}

// checkLength compares audio_s with the expectation in milliseconds, within tol ms.
func (c *canary) checkLength(job cJob, wantMS, tolMS int64, what string) {
	if job.AudioS == nil {
		c.check("audio_length", false, "no audio_s")
		return
	}
	got, ok := milliOf(*job.AudioS)
	diff := got - wantMS
	if diff < 0 {
		diff = -diff
	}
	c.check("audio_length", ok && diff <= tolMS, "%s: audio %s s, expected %d.%03d s (+-%d ms)", what, job.AudioS.String(), wantMS/1000, wantMS%1000, tolMS)
}

func (c *canary) recordRender(job cJob, meas map[string]any) {
	if job.AudioS != nil {
		meas["audio_s"] = job.AudioS.String()
	}
	meas["expected_audio_s"] = job.ExpectedAudioS
	var names []string
	var total int64
	for _, a := range job.Artifacts {
		names = append(names, a.Name)
		total += a.Bytes
		meas["bytes."+a.Name] = a.Bytes
	}
	slices.Sort(names)
	meas["artifact_names"] = names
	meas["upload_bytes"] = total
	if job.Env != nil && job.Env.GPUClass != "" {
		meas["gpu_class"] = job.Env.GPUClass
	}
	if job.Timings.ExecutionMS != nil && job.AudioS != nil {
		if ms, ok := milliOf(*job.AudioS); ok && ms > 0 {
			meas["rtf_milli"] = *job.Timings.ExecutionMS * 1000 / ms // execution seconds per audio second, x1000
		}
	}
}

// recordP0b records the worker-reported P0b numbers from the job view's timings.extra (peak_vram_mb_<component> and
// lm_gpu_memory_utilization_milli); a number the worker did not report is recorded as such so the proposed patch says it
// still has to be read from the worker log.
func (c *canary) recordP0b(job cJob, meas map[string]any) {
	vram := map[string]int64{}
	for k, v := range job.Timings.Extra {
		if comp, ok := strings.CutPrefix(k, "peak_vram_mb_"); ok {
			if n, ok := wholeOf(v); ok {
				vram[comp] = n
			}
		}
	}
	if len(vram) > 0 {
		meas["peak_vram_mb"] = vram
	} else {
		meas["peak_vram_mb"] = notReportedByWorker
	}
	if n, ok := wholeOf(job.Timings.Extra["lm_gpu_memory_utilization_milli"]); ok {
		meas["lm_gpu_memory_utilization_milli"] = n
		return
	}
	meas["lm_gpu_memory_utilization_milli"] = notReportedByWorker
}

// notReportedByWorker is what a P0b measurement reads when timings.extra does not carry it.
const notReportedByWorker = "not_reported_by_worker"

// wholeOf reads a timing as a whole number (a fractional value is rounded; a MiB or per-mille figure needs no more).
func wholeOf(n json.Number) (int64, bool) {
	if n == "" {
		return 0, false
	}
	f, err := n.Float64()
	if err != nil || f < 0 {
		return 0, false
	}
	return int64(math.Round(f)), true
}

// sameArtifact reports whether two stages produced the same audio.flac bytes.
func sameArtifact(a, b *stageResult) (same, known bool) {
	x, y := a.sha["audio.flac"], b.sha["audio.flac"]
	if x == "" || y == "" {
		return false, false
	}
	return x == y, true
}

// ---- init-fail ----------------------------------------------------------------------------------------------------

// initFailDriver replaces the normal wait loop: the worker can never start (a wrong revision), so the case waits up to
// the start window for main's evidence trip (crash_loop, C151.4), cancels the job, and confirms that the workers are 0.
func initFailDriver(ctx context.Context, c *canary, spec CanarySpec, prov cProvider) *CanaryReport {
	window := c.o.StartTimeout
	if window <= 0 {
		window = DefaultCanaryStartTimeout
	}
	meas := map[string]any{}
	c.rep.Measured = meas
	started := c.o.Clock.Now()
	every := max(c.o.SweepEvery, 20*time.Millisecond)
	reason := ""
	for c.o.Clock.Now().Sub(started) <= window {
		if ctx.Err() != nil {
			_ = c.cancelJob(c.rep.JobID)
			return c.finish("aborted", ExitAborted, "interrupted; the job was cancelled")
		}
		p, err := c.status(ctx)
		if err == nil && p.tripped() {
			var t struct{ Reason string }
			_ = json.Unmarshal(p.Trip, &t)
			reason = t.Reason
			break
		}
		if c.sleep(ctx, every) != nil {
			break
		}
	}
	_ = c.cancelJob(c.rep.JobID)
	meas["seconds_to_trip"] = int(c.o.Clock.Now().Sub(started) / time.Second)
	meas["trip_reason"] = reason
	c.check("crash_loop_trip", reason == "crash_loop", "trip reason %q within %s", reason, window)
	ctx2, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	live := -1
	for i := 0; i < 12; i++ {
		if live = c.liveWorkers(ctx2); live == 0 {
			break
		}
		if c.sleep(ctx2, every) != nil {
			break
		}
	}
	meas["workers_after"] = live
	c.check("workers_zero", live == 0, "%d live workers after the cancel (-1: unreadable)", live)
	c.logf("restore the endpoint now: `metamusic runpod provision --engine acestep15 --deactivate` (re-pins the revision), then `metamusic runpod reset-trip --engine acestep15 --reason ...`")
	for _, ck := range c.rep.Checks {
		if !ck.OK {
			return c.finish("fail", ExitFail, "check failed: %s (%s)", ck.Name, ck.Note)
		}
	}
	return c.finish("pass", ExitPass, "")
}

// jsonNum wraps a decimal string as a json.Number.
func jsonNum(s string) json.Number { return json.Number(s) }

// srcMS is the audio length of the source this case rendered, in milliseconds (false when --source-track was used).
func srcMS(s *aceState) (int64, bool) {
	if s.srcAud == "" {
		return 0, false
	}
	return milliOf(jsonNum(s.srcAud))
}

// sec is a whole number of seconds as the JSON number the API's 0.1 s grid takes (the package carries no floats).
func sec(n int) json.Number { return json.Number(strconv.Itoa(n)) }
