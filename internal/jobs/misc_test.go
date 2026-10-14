package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestBackoff(t *testing.T) {
	t.Parallel()
	want := []time.Duration{time.Minute, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 30 * time.Minute, 30 * time.Minute}
	for k, w := range want {
		if got := backoff(k); got != w {
			t.Errorf("backoff(%d) = %v, want %v", k, got, w)
		}
	}
}

func TestCanTransition(t *testing.T) {
	t.Parallel()
	if err := CanTransition(StateQueued, StateQueued); err != nil {
		t.Error("a no-op is not a transition")
	}
	if !errors.Is(CanTransition(StateFailed, StateQueued), ErrTerminalImmutable) || !errors.Is(CanTransition(StateQueued, StateCompleted), ErrIllegalTransition) {
		t.Error("guard")
	}
	for _, s := range AllStates {
		if IsLeased(s) && (IsTerminal(s) || s == StateQueued || s == StateAwaiting) {
			t.Errorf("%s", s)
		}
	}
}

func TestErrShapeAndClassify(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		kind failKind
		code string
		trip bool
	}{
		{"endpoint not found sentinel", provider.ErrEndpointNotFound, failRetry, "", true},
		{"wrapped access", errors.Join(errors.New("x"), provider.ErrAccess), failRefused, CodeProviderAuth, true},
		{"forbidden", &httpErr{Code: "FORBIDDEN", Status: 403}, failRefused, CodeProviderAuth, true},
		{"not found code", &httpErr{Code: "NOT_FOUND", Status: 404}, failRetry, "", true},
		{"decode", &httpErr{Code: "DECODE", Status: 200}, failAmbiguous, "", false},
		{"plain error", errors.New("boom"), failAmbiguous, "", false},
	}
	for _, c := range cases {
		got := classifySubmit(c.err)
		if got.kind != c.kind || got.code != c.code || got.trip != c.trip {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
	if code, status, ra, ok := errShape(&httpErr{Code: "RATE_LIMITED", Status: 429, RetryAfter: time.Minute}); !ok || code != "RATE_LIMITED" || status != 429 || ra != time.Minute {
		t.Errorf("shape %v %v %v %v", code, status, ra, ok)
	}
	if _, _, _, ok := errShape(errors.New("x")); ok {
		t.Error("shape of a plain error")
	}
}

func TestRunner_PollErrors(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var perr error
	r := newRig(t)
	r.run.o.Backend = &pollFailBackend{Backend: r.be, err: func() error { mu.Lock(); defer mu.Unlock(); return perr }}
	job := r.seedBatch(batchSpec{}).Jobs[0]
	r.submitOne(job)
	ref := r.refOf(job, 1)
	// a rate limit holds the next poll
	mu.Lock()
	perr = &httpErr{Code: "RATE_LIMITED", Status: 429, RetryAfter: time.Minute}
	mu.Unlock()
	r.advance(6 * time.Second)
	r.step()
	before := r.pollCount(ref)
	mu.Lock()
	perr = nil
	mu.Unlock()
	r.advance(30 * time.Second)
	r.step()
	if r.pollCount(ref) != before {
		t.Error("polled again inside the Retry-After hold")
	}
	r.advance(40 * time.Second)
	r.step()
	if r.pollCount(ref) == before {
		t.Error("never polled after the hold")
	}
	// access loss trips the provider; a generic error just retries
	mu.Lock()
	perr = errors.New("connection reset")
	mu.Unlock()
	r.advance(10 * time.Second)
	r.step()
	if r.count(`SELECT COUNT(*) FROM provider_trips`) != 0 {
		t.Error("a transient poll error must not trip")
	}
	mu.Lock()
	perr = provider.ErrAccess
	mu.Unlock()
	r.advance(10 * time.Second)
	r.step()
	if r.count(`SELECT COUNT(*) FROM provider_trips WHERE reason = 'provider_access'`) != 1 {
		t.Error("access loss must trip provider_access")
	}
	r.wantState(job, StateStarting)
	// the attempt is closed by its time bound later: nothing can be polled
	mu.Lock()
	perr = nil
	mu.Unlock()
	r.advance(4 * time.Hour)
	r.step()
	if a := r.att(job, 1); a.State != "done" || a.TerminalBasis != "time_bound" {
		t.Errorf("attempt %+v", a)
	}
}

type pollFailBackend struct {
	provider.Backend
	err func() error
}

func (p *pollFailBackend) Poll(ctx context.Context, ref provider.Ref) (provider.RemoteStatus, error) {
	if e := p.err(); e != nil {
		return provider.RemoteStatus{}, e
	}
	return p.Backend.Poll(ctx, ref)
}

func TestRunner_ExcessTripsAndCustomTrip(t *testing.T) {
	t.Parallel()
	var tripped struct {
		sync.Mutex
		reasons []string
	}
	r := newRig(t, withOpts(func(o *RunnerOptions) {
		o.Trip = func(_ context.Context, _ *sql.Tx, _ store.LeaseFence, backend, reason string, _ map[string]any) error {
			tripped.Lock()
			tripped.reasons = append(tripped.reasons, backend+":"+reason)
			tripped.Unlock()
			return nil
		}
	}))
	b := r.seedBatch(batchSpec{})
	job := b.Jobs[0]
	r.submitOne(job)
	// the provider reports far more execution than reserved
	r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.Failed, Raw: "FAILED", ExecutionMS: ms(900000)})
	r.advance(6 * time.Second)
	r.step()
	if r.ledger(b.Batch, "settle", "excess") == 0 {
		t.Fatal("no excess row")
	}
	if got, want := r.ledger(b.Batch, "settle", "execution"), r.ledger(b.Batch, "reserve", "take"); got != want {
		t.Errorf("execution settle %d, want the pot %d", got, want)
	}
	tripped.Lock()
	defer tripped.Unlock()
	if !slices.Contains(tripped.reasons, "fake:provisional_exceeds_reserved") {
		t.Errorf("trips %v", tripped.reasons)
	}
}

