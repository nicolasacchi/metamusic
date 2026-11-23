package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/jobs"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/pods"
	"github.com/nicolasacchi/metamusic/internal/redact"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// The pod backend's operator commands (BULK-P7, C399.11, C399.12): `runpod probe --pods` and `--backend runpod-pod` on status, arm,
// disarm and reset-trip. They are operator-only on purpose: no API operation, no MCP tool, no SPA route. The pod key is read from the
// secrets directory by the pods client at every call and is never printed, logged or put in an error or an audit row.

// opProbePods is the audit row of `runpod probe --pods`.
const opProbePods = "cli:runpod-probe-pods"

func init() { audit.AddSystemOps(opProbePods) }

// podBackendFlag is the --backend value of the pod commands.
const podBackendFlag = provider.BackendRunPodPod

func (g rpEngine) isPod() bool { return g.backend == podBackendFlag }

// parseTarget resolves --engine and --backend: --backend runpod-pod selects the pod backend (its engine is always yue2, so --engine
// must be absent or yue2). Any other --backend value is a usage error: the serverless backends are selected by --engine.
func parseTarget(engine, backend string) (rpEngine, error) {
	switch backend {
	case "":
		return parseEngine(engine)
	case podBackendFlag:
		if engine != "" && engine != "yue2" {
			return rpEngine{}, usageError("--backend " + podBackendFlag + " runs the yue2 engine: do not combine it with --engine " + engine)
		}
		return rpEngine{name: "yue2", backend: podBackendFlag}, nil
	}
	return rpEngine{}, usageError("--backend must be " + podBackendFlag + " (the serverless backends are selected by --engine)")
}

// podKeyPresent reports whether the pod key file is there now (a refused file is an error naming the file, never a value).
func podKeyPresent(cfg config.Config) (bool, error) {
	_, ok, err := config.LoadPodKey(cfg.SecretsDir)
	return ok, err
}

// podClient builds the pod API client of the secrets directory. MM_RUNPOD_BASE_URL (https, or loopback http) points it at a test server.
func podClient(cfg config.Config, getenv func(string) string) (*pods.Client, error) {
	mgmt, _, err := runpodBases(getenv)
	if err != nil {
		return nil, err
	}
	return pods.NewClient(pods.Options{BaseURL: mgmt, KeyDir: cfg.SecretsDir, UserAgent: pods.UserAgentFor(version)}), nil
}

// ---- probe --pods ------------------------------------------------------------------------------------------------

// probeReport is what `runpod probe --pods` prints.
type probeReport struct {
	Verdict          string         `json:"verdict"`
	AcceptedBroadKey bool           `json:"accepted_broad_key"`
	Detail           map[string]any `json:"detail"`
}

// cmdRunPodProbePods is spend-free and read-only: it lists pods, reads the catalog prices of MM_POD_GPU_TYPES, one hour of pod billing
// and GET /v2/serverless (a correctly scoped key is refused there), and appends a `probe` row to pod_safety_log. It never creates,
// starts or stops a pod. A key that can read serverless endpoints is `too_broad`: the row is still written (arming needs it), but the
// command exits 3 unless --accept-broad-pod-key is given; a failed probe exits 1.
func cmdRunPodProbePods(ctx context.Context, acceptBroad bool, getenv func(string) string, s streams) error {
	cfg, err := config.LoadOffline(getenv)
	if err != nil {
		return fatal(err)
	}
	if len(cfg.PodGPUTypes) == 0 {
		return usageError("runpod probe --pods: MM_POD_GPU_TYPES is not set (the catalog prices of those types are read)")
	}
	present, err := podKeyPresent(cfg)
	if err != nil {
		return exitError{code: 3, err: err}
	}
	if !present {
		return exitError{code: 3, err: errors.New("runpod probe --pods: runpod-pod.env was not found in the secrets directory (keys_absent)")}
	}
	cl, err := podClient(cfg, getenv)
	if err != nil {
		return fatal(err)
	}
	st, err := openExistingDB(ctx, cfg)
	if err != nil {
		return fatal(err)
	}
	defer func() { _ = st.Close() }()
	started := time.Now()
	res := pods.RunProbe(ctx, cl, cfg.PodGPUTypes, started)
	if err := pods.AppendSafety(ctx, st.W, pods.SafetyEntry{Kind: "probe", Verdict: res.Verdict, CreatedBy: "system:cli", Detail: res.Detail, At: started}); err != nil {
		return fatal(fmt.Errorf("runpod probe --pods: the probe row could not be written: %s", redact.Error(err)))
	}
	accepted := acceptBroad && res.Verdict == pods.ProbeTooBroad
	if err := auditCLI(ctx, st, opProbePods, map[string]any{"verdict": res.Verdict, "accept_broad_pod_key": accepted, "billing_readable": res.Detail["billing_readable"]},
		time.Since(started), res.Verdict == pods.ProbeFailed); err != nil {
		return fatal(err)
	}
	if err := printJSON(s, probeReport{Verdict: res.Verdict, AcceptedBroadKey: accepted, Detail: res.Detail}); err != nil {
		return err
	}
	switch res.Verdict {
	case pods.ProbeFailed:
		return exitError{code: 1, err: errors.New("the pod key probe failed (see detail); nothing can be armed on it")}
	case pods.ProbeTooBroad:
		if !acceptBroad {
			return exitError{code: 3, err: errors.New("the pod key can read serverless endpoints (too_broad): replace it with a key limited to pods, catalog and billing, or accept it with --accept-broad-pod-key (arming needs the flag too)")}
		}
	}
	return nil
}

