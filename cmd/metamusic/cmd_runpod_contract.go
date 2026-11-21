package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/jobs"
	"github.com/nicolasacchi/metamusic/internal/jobspec"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// `metamusic runpod contract status|open|close` is the operator's side of the worker capability gate (C322.6). It is a CLI only: no
// API operation, no MCP tool, no web action, so an agent can never open a capability. Every change is a worker_contract_log row and an
// audit row.

const (
	opContractOpen  = "cli:runpod-contract-open"
	opContractClose = "cli:runpod-contract-close"
)

func init() { audit.AddSystemOps(opContractOpen, opContractClose) }

const contractUsage = `  contract status [--backend B] [--json]      the open worker capabilities of a backend's image and the log row behind them
  contract open --backend B --caps v2,ears --reason TEXT
                                              open capabilities for the image digest of B's fresh quote (adds to those already open)
  contract close --backend B [--caps ...] --reason TEXT [--strand-queued]
                                              close the listed capabilities (default: all); refuses while queued jobs need them
                                              unless --strand-queued (they then fail WORKER_OUTDATED after an hour)
`

// contractView is what `contract status --json` prints.
type contractView struct {
	Backend     string   `json:"backend"`
	Caps        []string `json:"caps"`
	ImageDigest string   `json:"image_digest,omitempty"`
	QuoteDigest string   `json:"quote_image_digest,omitempty"`
	Via         string   `json:"via,omitempty"`
	ChangedBy   string   `json:"changed_by,omitempty"`
	ChangedAt   string   `json:"changed_at,omitempty"`
	Reason      string   `json:"reason,omitempty"`
	QueuedNeed  []string `json:"queued_jobs_needing_caps"`
}

func cmdRunPodContract(ctx context.Context, args []string, getenv func(string) string, s streams) error {
	if len(args) == 0 {
		return usageError("runpod contract: expected status|open|close\n\n" + contractUsage)
	}
	switch args[0] {
	case "status":
		return cmdContractStatus(ctx, args[1:], getenv, s)
	case "open":
		return cmdContractOpen(ctx, args[1:], getenv, s)
	case "close":
		return cmdContractClose(ctx, args[1:], getenv, s)
	}
	return usageError(fmt.Sprintf("runpod contract: unknown subcommand %q\n\n%s", args[0], contractUsage))
}

// contractFlags are the flags the three subcommands share.
type contractFlags struct {
	backend, caps, reason string
	strand, asJSON        bool
}

func parseContractFlags(name string, args []string, s streams, want ...string) (contractFlags, error) {
	var f contractFlags
	fs := newFlags("runpod contract "+name, s)
	fs.StringVar(&f.backend, "backend", provider.BackendRunPod, "the backend (provider_state key) whose worker image to gate")
	if slices.Contains(want, "caps") {
		fs.StringVar(&f.caps, "caps", "", "comma-separated capabilities: "+strings.Join(provider.Caps, ","))
	}
	if slices.Contains(want, "reason") {
		fs.StringVar(&f.reason, "reason", "", "why (1 to 500 characters)")
	}
	if slices.Contains(want, "strand") {
		fs.BoolVar(&f.strand, "strand-queued", false, "close even though queued jobs need the capabilities")
	}
	if slices.Contains(want, "json") {
		fs.BoolVar(&f.asJSON, "json", false, "print JSON")
	}
	if err := fs.Parse(args); err != nil {
		return f, usageError(err.Error())
	}
	if fs.NArg() > 0 {
		return f, usageError("runpod contract " + name + ": unexpected argument " + fs.Arg(0))
	}
	if strings.TrimSpace(f.backend) == "" {
		return f, usageError("runpod contract " + name + ": --backend is required")
	}
	f.reason = strings.TrimSpace(f.reason)
	return f, nil
}

