package runpod

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/redact"
)

// Canary exit codes (C79).
const (
	ExitPass    = 0
	ExitFail    = 1
	ExitUsage   = 2
	ExitRefused = 3 // cost over --max-usd, disarmed, or the server is not configured for the case
	ExitAborted = 4 // the job never reached generating; cancelled
)

// DefaultCanaryMaxUSD is the per-case ceiling the canary accepts: $0.25 (C79, P.5).
const DefaultCanaryMaxUSD money.MicroUSD = 250_000

// CanaryCase names one scripted case.
type CanaryCase string

// The cases of C79.
const (
	CaseMain       CanaryCase = "main"
	CaseCotFull    CanaryCase = "cot-full"
	CaseTerminate  CanaryCase = "terminate"
	CaseTimeout    CanaryCase = "timeout"
	CaseKillSwitch CanaryCase = "kill-switch"
)

// CanaryCases lists them in documentation order.
var CanaryCases = []CanaryCase{CaseMain, CaseCotFull, CaseTerminate, CaseTimeout, CaseKillSwitch}

// ParseCanaryCase validates a --case value.
func ParseCanaryCase(s string) (CanaryCase, error) {
	for _, c := range CanaryCases {
		if string(c) == s {
			return c, nil
		}
	}
	return "", fmt.Errorf("runpod: canary: unknown case %q (want main, cot-full, terminate, timeout or kill-switch)", s)
}

// canaryBars is the length of each case's score: YuE2 has no duration control, so the explicit ABC score
// bounds the render (two seconds per bar at the fixed tempo).
var canaryBars = map[CanaryCase]int{CaseMain: 8, CaseCotFull: 8, CaseTerminate: 32, CaseTimeout: 64, CaseKillSwitch: 64}

// CanarySpec is one case's synthetic take.
type CanarySpec struct {
	Case           CanaryCase
	RequestID      string
	Recipe         json.RawMessage // the song request, as sent
	Bars           int
	Cot            string
	ExpectedAudioS int // after the engine's clamp
}

// abcScore renders a two-voice score of n 4/4 bars at 120 quarter notes per minute (2 s per bar).
func abcScore(n int) string {
	var b strings.Builder
	b.WriteString("X:1\nT:mm canary\nM:4/4\nL:1/8\nQ:1/4=120\nK:C\n")
	for _, v := range []struct{ id, bar string }{{"1", "CDEFGABc"}, {"2", "C,2E,2G,2C2"}} {
		b.WriteString("V:" + v.id + "\n")
		for i := 0; i < n; i++ {
			b.WriteString(v.bar + "|")
			if i%4 == 3 || i == n-1 {
				b.WriteString("\n")
			}
		}
	}
	return b.String()
}

// NewCanarySpec builds the synthetic recipe of a case: id mm-canary-<yyyymmdd>-<case>-<n>, style
// `instrumental, ambient, 80 BPM, no vocals`, lyrics `[Intro]\n\n[Outro]\n`, seed 1.
func NewCanarySpec(c CanaryCase, day time.Time, n int) (CanarySpec, error) {
	bars, ok := canaryBars[c]
	if !ok {
		return CanarySpec{}, fmt.Errorf("runpod: canary: unknown case %q", c)
	}
	if n < 1 {
		n = 1
	}
	cot := "melody"
	if c == CaseCotFull {
		cot = "full"
	}
	abc := abcScore(bars)
	id := fmt.Sprintf("mm-canary-%s-%s-%d", day.UTC().Format("20060102"), c, n)
	raw, err := json.Marshal(yue2.Request{ID: id, Style: "instrumental, ambient, 80 BPM, no vocals", Lyrics: "[Intro]\n\n[Outro]\n",
		Cot: cot, Seed: 1, ABC: &abc})
	if err != nil {
		return CanarySpec{}, err
	}
	secs, err := yue2.Engine{}.ExpectedAudioSeconds(raw)
	if err != nil {
		return CanarySpec{}, err
	}
	return CanarySpec{Case: c, RequestID: id, Recipe: raw, Bars: bars, Cot: cot, ExpectedAudioS: secs}, nil
}

