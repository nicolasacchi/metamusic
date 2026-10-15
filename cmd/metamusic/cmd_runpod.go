package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"uuid"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// runpodUsage is the help of the runpod subcommands (main.go lists `runpod` and defers here).
const runpodUsage = `usage: metamusic runpod <command>

  probe [--schema] [--keys] [--out FILE]      compare the public API spec with the snapshot; classify the keys
  provision [--admin-env FILE] [--image REF@sha256:...] [--activate [--max 1|2] | --deactivate]
            [--pools AMPERE_24[,ADA_24]] [--vae-source image|cached|volume:PATH] [--timeout-case] [--dry-run]
                                              create or reconcile the endpoint (the only command that reads the admin key)
  status [--json] [--job ID [--repoll]]       read-only: endpoint, workers, health, armed/trip state, billing, meter
  arm --endpoint-name N --reason TEXT [--after-restore]
  disarm --reason TEXT
  reset-trip --reason TEXT
  canary --url URL [--max-usd 0.25] [--case main|cot-full|terminate|timeout|kill-switch]
         [--verify-billing BATCH] [--keep] [--start-timeout 12m]
                                              scripted agent over REST with MM_TOKEN (read + write + generate)

canary --start-timeout: how long the job may take to start generating (default 12m). The first cold start
includes a one-time image pull of about 7.7 GB, which RunPod does not bill. When the window expires the
canary cancels the job and waits for the workers to reach 0 (or prints the manual command) before exiting.

canary exit codes: 0 pass, 1 fail, 2 usage, 3 refused (cost or disarmed), 4 aborted
`

// maxReasonRunes is the C69 bound on provider_state.reason and trip reset reasons.
const maxReasonRunes = 500

// cmdRunPod dispatches `metamusic runpod ...`.
func cmdRunPod(ctx context.Context, args []string, getenv func(string) string, s streams) error {
	if len(args) == 0 {
		return usageError("runpod: expected probe|provision|status|arm|disarm|reset-trip|canary\n\n" + runpodUsage)
	}
	rest := args[1:]
	switch args[0] {
	case "probe":
		return cmdRunPodProbe(ctx, rest, getenv, s)
	case "provision":
		return cmdRunPodProvision(ctx, rest, getenv, s)
	case "status":
		return cmdRunPodStatus(ctx, rest, getenv, s)
	case "arm":
		return cmdRunPodArm(ctx, rest, getenv, s)
	case "disarm":
		return cmdRunPodDisarm(ctx, rest, getenv, s)
	case "reset-trip":
		return cmdRunPodResetTrip(ctx, rest, getenv, s)
	case "canary":
		return cmdRunPodCanary(ctx, rest, getenv, s)
	case "help", "-h", "--help":
		fmt.Fprint(s.out, runpodUsage)
		return nil
	}
	return usageError(fmt.Sprintf("runpod: unknown subcommand %q\n\n%s", args[0], runpodUsage))
}

// ---- shared setup ------------------------------------------------------------------------------------------

// runpodBases returns the management and jobs base URLs. MM_RUNPOD_BASE_URL points both at one server
// (tests, runpodtest); it must be https or a loopback http URL so that a key can never be sent in clear
// text to a remote host.
func runpodBases(getenv func(string) string) (mgmt, jobs string, err error) {
	v := getenv("MM_RUNPOD_BASE_URL")
	if v == "" {
		return runpod.DefaultMgmtBase, runpod.DefaultJobsBase, nil
	}
	u, perr := url.Parse(v)
	if perr != nil || u.Host == "" {
		return "", "", errors.New("MM_RUNPOD_BASE_URL must be an absolute URL")
	}
	if u.Scheme != "https" {
		ip, aerr := netip.ParseAddr(u.Hostname())
		if u.Scheme != "http" || (u.Hostname() != "localhost" && (aerr != nil || !ip.IsLoopback())) {
			return "", "", errors.New("MM_RUNPOD_BASE_URL must be https (http only for loopback)")
		}
	}
	return v, v, nil
}

type runpodEnv struct {
	cfg      config.Config
	keys     config.RunPodKeys
	haveKeys bool
	mgmt     string
	jobs     string
}

func loadRunPod(getenv func(string) string) (*runpodEnv, error) {
	return loadRunPodWith(getenv, config.LoadRunPodKeys)
}

// loadRunPodWith is loadRunPod with the runpod.env loader chosen by the caller (provision tolerates a
// missing RUNPOD_ENDPOINT_ID).
func loadRunPodWith(getenv func(string) string, load func(string) (config.RunPodKeys, bool, error)) (*runpodEnv, error) {
	cfg, err := config.LoadOffline(getenv)
	if err != nil {
		return nil, err
	}
	mgmt, jobs, err := runpodBases(getenv)
	if err != nil {
		return nil, err
	}
	keys, ok, err := load(cfg.SecretsDir)
	if err != nil {
		return nil, err
	}
	return &runpodEnv{cfg: cfg, keys: keys, haveKeys: ok, mgmt: mgmt, jobs: jobs}, nil
}

