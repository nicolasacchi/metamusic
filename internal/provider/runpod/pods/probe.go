package pods

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
)

// The pod key probe (C399.11, BULK-P7). Everything here only reads: it lists pods, reads the catalog and one hour of pod billing, and
// asks for the serverless endpoint list, which a correctly scoped key must be refused. It never creates, starts or stops a pod. The
// result keeps statuses and counts only: no body, no pod id, no key.

// Probe verdicts, the values of pod_safety_log.verdict for kind = 'probe'.
const (
	ProbeOK       = "ok"
	ProbeTooBroad = "too_broad"
	ProbeFailed   = "failed"
)

// ProbeAPI is what RunProbe reads; *Client implements it.
type ProbeAPI interface {
	ListPods(ctx context.Context) ([]Pod, error)
	CatalogSecurePrices(ctx context.Context, typeIDs []string) (map[string]money.MicroUSD, error)
	ProbeBilling(ctx context.Context, from, to time.Time) (status int, err error)
	ProbeServerless(ctx context.Context) (status int, err error)
}

// ProbeResult is the row the probe writes: Detail is the detail_json of the probe row (scopes seen, billing_readable).
type ProbeResult struct {
	Verdict string
	Detail  map[string]any
}

// ProbeServerless asks GET /v2/serverless and returns the HTTP status (a correctly scoped key gets 401 or 403). It sends once.
func (c *Client) ProbeServerless(ctx context.Context) (int, error) {
	rep, err := c.do(ctx, call{op: "probe_serverless", method: http.MethodGet, path: "/v2/serverless", timeout: OtherTimeout})
	if err != nil {
		return 0, err
	}
	return rep.status, nil
}

// ProbeBilling asks for one window of hourly pod billing, account-wide, and returns the HTTP status. An answer too large to read is a
// 2xx the client refused to buffer: the key reads billing, so it is reported as the status it carried.
func (c *Client) ProbeBilling(ctx context.Context, from, to time.Time) (int, error) {
	q := url.Values{"bucketSize": {"hour"}, "startTime": {from.UTC().Format(time.RFC3339)}, "endTime": {to.UTC().Format(time.RFC3339)}}
	rep, err := c.do(ctx, call{op: "probe_billing", method: http.MethodGet, path: "/v2/billing/pods", query: q, timeout: OtherTimeout})
	var ae *APIError
	if errors.As(err, &ae) && ae.Status >= 200 && ae.Status < 300 {
		return ae.Status, nil
	}
	if err != nil {
		return 0, err
	}
	return rep.status, nil
}

func denied(status int) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden
}

// RunProbe runs the four reads and judges the key. failed: the pods list or the catalog could not be read, the serverless answer was
// neither a refusal nor a list, or billing answered something other than a list or a refusal. too_broad: the key can list serverless
// endpoints. ok otherwise. Billing refused is not a failure: billing_readable is then false and the backend's billing is "none".
func RunProbe(ctx context.Context, api ProbeAPI, gpuTypes []string, now time.Time) ProbeResult {
	d := map[string]any{"write_scope": "unverified", "gpu_types": len(gpuTypes)}
	failed := false
	if pl, err := api.ListPods(ctx); err != nil {
		d["pods_read"], failed = false, true
		d["pods_error"] = errKind(err)
	} else {
		d["pods_read"], d["pods_listed"] = true, len(pl)
	}
	if _, err := api.CatalogSecurePrices(ctx, gpuTypes); err != nil {
		d["catalog_read"], failed = false, true
		d["catalog_error"] = errKind(err)
	} else {
		d["catalog_read"] = true
	}
	bs, err := api.ProbeBilling(ctx, now.Add(-time.Hour), now)
	switch {
	case err != nil:
		d["billing_readable"], failed = false, true
		d["billing_error"] = errKind(err)
	case bs >= 200 && bs < 300:
		d["billing_readable"] = true
	default:
		d["billing_readable"] = false
		if !denied(bs) {
			failed = true
			d["billing_status"] = bs
		}
	}
	ss, err := api.ProbeServerless(ctx)
	broad := false
	switch {
	case err != nil:
		d["serverless_read"], failed = false, true
		d["serverless_error"] = errKind(err)
	case ss >= 200 && ss < 300:
		d["serverless_read"], broad = true, true
	case denied(ss):
		d["serverless_read"] = false
	default:
		d["serverless_read"], failed = false, true
		d["serverless_status"] = ss
	}
	switch {
	case failed:
		return ProbeResult{ProbeFailed, d}
	case broad:
		return ProbeResult{ProbeTooBroad, d}
	}
	return ProbeResult{ProbeOK, d}
}

// errKind names a failure without its text: the HTTP status of an answered refusal, else a fixed word.
func errKind(err error) string {
	var ae *APIError
	var ac *accessError
	switch {
	case errors.Is(err, ErrNoKey):
		return "no_key"
	case errors.As(err, &ae):
		return "http_" + strconv.Itoa(ae.Status)
	case errors.As(err, &ac):
		return "http_" + strconv.Itoa(ac.status)
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	return "error"
}