// CeilingAllowed reports whether an estimate's ceiling (the "spend up to" amount, not the expected cost)
// is within the per-case limit.
func CeilingAllowed(ceiling, limit money.MicroUSD) bool { return ceiling > 0 && ceiling <= limit }

// CanaryOptions drives one canary case over REST with MM_TOKEN (a PAT with read + write + generate).
type CanaryOptions struct {
	BaseURL string
	Token   string // never printed, logged or put in the report
	HTTP    *http.Client
	Clock   clock.Clock
	Case    CanaryCase
	MaxUSD  money.MicroUSD // default DefaultCanaryMaxUSD
	Keep    bool           // keep the canary takes (else they are discarded)
	Day     time.Time      // default now
	N       int            // sequence number in the request id (default 1)
	Log     io.Writer      // progress lines; nil = none
	// Timing knobs; zero values are the production ones.
	WaitS           int           // wait_for_job long-poll, default 25
	SweepEvery      time.Duration // provider status polling, default 10 s
	AbortAfter      time.Duration // not generating after this long => abort; default ColdStartS + 120 s
	AfterCancelWait time.Duration // terminate: second worker observation, default 60 s
}

// CanaryReport is the JSON a canary case prints. It carries no token, no confirmation token and no
// provider reference.
type CanaryReport struct {
	Case       CanaryCase         `json:"case"`
	Outcome    string             `json:"outcome"` // pass | fail | refused | aborted
	Exit       int                `json:"exit"`
	Reason     string             `json:"reason,omitempty"`
	RequestID  string             `json:"request_id,omitempty"`
	BatchID    string             `json:"batch_id,omitempty"`
	JobID      string             `json:"job_id,omitempty"`
	FinalState string             `json:"final_state,omitempty"`
	ErrorCode  string             `json:"error_code,omitempty"`
	Ceiling    int64              `json:"ceiling_micro_usd,omitempty"`
	Checks     []CanaryCheck      `json:"checks,omitempty"`
	Measured   map[string]any     `json:"measured,omitempty"`
	Billing    *CanaryBillingView `json:"billing,omitempty"`
}

// CanaryCheck is one assertion.
type CanaryCheck struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
	Note string `json:"note,omitempty"`
}

// CanaryBillingView is the result of --verify-billing.
type CanaryBillingView struct {
	Reconciled        bool   `json:"reconciled"`
	BilledLEReserved  bool   `json:"billed_le_reserved"`
	SettledMicroUSD   int64  `json:"settled_micro_usd"`
	ReservedMicroUSD  int64  `json:"reserved_micro_usd"`
	RatioMilli        *int64 `json:"billed_over_list_milli,omitempty"`
	RatioNoIdleMilli  *int64 `json:"billed_over_list_without_idle_milli,omitempty"`
	MeteredSeconds    *int64 `json:"metered_seconds,omitempty"`
	IdleSeconds       *int   `json:"idle_s,omitempty"`
	IdleTailBilledHnt string `json:"idle_tail_hint,omitempty"`
}

// ---- REST views (only the fields the canary reads) ---------------------------------------------------------

type cProvider struct {
	GenerationEnabled bool            `json:"generation_enabled"`
	DisabledReason    *string         `json:"disabled_reason"`
	Armed             bool            `json:"armed"`
	Trip              json.RawMessage `json:"trip"`
	Workers           *struct {
		Running, Idle, Initializing int
	} `json:"workers"`
	Endpoint *struct {
		IdleTimeoutS *int `json:"idle_timeout_s"`
	} `json:"endpoint"`
	Price *struct {
		PerHourMicroUSD int64 `json:"per_hour_micro_usd"`
	} `json:"price"`
	Meter *struct {
		BilledSeconds24h int64 `json:"billed_seconds_24h"`
	} `json:"meter"`
	ColdStartS  int `json:"cold_start_s"`
	KillSlackS  int `json:"kill_slack_s"`
	MaxRuntimeS int `json:"max_runtime_s"`
}

func (p cProvider) tripped() bool {
	t := bytes.TrimSpace(p.Trip)
	return len(t) > 0 && string(t) != "null"
}

