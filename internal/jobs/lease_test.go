package jobs

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// sqlWrites reports, per function of the non-test files of dir, how many SQL writes (INSERT, UPDATE,
// DELETE statements, written inline or through a package constant) it contains and how many times it
// renders the lease predicate (LeaseFence.And or .Where).
type sqlWrites struct {
	file, fn      string
	writes, fenc  int
	mentionsLease bool // touches runner_lease itself
}

var writeRE = regexp.MustCompile(`(?is)^\s*(INSERT\s+INTO|UPDATE\s+\w+\s+SET|DELETE\s+FROM)`)

func scanWrites(t *testing.T, dir string) []sqlWrites {
	t.Helper()
	fset := token.NewFileSet()
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	isWrite := func(lit *ast.BasicLit) bool {
		if lit.Kind != token.STRING {
			return false
		}
		v, err := strconv.Unquote(lit.Value)
		return err == nil && writeRE.MatchString(v)
	}
	// package constants that hold a write statement
	writeConsts := map[string]bool{}
	for _, f := range files {
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, sp := range gd.Specs {
				vs := sp.(*ast.ValueSpec)
				for i, v := range vs.Values {
					ast.Inspect(v, func(n ast.Node) bool {
						if lit, ok := n.(*ast.BasicLit); ok && isWrite(lit) {
							writeConsts[vs.Names[i].Name] = true
						}
						return true
					})
				}
			}
		}
	}
	var out []sqlWrites
	for _, f := range files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			w := sqlWrites{file: filepath.Base(fset.Position(f.Pos()).Filename), fn: fd.Name.Name}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.BasicLit:
					if isWrite(x) {
						w.writes++
					}
					if x.Kind == token.STRING && strings.Contains(x.Value, "runner_lease") {
						w.mentionsLease = true
					}
				case *ast.Ident:
					if writeConsts[x.Name] {
						w.writes++
					}
				case *ast.CallExpr:
					if sel, ok := x.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "And" || sel.Sel.Name == "Where") && len(x.Args) == 0 {
						w.fenc++
					}
				}
				return true
			})
			if w.writes > 0 {
				out = append(out, w)
			}
		}
	}
	return out
}

// TestSQL_LeasePredicate: every write of the runner carries the C69 lease predicate (D-M3-30). The only
// statements without it are the ones that define the lease itself.
func TestSQL_LeasePredicate(t *testing.T) {
	t.Parallel()
	writes := 0
	for _, w := range scanWrites(t, ".") {
		writes += w.writes
		if w.mentionsLease {
			continue // acquire, heartbeat (also renews job leases under the predicate) and releaseLease
		}
		if w.fenc < w.writes {
			t.Errorf("%s: %s has %d writes but renders the lease predicate %d times", w.file, w.fn, w.writes, w.fenc)
		}
	}
	if writes < 10 {
		t.Errorf("only %d writes found: the scan is broken", writes)
	}
	// upload: the runner path (OpenAttempt) carries the predicate; the worker-facing writes are listed so a
	// new one cannot slip in unreviewed (Consume and CloseAttempt run inside a transaction whose first
	// statement is fenced, C74.0)
	reviewed := map[string]bool{"commit": true, "announce": true, "Consume": true, "CloseAttempt": true}
	for _, w := range scanWrites(t, "../upload") {
		switch {
		case w.fn == "OpenAttempt":
			if w.fenc < w.writes {
				t.Errorf("upload.OpenAttempt: %d writes, %d predicates", w.writes, w.fenc)
			}
		case !reviewed[w.fn]:
			t.Errorf("upload.%s has an unreviewed write", w.fn)
		}
	}
}

func TestRunnerLease_PausedRunnerCannotClaim(t *testing.T) {
	t.Parallel()
	var b *Runner
	var r *rig
	var bErr error
	fired := false
	r = newRig(t, withOpts(func(o *RunnerOptions) {
		o.Preflight = func(_ context.Context, _ int64) error {
			if fired {
				return nil
			}
			fired = true
			// runner A pauses for 120 s; runner B takes the lease and claims
			r.advance(120 * time.Second)
			_, bErr = b.Step(r.t.Context())
			return nil
		}
	}))
	b = r.second("runner-2", func(o *RunnerOptions) { o.Preflight = nil })
	job := r.seedBatch(batchSpec{}).Jobs[0]
	_, err := r.run.Step(t.Context())
	if !errors.Is(err, store.ErrLeaseLost) {
		t.Fatalf("A's Step: %v, want ErrLeaseLost", err)
	}
	if bErr != nil {
		t.Fatalf("B's Step: %v", bErr)
	}
	if got := r.be.Calls().Submit; got != 1 {
		t.Errorf("submits %d: only B may call the provider", got)
	}
	j := r.job(job)
	if j.LeaseOwner != "runner-2" || j.State != StateStarting || j.Attempts != 1 {
		t.Errorf("B's state was disturbed: %+v", j)
	}
	if n := r.count(`SELECT COUNT(*) FROM job_attempts`); n != 1 {
		t.Errorf("attempts %d", n)
	}
	if r.run.Holding() {
		t.Error("A still believes it holds the lease")
	}
	// A's next tick cannot acquire
	if _, err := r.run.Step(t.Context()); !errors.Is(err, ErrNotHolder) {
		t.Errorf("A's next Step: %v", err)
	}
}