func (e *runpodEnv) requireKeys() error {
	if !e.haveKeys {
		return errors.New("runpod.env was not found in the secrets directory (keys_absent)")
	}
	return nil
}

// client builds the RunPod client. admin is non-nil only for `provision`.
func (e *runpodEnv) client(admin *config.AdminKey) (*runpod.Client, *runpod.FileRecorder, error) {
	o := runpod.ClientOptions{MgmtBase: e.mgmt, JobsBase: e.jobs, Keys: e.keys, Admin: admin, UserAgent: runpod.UserAgentFor(version)}
	var rec *runpod.FileRecorder
	if e.cfg.RunPodRecordDir != "" {
		secrets := []string{e.keys.Jobs, e.keys.Read, e.keys.Write}
		if admin != nil {
			secrets = append(secrets, admin.Value())
		}
		r, err := runpod.NewFileRecorder(e.cfg.RunPodRecordDir, e.keys.EndpointID, secrets...)
		if err != nil {
			return nil, nil, err
		}
		rec, o.Recorder = r, r
	}
	return runpod.NewClient(o), rec, nil
}

func openExistingDB(ctx context.Context, cfg config.Config) (*store.Store, error) {
	p := filepath.Join(cfg.DataDir, "metamusic.db")
	if _, err := os.Stat(p); err != nil {
		return nil, errors.New("runpod: no database in the data directory")
	}
	return store.Open(ctx, p)
}

func checkReason(r string) error {
	if r = strings.TrimSpace(r); r == "" || utf8.RuneCountInString(r) > maxReasonRunes {
		return usageError("--reason is required (1 to 500 characters)")
	}
	return nil
}

// auditCLI appends the cli:* audit row of an action (principal system:cli, transport cli, D-M3-22).
func auditCLI(ctx context.Context, st *store.Store, op string, details any, dur time.Duration, failed bool) error {
	e, err := audit.SystemEntry(op, time.Now(), details, dur, 0, failed)
	if err != nil {
		return err
	}
	return audit.NewStore(st, func() string { return uuid.NewV7().String() }).Insert(ctx, e)
}