func (p cProvider) live() int {
	if p.Workers == nil {
		return 0
	}
	return p.Workers.Running + p.Workers.Initializing + p.Workers.Idle
}

type cEstimate struct {
	ConfirmationToken     string `json:"confirmation_token"`
	Takes                 int    `json:"takes"`
	RequiresHumanApproval bool   `json:"requires_human_approval"`
	Cost                  struct {
		CeilingMicroUSD int64 `json:"ceiling_micro_usd"`
	} `json:"cost"`
}

type cBatch struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Cost  struct {
		ReservedMicroUSD int64 `json:"reserved_micro_usd"`
		SettledMicroUSD  int64 `json:"settled_micro_usd"`
		Reconciled       bool  `json:"reconciled"`
	} `json:"cost"`
	Jobs []struct {
		ID string `json:"id"`
	} `json:"jobs"`
}

type cJob struct {
	ID        string       `json:"id"`
	State     string       `json:"state"`
	Outcome   *string      `json:"outcome"`
	ErrorCode *string      `json:"error_code"`
	TrackID   *string      `json:"track_id"`
	AudioS    *json.Number `json:"audio_s"`
	TimeoutS  int          `json:"timeout_s"`
	Artifacts []struct {
		Name     string `json:"name"`
		Verified bool   `json:"verified"`
	} `json:"artifacts"`
	Timings struct {
		ExecutionMS *int64 `json:"execution_ms"`
		ColdStartS  *int64 `json:"cold_start_s"`
	} `json:"timings"`
}

func (j cJob) terminal() bool {
	switch j.State {
	case "completed", "failed", "cancelled", "expired":
		return true
	}
	return false
}

// pastStarting reports a state at or after generating (the job is not stuck in the queue).
func (j cJob) pastStarting() bool {
	switch j.State {
	case "generating", "transferring", "ingesting", "completed", "cancelling", "failing", "failed", "cancelled", "expired":
		return true
	}
	return false
}

type canary struct {
	o   CanaryOptions
	hc  *http.Client
	rep *CanaryReport
}

type restError struct {
	Status  int
	Code    string
	Message string
}

func (e *restError) Error() string {
	return fmt.Sprintf("canary: HTTP %d %s: %s", e.Status, e.Code, redact.String(e.Message))
}

func (c *canary) logf(format string, a ...any) {
	if c.o.Log != nil {
		fmt.Fprintf(c.o.Log, format+"\n", a...)
	}
}

func (c *canary) do(ctx context.Context, method, path string, hdr map[string]string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.o.BaseURL, "/")+path, rdr)
	if err != nil {
		return errors.New("canary: invalid --url")
	}
	req.Header.Set("Authorization", "Bearer "+c.o.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("canary: the server is unreachable (" + method + " " + strings.SplitN(path, "?", 2)[0] + ")")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		var p struct{ Code, Message string }
		_ = json.Unmarshal(raw, &p)
		return &restError{Status: resp.StatusCode, Code: p.Code, Message: p.Message}
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return errors.New("canary: undecodable response from " + strings.SplitN(path, "?", 2)[0])
		}
	}
	return nil
}

func (c *canary) check(name string, ok bool, format string, a ...any) bool {
	c.rep.Checks = append(c.rep.Checks, CanaryCheck{Name: name, OK: ok, Note: fmt.Sprintf(format, a...)})
	return ok
}

func (c *canary) finish(outcome string, exit int, format string, a ...any) *CanaryReport {
	c.rep.Outcome, c.rep.Exit = outcome, exit
	if format != "" {
		c.rep.Reason = redact.String(fmt.Sprintf(format, a...))
	}
	return c.rep
}

func (c *canary) sleep(ctx context.Context, d time.Duration) error { return sleep(ctx, c.o.Clock, d) }

