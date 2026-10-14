package fake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
)

const (
	queueDelay     = 200 * time.Millisecond // a submitted job becomes visible to the scheduler after this
	startPollEvery = 15 * time.Second       // the worker re-polls its start URL this often while running
	fastRun        = time.Second
	defaultTimeout = 15 * time.Minute
)

type phase int

const (
	pQueued phase = iota
	pRunning
	pDone
)

type endKind int

const (
	endNormal endKind = iota
	endTimeout
	endLost
	endEarly // the handler stopped by itself (closed attempt, start failure, bad input)
)

// serve marks the moment a worker began executing a job; ownership of the worker's cost passes to that
// job's batch from then on (cold start belongs to the job that caused it, an idle tail to the last job
// served).
type serve struct {
	from  time.Time
	batch string
}

type worker struct {
	id           string
	start        time.Time
	ready        time.Time // end of the cold start
	stop         time.Time // zero while alive
	job          *job      // assigned (queued behind the cold start) or running
	idleSince    time.Time // valid while job == nil
	used         bool      // NoWarmReuse: served once, never again
	phantom      bool      // ExtraWorkersPerJob: billed, never serves
	stuck        bool      // fake:stuck_worker
	creatorBatch string
	serves       []serve
}

func (w *worker) alive() bool { return w.stop.IsZero() }

func (w *worker) status(now time.Time) string {
	switch {
	case w.stuck:
		return "RUNNING"
	case now.Before(w.ready):
		return "INITIALIZING"
	case w.job != nil && (w.job.phase == pRunning || w.job.zombie) && !now.Before(w.job.execStart):
		return "RUNNING"
	}
	return "IDLE"
}

type job struct {
	seq     int
	id      string
	spec    provider.TakeSpec
	mk      markers
	batch   string
	attempt int
	doomed  bool // fake:queue_expire: never scheduled

	submitted, eligible, ttlAt time.Time
	phase                      phase
	w                          *worker
	execStart                  time.Time

	runStart  time.Time
	endAt     time.Time
	endKind   endKind
	early     *Result
	requeueAt time.Time
	requeued  bool
	sess      *Session
	nextPoll  time.Time
	zombie    bool // reported finished (cancelled) while the handler still runs

	cancelPending bool
	cancelLeft    int

	state    provider.RemoteState
	raw      string
	execMS   *int64
	delayMS  *int64
	output   json.RawMessage
	errText  string
	workerID string
	finished time.Time
}

func (b *Backend) runDuration(j *job) time.Duration {
	switch {
	case j.mk.slow > 0:
		return j.mk.slow
	case b.o.Speed.Slow > 0:
		return b.o.Speed.Slow
	}
	return fastRun
}

func (b *Backend) timeout(j *job) time.Duration {
	if j.spec.TimeoutS > 0 {
		return time.Duration(j.spec.TimeoutS) * time.Second
	}
	return defaultTimeout
}

func (b *Backend) liveCount() int {
	n := 0
	for _, w := range b.workers {
		if w.alive() && !w.phantom && !w.stuck {
			n++
		}
	}
	return n
}

func (b *Backend) newWorker(j *job, t time.Time, cold time.Duration, phantom bool) *worker {
	b.wseq++
	w := &worker{id: fmt.Sprintf("fakew-%04d", b.wseq), start: t, ready: t.Add(cold), idleSince: t.Add(cold), phantom: phantom, creatorBatch: j.batch}
	b.workers = append(b.workers, w)
	return w
}

// release frees a worker whose job ended; it idles from now.
func (b *Backend) release(w *worker, now time.Time) {
	if w == nil {
		return
	}
	w.job = nil
	w.idleSince = now
	if now.Before(w.ready) {
		w.idleSince = w.ready
	}
	if b.o.Adversarial.NoWarmReuse {
		w.used = true
	}
}

// schedule assigns eligible queued jobs to workers (FIFO), starting cold workers up to the endpoint's max.
func (b *Backend) schedule(t time.Time) {
	for _, j := range b.jobs {
		if j.phase != pQueued || j.w != nil || j.doomed || j.eligible.After(t) {
			continue
		}
		var w *worker
		for _, c := range b.workers {
			if c.alive() && !c.stuck && !c.phantom && c.job == nil && !c.used {
				w = c
				break
			}
		}
		if w == nil {
			if b.liveCount() >= b.maxW {
				break // FIFO: nothing behind this job can start either
			}
			cold := time.Duration(b.o.ColdStartS) * time.Second
			if j.mk.hasCold {
				cold = j.mk.cold
			}
			w = b.newWorker(j, t, cold, false)
			if b.o.Adversarial.ExtraWorkersPerJob {
				b.newWorker(j, t, cold, true)
			}
		}
		w.job, j.w = j, w
		j.execStart = t
		if w.ready.After(t) {
			j.execStart = w.ready
		}
	}
}