// ---- status --backend runpod-pod ---------------------------------------------------------------------------------

// podStatusView is the pods block of `runpod status --backend runpod-pod`: local facts and the key file, no network.
type podStatusView struct {
	KeyPresent          bool        `json:"key_present"`
	Probe               string      `json:"probe"`
	Safety              string      `json:"safety"`
	SafetyDigestMatches bool        `json:"safety_digest_matches"`
	Window              *podWindowV `json:"window,omitempty"`
	Live                []podLiveV  `json:"live"`
}

type podWindowV struct {
	ArmedAt      string `json:"armed_at"`
	AutoDisarmAt string `json:"auto_disarm_at"`
}

type podLiveV struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	State string `json:"state"`
	Batch string `json:"batch_id"`
}

// cmdRunPodStatusPod is the read-only status of the pod backend. live_pods counts every pod that is not terminated (any backend).
func cmdRunPodStatusPod(ctx context.Context, cfg config.Config, g rpEngine, asJSON bool, jobID string, s streams) error {
	v := statusView{Backend: g.backend, Database: "absent", LiveAttempts: []liveAttemptView{}}
	present, kerr := podKeyPresent(cfg)
	v.KeysAbsent = !present
	if kerr != nil {
		v.Errors = append(v.Errors, redact.Error(kerr))
	}
	pv := &podStatusView{KeyPresent: present, Probe: "missing", Safety: generation.PodSafetyUnverified, Live: []podLiveV{}}
	v.Pods = pv
	st, err := openExistingDB(ctx, cfg)
	switch {
	case err == nil:
		defer func() { _ = st.Close() }()
		v.Database = "present"
		if err := fillStatusFromDB(ctx, st.R, &v, jobID); err != nil {
			return fatal(err)
		}
		if err := fillPodStatus(ctx, st.R, cfg, &v, pv); err != nil {
			return fatal(err)
		}
	case jobID != "":
		return fatal(errors.New("runpod status: --job needs the database"))
	}
	if asJSON {
		return printJSON(s, v)
	}
	printStatus(s, v)
	fmt.Fprintf(s.out, "pod key:        %s\n", map[bool]string{true: "present", false: "absent"}[pv.KeyPresent])
	fmt.Fprintf(s.out, "pod probe:      %s\n", pv.Probe)
	fmt.Fprintf(s.out, "pod safety:     %s (image digest matches: %v)\n", pv.Safety, pv.SafetyDigestMatches)
	if pv.Window != nil {
		fmt.Fprintf(s.out, "pod window:     armed %s, auto-disarm %s\n", pv.Window.ArmedAt, pv.Window.AutoDisarmAt)
	}
	for _, p := range pv.Live {
		fmt.Fprintf(s.out, "  live pod %s (%s) %s batch %s\n", p.Name, p.Kind, p.State, p.Batch)
	}
	return nil
}