// RunCanary runs one case (or --verify-billing) and returns its report. An error means the run could not
// be carried out at all (usage, an unreachable server before anything was created); a report always has an
// Exit. Once a batch exists, any early exit cancels the job first (a canary never leaves a take running).
func RunCanary(ctx context.Context, o CanaryOptions) (*CanaryReport, error) {
	if o.Clock == nil {
		o.Clock = clock.Real
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 90 * time.Second}
	}
	if o.MaxUSD <= 0 {
		o.MaxUSD = DefaultCanaryMaxUSD
	}
	if o.Day.IsZero() {
		o.Day = o.Clock.Now()
	}
	if o.WaitS <= 0 {
		o.WaitS = 25
	}
	if o.SweepEvery <= 0 {
		o.SweepEvery = 10 * time.Second
	}
	if o.AfterCancelWait <= 0 {
		o.AfterCancelWait = 60 * time.Second
	}
	if o.Token == "" {
		return nil, errors.New("canary: MM_TOKEN is not set")
	}
	if u, err := url.Parse(o.BaseURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, errors.New("canary: --url must be an absolute http(s) URL")
	}
	hc := *o.HTTP
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c := &canary{o: o, hc: &hc, rep: &CanaryReport{Case: o.Case}}
	if o.Case == "" {
		return nil, errors.New("canary: --case is required")
	}
	spec, err := NewCanarySpec(o.Case, o.Day, o.N)
	if err != nil {
		return nil, err
	}
	c.rep.RequestID = spec.RequestID
	return c.run(ctx, spec)
}

func (c *canary) run(ctx context.Context, spec CanarySpec) (*CanaryReport, error) {
	var prov cProvider
	if err := c.do(ctx, http.MethodGet, "/api/provider", nil, nil, &prov); err != nil {
		return nil, err
	}
	switch {
	case !prov.GenerationEnabled:
		reason := "unknown"
		if prov.DisabledReason != nil {
			reason = *prov.DisabledReason
		}
		return c.finish("refused", ExitRefused, "generation is disabled: %s", reason), nil
	case !prov.Armed:
		return c.finish("refused", ExitRefused, "the provider is disarmed (arm it with `metamusic runpod arm`)"), nil
	case prov.tripped():
		return c.finish("refused", ExitRefused, "the provider is tripped; reset the trip first"), nil
	case spec.Case == CaseTimeout && prov.MaxRuntimeS != 30:
		return c.finish("refused", ExitRefused, "the timeout case needs MM_JOB_MAX_RUNTIME=30s on the server and an endpoint provisioned with --timeout-case (max_runtime_s is %d)", prov.MaxRuntimeS), nil
	}

	var est cEstimate
	err := c.do(ctx, http.MethodPost, "/api/generations/estimate", nil, map[string]any{
		"engine": "yue2",
		"items":  []any{map[string]any{"mode": "compose_new", "recipe": spec.Recipe, "title": spec.RequestID}},
	}, &est)
	if err != nil {
		return nil, err
	}
	c.rep.Ceiling = est.Cost.CeilingMicroUSD
	if est.Takes != 1 {
		return c.finish("refused", ExitRefused, "the estimate has %d takes, want 1", est.Takes), nil
	}
	if !CeilingAllowed(money.MicroUSD(est.Cost.CeilingMicroUSD), c.o.MaxUSD) {
		return c.finish("refused", ExitRefused, "the estimate ceiling %s exceeds --max-usd %s",
			money.MicroUSD(est.Cost.CeilingMicroUSD).USD(), c.o.MaxUSD.USD()), nil
	}
	var batch cBatch
	err = c.do(ctx, http.MethodPost, "/api/generations", map[string]string{"Idempotency-Key": "canary-" + spec.RequestID},
		map[string]any{"confirmation_token": est.ConfirmationToken, "rationale": "runpod canary: " + string(spec.Case)}, &batch)
	if err != nil {
		return nil, err
	}
	if len(batch.Jobs) != 1 {
		return c.finish("fail", ExitFail, "the batch has %d jobs, want 1", len(batch.Jobs)), nil
	}
	c.rep.BatchID, c.rep.JobID = batch.ID, batch.Jobs[0].ID
	if batch.State == "awaiting_confirmation" {
		return c.finish("refused", ExitRefused, "the batch awaits a human confirmation; nothing was submitted"), nil
	}
	c.logf("batch %s created for case %s (ceiling %s)", batch.ID, spec.Case, money.MicroUSD(est.Cost.CeilingMicroUSD).USD())
	return c.drive(ctx, spec, prov), nil
}