func TestRunnerLease_Exclusive(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	b := r.second("runner-2")
	job := r.seedBatch(batchSpec{}).Jobs[0]
	r.step()
	r.wantState(job, StateStarting)
	if _, err := b.Step(t.Context()); !errors.Is(err, ErrNotHolder) {
		t.Fatalf("B's Step: %v", err)
	}
	if b.Holding() {
		t.Error("B holds")
	}
	// A stops heartbeating; B takes over within the lease length and adopts the job
	r.advance(89 * time.Second)
	if _, err := b.Step(t.Context()); !errors.Is(err, ErrNotHolder) {
		t.Fatalf("B before expiry: %v", err)
	}
	r.advance(2 * time.Second)
	if _, err := b.Step(t.Context()); err != nil {
		t.Fatalf("B after expiry: %v", err)
	}
	if !b.Holding() || r.job(job).LeaseOwner != "runner-2" {
		t.Errorf("owner %q", r.job(job).LeaseOwner)
	}
	if r.hasEvent(job, EvAdopted) == false {
		t.Error("adopted event")
	}
	if r.be.Calls().Submit != 1 {
		t.Error("adoption must not resubmit a submitted attempt")
	}
	// A is fenced out now
	if _, err := r.run.Step(t.Context()); !errors.Is(err, ErrNotHolder) {
		t.Errorf("A after B took over: %v", err)
	}
}

func TestRunner_StepDoesNotBlockOnIngest(t *testing.T) {
	t.Parallel()
	r, _, job, spec := generating(t)
	r.ing.block = make(chan struct{})
	r.ing.entered = make(chan struct{}, 1)
	out := r.deliver(spec, deliverOpts{})
	r.setStatus(r.refOf(job, 1), completed(out, 70000))
	r.advance(11 * time.Second)
	done := make(chan error, 1)
	go func() { _, err := r.run.Step(t.Context()); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Step blocked on the ingest")
	}
	<-r.ing.entered
	r.wantState(job, StateIngesting)
	// more ticks while the ingest runs: no second ingest, other work goes on
	r.advance(5 * time.Second)
	if _, err := r.run.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	if r.ing.callCount() != 1 {
		t.Errorf("ingest calls %d", r.ing.callCount())
	}
	close(r.ing.block)
	if err := r.run.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	r.wantState(job, StateCompleted)
}

func TestRunner_HeartbeatDuringLongIngest(t *testing.T) {
	t.Parallel()
	r, _, job, spec := generating(t)
	r.ing.block = make(chan struct{})
	r.ing.entered = make(chan struct{}, 1)
	out := r.deliver(spec, deliverOpts{})
	r.setStatus(r.refOf(job, 1), completed(out, 70000))
	r.advance(11 * time.Second)
	// Run owns the lease: the heartbeat goroutine renews it while the ingest takes 300 s of (manual) time
	ctx, cancel := contextWithCancel(t)
	runDone := make(chan error, 1)
	go func() { runDone <- r.run.Run(ctx) }()
	r.run.Wake()
	<-r.ing.entered
	r.wantState(job, StateIngesting)
	other := r.second("runner-2")
	// 300 s of ingest, in 30 s steps: after each one the heartbeat goroutine must have renewed the lease
	for range 10 {
		waitFor(t, func() bool { return r.clk.Pending() >= 2 }) // the heartbeat and tick timers exist
		r.advance(30 * time.Second)
		want := store.FormatTime(r.clk.Now())
		waitFor(t, func() bool { return r.str(`SELECT heartbeat_at FROM runner_lease WHERE id = 1`) >= want })
		if _, err := other.Step(ctx); !errors.Is(err, ErrNotHolder) {
			t.Fatalf("a second runner got the lease while the heartbeat should keep it: %v", err)
		}
		if exp := r.str(`SELECT lease_expires_at FROM jobs WHERE id = ?`, job); exp == "" || mustT(exp).Before(r.clk.Now()) {
			t.Fatalf("job lease %q at %s", exp, store.FormatTime(r.clk.Now()))
		}
	}
	close(r.ing.block)
	waitFor(t, func() bool { return r.state(job) == StateCompleted })
	cancel()
	if err := <-runDone; err != nil {
		t.Fatal(err)
	}
	// graceful stop released the lease
	if exp := r.str(`SELECT expires_at FROM runner_lease WHERE id = 1`); mustT(exp).After(r.clk.Now()) {
		t.Errorf("lease not released: %s", exp)
	}
}

var _ = atomic.Int32{}
var _ = provider.Queued