type event struct {
	t    time.Time
	prio int
	seq  int
	fire func()
}

// Event priorities at equal times: expiry before start, ends before polls, idle stops last.
const (
	prioTTL = iota
	prioEnd
	prioRequeue
	prioPoll
	prioExec
	prioEligible
	prioIdle
)

func (b *Backend) nextEvent(ctx context.Context, limit time.Time) *event {
	var best *event
	add := func(t time.Time, prio, seq int, fire func()) {
		if t.After(limit) {
			return
		}
		if t.Before(b.now) {
			t = b.now
		}
		if best == nil || t.Before(best.t) || (t.Equal(best.t) && (prio < best.prio || (prio == best.prio && seq < best.seq))) {
			best = &event{t: t, prio: prio, seq: seq, fire: fire}
		}
	}
	for _, j := range b.jobs {
		j := j
		switch {
		case j.phase == pQueued:
			if !j.ttlAt.IsZero() {
				add(j.ttlAt, prioTTL, j.seq, func() { b.expire(j) })
			}
			if j.w != nil {
				add(j.execStart, prioExec, j.seq, func() { b.startExec(ctx, j) })
			} else if !j.doomed && j.eligible.After(b.now) {
				add(j.eligible, prioEligible, j.seq, func() {})
			}
		case j.phase == pRunning || j.zombie:
			add(j.endAt, prioEnd, j.seq, func() { b.endJob(ctx, j) })
			if !j.requeueAt.IsZero() && !j.requeued && j.phase == pRunning {
				add(j.requeueAt, prioRequeue, j.seq, func() { b.requeue(j) })
			}
			if j.sess != nil && j.endKind != endEarly {
				add(j.nextPoll, prioPoll, j.seq, func() { b.pollSession(ctx, j) })
			}
		}
	}
	for i, w := range b.workers {
		w := w
		if w.alive() && !w.stuck && w.job == nil {
			add(w.idleSince.Add(time.Duration(b.idleS)*time.Second), prioIdle, i, func() { w.stop = b.now })
		}
	}
	return best
}

// advance moves the simulation to `to`, firing every event due until then in time order.
func (b *Backend) advance(ctx context.Context, to time.Time) {
	if to.Before(b.now) {
		to = b.now
	}
	for {
		ev := b.nextEvent(ctx, to)
		if ev == nil {
			break
		}
		b.now = ev.t
		ev.fire()
		b.schedule(b.now)
	}
	b.now = to
	b.schedule(b.now)
}

func ms(d time.Duration) *int64 {
	v := d.Milliseconds()
	return &v
}

// finish records a job's terminal state as the provider reports it.
func (b *Backend) finish(j *job, st provider.RemoteState, raw string, exec *int64) {
	j.phase = pDone
	j.state, j.raw, j.execMS, j.finished = st, raw, exec, b.now
}

func (b *Backend) expire(j *job) {
	b.finish(j, provider.TimedOut, "TIMED_OUT", nil)
	if j.w != nil {
		b.release(j.w, b.now)
		j.w = nil
	}
}

func (b *Backend) startExec(ctx context.Context, j *job) {
	j.phase = pRunning
	j.runStart = b.now
	j.workerID = j.w.id
	j.w.serves = append(j.w.serves, serve{from: b.now, batch: j.batch})
	d := ms(b.now.Sub(j.submitted))
	j.delayMS = d
	run := b.runDuration(j)
	timeout := b.timeout(j)
	if b.o.Worker != nil {
		raw, _ := provider.BuildWorkerInput(j.spec)
		sess, early := b.o.Worker.Begin(ctx, raw, j.id, true)
		j.sess = sess
		if early != nil {
			j.early, j.endKind, j.endAt = early, endEarly, b.now.Add(early.Elapsed)
			return
		}
		j.nextPoll = b.now.Add(startPollEvery)
	}
	switch {
	case run > timeout:
		j.endKind, j.endAt = endTimeout, b.now.Add(timeout+time.Duration(b.o.KillLatencyMS)*time.Millisecond)
	case j.mk.failWorkerLost:
		j.endKind, j.endAt = endLost, b.now.Add(run)
	default:
		j.endKind, j.endAt = endNormal, b.now.Add(run)
	}
	if (j.mk.requeue || b.o.Adversarial.Requeue) && !j.requeued && j.endKind != endTimeout {
		j.requeueAt = b.now.Add(max(run/2, time.Millisecond))
	}
}

