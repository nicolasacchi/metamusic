package jobs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/fake"

	"github.com/nicolasacchi/metamusic/internal/spend"
)

func spendCaps(m int64) spend.Caps {
	return spend.Caps{MonthlyMicro: money.MicroUSD(m), BatchMicro: money.MicroUSD(m), HumanDailyMicro: money.MicroUSD(m), AgentDailyMicro: money.MicroUSD(m), AutoApproveTakes: 100, AutoApproveMicro: money.MicroUSD(m)}
}

func (e *twoEnv) trips(backend string) int {
	return e.r.count(`SELECT COUNT(*) FROM provider_trips WHERE backend = ?`, backend)
}

func (e *twoEnv) liveWorkers(b *fake.Backend) int {
	rep, err := b.Sweep(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	return len(rep.Workers)
}

func (e *twoEnv) wantOutstandingZero() {
	e.t.Helper()
	if out, _, _, err := outstandingBy(e.r.st.R, ""); err != nil || out != 0 {
		e.t.Errorf("outstanding %d after settlement (%v)", out, err)
	}
}

// TestChaos_TripOneBackendOnly: an ACE-Step worker that cannot load trips the ACE-Step backend (main's evidence rule); the YuE2
// backend is untouched: its jobs, before and after the trip, complete, no trip row names it, no new ACE-Step job is claimed, and the
// ledger invariants (checked after every commit) hold throughout.
func TestChaos_TripOneBackendOnly(t *testing.T) {
	t.Parallel()
	e := newTwoEnv(t, 11)
	e.quietCaps()
	stuck := e.seedACE("fake:init_fail", 1, 60).Jobs[0]
	yue1 := e.seedYue("", 2, 60).Jobs
	e.drive2(80, 20*time.Second, func() bool {
		return e.trips(aceBackendName) > 0 && e.r.state(yue1[0]) == StateCompleted && e.r.state(yue1[1]) == StateCompleted
	})
	if n := e.trips(provider.BackendFake); n != 0 {
		t.Errorf("%d trips on the YuE2 backend", n)
	}
	if reason := e.r.str(`SELECT reason FROM provider_trips WHERE backend = ?`, aceBackendName); reason != TripCrashLoop {
		t.Errorf("ACE-Step trip reason %q, want %s", reason, TripCrashLoop)
	}
	if got := e.r.state(stuck); got == StateCompleted {
		t.Errorf("the stuck ACE-Step job completed")
	}
	// after the trip: a new ACE-Step job is not claimed, a new YuE2 job still runs
	submits := e.ace.Counters().Submit
	late := e.seedACE("", 1, 60).Jobs[0]
	yue2 := e.seedYue("", 1, 60).Jobs[0]
	e.drive2(60, 20*time.Second, e.jobTerminal(yue2))
	if got := e.r.state(yue2); got != StateCompleted {
		t.Fatalf("YuE2 job after the ACE-Step trip: %s (%v)", got, e.r.events(yue2))
	}
	if got := e.r.state(late); got != StateQueued {
		t.Errorf("a new ACE-Step job on a tripped backend is %s, want queued", got)
	}
	if e.ace.Counters().Submit != submits {
		t.Errorf("the tripped backend received %d more submits", e.ace.Counters().Submit-submits)
	}
	if n := e.trips(provider.BackendFake); n != 0 {
		t.Errorf("%d trips on the YuE2 backend at the end", n)
	}
}

// TestChaos_AceStepInitFailure: fake:init_fail keeps an UNHEALTHY worker; the reconciler trips on that evidence only after the
// grace (never earlier), the trip leaves the backend with zero workers, nothing more is claimed there, and once the stuck job ends
// every reserved amount is released (outstanding zero).
func TestChaos_AceStepInitFailure(t *testing.T) {
	t.Parallel()
	e := newTwoEnv(t, 12)
	e.quietCaps()
	job := e.seedACE("fake:init_fail", 1, 60).Jobs[0]
	// no trip inside the grace window
	e.ticks(3, 30*time.Second)
	if e.trips(aceBackendName) != 0 {
		t.Fatalf("tripped inside the unhealthy grace")
	}
	e.drive2(80, 20*time.Second, func() bool { return e.trips(aceBackendName) > 0 })
	submits := e.ace.Counters().Submit
	if submits == 0 {
		t.Fatal("the stuck job never reached the backend")
	}
	e.ticks(30, 30*time.Second)
	if n := e.liveWorkers(e.ace); n != 0 {
		t.Errorf("%d workers on the tripped ACE-Step backend", n)
	}
	e.seedACE("", 1, 60)
	e.ticks(10, 30*time.Second)
	if e.ace.Counters().Submit != submits {
		t.Errorf("claims continued after the trip: %d -> %d submits", submits, e.ace.Counters().Submit)
	}
	// the stuck job ends (TTL / cancel); everything reserved is released after reconciliation
	e.r.exec(`UPDATE provider_state SET armed = 1 WHERE backend = ?`, aceBackendName)
	for _, id := range e.openJobs() {
		_, _ = e.r.service(e.ctx).CancelJob(e.ctx, auth.Principal{Kind: auth.KindHuman, ID: "h1", Name: "h1"}, id, "")
	}
	e.drive2(200, 10*time.Minute, e.allTerminal)
	for range 60 {
		e.r.clk.Advance(2 * time.Hour)
		e.step()
		e.pass()
		if e.r.count(`SELECT COUNT(*) FROM batches WHERE reconciled_at IS NULL`) == 0 {
			break
		}
	}
	e.wantOutstandingZero()
	if got := e.r.state(job); got == StateCompleted {
		t.Errorf("job on a worker that cannot load completed")
	}
}

// TestChaos_LongInitializingThenReady: an ACE-Step worker that initialises for 40 minutes (a first model pull) raises one warning,
// never trips, and then serves the job.
func TestChaos_LongInitializingThenReady(t *testing.T) {
	t.Parallel()
	e := newTwoEnv(t, 13)
	e.quietCaps()
	job := e.seedACE("fake:slow_load=2400", 1, 60).Jobs[0]
	e.drive2(400, 30*time.Second, e.jobTerminal(job))
	if got := e.r.state(job); got != StateCompleted {
		t.Fatalf("job %s (%v)", got, e.r.events(job))
	}
	if n := e.trips(aceBackendName) + e.trips(provider.BackendFake); n != 0 {
		t.Errorf("%d trips on a slow but healthy start", n)
	}
	warnings := 0
	for _, title := range e.r.noteTitles() {
		if title == "Worker still initializing" {
			warnings++
		}
	}
	if warnings != 1 {
		t.Errorf("warnings %d, want 1 (notes %v)", warnings, e.r.noteTitles())
	}
}

// wantOutstandingAfterFinal reconciles past the billing lag and the residual hold, then expects nothing outstanding.
func (e *twoEnv) wantOutstandingAfterFinal() {
	e.t.Helper()
	for range 80 {
		e.r.clk.Advance(2 * time.Hour)
		e.step()
		e.pass()
		if e.r.count(`SELECT COUNT(*) FROM batches WHERE reconciled_at IS NULL`) == 0 {
			break
		}
	}
	e.wantOutstandingZero()
}

func (e *twoEnv) openJobs() []string {
	rows, err := e.r.st.R.Query(`SELECT id FROM jobs WHERE state NOT IN ('completed','failed','cancelled','expired') ORDER BY id`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	return ids
}

// ticks runs n runner/reconciler ticks of dt.
func (e *twoEnv) ticks(n int, dt time.Duration) {
	for i := 0; i < n; i++ {
		e.r.clk.Advance(dt)
		if e.step() {
			e.r.clk.Advance(100 * time.Second)
			e.takeOver2()
		}
		if i%3 == 0 {
			e.pass()
		}
	}
}

// refRequest is a reference render: a 30 s clip pinned by digest is fetched by the worker through a minted link.
func refRequest(t *testing.T, sha string) string {
	return aceRequest(t, map[string]any{"id": "ace-ref-" + sha[:8], "seed": 1, "caption": "synth pop", "thinking": false, "bpm": nil, "duration_ds": 300,
		"ref": map[string]any{"track_id": "01900000-0000-7000-8000-00000000aaaa", "clip_sha256": sha, "from_ds": 50, "to_ds": 250}})
}

func sum256(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// TestChaos_InputFetchMismatch: the worker's fetched reference does not match its pinned digest: it answers INPUT_MISMATCH, the job
// fails at once with its minimal cost settled, and nothing is retried (one submit, one attempt, one fetch).
func TestChaos_InputFetchMismatch(t *testing.T) {
	t.Parallel()
	e := newTwoEnv(t, 14)
	e.quietCaps()
	clip := []byte("reference clip bytes")
	e.minter = func(json.RawMessage) (map[string]provider.InputRef, error) {
		return map[string]provider.InputRef{provider.InputReferenceAudio: {URL: "http://mm.example.com/media/x.flac?exp=1&sig=s", SHA256: sum256(clip), Bytes: int64(len(clip))}}, nil
	}
	e.fetch = func(string) (int, []byte, error) { return 200, []byte("another clip entirely"), nil }
	job := e.seedACERecipe(e.aceRecipeReq("ref-bad", refRequest(t, sum256(clip))), 1, 60).Jobs[0]
	e.drive2(120, 20*time.Second, e.jobTerminal(job))
	// a long quiet stretch: a retry storm would show as more submits
	e.ticks(60, 30*time.Second)
	if got := e.r.state(job); got != StateFailed {
		t.Fatalf("job %s (%v)", got, e.r.events(job))
	}
	// the worker's INPUT_MISMATCH arrives in the handler's wrapped form, which the classifier maps to WORKER_LOST (only DEADLINE
	// has a code of its own); the pinned fact is that the job fails once and is not retried
	if code := e.r.job(job).ErrorCode; code != CodeWorkerLost && code != CodeWorkerError {
		t.Errorf("error code %q", code)
	}
	if n := e.ace.Counters().Submit; n != 1 {
		t.Errorf("submits %d, want 1", n)
	}
	if n := e.r.count(`SELECT COUNT(*) FROM job_attempts WHERE job_id = ?`, job); n != 1 {
		t.Errorf("attempts %d, want 1", n)
	}
	if e.fetches != 1 {
		t.Errorf("fetches %d, want 1", e.fetches)
	}
	if n := e.r.count(`SELECT COUNT(*) FROM tracks WHERE origin = 'job'`); n != 0 {
		t.Errorf("%d tracks from a failed input check", n)
	}
	exec := e.r.sum(`SELECT COALESCE(SUM(micro_usd),0) FROM ledger WHERE job_id = ? AND kind = 'settle' AND reason = 'execution'`, job)
	if max := int64(money.ForSeconds(60, propPrice)); exec > max {
		t.Errorf("settled %d for a job that failed before rendering, want at most %d", exec, max)
	}
	if n := e.trips(aceBackendName); n != 0 {
		t.Errorf("an input mismatch tripped the backend")
	}
}

// TestChaos_RestartDuringReferenceRender: the runner dies while a reference render runs. The new instance adopts the attempt through
// the persisted ref (no second submit, no second signing), the render completes with its fetched input verified, and no link is ever
// stored.
func TestChaos_RestartDuringReferenceRender(t *testing.T) {
	t.Parallel()
	e := newTwoEnv(t, 15)
	e.quietCaps()
	clip := []byte("reference clip bytes")
	const link = "http://mm.example.com/media/x.flac?exp=1999999999&sig=SECRETSIG"
	e.minter = func(json.RawMessage) (map[string]provider.InputRef, error) {
		return map[string]provider.InputRef{provider.InputReferenceAudio: {URL: link, SHA256: sum256(clip), Bytes: int64(len(clip))}}, nil
	}
	e.fetch = func(string) (int, []byte, error) { return 200, clip, nil }
	job := e.seedACERecipe(e.aceRecipeReq("ref-ok", refRequest(t, sum256(clip))), 1, 60).Jobs[0]
	e.r.clk.Advance(5 * time.Second)
	e.step() // claims and submits; the ref is persisted
	if ref := e.r.str(`SELECT COALESCE(ref,'') FROM job_attempts WHERE job_id = ? AND n = 1`, job); ref == "" {
		t.Fatal("the ref was not persisted by the first step")
	}
	if e.ace.Counters().Submit != 1 {
		t.Fatalf("submits %d after the first step", e.ace.Counters().Submit)
	}
	// the runner process dies; its lease lapses; a new instance takes over
	e.r.clk.Advance(100 * time.Second)
	e.takeOver2()
	e.drive2(120, 20*time.Second, e.jobTerminal(job))
	if got := e.r.state(job); got != StateCompleted {
		t.Fatalf("job %s (%v)", got, e.r.events(job))
	}
	if n := e.ace.Counters().Submit; n != 1 {
		t.Errorf("submits %d, want exactly 1 across the restart", n)
	}
	if e.mints != 1 {
		t.Errorf("inputs signed %d times, want once", e.mints)
	}
	if e.fetches != 1 {
		t.Errorf("worker fetches %d, want 1", e.fetches)
	}
	if n := e.r.count(`SELECT COUNT(*) FROM job_attempts WHERE job_id = ?`, job); n != 1 {
		t.Errorf("attempts %d, want 1", n)
	}
	db := dumpDB(t, e.r)
	for _, leak := range []string{"SECRETSIG", "sig=", "exp=", link} {
		if strings.Contains(db, leak) {
			t.Errorf("the database holds %q", leak)
		}
	}
	e.wantOutstandingAfterFinal()
}