func TestRunner_WorkerKeyIsHashed(t *testing.T) {
	t.Parallel()
	r := newRig(t, withOpts(func(o *RunnerOptions) { o.MeterKey = []byte("meter-key-for-tests") }))
	job := r.seedBatch(batchSpec{}).Jobs[0]
	r.submitOne(job)
	r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.Running, Raw: "IN_PROGRESS", WorkerID: "raw-worker-id-77"})
	r.advance(6 * time.Second)
	r.step()
	wk := r.att(job, 1).WorkerKey
	if len(wk) != 64 || strings.Contains(wk, "raw-worker") || wk != workerKey([]byte("meter-key-for-tests"), "raw-worker-id-77") {
		t.Errorf("worker_key %q", wk)
	}
}

func TestRunner_AuditRows(t *testing.T) {
	t.Parallel()
	var as *audit.Store
	r := newRig(t, withOpts(func(o *RunnerOptions) {}))
	as = audit.NewStore(r.st, r.newID)
	r.run.o.Audit = as
	b := r.seedBatch(batchSpec{})
	job := b.Jobs[0]
	r.submitErr(&httpErr{Code: "UPSTREAM", Status: 502}) // claim -> runner:submit, ghost -> runner:topup
	r.step()
	r.onSubmit(nil)
	r.advance(2 * time.Minute)
	r.step()
	r.cancelIntent(job)
	r.step() // -> runner:cancel
	ref := r.refOf(job, 2)
	r.setStatus(ref, provider.RemoteStatus{State: provider.Cancelled, Raw: "CANCELLED", ExecutionMS: ms(500)})
	r.advance(3 * time.Second)
	r.step()
	r.advance(4 * time.Hour)
	r.steps(2) // ghost closes, batch finishes -> runner:batch_finish
	counts := map[string]int{}
	rows, err := r.st.R.Query(`SELECT op, principal_kind || ':' || principal_id, transport, status, COALESCE(SUM(cost_micro_usd),0) FROM audit_log GROUP BY op, principal_kind, principal_id, transport, status`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var op, who, tr string
		var status int
		var cost int64
		if err := rows.Scan(&op, &who, &tr, &status, &cost); err != nil {
			t.Fatal(err)
		}
		counts[op]++
		actor := "system:" + strings.SplitN(op, ":", 2)[0]
		if !strings.HasSuffix(who, actor) || tr != "runner" || status != 200 {
			t.Errorf("%s: %s %s %d", op, who, tr, status)
		}
		if op == "runner:topup" && cost != int64(money.ForSeconds(201, testPrice)) {
			t.Errorf("topup cost %d", cost)
		}
	}
	for _, op := range []string{"runner:submit", "runner:topup", "runner:cancel", "runner:batch_finish"} {
		if counts[op] == 0 {
			t.Errorf("no %s audit row (have %v)", op, counts)
		}
	}
}

