package jobs

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestTransitions_GoEqualsSQL(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rows, err := r.st.R.Query(`SELECT from_state, to_state FROM job_transitions ORDER BY from_state, to_state`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var sqlPairs []string
	for rows.Next() {
		var a, b string
		if err := rows.Scan(&a, &b); err != nil {
			t.Fatal(err)
		}
		sqlPairs = append(sqlPairs, a+">"+b)
	}
	var goPairs []string
	for _, tr := range Transitions {
		goPairs = append(goPairs, tr.From+">"+tr.To)
	}
	slices.Sort(goPairs)
	if len(sqlPairs) != 34 || !slices.Equal(sqlPairs, goPairs) {
		t.Errorf("Go table (%d) differs from job_transitions (%d)\n go: %v\nsql: %v", len(goPairs), len(sqlPairs), goPairs, sqlPairs)
	}
	for _, tr := range Transitions {
		if len(tr.Rows) == 0 {
			t.Errorf("%s>%s names no table row", tr.From, tr.To)
		}
	}
}

// pathTo lists the states that lead from `queued` to a state through legal moves only.
var pathTo = map[string][]string{
	StateQueued:       nil,
	StateSubmitting:   {StateSubmitting},
	StateStarting:     {StateSubmitting, StateStarting},
	StateGenerating:   {StateSubmitting, StateStarting, StateGenerating},
	StateTransferring: {StateSubmitting, StateStarting, StateGenerating, StateTransferring},
	StateIngesting:    {StateSubmitting, StateStarting, StateGenerating, StateTransferring, StateIngesting},
	StateCompleted:    {StateSubmitting, StateStarting, StateGenerating, StateTransferring, StateIngesting, StateCompleted},
	StateCancelling:   {StateSubmitting, StateStarting, StateCancelling},
	StateFailing:      {StateSubmitting, StateStarting, StateFailing},
	StateFailed:       {StateSubmitting, StateFailed},
	StateCancelled:    {StateCancelled},
}

// force moves a job to state through the legal path, with the companion columns the CHECKs need.
func (r *rig) force(job, state string) {
	r.t.Helper()
	if state == StateAwaiting {
		return
	}
	path := pathTo[state]
	if state == StateExpired {
		r.exec(`UPDATE jobs SET state='expired', error_code='APPROVAL_EXPIRED', finished_at=? WHERE id=?`, store.FormatTime(t0), job)
		return
	}
	if state != StateQueued && path == nil {
		r.t.Fatalf("no path to %s", state)
	}
	cur := StateQueued
	for _, next := range path {
		if err := CanTransition(cur, next); err != nil {
			r.t.Fatalf("path step %s>%s: %v", cur, next, err)
		}
		switch {
		case IsTerminal(next):
			set := "state=?, finished_at=?, lease_owner=NULL, lease_expires_at=NULL, error_code='X'"
			args := []any{next, store.FormatTime(t0)}
			if next == StateCompleted {
				track := r.freshTrack()
				set += ", outcome='clean', track_id=?, error_code=NULL"
				args = append(args, track)
			}
			args = append(args, job)
			r.exec(`UPDATE jobs SET `+set+` WHERE id=?`, args...)
		case IsLeased(next):
			r.exec(`UPDATE jobs SET state=?, lease_owner='runner-x', lease_expires_at=? WHERE id=?`, next, store.FormatTime(t0.Add(time.Hour)), job)
		default:
			r.exec(`UPDATE jobs SET state=? WHERE id=?`, next, job)
		}
		cur = next
	}
}

func (r *rig) freshTrack() string {
	r.t.Helper()
	id, err := r.ing.IngestTake(r.t.Context(), JobRow{RecipeID: testRecipe}, AttemptRow{}, nil, providerOutput())
	if err != nil {
		r.t.Fatal(err)
	}
	return id
}

func TestTransitions_IllegalRefused(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	for _, from := range AllStates {
		legalTo := map[string]bool{}
		for _, tr := range Transitions {
			if tr.From == from {
				legalTo[tr.To] = true
			}
		}
		for _, to := range AllStates {
			if to == from || legalTo[to] {
				continue
			}
			t.Run(from+">"+to, func(t *testing.T) {
				gerr := CanTransition(from, to)
				want := ErrIllegalTransition
				if IsTerminal(from) {
					want = ErrTerminalImmutable
				}
				if !errors.Is(gerr, want) {
					t.Fatalf("Go guard: %v, want %v", gerr, want)
				}
			})
		}
	}
	// the SQL trigger, from every reachable source state to every other state; a refused update leaves the
	// job where it was, so one job per source state serves all its targets
	n := 0
	for _, from := range AllStates {
		legalTo := map[string]bool{}
		for _, tr := range Transitions {
			if tr.From == from {
				legalTo[tr.To] = true
			}
		}
		bs := r.seedBatch(batchSpec{state: map[bool]string{true: "awaiting_confirmation", false: "active"}[from == StateAwaiting || from == StateExpired]})
		job := bs.Jobs[0]
		r.force(job, from)
		if got := r.state(job); got != from {
			t.Fatalf("reached %s, want %s", got, from)
		}
		for _, to := range AllStates {
			if to == from || legalTo[to] {
				continue
			}
			n++
			_, err := r.st.W.Exec(`UPDATE jobs SET state = ? WHERE id = ?`, to, job)
			want := "illegal transition"
			if IsTerminal(from) {
				want = "terminal jobs are immutable"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("SQL %s>%s: %v, want %q", from, to, err, want)
			}
			if got := r.state(job); got != from {
				t.Errorf("SQL %s>%s changed the state to %s", from, to, got)
			}
		}
	}
	if n < 100 {
		t.Errorf("only %d illegal pairs checked", n)
	}
}