// cancelJob cancels with a fresh context so that an interrupted run still stops the take.
func (c *canary) cancelJob(jobID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return c.do(ctx, http.MethodPost, "/api/jobs/"+jobID+"/cancel", nil, map[string]any{"reason": "runpod canary"}, nil)
}

func (c *canary) drive(ctx context.Context, spec CanarySpec, prov cProvider) *CanaryReport {
	id := c.rep.JobID
	abortAfter := c.o.AbortAfter
	if abortAfter <= 0 {
		abortAfter = time.Duration(prov.ColdStartS+120) * time.Second
	}
	// a job that never ends is cancelled rather than waited for: its own limits plus ten minutes
	overall := abortAfter + time.Duration(prov.MaxRuntimeS+prov.KillSlackS)*time.Second + 10*time.Minute
	started := c.o.Clock.Now()
	meas := map[string]any{}
	c.rep.Measured = meas
	maxWorkers := 0
	lastSweep := time.Time{}
	var job cJob
	since := ""
	cancelled := false
	var seenGenerating bool
	hinted := false
	for {
		if err := ctx.Err(); err != nil {
			_ = c.cancelJob(id)
			return c.finish("aborted", ExitAborted, "interrupted; the job was cancelled")
		}
		var w struct {
			Job cJob `json:"job"`
		}
		q := fmt.Sprintf("/api/jobs/%s/wait?timeout_s=%d", id, c.o.WaitS)
		if since != "" {
			q += "&since_state=" + since
		}
		if err := c.do(ctx, http.MethodGet, q, nil, nil, &w); err != nil {
			_ = c.cancelJob(id)
			return c.finish("fail", ExitFail, "waiting for the job failed (%v); the job was cancelled", err)
		}
		job = w.Job
		since = job.State
		if now := c.o.Clock.Now(); lastSweep.IsZero() || now.Sub(lastSweep) >= c.o.SweepEvery {
			lastSweep = now
			var p cProvider
			if err := c.do(ctx, http.MethodGet, "/api/provider", nil, nil, &p); err == nil {
				maxWorkers = max(maxWorkers, p.live())
			}
		}
		if job.State == "generating" {
			seenGenerating = true
		}
		if job.terminal() {
			break
		}
		// terminate: cancel as soon as starting or generating is seen
		if spec.Case == CaseTerminate && !cancelled && (job.State == "starting" || job.State == "generating") {
			c.logf("job is %s: cancelling", job.State)
			meas["workers_at_cancel"] = c.liveWorkers(ctx)
			if err := c.cancelJob(id); err != nil {
				return c.finish("fail", ExitFail, "cancel failed: %v", err)
			}
			cancelled = true
			continue
		}
		if spec.Case == CaseKillSwitch && seenGenerating && !hinted {
			hinted = true
			c.logf("the job is generating: run `metamusic runpod provision --deactivate` now and watch what happens to it")
		}
		if !cancelled && !job.pastStarting() && c.o.Clock.Now().Sub(started) > abortAfter {
			_ = c.cancelJob(id)
			c.logf("the job was not generating within %s: cancelled. Run `metamusic runpod provision --deactivate` to stop the endpoint.", abortAfter)
			return c.finish("aborted", ExitAborted, "the job did not reach generating within %s (state %s); cancelled. Run `metamusic runpod provision --deactivate`", abortAfter, job.State)
		}
		if c.o.Clock.Now().Sub(started) > overall {
			_ = c.cancelJob(id)
			return c.finish("aborted", ExitAborted, "the job did not finish within %s; cancelled", overall)
		}
	}
	c.rep.FinalState = job.State
	if job.ErrorCode != nil {
		c.rep.ErrorCode = *job.ErrorCode
	}
	meas["max_workers_seen"] = maxWorkers
	if job.Timings.ColdStartS != nil {
		meas["cold_start_s"] = *job.Timings.ColdStartS
	}
	if job.Timings.ExecutionMS != nil {
		meas["execution_ms"] = *job.Timings.ExecutionMS
	}
	if cancelled {
		_ = c.sleep(ctx, c.o.AfterCancelWait)
		meas["workers_after_cancel_wait"] = c.liveWorkers(ctx)
		meas["cancel_wait_s"] = int(c.o.AfterCancelWait / time.Second)
	}

	c.assertOutcome(spec, prov, job, meas)
	c.waitIdle(ctx, prov)

	if !c.o.Keep && job.TrackID != nil && *job.TrackID != "" {
		if err := c.do(ctx, http.MethodPatch, "/api/tracks/"+*job.TrackID, nil, map[string]string{"status": "discarded"}, nil); err != nil {
			c.check("discard_track", false, "%v", err)
		}
	}
	for _, ck := range c.rep.Checks {
		if !ck.OK {
			return c.finish("fail", ExitFail, "check failed: %s", ck.Name)
		}
	}
	return c.finish("pass", ExitPass, "")
}