func printJSON(s streams, v any) error {
	enc := json.NewEncoder(s.out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// ---- probe ---------------------------------------------------------------------------------------------------

const defaultCapabilitiesPath = "internal/provider/runpod/key-capabilities.json"

func cmdRunPodProbe(ctx context.Context, args []string, getenv func(string) string, s streams) error {
	fs := newFlags("runpod probe", s)
	schema := fs.Bool("schema", false, "compare the public API spec with the committed snapshot (no keys)")
	keys := fs.Bool("keys", false, "classify the keys from runpod.env (spend-free)")
	out := fs.String("out", "", "write key-capabilities.json to `FILE` (default with --keys: "+defaultCapabilitiesPath+")")
	specURL := fs.String("spec-url", runpod.PublicSpecURL, "the public OpenAPI document (tests)")
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if fs.NArg() > 0 {
		return usageError("runpod probe: unexpected argument " + fs.Arg(0))
	}
	if !*schema && !*keys {
		return usageError("runpod probe: give --schema and/or --keys")
	}
	var sr *runpod.SchemaReport
	failed := false
	if *schema {
		live, err := runpod.FetchSpec(ctx, nil, *specURL)
		if err != nil {
			return fatal(err)
		}
		r, err := runpod.CompareSchema(runpod.Snapshot(), live)
		if err != nil {
			return fatal(err)
		}
		sr = &r
		failed = r.Drift || !r.OK
	}
	var kr *runpod.KeysReport
	endpointID := ""
	if *keys {
		e, err := loadRunPod(getenv)
		if err != nil {
			return fatal(err)
		}
		if err := e.requireKeys(); err != nil {
			return fatal(err)
		}
		endpointID = e.keys.EndpointID
		cl, _, err := e.client(nil) // the admin key is never loaded here
		if err != nil {
			return fatal(err)
		}
		kr, err = runpod.ProbeKeys(ctx, runpod.ProbeOptions{Client: cl, Keys: e.keys})
		if err != nil {
			if errors.Is(err, runpod.ErrProbeCritical) {
				return exitError{code: 1, err: err}
			}
			return fatal(err)
		}
	}
	caps := runpod.BuildCapabilities(time.Now(), sr, kr)
	raw, err := json.MarshalIndent(caps, "", "  ")
	if err != nil {
		return fatal(err)
	}
	raw = append(raw, '\n')
	if err := runpod.CheckFileSafe(raw, endpointID); err != nil {
		return fatal(err)
	}
	dest := *out
	if dest == "" && *keys {
		dest = defaultCapabilitiesPath
	}
	if dest != "" {
		if err := os.WriteFile(dest, raw, 0o600); err != nil {
			return fatal(errors.New("runpod probe: cannot write the output file"))
		}
		fmt.Fprintf(s.err, "wrote %s\n", dest)
	}
	_, _ = s.out.Write(raw)
	if failed {
		return exitError{code: 1}
	}
	return nil
}

// ---- provision -----------------------------------------------------------------------------------------------

func cmdRunPodProvision(ctx context.Context, args []string, getenv func(string) string, s streams) error {
	fs := newFlags("runpod provision", s)
	adminEnv := fs.String("admin-env", "", "the all-access key file (default: <secrets dir>/runpod-admin.env)")
	image := fs.String("image", "", "worker image `REF@sha256:DIGEST` (tags are refused)")
	activate := fs.Bool("activate", false, "set workers.max above zero")
	deactivate := fs.Bool("deactivate", false, "set workers.max to zero (the manual kill switch)")
	maxW := fs.Int("max", 0, "workers.max with --activate: 1 or 2 (default 2)")
	pools := fs.String("pools", "", "GPU pools, comma separated (default AMPERE_24,ADA_24)")
	vae := fs.String("vae-source", "image", "where the worker finds the VAE: image (baked into the worker image; default), cached (needs a second cached model, which RunPod does not offer) or volume:PATH")
	timeoutCase := fs.Bool("timeout-case", false, "add MM_WORKER_DEADLINE=provider (the canary's timeout case only)")
	dry := fs.Bool("dry-run", false, "print the diff and change nothing")
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if fs.NArg() > 0 {
		return usageError("runpod provision: unexpected argument " + fs.Arg(0))
	}
	switch {
	case *activate && *deactivate:
		return usageError("runpod provision: --activate and --deactivate exclude each other")
	case *maxW != 0 && !*activate:
		return usageError("runpod provision: --max needs --activate")
	case *maxW != 0 && *maxW != 1 && *maxW != 2:
		return usageError("runpod provision: --max must be 1 or 2")
	}
	workers := 0
	if *activate {
		workers = 2
		if *maxW != 0 {
			workers = *maxW
		}
	}
	e, err := loadRunPodWith(getenv, config.LoadRunPodKeysForProvision)
	if err != nil {
		return fatal(err)
	}
	path := *adminEnv
	if path == "" {
		path = filepath.Join(e.cfg.SecretsDir, "runpod-admin.env")
	}
	admin, err := config.LoadAdminKey(path) // the only place the admin key is read
	if err != nil {
		return fatal(err)
	}
	var st *store.Store
	if !*dry {
		if st, err = openExistingDB(ctx, e.cfg); err != nil {
			return fatal(fmt.Errorf("provision records an audit row: %w", err))
		}
		defer func() { _ = st.Close() }()
	}
	e.keys.EndpointID = "" // the endpoint is found by name
	cl, _, err := e.client(&admin)
	if err != nil {
		return fatal(err)
	}
	opts := runpod.ProvisionOptions{
		Desired: runpod.DesiredConfig{ColdStartS: e.cfg.ColdStartS, MaxRuntime: e.cfg.JobMaxRuntime, VAESource: *vae, TimeoutCase: *timeoutCase},
		Weights: yue2.Weights, Image: *image, MaxWorkers: workers, DryRun: *dry,
	}
	if *pools != "" {
		opts.Desired.Pools = strings.Split(*pools, ",")
	}
	started := time.Now()
	res, perr := runpod.Provision(ctx, cl, opts)
	if st != nil && (res.Applied || (perr != nil && (res.Action == "create" || res.Action == "patch"))) {
		d := map[string]any{"action": res.Action, "diff": res.Diff, "workers_max": res.WorkersMax, "timeout_case": *timeoutCase}
		if aerr := auditCLI(ctx, st, "cli:runpod-provision", d, time.Since(started), perr != nil); aerr != nil && perr == nil {
			perr = aerr
		}
	}
	printProvision(s, res, *dry)
	if perr != nil {
		return fatal(errors.New(cl.Redactor().Error(perr)))
	}
	if res.Action == "create" && res.Applied && res.EndpointID != "" {
		fmt.Fprintf(s.out, "endpoint id: %s\nappend this line to runpod.env: RUNPOD_ENDPOINT_ID=%s\n", res.EndpointID, res.EndpointID)
	}
	return nil
}

func printProvision(s streams, r runpod.ProvisionResult, dry bool) {
	mode := "applied"
	if dry {
		mode = "dry run: nothing changed"
	} else if !r.Applied {
		mode = "nothing applied"
	}
	if r.Action == "" {
		return
	}
	fmt.Fprintf(s.out, "endpoint %s: %s (%s), workers.max %d\n", r.Name, r.Action, mode, r.WorkersMax)
	if len(r.Diff) > 0 {
		fmt.Fprintf(s.out, "differs in: %s\n", strings.Join(r.Diff, ", "))
	}
	if len(r.Body) > 0 {
		var buf bytes.Buffer
		if json.Indent(&buf, r.Body, "", "  ") == nil {
			fmt.Fprintf(s.out, "%s\n", buf.String())
		}
	}
	for _, w := range r.Warnings {
		fmt.Fprintf(s.out, "warning: %s\n", w)
	}
	for i, st := range r.ConsoleSteps {
		fmt.Fprintf(s.out, "console step %d: %s\n", i+1, st)
	}
}

// ---- status --------------------------------------------------------------------------------------------------

type statusView struct {
	Backend       string         `json:"backend"`
	Database      string         `json:"database"`
	Armed         *bool          `json:"armed,omitempty"`
	ChangedBy     string         `json:"armed_changed_by,omitempty"`
	ChangedAt     string         `json:"armed_changed_at,omitempty"`
	Reason        string         `json:"reason,omitempty"`
	Trip          *tripView      `json:"trip"`
	LastReconcile string         `json:"last_reconcile,omitempty"`
	Billing       map[string]any `json:"billing,omitempty"`
	Meter         map[string]any `json:"meter,omitempty"`
	Endpoint      map[string]any `json:"endpoint,omitempty"`
	Workers       []workerView   `json:"workers,omitempty"`
	Health        map[string]any `json:"health,omitempty"`
	KeysAbsent    bool           `json:"keys_absent,omitempty"`
	Errors        []string       `json:"errors,omitempty"`
	Job           *jobStatusView `json:"job,omitempty"`
	// LiveAttempts is always present (empty when none, or when there is no database): the attempts in a
	// state that may still spend (pending, submitted, unknown). scripts/deploy-preflight.sh reads it.
	LiveAttempts []liveAttemptView `json:"live_attempts"`
}

type liveAttemptView struct {
	ID    string `json:"id"`
	JobID string `json:"job_id"`
	N     int    `json:"n"`
	State string `json:"state"`
}

type tripView struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
	At     string `json:"tripped_at"`
}

type workerView struct {
	Status  string `json:"status"`
	Stale   bool   `json:"stale,omitempty"`
	Uptime  *int   `json:"uptime_s,omitempty"`
	Started string `json:"started_at,omitempty"`
}

type jobStatusView struct {
	ID       string        `json:"id"`
	Events   []eventView   `json:"events"`
	Attempts []attemptView `json:"attempts"`
}

type eventView struct {
	At    string `json:"at"`
	From  string `json:"from,omitempty"`
	To    string `json:"to,omitempty"`
	Event string `json:"event"`
	Code  string `json:"code,omitempty"`
}

type attemptView struct {
	N           int    `json:"n"`
	State       string `json:"state"`
	Submitted   string `json:"submitted_at"`
	Announced   string `json:"announced_at,omitempty"`
	ExecutionMS *int64 `json:"execution_ms,omitempty"`
	Terminal    string `json:"terminal_at,omitempty"`
	Basis       string `json:"terminal_basis,omitempty"`
	Repoll      string `json:"repoll,omitempty"` // "http=200 status=COMPLETED" (--repoll only)
	hasRef      bool
	ref         string
}

// cmdRunPodStatus is read-only: it never POSTs or PATCHes. --repoll issues one GET /status per attempt ref.
func cmdRunPodStatus(ctx context.Context, args []string, getenv func(string) string, s streams) error {
	fs := newFlags("runpod status", s)
	asJSON := fs.Bool("json", false, "print JSON")
	jobID := fs.String("job", "", "also show the redacted events and attempt timings of job `ID`")
	repoll := fs.Bool("repoll", false, "with --job: GET /status once more for each attempt and print the HTTP code and status")
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if fs.NArg() > 0 {
		return usageError("runpod status: unexpected argument " + fs.Arg(0))
	}
	if *repoll && *jobID == "" {
		return usageError("runpod status: --repoll needs --job")
	}
	e, err := loadRunPod(getenv)
	if err != nil {
		return fatal(err)
	}
	v := statusView{Backend: provider.BackendRunPod, Database: "absent", KeysAbsent: !e.haveKeys, LiveAttempts: []liveAttemptView{}}
	var st *store.Store
	if _, serr := os.Stat(filepath.Join(e.cfg.DataDir, "metamusic.db")); serr == nil {
		if st, err = store.Open(ctx, filepath.Join(e.cfg.DataDir, "metamusic.db")); err != nil {
			return fatal(err)
		}
		defer func() { _ = st.Close() }()
		v.Database = "present"
		if err := fillStatusFromDB(ctx, st.R, &v, *jobID); err != nil {
			return fatal(err)
		}
	} else if *jobID != "" {
		return fatal(errors.New("runpod status: --job needs the database"))
	}
	if e.haveKeys {
		cl, _, err := e.client(nil)
		if err != nil {
			return fatal(err)
		}
		fillStatusFromProvider(ctx, cl, &v, *repoll)
	} else if *repoll {
		return fatal(errors.New("runpod status: --repoll needs runpod.env"))
	}
	if *asJSON {
		return printJSON(s, v)
	}
	printStatus(s, v)
	return nil
}

func fillStatusFromDB(ctx context.Context, db *sql.DB, v *statusView, jobID string) error {
	var armed int
	var by, at string
	var reason sql.NullString
	switch err := db.QueryRowContext(ctx, `SELECT armed, changed_by, changed_at, reason FROM provider_state WHERE backend = ?`, provider.BackendRunPod).
		Scan(&armed, &by, &at, &reason); {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return err
	default:
		a := armed == 1
		v.Armed, v.ChangedBy, v.ChangedAt, v.Reason = &a, by, at, reason.String
	}
	var t tripView
	switch err := db.QueryRowContext(ctx, `SELECT id, reason, tripped_at FROM provider_trips WHERE backend = ? AND reset_at IS NULL`, provider.BackendRunPod).
		Scan(&t.ID, &t.Reason, &t.At); {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return err
	default:
		v.Trip = &t
	}
	var last sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT MAX(at) FROM audit_log WHERE principal_id = 'system:reconciler'`).Scan(&last); err != nil {
		return err
	}
	v.LastReconcile = last.String
	var n, billed int64
	var end sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*), MAX(window_end), COALESCE(SUM(billed_micro_usd),0) FROM billing_windows`).Scan(&n, &end, &billed); err != nil {
		return err
	}
	v.Billing = map[string]any{"windows": n, "last_window_end": end.String, "billed": money.MicroUSD(billed).USD()}
	var lifetimes, secs int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(uptime_s),0) FROM worker_meter`).Scan(&lifetimes, &secs); err != nil {
		return err
	}
	v.Meter = map[string]any{"lifetimes": lifetimes, "uptime_s": secs}
	lrows, err := db.QueryContext(ctx, `SELECT id, job_id, n, state FROM job_attempts WHERE state IN ('pending','submitted','unknown') ORDER BY submitted_at, id`)
	if err != nil {
		return err
	}
	for lrows.Next() {
		var la liveAttemptView
		if err := lrows.Scan(&la.ID, &la.JobID, &la.N, &la.State); err != nil {
			_ = lrows.Close()
			return err
		}
		v.LiveAttempts = append(v.LiveAttempts, la)
	}
	if err := lrows.Close(); err != nil {
		return err
	}
	if jobID == "" {
		return nil
	}
	jv := &jobStatusView{ID: jobID, Events: []eventView{}, Attempts: []attemptView{}}
	var exists int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE id = ?`, jobID).Scan(&exists); err != nil || exists == 0 {
		return errors.New("runpod status: no such job")
	}
	rows, err := db.QueryContext(ctx, `SELECT at, COALESCE(from_state,''), COALESCE(to_state,''), event, COALESCE(code,'') FROM job_events WHERE job_id = ? ORDER BY id`, jobID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var ev eventView
		if err := rows.Scan(&ev.At, &ev.From, &ev.To, &ev.Event, &ev.Code); err != nil {
			_ = rows.Close()
			return err
		}
		jv.Events = append(jv.Events, ev)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	arows, err := db.QueryContext(ctx, `SELECT n, state, submitted_at, COALESCE(announced_at,''), execution_ms, COALESCE(terminal_at,''), COALESCE(terminal_basis,''), COALESCE(ref,'')
		FROM job_attempts WHERE job_id = ? ORDER BY n`, jobID)
	if err != nil {
		return err
	}
	defer arows.Close()
	for arows.Next() {
		var a attemptView
		var ms sql.NullInt64
		if err := arows.Scan(&a.N, &a.State, &a.Submitted, &a.Announced, &ms, &a.Terminal, &a.Basis, &a.ref); err != nil {
			return err
		}
		if ms.Valid {
			a.ExecutionMS = &ms.Int64
		}
		a.hasRef = a.ref != ""
		jv.Attempts = append(jv.Attempts, a)
	}
	v.Job = jv
	return arows.Err()
}

