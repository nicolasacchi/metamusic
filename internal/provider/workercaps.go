package provider

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/jobspec"
)

// The worker capability gate (C322). The operator opens capabilities for one backend's worker image; the open set is bound to the image
// digest the backend's fresh quote reports, so a new image closes everything by itself. A job with no needs never consults the gate.

// Caps is the frozen capability vocabulary (C322.1), in canonical (sorted) order.
var Caps = []string{jobspec.CapAgent, jobspec.CapAnalyze, jobspec.CapAttach, jobspec.CapEars, jobspec.CapFallback, jobspec.CapPlan, jobspec.CapV2}

// ValidCap reports whether c is in the vocabulary.
func ValidCap(c string) bool { return slices.Contains(Caps, c) }

// NormalizeCaps sorts and de-duplicates a capability list and refuses a name outside the vocabulary. An empty input is the empty set.
func NormalizeCaps(in []string) ([]string, error) {
	out := []string{}
	for _, c := range in {
		c = strings.TrimSpace(c)
		if !ValidCap(c) {
			return nil, fmt.Errorf("provider: unknown worker capability %q (known: %s)", c, strings.Join(Caps, ", "))
		}
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	slices.Sort(out)
	return out, nil
}

// Via values of a worker_contract_log row.
const (
	ContractViaCLI  = "cli"
	ContractViaAuto = "auto"
)

// WorkerContractRow is one row of worker_contract_log (the operator's or the runner's decision about one backend's image).
type WorkerContractRow struct {
	ID          int64
	Backend     string
	ImageDigest string
	Caps        []string
	Via         string
	AttemptID   string // auto rows only
	ChangedBy   string
	ChangedAt   time.Time
	Reason      string
}

// IsFakeBackend reports whether a backend name is a simulated one: it has no image to bind a digest to.
func IsFakeBackend(backend string) bool {
	return backend == BackendFake || backend == BackendFakeACEStep || backend == BackendFakePod
}

const contractCols = `id, backend, image_digest, caps_json, via, attempt_id, changed_by, changed_at, reason`

func scanContract(row interface{ Scan(...any) error }) (WorkerContractRow, error) {
	var r WorkerContractRow
	var caps, at string
	var att sql.NullString
	if err := row.Scan(&r.ID, &r.Backend, &r.ImageDigest, &caps, &r.Via, &att, &r.ChangedBy, &at, &r.Reason); err != nil {
		return r, err
	}
	r.AttemptID, r.ChangedAt = att.String, parseStoreTime(at)
	if err := json.Unmarshal([]byte(caps), &r.Caps); err != nil {
		return r, fmt.Errorf("provider: worker contract caps: %w", err)
	}
	return r, nil
}

// LatestContract is the newest worker_contract_log row of a backend; ok is false when the backend has none.
func LatestContract(ctx context.Context, q querier, backend string) (r WorkerContractRow, ok bool, err error) {
	r, err = scanContract(q.QueryRowContext(ctx, `SELECT `+contractCols+` FROM worker_contract_log WHERE backend = ? ORDER BY id DESC LIMIT 1`, backend))
	if errors.Is(err, sql.ErrNoRows) {
		return WorkerContractRow{}, false, nil
	}
	if err != nil {
		return WorkerContractRow{}, false, fmt.Errorf("provider: read worker contract: %w", err)
	}
	return r, true, nil
}

// QuoteImageDigest is the image digest of a fresh quote: "" for a nil, stale or digest-less quote.
func QuoteImageDigest(quote *Quote, now time.Time) string {
	if quote == nil || quote.ObservedAt.IsZero() || now.Sub(quote.ObservedAt) > QuoteMaxAge {
		return ""
	}
	return quote.Endpoint.ImageDigest
}

// WorkerCaps are the capabilities open for backend at now (C322.4): the latest log row's caps when its digest equals the digest of the
// fresh quote, else none. A simulated backend with no log row has every capability open (the fake registers them all); one with a row
// follows the row, so tests can close capabilities, and its digest is not compared. The result is sorted and never nil.
func WorkerCaps(ctx context.Context, q querier, backend string, quote *Quote, now time.Time) ([]string, error) {
	row, ok, err := LatestContract(ctx, q, backend)
	if err != nil {
		return nil, err
	}
	switch {
	case IsFakeBackend(backend):
		if !ok {
			return slices.Clone(Caps), nil
		}
		return NormalizeCaps(row.Caps)
	case !ok:
		return []string{}, nil
	}
	if d := QuoteImageDigest(quote, now); d == "" || d != row.ImageDigest {
		return []string{}, nil
	}
	return NormalizeCaps(row.Caps)
}

// MissingCaps are the needs not in open, in the order of needs.
func MissingCaps(needs, open []string) []string {
	var out []string
	for _, n := range needs {
		if !slices.Contains(open, n) {
			out = append(out, n)
		}
	}
	return out
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// AppendContract writes one worker_contract_log row (append-only). Caps are normalised; the caller supplies the digest of the fresh quote.
func AppendContract(ctx context.Context, x execer, r WorkerContractRow) error {
	caps, err := NormalizeCaps(r.Caps)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(caps)
	var att any
	if r.AttemptID != "" {
		att = r.AttemptID
	}
	if _, err := x.ExecContext(ctx, `INSERT INTO worker_contract_log(backend, image_digest, caps_json, via, attempt_id, changed_by, changed_at, reason)
		VALUES (?,?,?,?,?,?,?,?)`, r.Backend, r.ImageDigest, string(raw), r.Via, att, r.ChangedBy, r.ChangedAt.UTC().Format("2006-01-02T15:04:05.000Z"), r.Reason); err != nil {
		return fmt.Errorf("provider: append worker contract: %w", err)
	}
	return nil
}

// ReadQuote is the backend's cached quote from provider_state, with ObservedAt set from quote_at; nil when there is none or it does not
// decode. Freshness is the caller's question (QuoteImageDigest, QuoteMaxAge).
func ReadQuote(ctx context.Context, q querier, backend string) (*Quote, error) {
	var raw, at sql.NullString
	err := q.QueryRowContext(ctx, `SELECT quote_json, quote_at FROM provider_state WHERE backend = ?`, backend).Scan(&raw, &at)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !raw.Valid) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("provider: read quote: %w", err)
	}
	var qt Quote
	if json.Unmarshal([]byte(raw.String), &qt) != nil {
		return nil, nil //nolint:nilerr // an undecodable cached quote is no quote: the gate then fails closed (no digest)
	}
	qt.ObservedAt = parseStoreTime(at.String)
	return &qt, nil
}