func TestRunner_FailRemainderAudited(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.run.o.Audit = audit.NewStore(r.st, r.newID)
	b := r.seedBatch(batchSpec{jobs: 3, workersMax: 1})
	r.exec(`UPDATE jobs SET first_submitted_at = ?, attempts = 1, queue_expiries = 7, next_attempt_at = ? WHERE id = ?`,
		store.FormatTime(t0), store.FormatTime(t0.Add(time.Hour)), b.Jobs[0])
	r.step()
	if n := r.count(`SELECT COUNT(*) FROM audit_log WHERE op = 'runner:fail_remainder'`); n != 1 {
		t.Errorf("fail_remainder rows %d", n)
	}
}

func TestEvents_DetailAllowlist(t *testing.T) {
	t.Parallel()
	r, _, job, spec := generating(t)
	ghost := r.ghost(job, "unknown", true)
	r.exec(`UPDATE job_attempts SET ref = 'ghost-ref' WHERE id = ?`, ghost)
	r.setStatus("ghost-ref", running())
	out := r.deliver(spec, deliverOpts{})
	r.setStatus(r.refOf(job, 1), completed(out, 60000))
	r.advance(11 * time.Second)
	r.steps(2)
	rows, err := r.st.R.Query(`SELECT event, detail_json FROM job_events WHERE detail_json IS NOT NULL`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var ev, d string
		if err := rows.Scan(&ev, &d); err != nil {
			t.Fatal(err)
		}
		allowed, ok := EventDetailKeys(ev)
		if !ok {
			t.Fatalf("event %q is not in the table", ev)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(d), &m); err != nil {
			t.Fatal(err)
		}
		for k := range m {
			n++
			if !slices.Contains(allowed, k) {
				t.Errorf("event %s carries %q", ev, k)
			}
		}
		if len(d) > 2048 {
			t.Errorf("detail of %s has %d bytes", ev, len(d))
		}
	}
	if n == 0 {
		t.Error("nothing checked")
	}
}

func TestRunner_RefuseClaimWithLiveGhostDefers(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.disarm()
	b := r.seedBatch(batchSpec{ceiling: 38525*2 + 1000})
	job := b.Jobs[0]
	ghost := r.ghost(job, "unknown", true) // takes the room for the next attempt's overhead
	r.arm()
	r.step()
	// the resubmission's overhead is refused while the ghost may still run: the job waits for the window
	r.wantState(job, StateQueued)
	if r.be.Calls().Submit != 0 || !r.job(job).NextAttemptAt.After(t0.Add(2*time.Hour)) {
		t.Errorf("submits %d next %v", r.be.Calls().Submit, r.job(job).NextAttemptAt)
	}
	_ = ghost
	r.advance(4 * time.Hour)
	r.steps(2) // the ghost closes, then the refused claim fails the job
	r.wantState(job, StateFailed)
	r.wantCode(job, CodeBudgetExceeded)
}

func TestRunner_ApprovalOfRejectedBatchDoesNotRelease(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	b := r.seedBatch(batchSpec{state: "expired", jobs: 2})
	r.cancelIntent(b.Jobs[0])
	r.step()
	r.wantState(b.Jobs[0], StateCancelled) // the cancel intent wins
	r.wantState(b.Jobs[1], StateExpired)
	if r.ledger(b.Batch, "release", "") != 0 {
		t.Error("an expired batch was released as a whole by its operation")
	}
}

