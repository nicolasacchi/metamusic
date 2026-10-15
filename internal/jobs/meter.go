package jobs

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// meterSweepGap is the one sweep interval added to a lifetime once it has vanished from a sweep
// (C76.6): conservative, because the worker may have run until just before the sweep missed it.
const meterSweepGap = 10 * time.Second

// workerLive reports whether a RunPod worker status means the worker still exists (and may bill).
func workerLive(status string) bool {
	switch strings.ToUpper(status) {
	case "RUNNING", "IDLE", "INITIALIZING", "THROTTLED", "UNHEALTHY":
		return true
	}
	return false
}

// lifetime is one worker_meter row; uptimeS is billable seconds observed (see meterbillable.go).
type lifetime struct {
	key               string
	started, lastSeen time.Time
	uptimeS           int64
	status            string
}

type meterAttempt struct {
	id, batchID, key        string
	submitted               time.Time
	live                    bool
	ranEvidence             bool
	terminalAt              *time.Time
	execMS                  *int64
	timeBound               bool
	jobStarted, announcedAt *time.Time
	timeoutS, killSlackS    int
}

type meterBatch struct {
	id                  string
	created             time.Time
	finished            *time.Time
	idleS               int
	price               money.MicroUSD
	reserved, execSettl money.MicroUSD
}

// meterModel is the attribution of C76.6 over one snapshot of the database.
type meterModel struct {
	now         time.Time
	lastSweep   time.Time
	lifetimes   []lifetime
	attempts    []meterAttempt
	batches     []meterBatch   // unreconciled batches, oldest first
	owner       map[int]string // lifetime index -> batch id ("" = unattributed)
	overheadS   map[string]int64
	unattribL   int64
	lastEnd     map[string]time.Time
	lifetimesOf map[string][]int
}

func (m *meterModel) lifeSeconds(i int) int64 {
	l := m.lifetimes[i]
	s := l.uptimeS
	if m.vanished(i) {
		s += int64(meterSweepGap / time.Second)
	}
	return s
}

// vanished: seen by an earlier sweep but not by the latest one, or no longer a live status.
func (m *meterModel) vanished(i int) bool {
	l := m.lifetimes[i]
	if !workerLive(l.status) {
		return true
	}
	return !m.lastSweep.IsZero() && l.lastSeen.Before(m.lastSweep)
}

func (m *meterModel) execSeconds(a meterAttempt) int64 {
	switch {
	case a.live:
		if !a.ranEvidence {
			return 0
		}
		from := a.submitted
		if a.announcedAt != nil {
			from = *a.announcedAt
		} else if a.jobStarted != nil {
			from = *a.jobStarted
		}
		d := int64(m.now.Sub(from) / time.Second)
		return max(0, min(d, int64(a.timeoutS+a.killSlackS)))
	case a.execMS != nil:
		return (*a.execMS + 999) / 1000
	case a.timeBound:
		return int64(a.timeoutS + a.killSlackS)
	}
	return 0
}