func (c *canary) liveWorkers(ctx context.Context) int {
	var p cProvider
	if err := c.do(ctx, http.MethodGet, "/api/provider", nil, nil, &p); err != nil {
		return -1
	}
	return p.live()
}

func (c *canary) assertOutcome(spec CanarySpec, prov cProvider, job cJob, meas map[string]any) {
	switch spec.Case {
	case CaseMain, CaseCotFull:
		ok := c.check("completed", job.State == "completed" && job.Outcome != nil && *job.Outcome == "clean", "state %s", job.State)
		if ok {
			allVerified := len(job.Artifacts) > 0
			var names []string
			for _, a := range job.Artifacts {
				allVerified = allVerified && a.Verified
				names = append(names, a.Name)
			}
			c.check("artifacts_verified", allVerified, "%d artifacts", len(job.Artifacts))
			if spec.Case == CaseCotFull {
				meas["artifact_names"] = names
			}
			if job.AudioS != nil {
				ms, ok := milliOf(*job.AudioS)
				// audio_s <= 1.5 x expected, in integers: 2 x audio <= 3 x expected
				c.check("audio_within_1_5x", ok && 2*ms <= 3*int64(spec.ExpectedAudioS)*1000, "audio %s s, limit %d.%d s", job.AudioS.String(), spec.ExpectedAudioS*3/2, spec.ExpectedAudioS*3%2*5)
				meas["audio_s"] = job.AudioS.String()
			} else {
				c.check("audio_within_1_5x", false, "no audio_s")
			}
		}
	case CaseTerminate:
		c.check("cancelled", job.State == "cancelled", "state %s", job.State)
	case CaseTimeout:
		deadline := job.State == "failed" && job.ErrorCode != nil && *job.ErrorCode == "DEADLINE"
		c.check("failed_deadline", deadline, "state %s", job.State)
		if job.Timings.ExecutionMS == nil {
			c.check("execution_time_window", false, "no execution time")
			break
		}
		ms := *job.Timings.ExecutionMS
		lo, hi := int64(30_000), int64(30+prov.KillSlackS)*1000
		if ms < lo && !deadline {
			c.check("provisioned_with_timeout_case", false, "endpoint not provisioned with --timeout-case: the worker stopped itself first")
			break
		}
		c.check("execution_time_window", ms >= lo && ms <= hi, "execution %d ms, window %d..%d ms", ms, lo, hi)
		meas["kill_latency_ms"] = ms - 30_000
	case CaseKillSwitch:
		meas["kill_switch_outcome"] = job.State
		c.check("terminal", job.terminal(), "state %s", job.State)
	}
}

// waitIdle polls until no worker is running or initializing, within idle_timeout_s + 30 s.
func (c *canary) waitIdle(ctx context.Context, prov cProvider) {
	idle := 20
	var p cProvider
	if err := c.do(ctx, http.MethodGet, "/api/provider", nil, nil, &p); err != nil {
		c.check("workers_stopped", false, "provider status unreadable")
		return
	}
	if p.Endpoint != nil && p.Endpoint.IdleTimeoutS != nil {
		idle = *p.Endpoint.IdleTimeoutS
	}
	deadline := c.o.Clock.Now().Add(time.Duration(idle+30) * time.Second)
	for {
		running := 0
		if p.Workers != nil {
			running = p.Workers.Running + p.Workers.Initializing
		}
		if running == 0 {
			c.check("workers_stopped", true, "")
			return
		}
		if c.o.Clock.Now().After(deadline) {
			c.check("workers_stopped", false, "%d workers still running after idle_timeout_s + 30 s", running)
			return
		}
		if err := c.sleep(ctx, c.o.SweepEvery); err != nil {
			c.check("workers_stopped", false, "interrupted")
			return
		}
		if err := c.do(ctx, http.MethodGet, "/api/provider", nil, nil, &p); err != nil {
			c.check("workers_stopped", false, "provider status unreadable")
			return
		}
	}
}