func TestPool(t *testing.T) {
	t.Parallel()
	p := newPool(1)
	block := make(chan struct{})
	started := make(chan struct{})
	if !p.start("a", func() { close(started); <-block }) {
		t.Fatal("start")
	}
	<-started
	if p.start("a", func() {}) || p.start("b", func() {}) {
		t.Error("duplicate or over-capacity start accepted")
	}
	if !p.has("a") || p.has("b") {
		t.Error("has")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := p.drain(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("drain: %v", err)
	}
	close(block)
	if err := p.drain(t.Context()); err != nil {
		t.Error(err)
	}
	if !p.start("c", func() {}) {
		t.Error("restart after drain")
	}
	if err := p.drain(t.Context()); err != nil {
		t.Error(err)
	}
	if id := newUUID(); len(id) != 36 {
		t.Errorf("uuid %q", id)
	}
}

func TestNewPreflight(t *testing.T) {
	t.Parallel()
	t.Run("route and disk pass", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		srv := httptest.NewServer(r.up.Handler())
		defer srv.Close()
		pf := NewPreflight(PreflightOptions{DataDir: t.TempDir(), PublicURL: srv.URL, Free: func(string) (uint64, error) { return 5 << 30, nil }})
		if err := pf(t.Context(), 1<<30); err != nil {
			t.Fatal(err)
		}
		// 5 GiB free, 4.5 GiB in flight + 1 GiB reserve: too little
		err := pf(t.Context(), 4<<30+1<<29)
		var pe *PreflightError
		if !errors.As(err, &pe) || pe.Check != "disk" {
			t.Errorf("disk: %v", err)
		}
	})
	t.Run("free space error", func(t *testing.T) {
		t.Parallel()
		pf := NewPreflight(PreflightOptions{DataDir: t.TempDir(), PublicURL: "http://127.0.0.1:1", Free: func(string) (uint64, error) { return 0, errors.New("statfs failed") }})
		var pe *PreflightError
		if err := pf(t.Context(), 0); !errors.As(err, &pe) || pe.Check != "disk" || pe.Unwrap() == nil {
			t.Errorf("%v", err)
		}
	})
	t.Run("route answers something else", func(t *testing.T) {
		t.Parallel()
		for name, h := range map[string]http.HandlerFunc{
			"200":      func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) },
			"404 html": func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "<html>", 404) },
			"403 other code": func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(403)
				_, _ = w.Write([]byte(`{"code":"FORBIDDEN"}`))
			},
			"403 not json": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(403); _, _ = w.Write([]byte(`nope`)) },
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				srv := httptest.NewServer(h)
				defer srv.Close()
				pf := NewPreflight(PreflightOptions{DataDir: t.TempDir(), PublicURL: srv.URL, Free: func(string) (uint64, error) { return 5 << 30, nil }})
				var pe *PreflightError
				if err := pf(t.Context(), 0); !errors.As(err, &pe) || pe.Check != "route" {
					t.Errorf("%v", err)
				}
			})
		}
	})
	t.Run("route unreachable and invalid URL", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.NotFoundHandler())
		url := srv.URL
		srv.Close()
		for _, u := range []string{url, "http://[::1"} {
			pf := NewPreflight(PreflightOptions{DataDir: t.TempDir(), PublicURL: u, Free: func(string) (uint64, error) { return 5 << 30, nil }})
			var pe *PreflightError
			if err := pf(t.Context(), 0); !errors.As(err, &pe) || pe.Check != "route" {
				t.Errorf("%s: %v", u, err)
			}
		}
	})
	t.Run("real free space", func(t *testing.T) {
		t.Parallel()
		free, err := freeBytes(t.TempDir())
		if err != nil || free == 0 {
			t.Fatalf("%d %v", free, err)
		}
		if _, err := freeBytes("/nonexistent-dir-for-test"); err == nil {
			t.Error("statfs of a missing dir")
		}
	})
}
