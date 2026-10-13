package gate_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/gate"
	"github.com/nicolasacchi/metamusic/internal/idem"
	"github.com/nicolasacchi/metamusic/internal/ratelimit"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// A gate built with only the required options uses the wall clock, a real ticker and the 8 MiB body bound.
func TestNew_Defaults(t *testing.T) {
	t.Parallel()
	st, err := store.Open(context.Background(), t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	n := 0
	mux := http.NewServeMux()
	a := humago.New(mux, api.HumaConfig())
	createOp := huma.Operation{OperationID: "t_create", Method: "POST", Path: "/api/t/create", Summary: "c", Description: "c", DefaultStatus: 201}
	api.Register(a, createOp, api.Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "tool", UI: "none:test", Idempotency: "create"},
		func(ctx context.Context, in *createIn) (*okOut, error) {
			o := &okOut{}
			o.Body.OK = true
			return o, nil
		})
	// a route the API does not know: answered by the mux itself, writing without WriteHeader
	mux.HandleFunc("GET /plain", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("plain")) })

	h := gate.New(gate.Options{
		API: a, Inner: mux,
		Audit:   audit.NewStore(st, func() string { n++; return fmt.Sprintf("a%06d", n) }),
		Idem:    idem.NewStore(st, nil),
		Limiter: ratelimit.New(ratelimit.Default.Scaled(1000), nil),
	})
	do := func(method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), human)))
		return w
	}
	if w := do("POST", "/api/t/create", `{"name":"x"}`, map[string]string{"Idempotency-Key": "k-1"}); w.Code != 201 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := do("GET", "/plain", "", nil); w.Code != 200 || w.Body.String() != "plain" {
		t.Fatalf("%d %q", w.Code, w.Body)
	}
	time.Sleep(10 * time.Millisecond)
}

func TestIdempotency_MalformedKeyIs422(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{})
	w := r.do(create(agent("t1"), "has space", `{"name":"x"}`))
	if w.Code != 422 || code(w) != "VALIDATION_FAILED" || r.handled.Load() != 0 {
		t.Fatalf("%d %s handled=%d", w.Code, w.Body, r.handled.Load())
	}
}

func TestIdempotency_StoreFailureIs500NotAProceed(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{})
	if _, err := r.st.W.Exec(`ALTER TABLE idempotency RENAME TO idempotency_gone`); err != nil {
		t.Skipf("cannot break the schema here: %v", err)
	}
	w := r.do(create(agent("t1"), "k-1", `{"name":"x"}`))
	if w.Code != 500 || r.handled.Load() != 0 {
		t.Fatalf("%d %s handled=%d", w.Code, w.Body, r.handled.Load())
	}
}