// fillStatusFromProvider reads the endpoint, workers and health with the read and jobs keys. Provider
// errors go through the client's redactor.
func fillStatusFromProvider(ctx context.Context, cl *runpod.Client, v *statusView, repoll bool) {
	note := func(what string, err error) { v.Errors = append(v.Errors, what+": "+cl.Redactor().Error(err)) }
	if ep, err := cl.GetEndpoint(ctx, runpod.KeyRead); err != nil {
		note("endpoint", err)
	} else {
		digest := ""
		if _, d, ok := strings.Cut(ep.Image, "@sha256:"); ok && len(d) >= 12 {
			digest = d[:12]
		}
		v.Endpoint = map[string]any{"name": ep.Name, "workers_min": ep.Workers.Min, "workers_max": ep.Workers.Max, "gpu_pools": ep.GPU.Pools,
			"flashboot": ep.Flashboot, "timeout_ms": ep.TimeoutMS, "image_digest_short": digest}
	}
	if ws, err := cl.Workers(ctx); err != nil {
		note("workers", err)
	} else {
		for _, w := range ws {
			wv := workerView{Status: w.Status, Stale: w.IsStale, Uptime: w.UptimeSeconds}
			if w.StartedAt != nil {
				wv.Started = w.StartedAt.UTC().Format(time.RFC3339)
			}
			v.Workers = append(v.Workers, wv)
		}
	}
	if h, err := cl.Health(ctx); err != nil {
		note("health", err)
	} else {
		v.Health = map[string]any{"jobs": h.Jobs, "workers": h.Workers}
	}
	if repoll && v.Job != nil {
		for i := range v.Job.Attempts {
			a := &v.Job.Attempts[i]
			if !a.hasRef {
				continue
			}
			st, err := cl.Status(ctx, a.ref)
			var re *runpod.Error
			switch {
			case err == nil:
				a.Repoll = "http=200 status=" + st.Status
			case errors.As(err, &re):
				a.Repoll = fmt.Sprintf("http=%d", re.Status)
			default:
				a.Repoll = "error"
			}
		}
	}
}