func fillPodStatus(ctx context.Context, db *sql.DB, cfg config.Config, v *statusView, pv *podStatusView) error {
	var verdict string
	switch err := db.QueryRowContext(ctx, `SELECT verdict FROM pod_safety_log WHERE kind = 'probe' ORDER BY id DESC LIMIT 1`).Scan(&verdict); {
	case err == nil:
		pv.Probe = verdict
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	digest := pods.ImageDigest(cfg.PodImage)
	safety, err := generation.PodSafety(ctx, db, digest)
	if err != nil {
		return err
	}
	pv.Safety = safety
	var latest sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT image_digest FROM pod_safety_log WHERE kind = 'canary' ORDER BY id DESC LIMIT 1`).Scan(&latest); err == nil {
		pv.SafetyDigestMatches = digest != "" && latest.String == digest
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if v.Armed != nil && *v.Armed && cfg.PodWindowMax > 0 {
		if at, perr := time.Parse(time.RFC3339Nano, v.ChangedAt); perr == nil {
			pv.Window = &podWindowV{ArmedAt: v.ChangedAt, AutoDisarmAt: store.FormatTime(at.Add(cfg.PodWindowMax))}
		}
	}
	rows, err := db.QueryContext(ctx, `SELECT name, kind, state, batch_id FROM pods WHERE state <> 'terminated' ORDER BY created_at, id LIMIT 20`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var p podLiveV
		if err := rows.Scan(&p.Name, &p.Kind, &p.State, &p.Batch); err != nil {
			return err
		}
		pv.Live = append(pv.Live, p)
	}
	return rows.Err()
}

// ---- arm --backend runpod-pod ------------------------------------------------------------------------------------

// refuseArm is the exit-3 refusal of an arm.
func refuseArm(format string, a ...any) error {
	return exitError{code: 3, err: fmt.Errorf("refusing to arm: "+format, a...)}
}

// cmdRunPodArmPod arms the pod backend for one bulk window (D-BULK-11). It refuses without the pod key, without a cached pod overhead
// quote, while generation is disabled, and without a usable probe row: `ok`, or `too_broad` with --accept-broad-pod-key. It needs no
// endpoint name (a pod backend has no endpoint). The window counts from changed_at, which a re-arm of an armed backend keeps.
func cmdRunPodArmPod(ctx context.Context, cfg config.Config, reason string, accept bool, s streams) error {
	present, err := podKeyPresent(cfg)
	if err != nil {
		return refuseArm("%s", redact.Error(err))
	}
	if !present {
		return refuseArm("runpod-pod.env was not found in the secrets directory (keys_absent)")
	}
	st, err := openExistingDB(ctx, cfg)
	if err != nil {
		return fatal(err)
	}
	defer func() { _ = st.Close() }()
	gate := provider.NewGate(st.R, provider.GateConfig{Mode: provider.ModeRunPod, Backend: podBackendFlag, Engine: "yue2", KeysLoaded: true,
		ColdStartS: cfg.PodBootS, MaxRuntime: cfg.PodMaxRuntime}, time.Now)
	state, err := gate.State(ctx)
	if err != nil {
		return fatal(err)
	}
	if state.Quote == nil {
		return refuseArm("no cached pod overhead quote yet (the server has not read the catalog prices: is MM_POD_BACKEND=1 and the server running?)")
	}
	if !state.Enabled {
		return refuseArm("generation is disabled (%s)", state.Reason)
	}
	var verdict string
	switch err := st.R.QueryRowContext(ctx, `SELECT verdict FROM pod_safety_log WHERE kind = 'probe' ORDER BY id DESC LIMIT 1`).Scan(&verdict); {
	case errors.Is(err, sql.ErrNoRows):
		return refuseArm("pod_probe_missing: run `metamusic runpod probe --pods` first")
	case err != nil:
		return fatal(err)
	}
	// the decision is the REST arm's own rule (jobs.CheckArmPod); the verdict only picks the words
	var gde *jobs.GenerationDisabledError
	if err := jobs.CheckArmPod(ctx, st.R, podBackendFlag, accept); errors.As(err, &gde) {
		if verdict == pods.ProbeTooBroad {
			return refuseArm("pod_probe_missing: the latest probe found the pod key too broad (it reads serverless endpoints); replace the key, or arm with --accept-broad-pod-key")
		}
		return refuseArm("pod_probe_missing: the latest probe %s; run `metamusic runpod probe --pods` again", verdict)
	} else if err != nil {
		return fatal(err)
	}
	var trips int
	if err := st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM provider_trips WHERE backend = ? AND reset_at IS NULL`, podBackendFlag).Scan(&trips); err != nil {
		return fatal(err)
	}
	if trips > 0 {
		return refuseArm("the backend has an unreset trip: run `metamusic runpod reset-trip --backend runpod-pod` first")
	}
	started := time.Now()
	details := map[string]any{"armed": true, "reason": reason, "backend": podBackendFlag, "probe": verdict, "accept_broad_pod_key": accept && verdict == pods.ProbeTooBroad,
		"prior_armed": state.Armed}
	if err := setArmedPod(ctx, st, reason, details, started); err != nil {
		return fatal(err)
	}
	fmt.Fprintln(s.out, "armed")
	return nil
}

// setArmedPod arms the pod backend like setArmed, except that re-arming an armed backend keeps changed_at: it is when the window opened,
// and MM_POD_WINDOW_MAX counts from it.
func setArmedPod(ctx context.Context, st *store.Store, reason string, details map[string]any, started time.Time) error {
	tx, err := st.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := store.FormatTime(time.Now())
	if _, err := tx.ExecContext(ctx, `INSERT INTO provider_state(backend, armed, changed_by, changed_at, reason) VALUES (?,1,'system:cli',?,?)
		ON CONFLICT(backend) DO UPDATE SET armed = 1, changed_by = 'system:cli', reason = excluded.reason,
		changed_at = CASE WHEN provider_state.armed = 1 THEN provider_state.changed_at ELSE excluded.changed_at END`, podBackendFlag, now, reason); err != nil {
		return err
	}
	e, err := audit.SystemEntry("cli:runpod-arm", time.Now(), details, time.Since(started), 0, false)
	if err != nil {
		return err
	}
	if err := audit.InsertTx(ctx, tx, e); err != nil {
		return err
	}
	return tx.Commit()
}

// addBackendFlag registers --backend on a command that also takes --engine.
func addBackendFlag(fs *flag.FlagSet) *string {
	return fs.String("backend", "", "operate the pod backend instead of a serverless engine: "+podBackendFlag)
}

// podOnlyFlagCheck refuses pod-only flags on a serverless target.
func podOnlyFlagCheck(g rpEngine, accept bool) error {
	if accept && !g.isPod() {
		return usageError("--accept-broad-pod-key applies to --backend " + podBackendFlag + " only")
	}
	return nil
}
