package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/objstore"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/upload/fallback"
)

// `metamusic upload fallback check` and `metamusic upload drill` are the operator's side of the upload fallback (BULK-R2, C399.8). They
// are CLI only: no API operation, MCP tool or web route, so an agent can neither probe the bucket nor take an upload route down.
// Neither prints a URL, key or credential; `check` spends nothing and `drill` makes no network call.

const uploadUsage = `usage: metamusic upload <command>

  fallback check                  presigned PUT, HEAD, GET, DELETE of a 1 KiB random object in the configured bucket and the
                                  clock skew; spend-free. Needs <MM_SECRETS_DIR>/r2.env (exit 3 when it is absent)
  drill --attempt ID --seconds N --reason TEXT
                                  for N seconds (30..600) the primary upload route answers 503 UPLOAD_DRILL for that attempt only,
                                  so its worker falls back to the bucket; audited. The attempt must have fallback objects
`

func cmdUpload(ctx context.Context, args []string, getenv func(string) string, s streams) error {
	if len(args) == 0 {
		return usageError("upload: expected fallback|drill\n\n" + uploadUsage)
	}
	switch args[0] {
	case "fallback":
		if len(args) < 2 || args[1] != "check" {
			return usageError("upload fallback: expected check\n\n" + uploadUsage)
		}
		return cmdUploadFallbackCheck(ctx, args[2:], getenv, s)
	case "drill":
		return cmdUploadDrill(ctx, args[1:], getenv, s)
	case "help", "-h", "--help":
		fmt.Fprint(s.out, uploadUsage)
		return nil
	}
	return usageError(fmt.Sprintf("upload: unknown subcommand %q\n\n%s", args[0], uploadUsage))
}

// checkHTTPTimeout bounds every request of the self-check.
const checkHTTPTimeout = 30 * time.Second

// fallbackClient builds the bucket client from <secrets>/r2.env (ok is false when the file is absent). It is a variable so that the
// tests can point the command at an in-process bucket, whose endpoint r2.env would refuse (https only).
var fallbackClient = func(secretsDir string) (fallback.Objects, *http.Client, string, bool, error) {
	r2, ok, err := config.LoadUploadFallback(secretsDir)
	if err != nil || !ok {
		return nil, nil, "", false, err
	}
	hc := &http.Client{Timeout: checkHTTPTimeout}
	client, err := objstore.New(objstore.Config{Endpoint: r2.Endpoint, Bucket: r2.Bucket, Region: r2.Region, AccessKeyID: r2.AccessKeyID, SecretAccessKey: r2.SecretAccessKey}, hc, time.Now)
	if err != nil {
		return nil, nil, "", false, err
	}
	return client, hc, r2.Prefix, true, nil
}

func cmdUploadFallbackCheck(ctx context.Context, args []string, getenv func(string) string, s streams) error {
	fs := newFlags("upload fallback check", s)
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if fs.NArg() > 0 {
		return usageError("upload fallback check: unexpected argument " + fs.Arg(0))
	}
	cfg, err := config.LoadOffline(getenv)
	if err != nil {
		return fatal(err)
	}
	client, hc, prefix, ok, err := fallbackClient(cfg.SecretsDir)
	if err != nil {
		return fatal(err)
	}
	if !ok {
		return exitError{code: 3, err: errors.New("upload fallback: not configured (no r2.env in the secrets directory)")}
	}
	steps, err := fallback.SelfCheck(ctx, client, hc, prefix, time.Now)
	for _, st := range steps {
		mark := "ok  "
		if !st.OK {
			mark = "FAIL"
		}
		fmt.Fprintf(s.out, "%s %-18s %s\n", mark, st.Name, st.Note)
	}
	if err != nil {
		return exitError{code: 1, err: errors.New("upload fallback check failed")}
	}
	fmt.Fprintln(s.out, "upload fallback check passed")
	return nil
}

