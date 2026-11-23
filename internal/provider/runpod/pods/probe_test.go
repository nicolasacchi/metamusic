package pods_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/pods"
)

// probeStub is a scripted ProbeAPI.
type probeStub struct {
	listErr, catErr, billErr, srvErr error
	billStatus, srvStatus            int
	calls                            []string
}

func (p *probeStub) ListPods(context.Context) ([]pods.Pod, error) {
	p.calls = append(p.calls, "list")
	return nil, p.listErr
}

func (p *probeStub) CatalogSecurePrices(_ context.Context, ids []string) (map[string]money.MicroUSD, error) {
	p.calls = append(p.calls, "catalog")
	if p.catErr != nil {
		return nil, p.catErr
	}
	out := map[string]money.MicroUSD{}
	for _, id := range ids {
		out[id] = 690_000
	}
	return out, nil
}

func (p *probeStub) ProbeBilling(context.Context, time.Time, time.Time) (int, error) {
	p.calls = append(p.calls, "billing")
	return p.billStatus, p.billErr
}

func (p *probeStub) ProbeServerless(context.Context) (int, error) {
	p.calls = append(p.calls, "serverless")
	return p.srvStatus, p.srvErr
}

func TestProbePods_Verdicts(t *testing.T) {
	t.Parallel()
	denied := &pods.APIError{Op: "x", Status: 500, Detail: "boom"}
	for name, tc := range map[string]struct {
		stub     probeStub
		verdict  string
		billable bool
	}{
		"narrow key with billing":       {probeStub{billStatus: 200, srvStatus: 403}, "ok", true},
		"narrow key without billing":    {probeStub{billStatus: 403, srvStatus: 403}, "ok", false},
		"unauthorized serverless":       {probeStub{billStatus: 200, srvStatus: 401}, "ok", true},
		"key reads serverless":          {probeStub{billStatus: 200, srvStatus: 200}, "too_broad", true},
		"serverless answer is unusable": {probeStub{billStatus: 200, srvStatus: 404}, "failed", true},
		"serverless transport failure":  {probeStub{billStatus: 200, srvErr: denied}, "failed", true},
		"pods unreadable":               {probeStub{listErr: denied, billStatus: 200, srvStatus: 403}, "failed", true},
		"catalog unreadable":            {probeStub{catErr: denied, billStatus: 200, srvStatus: 403}, "failed", true},
		"billing answers 500":           {probeStub{billStatus: 500, srvStatus: 403}, "failed", false},
		"billing transport failure":     {probeStub{billErr: denied, srvStatus: 403}, "failed", false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := tc.stub
			r := pods.RunProbe(context.Background(), &s, []string{"NVIDIA L40S"}, t0)
			if r.Verdict != tc.verdict {
				t.Fatalf("verdict %q, want %q (detail %v)", r.Verdict, tc.verdict, r.Detail)
			}
			if got, _ := r.Detail["billing_readable"].(bool); got != tc.billable {
				t.Errorf("billing_readable = %v, want %v", r.Detail["billing_readable"], tc.billable)
			}
			if _, err := json.Marshal(r.Detail); err != nil {
				t.Errorf("detail does not marshal: %v", err)
			}
		})
	}
}

func TestProbePods_DetailHoldsNoSecret(t *testing.T) {
	t.Parallel()
	key := canary("pod")
	err := &pods.APIError{Op: "list_pods", Status: 500, Detail: "bad token " + key}
	s := &probeStub{listErr: err, billStatus: 200, srvStatus: 403}
	r := pods.RunProbe(context.Background(), s, []string{"NVIDIA L40S"}, t0)
	raw, _ := json.Marshal(r.Detail)
	// the client masks its own key; the probe also keeps only a status, never an error text
	if strings.Contains(string(raw), key) {
		t.Fatalf("the detail carries the key: %s", raw)
	}
}

func TestProbePods_ClientSurface(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/v2/serverless":
			w.WriteHeader(403)
		case "/v2/billing/pods":
			// an account-wide answer larger than the client's cap still proves the key reads billing
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"records":[` + strings.Repeat(`{"podId":"x"},`, 100_000) + `{"podId":"y"}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	writeKey(t, dir, canary("pod"), 0o600)
	cl := pods.NewClient(pods.Options{BaseURL: srv.URL, KeyDir: dir})
	if st, err := cl.ProbeServerless(context.Background()); err != nil || st != 403 {
		t.Errorf("serverless = %d, %v", st, err)
	}
	if st, err := cl.ProbeBilling(context.Background(), t0.Add(-time.Hour), t0); err != nil || st != 200 {
		t.Errorf("billing = %d, %v", st, err)
	}
	// no key: nothing is sent
	cl2 := pods.NewClient(pods.Options{BaseURL: srv.URL, KeyDir: t.TempDir()})
	if _, err := cl2.ProbeServerless(context.Background()); !errors.Is(err, pods.ErrNoKey) {
		t.Errorf("no key: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 || seen[0] != "GET /v2/serverless" {
		t.Errorf("requests %v", seen)
	}
}
