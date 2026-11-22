package pods_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sort"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/provider/runpod/pods"
)

var allStates = []string{"intent", "creating", "booting", "ready", "busy", "orphaned", "terminating", "terminated"}

// walk puts a fresh pod into the given state through legal moves only.
func walk(t *testing.T, r *srig, id, name, state string) {
	t.Helper()
	r.pod(id, name, state)
}

// TestPodTransitions_GoEqualsSQL: pods.Transitions is exactly the pod_transitions table of the migration.
func TestPodTransitions_GoEqualsSQL(t *testing.T) {
	t.Parallel()
	r := newSRig(t)
	rows, err := r.st.R.Query(`SELECT from_state, to_state FROM pod_transitions ORDER BY 1, 2`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string][]string{}
	for rows.Next() {
		var f, to string
		if err := rows.Scan(&f, &to); err != nil {
			t.Fatal(err)
		}
		got[f] = append(got[f], to)
	}
	want := map[string][]string{}
	for k, v := range pods.Transitions {
		c := slices.Clone(v)
		sort.Strings(c)
		want[k] = c
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sql %v\ngo  %v", got, want)
	}
}

// TestPodTransition_<From>_<To>: one subtest per row of the table: the move works, writes terminated_at only for terminated, and
// appends one event.
func TestPodTransition_AllLegalMoves(t *testing.T) {
	t.Parallel()
	for from, tos := range pods.Transitions {
		for _, to := range tos {
			t.Run(from+"_"+to, func(t *testing.T) {
				t.Parallel()
				r := newSRig(t)
				walk(t, r, rigPod, "mm-0190aaaaaaaa", from)
				life := 61
				err := pods.Transition(context.Background(), r.st.W, rigPod, from, to, pods.Move{At: rigNow, Code: "WHY", Detail: map[string]any{"n": 1}, LifetimeS: &life})
				if err != nil {
					t.Fatal(err)
				}
				row, _ := pods.GetRow(context.Background(), r.st.R, rigPod)
				if row.State != to || (to == "terminated") != (row.TerminatedAt != nil) || (to == "terminated") != (row.LifetimeS != nil) {
					t.Fatalf("%+v", row)
				}
				if to == "terminating" && r.str(`SELECT terminate_sent_at FROM pods WHERE id = ?`, rigPod) == "<null>" {
					t.Fatal("terminate_sent_at not set")
				}
				if row.ErrorCode != "WHY" {
					t.Fatalf("error_code %q", row.ErrorCode)
				}
				ev := r.str(`SELECT from_state||'>'||to_state||':'||event||':'||code FROM pod_events WHERE pod_id = ? ORDER BY id DESC LIMIT 1`, rigPod)
				if ev != from+">"+to+":state:WHY" {
					t.Fatalf("event %s", ev)
				}
			})
		}
	}
}

// TestPodTransitions_IllegalRefused: every pair that is not a row is refused by Go and (checked raw) by the database trigger.
func TestPodTransitions_IllegalRefused(t *testing.T) {
	t.Parallel()
	r := newSRig(t)
	n := 0
	for _, from := range allStates {
		for _, to := range allStates {
			if pods.CanMove(from, to) {
				continue
			}
			n++
			err := pods.Transition(context.Background(), r.st.W, rigPod, from, to, pods.Move{At: rigNow})
			if !errors.Is(err, pods.ErrIllegalTransition) {
				t.Fatalf("%s -> %s: %v", from, to, err)
			}
		}
	}
	if n != 8*8-19 {
		t.Fatalf("%d illegal pairs", n)
	}
	// the trigger, bypassing Go: a few representative illegal moves
	for _, tc := range [][2]string{{"intent", "ready"}, {"booting", "busy"}, {"terminating", "ready"}, {"orphaned", "ready"}} {
		rr := newSRig(t)
		walk(t, rr, rigPod, "mm-0190aaaaaaaa", tc[0])
		if _, err := rr.st.W.Exec(`UPDATE pods SET state = ? WHERE id = ?`, tc[1], rigPod); err == nil {
			t.Fatalf("%v: the database allowed it", tc)
		}
	}
	// a terminated pod has no way out
	walk(t, r, rigPod, "mm-0190aaaaaaaa", "terminating")
	r.setState(rigPod, "terminated")
	if len(pods.Transitions["terminated"]) != 0 || pods.Live("terminated") || !pods.Live("busy") {
		t.Fatal("terminal state")
	}
}

func TestPodTransition_StateChanged(t *testing.T) {
	t.Parallel()
	r := newSRig(t)
	walk(t, r, rigPod, "mm-0190aaaaaaaa", "booting")
	err := pods.Transition(context.Background(), r.st.W, rigPod, "ready", "busy", pods.Move{At: rigNow})
	if !errors.Is(err, pods.ErrStateChanged) {
		t.Fatalf("%v", err)
	}
	if _, err := pods.GetRow(context.Background(), r.st.R, "nope"); !errors.Is(err, pods.ErrNotFound) {
		t.Fatalf("%v", err)
	}
}