func (b *Backend) pollSession(ctx context.Context, j *job) {
	if j.sess.Poll(ctx) {
		j.endKind, j.early = endEarly, j.sess.closedResult()
		j.endAt = b.now
		return
	}
	j.nextPoll = j.nextPoll.Add(startPollEvery)
}

func (b *Backend) requeue(j *job) {
	w := j.w
	w.stop = b.now // the worker dies; its billing ends here
	w.job = nil
	j.w, j.sess, j.early = nil, nil, nil
	j.phase, j.requeued, j.eligible = pQueued, true, b.now
	j.requeueAt, j.endAt = time.Time{}, time.Time{}
}

func (b *Backend) endJob(ctx context.Context, j *job) {
	w := j.w
	defer func() {
		if j.zombie {
			j.zombie = false
		}
	}()
	if j.zombie { // already reported cancelled: the handler just stops
		if j.endKind == endNormal && j.sess != nil {
			_ = j.sess.Finish(ctx) // uploads meet a closed attempt; the result is discarded
		}
		if j.endKind == endLost {
			w.stop = b.now
		} else {
			b.release(w, b.now)
		}
		j.w = nil
		return
	}
	exec := ms(b.now.Sub(j.runStart))
	switch j.endKind {
	case endTimeout:
		timeoutMS := b.timeout(j).Milliseconds() + b.o.KillLatencyMS
		b.finish(j, provider.TimedOut, "TIMED_OUT", &timeoutMS)
		b.release(w, b.now)
	case endLost:
		b.finish(j, provider.Failed, "FAILED", nil)
		j.errText = "fake: worker lost"
		w.stop = b.now
	case endEarly:
		b.finish(j, provider.Failed, "FAILED", exec)
		j.output, j.errText = j.early.Raw, j.early.ErrText
		b.release(w, b.now)
	default:
		if j.sess == nil {
			if j.mk.failWorkerError {
				b.finish(j, provider.Failed, "FAILED", exec)
				j.errText = "fake: simulated pipeline failure"
			} else {
				b.finish(j, provider.Completed, "COMPLETED", exec)
			}
		} else if res := j.sess.Finish(ctx); res.Failed {
			b.finish(j, provider.Failed, "FAILED", exec)
			j.output, j.errText = res.Raw, res.ErrText
		} else {
			b.finish(j, provider.Completed, "COMPLETED", exec)
			j.output = res.Raw
		}
		b.release(w, b.now)
	}
	j.w, j.sess = nil, nil
}

// ---- backend methods -------------------------------------------------------------------------------

// Submit implements provider.Backend.
func (b *Backend) Submit(ctx context.Context, t provider.TakeSpec) (provider.Ref, error) {
	b.n.submit.Add(1)
	if b.revoked() {
		return provider.Ref{}, &Error{Op: "submit", Status: 401, Code: Unauthorized}
	}
	now := b.enter(ctx)
	defer b.mu.Unlock()
	mk := parseMarkers(styleOf(t.Request))
	b.attempts[t.JobID]++
	n := b.attempts[t.JobID]
	if n == 1 {
		if fe := submitFault(mk.submit); fe != nil {
			return provider.Ref{}, fe
		}
	}
	b.seq++
	sum := sha256.Sum256([]byte(t.JobID + "/" + t.AttemptID))
	j := &job{
		seq: b.seq, id: fmt.Sprintf("fake-%06d-%s", b.seq, hex.EncodeToString(sum[:4])), spec: t, mk: mk, attempt: n,
		batch: t.JobID, submitted: now, eligible: now.Add(queueDelay), doomed: n <= mk.queueExpire, state: provider.Queued, raw: "IN_QUEUE",
	}
	if b.o.BatchOf != nil {
		j.batch = b.o.BatchOf(t.JobID)
	}
	if t.TTL > 0 {
		j.ttlAt = now.Add(t.TTL)
	}
	b.jobs = append(b.jobs, j)
	b.byID[j.id] = j
	if mk.stuckWorker && !b.hasStuck() {
		b.wseq++
		b.workers = append(b.workers, &worker{id: fmt.Sprintf("fakew-%04d", b.wseq), start: now, ready: now, stuck: true, phantom: true, creatorBatch: j.batch})
	}
	b.applyDrift()
	if (n == 1 && mk.submit == "accepted_5xx") || b.acceptThenFail(t.JobID, t.AttemptID) {
		return provider.Ref{}, acceptedThenFailed()
	}
	return provider.Ref{Backend: provider.BackendFake, ID: j.id}, nil
}