// VerifyBilling reads a finished canary batch after MM_BILLING_LAG: reconciled, billed <= reserved, the
// ratio billed / (list price x metered worker-seconds), and the same ratio without the idle tail.
func VerifyBilling(ctx context.Context, o CanaryOptions, batchID string) (*CanaryReport, error) {
	if o.Clock == nil {
		o.Clock = clock.Real
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 60 * time.Second}
	}
	if o.Token == "" {
		return nil, errors.New("canary: MM_TOKEN is not set")
	}
	hc := *o.HTTP
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c := &canary{o: o, hc: &hc, rep: &CanaryReport{Case: "verify-billing", BatchID: batchID}}
	var b cBatch
	if err := c.do(ctx, http.MethodGet, "/api/batches/"+url.PathEscape(batchID), nil, nil, &b); err != nil {
		return nil, err
	}
	var p cProvider
	if err := c.do(ctx, http.MethodGet, "/api/provider", nil, nil, &p); err != nil {
		return nil, err
	}
	v := &CanaryBillingView{Reconciled: b.Cost.Reconciled, SettledMicroUSD: b.Cost.SettledMicroUSD, ReservedMicroUSD: b.Cost.ReservedMicroUSD,
		BilledLEReserved: b.Cost.SettledMicroUSD <= b.Cost.ReservedMicroUSD}
	if p.Price != nil && p.Meter != nil && p.Price.PerHourMicroUSD > 0 && p.Meter.BilledSeconds24h > 0 {
		secs := p.Meter.BilledSeconds24h
		v.MeteredSeconds = &secs
		list := money.ForSeconds(secs, money.MicroUSD(p.Price.PerHourMicroUSD))
		if list > 0 {
			r := b.Cost.SettledMicroUSD * 1000 / int64(list)
			v.RatioMilli = &r
		}
		if p.Endpoint != nil && p.Endpoint.IdleTimeoutS != nil {
			idle := *p.Endpoint.IdleTimeoutS
			v.IdleSeconds = &idle
			if noIdle := money.ForSeconds(max(secs-int64(idle), 1), money.MicroUSD(p.Price.PerHourMicroUSD)); noIdle > 0 {
				r := b.Cost.SettledMicroUSD * 1000 / int64(noIdle)
				v.RatioNoIdleMilli = &r
				if v.RatioMilli != nil {
					v.IdleTailBilledHnt = "compare the two ratios: the one closer to 1000 shows whether the idle tail is billed"
				}
			}
		}
	}
	c.rep.Billing = v
	c.check("reconciled", v.Reconciled, "")
	c.check("billed_le_reserved", v.BilledLEReserved, "settled %d reserved %d", v.SettledMicroUSD, v.ReservedMicroUSD)
	for _, ck := range c.rep.Checks {
		if !ck.OK {
			return c.finish("fail", ExitFail, "check failed: %s", ck.Name), nil
		}
	}
	return c.finish("pass", ExitPass, ""), nil
}

// milliOf converts a non-negative decimal JSON number to thousandths without floating point.
func milliOf(n json.Number) (int64, bool) {
	whole, frac, _ := strings.Cut(n.String(), ".")
	if whole == "" || strings.ContainsAny(n.String(), "eE-+") {
		return 0, false
	}
	frac = (frac + "000")[:3]
	w, err1 := strconv.ParseInt(whole, 10, 64)
	f, err2 := strconv.ParseInt(frac, 10, 64)
	if err1 != nil || err2 != nil || w > 1<<40 {
		return 0, false
	}
	return w*1000 + f, true
}