func splitCaps(v string) []string {
	var out []string
	for _, c := range strings.Split(v, ",") {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}

func openContractDB(ctx context.Context, getenv func(string) string) (*store.Store, error) {
	cfg, err := config.LoadOffline(getenv)
	if err != nil {
		return nil, fatal(err)
	}
	st, err := openExistingDB(ctx, cfg)
	if err != nil {
		return nil, fatal(err)
	}
	return st, nil
}

// queuedNeeding lists the queued jobs of a backend whose needs include any of caps.
func queuedNeeding(ctx context.Context, db interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}, backend string, caps []string) ([]string, error) {
	if len(caps) == 0 {
		return []string{}, nil
	}
	raw := `["` + strings.Join(caps, `","`) + `"]` // caps are vocabulary words, never user text
	rows, err := db.QueryContext(ctx, `SELECT j.id FROM jobs j WHERE j.state = 'queued' AND j.backend = ?
		AND EXISTS (SELECT 1 FROM json_each(j.needs_json) n WHERE n.value IN (SELECT value FROM json_each(?))) ORDER BY j.created_at, j.take_index`, backend, raw)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func cmdContractStatus(ctx context.Context, args []string, getenv func(string) string, s streams) error {
	f, err := parseContractFlags("status", args, s, "json")
	if err != nil {
		return err
	}
	st, err := openContractDB(ctx, getenv)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	now := time.Now()
	qt, err := provider.ReadQuote(ctx, st.R, f.backend)
	if err != nil {
		return fatal(err)
	}
	open, err := provider.WorkerCaps(ctx, st.R, f.backend, qt, now)
	if err != nil {
		return fatal(err)
	}
	v := contractView{Backend: f.backend, Caps: open, QuoteDigest: provider.QuoteImageDigest(qt, now)}
	row, ok, err := provider.LatestContract(ctx, st.R, f.backend)
	if err != nil {
		return fatal(err)
	}
	if ok {
		v.ImageDigest, v.Via, v.ChangedBy, v.ChangedAt, v.Reason = row.ImageDigest, row.Via, row.ChangedBy, store.FormatTime(row.ChangedAt), row.Reason
	}
	if v.QueuedNeed, err = queuedNeeding(ctx, st.R, f.backend, provider.Caps); err != nil {
		return fatal(err)
	}
	if f.asJSON {
		return printJSON(s, v)
	}
	fmt.Fprintf(s.out, "backend %s\nopen capabilities: %s\n", v.Backend, listOrNone(v.Caps))
	if ok {
		fmt.Fprintf(s.out, "latest decision: %s by %s at %s (%s)\n  image %s\n", v.Via, v.ChangedBy, v.ChangedAt, v.Reason, v.ImageDigest)
		if v.QuoteDigest != v.ImageDigest {
			fmt.Fprintln(s.out, "the fresh quote reports another image (or none): every capability is closed until it is opened for that image")
		}
	} else {
		fmt.Fprintln(s.out, "no decision recorded: every capability is closed")
	}
	fmt.Fprintf(s.out, "queued jobs that need a capability: %d\n", len(v.QueuedNeed))
	return nil
}

func listOrNone(c []string) string {
	if len(c) == 0 {
		return "none"
	}
	return strings.Join(c, ",")
}

func cmdContractOpen(ctx context.Context, args []string, getenv func(string) string, s streams) error {
	f, err := parseContractFlags("open", args, s, "caps", "reason")
	if err != nil {
		return err
	}
	if err := checkReason(f.reason); err != nil {
		return err
	}
	want, err := provider.NormalizeCaps(splitCaps(f.caps))
	if err != nil {
		return usageError("runpod contract open: " + err.Error())
	}
	if len(want) == 0 {
		return usageError("runpod contract open: --caps is required")
	}
	if !slices.Contains(want, jobspec.CapV2) {
		return usageError("runpod contract open: every capability needs v2 (C322.1): include it in --caps")
	}
	st, err := openContractDB(ctx, getenv)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	started, now := time.Now(), time.Now()
	qt, err := provider.ReadQuote(ctx, st.R, f.backend)
	if err != nil {
		return fatal(err)
	}
	digest := provider.QuoteImageDigest(qt, now)
	if digest == "" {
		return exitError{code: 3, err: errors.New("refusing to open: the backend has no fresh quote with an image digest (wait for the reconciler to read the endpoint)")}
	}
	tx, err := st.W.BeginTx(ctx, nil)
	if err != nil {
		return fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	prior, have, err := provider.LatestContract(ctx, tx, f.backend)
	if err != nil {
		return fatal(err)
	}
	next := want
	if have && prior.ImageDigest == digest { // the same image: opening adds to what is open
		if next, err = provider.NormalizeCaps(append(slices.Clone(prior.Caps), want...)); err != nil {
			return fatal(err)
		}
	}
	if err := provider.AppendContract(ctx, tx, provider.WorkerContractRow{Backend: f.backend, ImageDigest: digest, Caps: next, Via: provider.ContractViaCLI,
		ChangedBy: "system:cli", ChangedAt: now, Reason: f.reason}); err != nil {
		return fatal(err)
	}
	e, err := audit.SystemEntry(opContractOpen, now, map[string]any{"backend": f.backend, "caps": next, "opened": want, "reason": f.reason}, time.Since(started), 0, false)
	if err != nil {
		return fatal(err)
	}
	if err := audit.InsertTx(ctx, tx, e); err != nil {
		return fatal(err)
	}
	if err := tx.Commit(); err != nil {
		return fatal(err)
	}
	fmt.Fprintf(s.out, "open capabilities for %s: %s\n", f.backend, strings.Join(next, ","))
	return nil
}

func cmdContractClose(ctx context.Context, args []string, getenv func(string) string, s streams) error {
	f, err := parseContractFlags("close", args, s, "caps", "reason", "strand")
	if err != nil {
		return err
	}
	if err := checkReason(f.reason); err != nil {
		return err
	}
	drop, err := provider.NormalizeCaps(splitCaps(f.caps))
	if err != nil {
		return usageError("runpod contract close: " + err.Error())
	}
	st, err := openContractDB(ctx, getenv)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	started, now := time.Now(), time.Now()
	tx, err := st.W.BeginTx(ctx, nil)
	if err != nil {
		return fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	prior, have, err := provider.LatestContract(ctx, tx, f.backend)
	if err != nil {
		return fatal(err)
	}
	if !have || len(prior.Caps) == 0 {
		fmt.Fprintln(s.out, "no capability is open; nothing changed")
		return nil
	}
	if len(drop) == 0 {
		drop = slices.Clone(prior.Caps)
	}
	var next []string
	for _, c := range prior.Caps {
		if !slices.Contains(drop, c) {
			next = append(next, c)
		}
	}
	closed := provider.MissingCaps(prior.Caps, next)
	if len(closed) == 0 {
		fmt.Fprintln(s.out, "none of those capabilities is open; nothing changed")
		return nil
	}
	queued, err := queuedNeeding(ctx, tx, f.backend, closed)
	if err != nil {
		return fatal(err)
	}
	if len(queued) > 0 && !f.strand {
		fmt.Fprintf(s.err, "queued jobs that need %s:\n", strings.Join(closed, ","))
		for _, id := range queued {
			fmt.Fprintf(s.err, "  %s\n", id)
		}
		return exitError{code: 3, err: fmt.Errorf("refusing to close: %d queued job(s) need these capabilities; cancel them, or pass --strand-queued (they then fail %s after %s and release their reservation)",
			len(queued), jobs.CodeWorkerOutdated, jobs.WorkerOutdatedAfter)}
	}
	if err := provider.AppendContract(ctx, tx, provider.WorkerContractRow{Backend: f.backend, ImageDigest: prior.ImageDigest, Caps: next, Via: provider.ContractViaCLI,
		ChangedBy: "system:cli", ChangedAt: now, Reason: f.reason}); err != nil {
		return fatal(err)
	}
	e, err := audit.SystemEntry(opContractClose, now, map[string]any{"backend": f.backend, "closed": closed, "caps": nonNilStrings(next), "stranded_jobs": len(queued), "reason": f.reason},
		time.Since(started), 0, false)
	if err != nil {
		return fatal(err)
	}
	if err := audit.InsertTx(ctx, tx, e); err != nil {
		return fatal(err)
	}
	if err := tx.Commit(); err != nil {
		return fatal(err)
	}
	fmt.Fprintf(s.out, "closed %s on %s; open now: %s\n", strings.Join(closed, ","), f.backend, listOrNone(next))
	if len(queued) > 0 {
		fmt.Fprintf(s.out, "%d queued job(s) stranded\n", len(queued))
	}
	return nil
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