// loadMeter reads the snapshot and computes attribution and per-batch overhead.
func loadMeter(ctx context.Context, x Querier, now, lastSweep time.Time) (*meterModel, error) {
	m := &meterModel{now: now, lastSweep: lastSweep, owner: map[int]string{}, overheadS: map[string]int64{}, lastEnd: map[string]time.Time{}, lifetimesOf: map[string][]int{}}
	since, err := meterBillableSince(ctx, x)
	if err != nil {
		return nil, err
	}
	// rows first seen before billable_since carry the raw listing uptime (not billable): ignored
	rows, err := x.QueryContext(ctx, `SELECT worker_key, started_at, first_seen_at, last_seen_at, uptime_s, status_last FROM worker_meter WHERE first_seen_at >= ? ORDER BY started_at, worker_key`, since)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var l lifetime
		var st, fs, ls string
		if err := rows.Scan(&l.key, &st, &fs, &ls, &l.uptimeS, &l.status); err != nil {
			rows.Close()
			return nil, err
		}
		// the lifetime starts when it first became billable, never before the record was first metered
		l.started, l.lastSeen = mustT(st), mustT(ls)
		if f := mustT(fs); f.After(l.started) {
			l.started = f
		}
		m.lifetimes = append(m.lifetimes, l)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	brows, err := x.QueryContext(ctx, `SELECT b.id, b.created_at, b.finished_at, b.idle_s, b.price_pph_micro_usd,
		COALESCE((SELECT SUM(micro_usd) FROM ledger WHERE batch_id = b.id AND kind = 'reserve'), 0),
		COALESCE((SELECT SUM(micro_usd) FROM ledger WHERE batch_id = b.id AND kind = 'settle' AND reason IN ('execution','execution_unknown','excess')), 0)
		FROM batches b WHERE b.reconciled_at IS NULL AND b.state IN ('active','finished') ORDER BY b.created_at, b.id`)
	if err != nil {
		return nil, err
	}
	for brows.Next() {
		var b meterBatch
		var cr string
		var fin sql.NullString
		var price, res, set int64
		if err := brows.Scan(&b.id, &cr, &fin, &b.idleS, &price, &res, &set); err != nil {
			brows.Close()
			return nil, err
		}
		b.created, b.finished, b.price, b.reserved, b.execSettl = mustT(cr), parseT(fin), money.MicroUSD(price), money.MicroUSD(res), money.MicroUSD(set)
		m.batches = append(m.batches, b)
	}
	if err := brows.Close(); err != nil {
		return nil, err
	}
	arows, err := x.QueryContext(ctx, `SELECT a.id, j.batch_id, a.worker_key, a.submitted_at, a.state, a.in_progress_seen, a.announced_at, a.terminal_at,
		a.execution_ms, a.terminal_basis, j.started_at, j.timeout_s, bt.kill_slack_s
		FROM job_attempts a JOIN jobs j ON j.id = a.job_id JOIN batches bt ON bt.id = j.batch_id
		WHERE a.worker_key IS NOT NULL AND a.worker_key <> '' ORDER BY a.submitted_at, a.id`)
	if err != nil {
		return nil, err
	}
	for arows.Next() {
		var a meterAttempt
		var sub, state string
		var prog int
		var ann, term, basis, started sql.NullString
		var exec sql.NullInt64
		if err := arows.Scan(&a.id, &a.batchID, &a.key, &sub, &state, &prog, &ann, &term, &exec, &basis, &started, &a.timeoutS, &a.killSlackS); err != nil {
			arows.Close()
			return nil, err
		}
		a.submitted, a.live = mustT(sub), state == "pending" || state == "submitted" || state == "unknown"
		a.announcedAt, a.terminalAt, a.jobStarted = parseT(ann), parseT(term), parseT(started)
		a.ranEvidence = prog == 1 || a.announcedAt != nil
		if exec.Valid {
			a.execMS = &exec.Int64
		}
		a.timeBound = basis.String == "time_bound"
		m.attempts = append(m.attempts, a)
	}
	if err := arows.Close(); err != nil {
		return nil, err
	}
	m.attribute()
	return m, nil
}

func (m *meterModel) batchByID(id string) *meterBatch {
	for i := range m.batches {
		if m.batches[i].id == id {
			return &m.batches[i]
		}
	}
	return nil
}

// lifetimeOf picks the lifetime an attempt ran on: the latest lifetime of its worker key that started
// no later than the attempt's end (or now while it is live).
func (m *meterModel) lifetimeOf(a meterAttempt) int {
	at := m.now
	if a.terminalAt != nil {
		at = *a.terminalAt
	}
	best, first := -1, -1
	for i, l := range m.lifetimes {
		if l.key != a.key {
			continue
		}
		if first < 0 || l.started.Before(m.lifetimes[first].started) {
			first = i
		}
		if !l.started.After(at) && (best < 0 || l.started.After(m.lifetimes[best].started)) {
			best = i
		}
	}
	if best < 0 {
		best = first // a lifetime first seen after the attempt ended still carried it
	}
	return best
}

func (m *meterModel) attribute() {
	execOn := map[int]int64{}
	earliest := map[int]meterAttempt{}
	for _, a := range m.attempts {
		i := m.lifetimeOf(a)
		if i < 0 {
			continue
		}
		execOn[i] += m.execSeconds(a)
		if b := m.batchByID(a.batchID); b != nil {
			if cur, ok := earliest[i]; !ok || a.submitted.Before(cur.submitted) {
				earliest[i] = a
			}
		}
		if a.terminalAt != nil {
			if t, ok := m.lastEnd[a.batchID]; !ok || a.terminalAt.After(t) {
				m.lastEnd[a.batchID] = *a.terminalAt
			}
		} else if a.live {
			m.lastEnd[a.batchID] = m.now
		}
	}
	for i, l := range m.lifetimes {
		owner := ""
		if a, ok := earliest[i]; ok {
			owner = a.batchID
		} else {
			for _, b := range m.batches {
				active := !l.started.Before(b.created) && (b.finished == nil || !l.started.After(b.finished.Add(time.Duration(b.idleS+60)*time.Second)))
				if active {
					owner = b.id
					break
				}
			}
		}
		m.owner[i] = owner
		over := max(0, m.lifeSeconds(i)-execOn[i])
		if owner == "" {
			if len(m.batches) > 0 && !l.started.Before(m.batches[0].created) {
				m.unattribL += m.lifeSeconds(i)
			}
			continue
		}
		m.overheadS[owner] += over
		m.lifetimesOf[owner] = append(m.lifetimesOf[owner], i)
	}
}

