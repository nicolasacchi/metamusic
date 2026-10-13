package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

var leaseNow = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

func seedLease(t *testing.T, s *Store, owner string, expires time.Time) {
	t.Helper()
	mustExec(t, s, `INSERT INTO runner_lease(id,owner,acquired_at,heartbeat_at,expires_at) VALUES(1,?,?,?,?)`,
		owner, FormatTime(leaseNow), FormatTime(leaseNow), FormatTime(expires))
}

// TestSQL_LeasePredicate (scaffold, M3-B1): the helper renders exactly the C69 predicate and it fences a
// real write. M3-B5 extends the grep to every runner write statement of internal/jobs.
func TestSQL_LeasePredicate(t *testing.T) {
	t.Parallel()
	f := LeaseFence{Owner: "o1", Now: leaseNow}
	const want = `EXISTS (SELECT 1 FROM runner_lease WHERE id = 1 AND owner = ? AND expires_at > ?)`
	if f.Where() != want || f.And() != " AND "+want {
		t.Fatalf("predicate drifted from C69: %q", f.Where())
	}
	if got := f.Args(); len(got) != 2 || got[0] != "o1" || got[1] != "2026-10-14T12:00:00.000Z" {
		t.Fatalf("args = %v", got)
	}
	if strings.Count(f.And(), "?") != len(f.Args()) {
		t.Fatal("placeholder count differs from args")
	}

	s, _ := open(t)
	m3World(t, s) // inserts its own runner_lease row; replace it with a controlled one
	mustExec(t, s, `UPDATE runner_lease SET owner='o1', expires_at=? WHERE id=1`, FormatTime(leaseNow.Add(90*time.Second)))
	run := func(f LeaseFence) int64 {
		t.Helper()
		res, err := s.W.ExecContext(context.Background(),
			`UPDATE job_attempts SET remote_status='x' WHERE id='a1'`+f.And(), f.Args()...)
		n, err := f.Fenced(res, err)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := run(f); n != 1 {
		t.Fatalf("holder: %d rows", n)
	}
	if n := run(LeaseFence{Owner: "o2", Now: leaseNow}); n != 0 {
		t.Fatalf("other owner: %d rows", n)
	}
	if n := run(LeaseFence{Owner: "o1", Now: leaseNow.Add(90 * time.Second)}); n != 0 {
		t.Fatalf("expired (expires_at > now is strict): %d rows", n)
	}
	// INSERT … SELECT … WHERE EXISTS form
	res, err := s.W.ExecContext(context.Background(),
		`INSERT INTO job_events(job_id,at,event) SELECT 'j1', ?, 'x' WHERE `+f.Where(), append([]any{FormatTime(leaseNow)}, f.Args()...)...)
	if n, err := f.Fenced(res, err); err != nil || n != 1 {
		t.Fatalf("insert-select: %d %v", n, err)
	}
}

func TestLeaseFence_Confirm(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := open(t)
	seedLease(t, s, "o1", leaseNow.Add(90*time.Second))
	f := LeaseFence{Owner: "o1", Now: leaseNow}
	if err := f.Confirm(ctx, s.R, LeaseMargin); err != nil {
		t.Fatalf("90 s left: %v", err)
	}
	if err := (LeaseFence{Owner: "o1", Now: leaseNow.Add(61 * time.Second)}).Confirm(ctx, s.R, LeaseMargin); !errors.Is(err, ErrLeaseShort) {
		t.Fatalf("29 s left: %v", err)
	}
	if err := (LeaseFence{Owner: "o1", Now: leaseNow.Add(60 * time.Second)}).Confirm(ctx, s.R, LeaseMargin); !errors.Is(err, ErrLeaseShort) {
		t.Fatalf("exactly 30 s left must be short: %v", err)
	}
	if err := (LeaseFence{Owner: "o1", Now: leaseNow.Add(2 * time.Minute)}).Confirm(ctx, s.R, LeaseMargin); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expired: %v", err)
	}
	if err := (LeaseFence{Owner: "o2", Now: leaseNow}).Confirm(ctx, s.R, LeaseMargin); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("other owner: %v", err)
	}
}
