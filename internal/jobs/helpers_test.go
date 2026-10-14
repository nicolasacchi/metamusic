package jobs

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/providertest"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/upload"
)

func (r *rig) disarm() {
	r.t.Helper()
	r.exec(`UPDATE provider_state SET armed = 0 WHERE backend = 'fake'`)
}
func (r *rig) arm() {
	r.t.Helper()
	r.exec(`UPDATE provider_state SET armed = 1 WHERE backend = 'fake'`)
}

func (r *rig) cancelIntent(job string) {
	r.t.Helper()
	r.exec(`UPDATE jobs SET cancel_requested_at = ?, cancel_requested_by = 'human:h1' WHERE id = ? AND cancel_requested_at IS NULL`, store.FormatTime(r.clk.Now()), job)
}

// ghost inserts an attempt in state (unknown or pending) without a ref, with a window of 3 h, and bumps the
// job's attempt counter. withTopup also reserves its ambiguous top-up.
func (r *rig) ghost(job, state string, withTopup bool) string {
	r.t.Helper()
	n := r.count(`SELECT attempts FROM jobs WHERE id = ?`, job) + 1
	id := r.newID()
	ts := store.FormatTime(r.clk.Now())
	r.exec(`INSERT INTO job_attempts(id, job_id, n, backend, state, submitted_at, window_ends_at, price_pph_micro_usd, created_at)
		VALUES(?, ?, ?, 'fake', ?, ?, ?, 690000, ?)`, id, job, n, state, ts, store.FormatTime(r.clk.Now().Add(3*time.Hour)), ts)
	r.exec(`UPDATE jobs SET attempts = ?, first_submitted_at = COALESCE(first_submitted_at, ?) WHERE id = ?`, n, ts, job)
	if withTopup {
		r.topup(job, id, "topup_ambiguous")
	}
	return id
}

func (r *rig) topup(job, attempt, reason string) {
	r.t.Helper()
	var batch, principal string
	var timeout int
	if err := r.st.R.QueryRow(`SELECT j.batch_id, b.principal_id, j.timeout_s FROM jobs j JOIN batches b ON b.id = j.batch_id WHERE j.id = ?`, job).Scan(&batch, &principal, &timeout); err != nil {
		r.t.Fatal(err)
	}
	amount := money.ForSeconds(int64(timeout+15), testPrice)
	if err := r.sp.RunTx(r.t.Context(), func(tx *sql.Tx) error {
		return r.sp.Ledger().Append(r.t.Context(), tx, spend.Entry{Kind: spend.KindReserve, MicroUSD: amount, BatchID: &batch, JobID: &job, AttemptID: &attempt, PrincipalID: principal, Reason: reason})
	}); err != nil {
		r.t.Fatal(err)
	}
}

// httpErr is a submit/poll error with the C77.4 shape, as the RunPod client's *Error has it.
type httpErr struct {
	Code       string
	Status     int
	RetryAfter time.Duration
}

func (e *httpErr) Error() string { return fmt.Sprintf("runpod: run: %s (%d)", e.Code, e.Status) }

func (r *rig) submitErr(err error) {
	r.onSubmit(func(provider.TakeSpec) (provider.Ref, error) { return provider.Ref{}, err })
}

func (r *rig) wantState(job, want string) {
	r.t.Helper()
	if got := r.state(job); got != want {
		r.t.Fatalf("job %s is %s, want %s (events %v)", job, got, want, r.events(job))
	}
}

func (r *rig) wantCode(job, want string) {
	r.t.Helper()
	if got := r.job(job).ErrorCode; got != want {
		r.t.Fatalf("job %s error_code %q, want %q", job, got, want)
	}
}

// openUploads opens the upload rows of a seeded attempt (the ghost helper does not).
func (r *rig) openUploads(job, attempt string, exp int64) {
	r.t.Helper()
	var batch string
	if err := r.st.R.QueryRow(`SELECT batch_id FROM jobs WHERE id = ?`, job).Scan(&batch); err != nil {
		r.t.Fatal(err)
	}
	arts := map[string]int64{}
	for _, n := range []string{"audio.flac", "request.json", "config.json", "result.json", "mm-result.json"} {
		limit, _ := provider.ArtifactMaxBytes(n)
		arts[n] = limit
	}
	r.exec(`INSERT OR IGNORE INTO runner_lease VALUES(1,'runner-1',?,?,?)`, store.FormatTime(t0), store.FormatTime(t0), store.FormatTime(t0.Add(24*time.Hour)))
	tx, err := r.st.W.Begin()
	if err != nil {
		r.t.Fatal(err)
	}
	if err := r.up.OpenAttempt(r.t.Context(), tx, upload.JobRef{JobID: job, BatchID: batch}, attempt, arts, exp, r.run.fence()); err != nil {
		tx.Rollback()
		r.t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		r.t.Fatal(err)
	}
}

func contextWithCancel(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithCancel(t.Context())
}

// pump advances the manual clock by one second whenever a timer waits, so Run's heartbeat and tick timers
// fire while the test waits in real time. It stops with ctx; the returned channel closes when it has.
func (r *rig) pump(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			if r.clk.Pending() > 0 {
				r.clk.Advance(time.Second)
			}
			time.Sleep(time.Millisecond)
		}
	}()
	return done
}

func (r *rig) winnerOf(job string) string {
	r.t.Helper()
	w, err := r.run.winner(r.t.Context(), r.st.R, job)
	if err != nil {
		r.t.Fatal(err)
	}
	return w
}

func sha256Sum(b []byte) [32]byte { return sha256.Sum256(b) }

// newNamedBackend is a scripted backend of the rig's behaviour under another provider name.
func newNamedBackend(r *rig, name string) *providertest.Backend {
	return providertest.New(name, providertest.Script{
		Capabilities: &r.caps,
		Quote: func(context.Context) (provider.Quote, error) {
			r.mu.Lock()
			fn := r.quoteFn
			r.mu.Unlock()
			if fn != nil {
				return fn()
			}
			return providertest.GoodQuote(name, r.clk.Now()), nil
		},
	})
}

func goodQuoteFor(name string, at time.Time) provider.Quote { return providertest.GoodQuote(name, at) }

func auth0CLI() auth.Principal { return auth.CLI }