func printStatus(s streams, v statusView) {
	fmt.Fprintf(s.out, "backend:        %s\n", v.Backend)
	if v.Armed == nil {
		fmt.Fprintf(s.out, "armed:          no provider_state row (database %s)\n", v.Database)
	} else {
		fmt.Fprintf(s.out, "armed:          %v (changed by %s at %s)\n", *v.Armed, v.ChangedBy, v.ChangedAt)
	}
	if v.Trip != nil {
		fmt.Fprintf(s.out, "trip:           OPEN %s since %s: %s\n", v.Trip.ID, v.Trip.At, v.Trip.Reason)
	} else {
		fmt.Fprintf(s.out, "trip:           none\n")
	}
	if v.LastReconcile != "" {
		fmt.Fprintf(s.out, "last reconcile: %s\n", v.LastReconcile)
	}
	if v.KeysAbsent {
		fmt.Fprintf(s.out, "provider:       runpod.env not found (keys_absent); not asked\n")
	}
	if v.Endpoint != nil {
		fmt.Fprintf(s.out, "endpoint:       %v\n", v.Endpoint)
	}
	if len(v.Workers) > 0 || v.Endpoint != nil {
		fmt.Fprintf(s.out, "workers:        %d\n", len(v.Workers))
		for _, w := range v.Workers {
			fmt.Fprintf(s.out, "  - %s%s\n", w.Status, map[bool]string{true: " (stale)", false: ""}[w.Stale])
		}
	}
	if v.Health != nil {
		fmt.Fprintf(s.out, "health:         %v\n", v.Health)
	}
	if v.Billing != nil {
		fmt.Fprintf(s.out, "billing:        %v\n", v.Billing)
	}
	if v.Meter != nil {
		fmt.Fprintf(s.out, "meter:          %v\n", v.Meter)
	}
	for _, e := range v.Errors {
		fmt.Fprintf(s.out, "error:          %s\n", e)
	}
	if j := v.Job; j != nil {
		fmt.Fprintf(s.out, "job %s\n", j.ID)
		for _, ev := range j.Events {
			fmt.Fprintf(s.out, "  %s  %s -> %s  %s %s\n", ev.At, ev.From, ev.To, ev.Event, ev.Code)
		}
		for _, a := range j.Attempts {
			fmt.Fprintf(s.out, "  attempt %d: %s submitted %s announced %s terminal %s %s %s\n", a.N, a.State, a.Submitted, a.Announced, a.Terminal, a.Basis, a.Repoll)
		}
	}
}