func cmdUploadDrill(ctx context.Context, args []string, getenv func(string) string, s streams) error {
	var attempt, reason string
	var seconds int
	fs := newFlags("upload drill", s)
	fs.StringVar(&attempt, "attempt", "", "the attempt id whose primary upload route answers 503")
	fs.IntVar(&seconds, "seconds", 0, fmt.Sprintf("how long (%d..%d)", fallback.MinDrillS, fallback.MaxDrillS))
	fs.StringVar(&reason, "reason", "", "why (1 to 500 characters)")
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if fs.NArg() > 0 {
		return usageError("upload drill: unexpected argument " + fs.Arg(0))
	}
	if strings.TrimSpace(attempt) == "" {
		return usageError("upload drill: --attempt is required")
	}
	if seconds < fallback.MinDrillS || seconds > fallback.MaxDrillS {
		return usageError(fmt.Sprintf("upload drill: --seconds must be %d..%d", fallback.MinDrillS, fallback.MaxDrillS))
	}
	reason = strings.TrimSpace(reason)
	if err := checkReason(reason); err != nil {
		return err
	}
	st, err := openContractDB(ctx, getenv)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	until, err := fallback.RecordDrill(ctx, st, clock.Real, strings.TrimSpace(attempt), seconds, reason, "system:cli")
	switch {
	case errors.Is(err, fallback.ErrNoFallbackObjects):
		return exitError{code: 3, err: errors.New("upload drill: the attempt has no fallback objects (it was not sent fallback URLs, or it does not exist)")}
	case err != nil:
		return fatal(err)
	}
	fmt.Fprintf(s.out, "drill: the primary upload route answers 503 for this attempt until %s\n", until.UTC().Format(time.RFC3339))
	return nil
}

// dbFallbackHooks is runpod.FallbackHooks over the app host's own database: the fallback canary case runs on the host, because the
// drill row and the receipts are not on the REST surface.
type dbFallbackHooks struct {
	st      *store.Store
	needles []string // what must never appear in a job's events: markers of a presigned URL and the bucket credentials
}

// newFallbackHooks opens the database of the data directory for the fallback canary case.
func newFallbackHooks(ctx context.Context, getenv func(string) string) (*dbFallbackHooks, func(), error) {
	cfg, err := config.LoadOffline(getenv)
	if err != nil {
		return nil, nil, fatal(err)
	}
	st, err := openExistingDB(ctx, cfg)
	if err != nil {
		return nil, nil, fatal(err)
	}
	needles := []string{"X-Amz-", "Signature="}
	if r2, ok, err := config.LoadUploadFallback(cfg.SecretsDir); err == nil && ok {
		needles = append(needles, r2.AccessKeyID, r2.SecretAccessKey)
	}
	return &dbFallbackHooks{st: st, needles: needles}, func() { _ = st.Close() }, nil
}

func (h *dbFallbackHooks) Drill(ctx context.Context, jobID string, seconds int) error {
	var attempt string
	err := h.st.R.QueryRowContext(ctx, `SELECT a.id FROM job_attempts a WHERE a.job_id = ? AND EXISTS (SELECT 1 FROM fallback_objects o WHERE o.attempt_id = a.id)
		ORDER BY a.n DESC LIMIT 1`, jobID).Scan(&attempt)
	if err != nil {
		return fallback.ErrNoFallbackObjects
	}
	_, err = fallback.RecordDrill(ctx, h.st, clock.Real, attempt, seconds, "runpod canary: fallback case", "system:cli")
	return err
}

func (h *dbFallbackHooks) Receipts(ctx context.Context, jobID string) (via, total int, err error) {
	err = h.st.R.QueryRowContext(ctx, `SELECT COALESCE(SUM(via = 'fallback'), 0), COUNT(*) FROM uploads WHERE job_id = ? AND state IN ('received','consumed')`, jobID).Scan(&via, &total)
	return via, total, err
}

func (h *dbFallbackHooks) ObjectsPending(ctx context.Context, jobID string) (n int, err error) {
	err = h.st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM fallback_objects o JOIN job_attempts a ON a.id = o.attempt_id WHERE a.job_id = ? AND o.deleted_at IS NULL`, jobID).Scan(&n)
	return n, err
}

func (h *dbFallbackHooks) Leaks(ctx context.Context, jobID string) (bool, error) {
	rows, err := h.st.R.QueryContext(ctx, `SELECT COALESCE(detail_json, '') FROM job_events WHERE job_id = ?`, jobID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return false, err
		}
		for _, n := range h.needles {
			if n != "" && strings.Contains(d, n) {
				return true, nil
			}
		}
	}
	return false, rows.Err()
}