// OverheadSeconds implements BatchMeter (C76.6). The batch is ready when every worker lifetime attributed
// to it has ended or its last attempt ended at least idle_s + 60 s ago; a batch whose attempts never
// touched a worker settles zero overhead at once.
func (r *Reconciler) OverheadSeconds(ctx context.Context, x Querier, batchID string) (int64, bool, error) {
	now := r.o.Clock.Now()
	m, err := loadMeter(ctx, x, now, r.lastSweepAt())
	if err != nil {
		return 0, false, err
	}
	var idle int
	var created string
	if err := x.QueryRowContext(ctx, `SELECT idle_s, created_at FROM batches WHERE id = ?`, batchID).Scan(&idle, &created); err != nil {
		return 0, false, err
	}
	var submitted int
	if err := x.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_attempts a JOIN jobs j ON j.id = a.job_id WHERE j.batch_id = ? AND a.state <> 'rejected'`, batchID).Scan(&submitted); err != nil {
		return 0, false, err
	}
	secs := m.overheadS[batchID]
	if submitted == 0 && len(m.lifetimesOf[batchID]) == 0 {
		return 0, true, nil
	}
	allEnded := true
	for _, i := range m.lifetimesOf[batchID] {
		if !m.vanished(i) {
			allEnded = false
		}
	}
	lastEnd, ok := m.lastEnd[batchID]
	waited := ok && !now.Before(lastEnd.Add(time.Duration(idle+60)*time.Second))
	if len(m.lifetimesOf[batchID]) == 0 {
		return secs, waited, nil // attempts ran but no worker has been seen yet: give the sweeps time
	}
	return secs, allEnded || waited, nil
}

// meterOver is the C76.6 trip test: metered worker time above what the open batches reserved.
func (r *Reconciler) meterOver(ctx context.Context) (over bool, detail map[string]any, err error) {
	m, err := loadMeter(ctx, r.o.Store.R, r.o.Clock.Now(), r.lastSweepAt())
	if err != nil {
		return false, nil, err
	}
	var used, reserved money.MicroUSD
	var maxPrice money.MicroUSD
	for _, b := range m.batches {
		used += b.execSettl + money.ForSeconds(m.overheadS[b.id], b.price)
		reserved += b.reserved
		maxPrice = max(maxPrice, b.price)
	}
	if m.unattribL > 0 {
		used += money.ForSeconds(m.unattribL, maxPrice)
	}
	return used > reserved && reserved > 0, map[string]any{"used_micro_usd": int64(used), "reserved_micro_usd": int64(reserved)}, nil
}

// MeterStats summarises the last 24 hours for get_provider_status.
// BilledSeconds is the sum of billable seconds observed (RUNNING plus the idle tail), not listing uptime.
type MeterStats struct{ Lifetimes, BilledSeconds, MaxWorkers int }

func meterStats(ctx context.Context, x Querier, now time.Time) (MeterStats, error) {
	var st MeterStats
	since := store.FormatTime(now.Add(-24 * time.Hour))
	trusted, err := meterBillableSince(ctx, x)
	if err != nil {
		return st, err
	}
	rows, err := x.QueryContext(ctx, `SELECT started_at, last_seen_at, uptime_s FROM worker_meter WHERE last_seen_at >= ? AND first_seen_at >= ? ORDER BY started_at`, since, trusted)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	type span struct{ from, to time.Time }
	var spans []span
	for rows.Next() {
		var s, l string
		var up int64
		if err := rows.Scan(&s, &l, &up); err != nil {
			return st, err
		}
		st.Lifetimes++
		st.BilledSeconds += int(up)
		spans = append(spans, span{mustT(s), mustT(l)})
	}
	// the most workers alive at once: sweep the interval endpoints
	type ev struct {
		at time.Time
		d  int
	}
	var evs []ev
	for _, s := range spans {
		evs = append(evs, ev{s.from, 1}, ev{s.to.Add(time.Nanosecond), -1})
	}
	sort.Slice(evs, func(i, j int) bool {
		if evs[i].at.Equal(evs[j].at) {
			return evs[i].d < evs[j].d
		}
		return evs[i].at.Before(evs[j].at)
	})
	cur := 0
	for _, e := range evs {
		cur += e.d
		st.MaxWorkers = max(st.MaxWorkers, cur)
	}
	return st, rows.Err()
}
