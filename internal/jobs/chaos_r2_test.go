package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/objstore"
	"github.com/nicolasacchi/metamusic/internal/objstore/objstoretest"
	"github.com/nicolasacchi/metamusic/internal/provider/fake"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/upload/fallback"
)

// The R2 half of the harness (BULK-R4, C399.13): the fake backend's simulated worker uploads through the real upload handlers, and
// when the app is unreachable it falls back to an objstoretest bucket through the fallback URLs the runner minted; the runner's
// rescue then completes the take from the bucket.

var r2Caps = spend.Caps{MonthlyMicro: 50_000_000, BatchMicro: 50_000_000, HumanDailyMicro: 50_000_000, AgentDailyMicro: 50_000_000, AutoApproveTakes: 100, AutoApproveMicro: 50_000_000}

// appLink is the network between the simulated worker and the app: Put fails like a dead app while putDown is set. The start
// handshake always passes (the worker reached the app when it began; the app died during the render).
type appLink struct {
	inner   atomic.Pointer[fake.HandlerUploader]
	putDown atomic.Bool
	puts    atomic.Int64 // puts that reached the app
	// dieAfter, when positive, is the number of puts the app still answers before it dies mid-upload (the worker then has some
	// artifacts at the app and the rest, with mm-result.json, in the bucket).
	dieAfter atomic.Int64
}

func (l *appLink) Start(ctx context.Context, url string, body []byte) (int, error) {
	return l.inner.Load().Start(ctx, url, body)
}

func (l *appLink) Put(ctx context.Context, url string, body []byte, digest string) (int, error) {
	if l.putDown.Load() {
		return 0, errors.New("connection refused")
	}
	l.puts.Add(1)
	if l.dieAfter.Load() > 0 && l.dieAfter.Add(-1) == 0 {
		l.putDown.Store(true)
	}
	return l.inner.Load().Put(ctx, url, body, digest)
}

// r2World is the bucket, the real fallback over it and the app link of one R2 timeline.
type r2World struct {
	bkt  *objstoretest.Server
	fb   *fallback.Fallback
	link *appLink
}

// newR2Env is newPropEnv with the upload fallback wired in: the runner mints and rescues, the worker has the channel and the
// capability `fallback` is open (a simulated backend with no gate row has every capability open).
func newR2Env(t *testing.T, seed uint64, profile string, opts ...any) (*propEnv, *r2World) {
	t.Helper()
	w := &r2World{bkt: objstoretest.New(t), link: &appLink{}}
	all := []any{
		func(o *fake.Options) {
			o.Worker = fake.NewSimWorker(fake.SimWorkerOptions{Uploader: w.link, Fallback: fake.HTTPFallback{Client: w.bkt.HTTPClient()},
				HeaderOnlyFLAC: headerOnly, TempDir: t.TempDir()})
		},
		rigOpt(func(r *rig, o *RunnerOptions) {
			if r.st == nil || r.up == nil {
				return // newRigAt first runs the options on a probe rig
			}
			w.link.inner.Store(&fake.HandlerUploader{UploadHandler: r.up.Handler(), StartHandler: r.up.StartHandler()})
			client, err := objstore.New(w.bkt.Config(), w.bkt.HTTPClient(), time.Now) // the bucket runs on the real clock
			if err != nil {
				t.Fatal(err)
			}
			fb, err := fallback.New(fallback.Options{Store: r.st, Uploads: r.up, Objects: client, Prefix: "mm/", Clock: r.clk, Log: slog.New(slog.DiscardHandler)})
			if err != nil {
				t.Fatal(err)
			}
			w.fb, o.Fallback, o.Rescue = fb, fb, fb
		}),
	}
	return newPropEnv(t, seed, profile, append(all, opts...)...), w
}

// viaCounts counts the received uploads of a job by the route they took.
func (r *rig) viaCounts(job string) (primary, fb int) {
	r.t.Helper()
	q := `SELECT COUNT(*) FROM uploads u JOIN job_attempts a ON a.id = u.attempt_id WHERE a.job_id = ? AND u.state IN ('received','consumed') AND u.via = ?`
	return r.count(q, job, "primary"), r.count(q, job, "fallback")
}

// noFallbackSecrets fails when a persisted row or an event carries anything of a presigned URL or the bucket credentials.
func (e *propEnv) noFallbackSecrets(w *r2World) {
	e.t.Helper()
	cfg := w.bkt.Config()
	for _, tbl := range []string{"job_events", "job_attempts", "uploads", "fallback_objects", "audit_log", "jobs", "batches", "ledger"} {
		rows, err := e.r.st.R.Query(`SELECT * FROM ` + tbl)
		if err != nil {
			e.t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			_ = rows.Scan(ptrs...)
			line := fmt.Sprint(vals...)
			for _, banned := range []string{"X-Amz", "Signature", cfg.SecretAccessKey, cfg.AccessKeyID, "127.0.0.1"} {
				if strings.Contains(line, banned) {
					e.t.Errorf("%s: %q reached a row", tbl, banned)
				}
			}
		}
		_ = rows.Close()
	}
}