// ---- arm, disarm, reset-trip ---------------------------------------------------------------------------------

func parseReasonFlags(name string, args []string, s streams, extra func(*flag.FlagSet)) (reason string, fs *flag.FlagSet, err error) {
	fs = newFlags("runpod "+name, s)
	r := fs.String("reason", "", "why (1 to 500 characters)")
	if extra != nil {
		extra(fs)
	}
	if err := fs.Parse(args); err != nil {
		return "", nil, usageError(err.Error())
	}
	if fs.NArg() > 0 {
		return "", nil, usageError("runpod " + name + ": unexpected argument " + fs.Arg(0))
	}
	if err := checkReason(*r); err != nil {
		return "", nil, err
	}
	return strings.TrimSpace(*r), fs, nil
}

func cmdRunPodArm(ctx context.Context, args []string, getenv func(string) string, s streams) error {
	var name *string
	var afterRestore *bool
	reason, _, err := parseReasonFlags("arm", args, s, func(fs *flag.FlagSet) {
		name = fs.String("endpoint-name", "", "the endpoint's name, typed to confirm (mm-yue2)")
		afterRestore = fs.Bool("after-restore", false, "acknowledge a restore when billing is not readable")
	})
	if err != nil {
		return err
	}
	if *name == "" {
		return usageError("runpod arm: --endpoint-name is required")
	}
	e, err := loadRunPod(getenv)
	if err != nil {
		return fatal(err)
	}
	if err := e.requireKeys(); err != nil {
		return exitError{code: 3, err: fmt.Errorf("refusing to arm: %w", err)}
	}
	st, err := openExistingDB(ctx, e.cfg)
	if err != nil {
		return fatal(err)
	}
	defer func() { _ = st.Close() }()
	gate := provider.NewGate(st.R, provider.GateConfig{Mode: provider.ModeRunPod, KeysLoaded: true, ColdStartS: e.cfg.ColdStartS,
		MaxRuntime: e.cfg.JobMaxRuntime}, time.Now)
	state, err := gate.State(ctx)
	if err != nil {
		return fatal(err)
	}
	if state.Quote == nil {
		return exitError{code: 3, err: errors.New("refusing to arm: no cached quote yet (the reconciler has not read the endpoint)")}
	}
	if state.Quote.Endpoint.Name != *name {
		return exitError{code: 3, err: errors.New("refusing to arm: --endpoint-name does not match the provisioned endpoint")}
	}
	if !state.Enabled {
		return exitError{code: 3, err: fmt.Errorf("refusing to arm: generation is disabled (%s)", state.Reason)}
	}
	if err := checkRestoreCovered(ctx, st.R, e, *afterRestore); err != nil {
		return exitError{code: 3, err: err}
	}
	started := time.Now()
	details := map[string]any{"armed": true, "reason": reason, "after_restore": *afterRestore, "prior_armed": state.Armed}
	if err := setArmed(ctx, st, true, reason, details, started); err != nil {
		return fatal(err)
	}
	fmt.Fprintln(s.out, "armed")
	return nil
}