func (b *Backend) hasStuck() bool {
	for _, w := range b.workers {
		if w.stuck && w.alive() {
			return true
		}
	}
	return false
}

func (b *Backend) lookup(r provider.Ref) *job {
	if r.Backend != provider.BackendFake {
		return nil
	}
	return b.byID[r.ID]
}

// Poll implements provider.Backend.
func (b *Backend) Poll(ctx context.Context, r provider.Ref) (provider.RemoteStatus, error) {
	b.n.poll.Add(1)
	if b.revoked() {
		return provider.RemoteStatus{}, provider.ErrAccess
	}
	now := b.enter(ctx)
	defer b.mu.Unlock()
	j := b.lookup(r)
	if j == nil {
		return provider.RemoteStatus{State: provider.NotFound}, nil
	}
	if j.phase == pDone && b.o.Adversarial.PurgeResultsAfter30m && !now.Before(j.finished.Add(resultRetention)) {
		return provider.RemoteStatus{State: provider.NotFound}, nil
	}
	if j.cancelPending && j.phase == pRunning {
		if j.cancelLeft > 0 {
			j.cancelLeft--
		} else {
			j.cancelPending = false
			b.cancelNow(j)
		}
	}
	return b.statusOf(j), nil
}

func (b *Backend) statusOf(j *job) provider.RemoteStatus {
	switch j.phase {
	case pQueued:
		return provider.RemoteStatus{State: provider.Queued, Raw: "IN_QUEUE"}
	case pRunning:
		if j.mk.runBetweenPolls {
			return provider.RemoteStatus{State: provider.Queued, Raw: "IN_QUEUE"}
		}
		return provider.RemoteStatus{State: provider.Running, Raw: "IN_PROGRESS", DelayMS: j.delayMS, WorkerID: j.workerID}
	}
	return provider.RemoteStatus{State: j.state, Raw: j.raw, ExecutionMS: j.execMS, DelayMS: j.delayMS, WorkerID: j.workerID,
		Output: j.output, ErrorText: j.errText}
}

// Cancel implements provider.Backend: acknowledged once the request was accepted; a finished or unknown
// job is a no-op.
func (b *Backend) Cancel(ctx context.Context, r provider.Ref) error {
	b.n.cancel.Add(1)
	if b.revoked() {
		return provider.ErrAccess
	}
	b.enter(ctx)
	defer b.mu.Unlock()
	j := b.lookup(r)
	if j == nil || j.phase == pDone {
		return nil
	}
	if j.phase == pRunning && j.mk.cancelIgnored > 0 && !j.cancelPending {
		j.cancelPending, j.cancelLeft = true, j.mk.cancelIgnored
		return nil
	}
	b.cancelNow(j)
	return nil
}

func (b *Backend) cancelNow(j *job) {
	switch j.phase {
	case pQueued:
		b.finish(j, provider.Cancelled, "CANCELLED", nil)
		if j.w != nil {
			b.release(j.w, b.now)
			j.w = nil
		}
	case pRunning:
		b.finish(j, provider.Cancelled, "CANCELLED", ms(b.now.Sub(j.runStart)))
		if j.mk.cancelAckKeeps || b.o.Adversarial.CancelAckButKeepRunning {
			j.zombie = true // acknowledged, but the handler keeps running until its next start poll
			return
		}
		b.release(j.w, b.now)
		j.w, j.sess = nil, nil
	}
}

// PurgeQueue implements provider.Backend: queued jobs are cancelled; running ones are untouched.
func (b *Backend) PurgeQueue(ctx context.Context) error {
	b.n.purge.Add(1)
	if b.revoked() {
		return provider.ErrAccess
	}
	b.enter(ctx)
	defer b.mu.Unlock()
	for _, j := range b.jobs {
		if j.phase == pQueued {
			b.cancelNow(j)
		}
	}
	return nil
}