// TestChaos_AppDownPastRetentionRescuedByR2: the app is unreachable while the worker renders and for longer than the provider keeps
// results (30 minutes). The worker's artifacts and mm-result.json went to the bucket; without it the take is unknown and settled by
// the time bound (TestChaos_ResultsPurgedDuringDowntime), with it the take completes from the bucket, once.
func TestChaos_AppDownPastRetentionRescuedByR2(t *testing.T) {
	t.Parallel()
	e, w := newR2Env(t, 1, "within", func(o *fake.Options) { o.Adversarial.PurgeResultsAfter30m = true })
	e.setCaps(r2Caps)
	e.capsOn = true
	if _, err := e.cur.Step(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.pass()
	w.link.putDown.Store(true)
	job := e.seedBatch("fake:slow=20", 1, 120).Jobs[0]
	e.drive(60, 5*time.Second, func() bool { s := e.r.state(job); return s == StateGenerating || s == StateStarting })
	e.r.clk.Advance(40 * time.Minute) // the runner is down; the provider purges the result
	e.takeOver()
	e.drive(120, 30*time.Second, e.jobTerminal(job))
	if got := e.r.state(job); got != StateCompleted {
		t.Fatalf("job %s (%v)\n%s", got, e.r.events(job), e.dumpStuck())
	}
	if n := e.tracks(); n != 1 {
		t.Errorf("%d tracks, want exactly one", n)
	}
	primary, fb := e.r.viaCounts(job)
	if primary != 0 || fb < 5 || fb != len(w.bkt.Keys()) {
		t.Errorf("uploads: %d primary, %d fallback", primary, fb)
	}
	if w.link.puts.Load() != 0 {
		t.Errorf("%d puts reached a dead app", w.link.puts.Load())
	}
	if n := e.r.count(`SELECT COUNT(*) FROM ledger WHERE job_id = ? AND kind = 'release' AND reason = 'never_ran'`, job); n != 0 {
		t.Error("never_ran released for a job that ran")
	}
	e.noFallbackSecrets(w)
}

// r2Quiet is a quiet R2 timeline: generous caps, no random events.
func r2Quiet(t *testing.T, opts ...any) (*propEnv, *r2World) {
	t.Helper()
	e, w := newR2Env(t, 1, "within", opts...)
	e.setCaps(r2Caps)
	e.capsOn = true
	if _, err := e.cur.Step(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.pass()
	return e, w
}

// TestChaos_AppDiesMidUploadMixedRoutes: the app answers the first artifacts and then dies; the worker switches to the bucket for the
// rest and for mm-result.json. The rescue commits only what the app does not have, and the take completes once.
func TestChaos_AppDiesMidUploadMixedRoutes(t *testing.T) {
	t.Parallel()
	e, w := r2Quiet(t)
	w.link.dieAfter.Store(3)
	job := e.seedBatch("", 1, 120).Jobs[0]
	e.drive(200, 20*time.Second, e.jobTerminal(job))
	if got := e.r.state(job); got != StateCompleted {
		t.Fatalf("job %s (%v)", got, e.r.events(job))
	}
	primary, fb := e.r.viaCounts(job)
	if primary != 3 || fb < 2 || primary+fb != len(w.bkt.Keys())+3 {
		t.Errorf("uploads: %d primary, %d fallback, %d objects in the bucket", primary, fb, len(w.bkt.Keys()))
	}
	if n := e.tracks(); n != 1 {
		t.Errorf("%d tracks", n)
	}
	e.noFallbackSecrets(w)
}

// TestChaos_AppAndBucketDown: with both routes dead the take fails (WORKER_LOST: the sim worker reports no result): nothing is received, no track appears
// and no result is invented.
func TestChaos_AppAndBucketDown(t *testing.T) {
	t.Parallel()
	e, w := r2Quiet(t)
	w.link.putDown.Store(true)
	w.bkt.FailNext(1_000_000, 503)
	job := e.seedBatch("", 1, 120).Jobs[0]
	e.drive(200, 20*time.Second, e.jobTerminal(job))
	if !e.jobTerminal(job)() {
		t.Fatalf("job still %s after the drive (%v)", e.r.state(job), e.r.events(job))
	}
	if got := e.r.state(job); got == StateCompleted {
		t.Fatalf("job completed with both routes dead (%v)", e.r.events(job))
	}
	if got := e.r.state(job); got != StateFailed {
		t.Fatalf("job %s, want failed (%v)", got, e.r.events(job))
	}
	if ev := e.r.events(job); ev[len(ev)-1] != "failed:WORKER_LOST" {
		t.Errorf("last event %q (%v)", ev[len(ev)-1], ev)
	}
	if n := e.tracks(); n != 0 {
		t.Fatalf("%d tracks while both routes were dead", n)
	}
	if n := e.r.count(`SELECT COUNT(*) FROM uploads WHERE state IN ('received','consumed')`); n != 0 {
		t.Fatalf("%d uploads received while both routes were dead", n)
	}
	e.noFallbackSecrets(w)
}