// checkRestoreCovered refuses with RECONCILE_REQUIRED while a restore is not covered by a billing
// reconciliation (C76.7); with billing none, --after-restore is the explicit acknowledgment.
func checkRestoreCovered(ctx context.Context, db *sql.DB, e *runpodEnv, afterRestore bool) error {
	var restored sql.NullString
	err := db.QueryRowContext(ctx, `SELECT restored_at FROM provider_state WHERE backend = ?`, provider.BackendRunPod).Scan(&restored)
	if errors.Is(err, sql.ErrNoRows) || !restored.Valid {
		return nil
	}
	if err != nil {
		return err
	}
	var covered bool
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(window_end),'') >= ? FROM billing_windows`, restored.String).Scan(&covered); err != nil {
		return err
	}
	if covered {
		return nil
	}
	be := runpod.NewBackend(runpod.BackendOptions{Keys: e.keys, ProbeJSON: runpod.EmbeddedCapabilities, ColdStartS: e.cfg.ColdStartS, MaxRuntime: e.cfg.JobMaxRuntime})
	if be.Capabilities().Billing == "none" && afterRestore {
		return nil
	}
	return errors.New("refusing to arm: RECONCILE_REQUIRED (a restore is not yet covered by a billing reconciliation; with billing none use --after-restore after checking the account)")
}

func setArmed(ctx context.Context, st *store.Store, armed bool, reason string, details map[string]any, started time.Time) error {
	tx, err := st.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	a := 0
	if armed {
		a = 1
	}
	now := store.FormatTime(time.Now())
	res, err := tx.ExecContext(ctx, `UPDATE provider_state SET armed = ?, changed_by = 'system:cli', changed_at = ?, reason = ? WHERE backend = ?`, a, now, reason, provider.BackendRunPod)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO provider_state(backend, armed, changed_by, changed_at, reason) VALUES (?,?,'system:cli',?,?)`,
			provider.BackendRunPod, a, now, reason); err != nil {
			return err
		}
	}
	op := "cli:runpod-disarm"
	if armed {
		op = "cli:runpod-arm"
	}
	e, err := audit.SystemEntry(op, time.Now(), details, time.Since(started), 0, false)
	if err != nil {
		return err
	}
	if err := audit.InsertTx(ctx, tx, e); err != nil {
		return err
	}
	return tx.Commit()
}

func cmdRunPodDisarm(ctx context.Context, args []string, getenv func(string) string, s streams) error {
	reason, _, err := parseReasonFlags("disarm", args, s, nil)
	if err != nil {
		return err
	}
	cfg, err := config.LoadOffline(getenv)
	if err != nil {
		return fatal(err)
	}
	st, err := openExistingDB(ctx, cfg)
	if err != nil {
		return fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := setArmed(ctx, st, false, reason, map[string]any{"armed": false, "reason": reason}, time.Now()); err != nil {
		return fatal(err)
	}
	fmt.Fprintln(s.out, "disarmed")
	return nil
}

func cmdRunPodResetTrip(ctx context.Context, args []string, getenv func(string) string, s streams) error {
	reason, _, err := parseReasonFlags("reset-trip", args, s, nil)
	if err != nil {
		return err
	}
	cfg, err := config.LoadOffline(getenv)
	if err != nil {
		return fatal(err)
	}
	st, err := openExistingDB(ctx, cfg)
	if err != nil {
		return fatal(err)
	}
	defer func() { _ = st.Close() }()
	started := time.Now()
	tx, err := st.W.BeginTx(ctx, nil)
	if err != nil {
		return fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var id string
	switch err := tx.QueryRowContext(ctx, `SELECT id FROM provider_trips WHERE backend = ? AND reset_at IS NULL`, provider.BackendRunPod).Scan(&id); {
	case errors.Is(err, sql.ErrNoRows):
		fmt.Fprintln(s.out, "no open trip; nothing changed")
		return nil
	case err != nil:
		return fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE provider_trips SET reset_at = ?, reset_by = 'system:cli', reset_reason = ? WHERE id = ?`,
		store.FormatTime(time.Now()), reason, id); err != nil {
		return fatal(err)
	}
	e, err := audit.SystemEntry("cli:runpod-reset-trip", time.Now(), map[string]any{"trip_id": id, "reason": reason}, time.Since(started), 0, false)
	if err != nil {
		return fatal(err)
	}
	if err := audit.InsertTx(ctx, tx, e); err != nil {
		return fatal(err)
	}
	if err := tx.Commit(); err != nil {
		return fatal(err)
	}
	fmt.Fprintln(s.out, "trip reset")
	return nil
}

// ---- canary --------------------------------------------------------------------------------------------------

func cmdRunPodCanary(ctx context.Context, args []string, getenv func(string) string, s streams) error {
	fs := newFlags("runpod canary", s)
	urlFlag := fs.String("url", "", "the server's base `URL`")
	maxUSD := fs.String("max-usd", "0.25", "refuse when the estimate's ceiling exceeds this many dollars")
	cs := fs.String("case", "main", "main|cot-full|terminate|timeout|kill-switch")
	verify := fs.String("verify-billing", "", "after MM_BILLING_LAG: check the billing of batch `BATCH`")
	keep := fs.Bool("keep", false, "keep the canary takes (default: discard them)")
	startTimeout := fs.Duration("start-timeout", runpod.DefaultCanaryStartTimeout,
		"how long the job may take to start generating; the first cold start includes a one-time ~7.7 GB image pull (not billed by RunPod)")
	if err := fs.Parse(args); err != nil {
		return exitError{code: runpod.ExitUsage, err: errors.New(err.Error())}
	}
	usage := func(msg string) error {
		return exitError{code: runpod.ExitUsage, err: errors.New("runpod canary: " + msg)}
	}
	if fs.NArg() > 0 {
		return usage("unexpected argument " + fs.Arg(0))
	}
	if *urlFlag == "" {
		return usage("--url is required")
	}
	limit, err := money.ParseUSD(*maxUSD)
	if err != nil || limit <= 0 {
		return usage("--max-usd must be a positive dollar amount")
	}
	token := getenv("MM_TOKEN")
	if token == "" {
		return usage("MM_TOKEN is not set (a PAT with read + write + generate)")
	}
	if *startTimeout <= 0 {
		return usage("--start-timeout must be a positive duration")
	}
	o := runpod.CanaryOptions{BaseURL: *urlFlag, Token: token, MaxUSD: limit, Keep: *keep, Log: s.err, StartTimeout: *startTimeout}
	var rep *runpod.CanaryReport
	if *verify != "" {
		rep, err = runpod.VerifyBilling(ctx, o, *verify)
	} else {
		c, cerr := runpod.ParseCanaryCase(*cs)
		if cerr != nil {
			return usage(cerr.Error())
		}
		o.Case = c
		rep, err = runpod.RunCanary(ctx, o)
	}
	if err != nil {
		return exitError{code: runpod.ExitFail, err: errors.New(strings.ReplaceAll(err.Error(), token, "<token>"))}
	}
	if err := printJSON(s, rep); err != nil {
		return err
	}
	if rep.Exit != 0 {
		return exitError{code: rep.Exit}
	}
	return nil
}
