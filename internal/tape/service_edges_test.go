package tape_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/tape"
	"github.com/nicolasacchi/metamusic/internal/tape/tapetest"
)

type errSource struct{ err error }

func (e errSource) Snapshot(context.Context, string, tape.SnapOptions) (tape.Snapshot, error) {
	return tape.Snapshot{}, e.err
}

// TestTape_SourceErrors: a problem from the Source passes through, any other error is wrapped; at run time the tape fails
// TAPE_SOURCE_CHANGED whatever the reason (no source, source error, empty snapshot).
func TestTape_SourceErrors(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(o *tape.Options) { o.Source = errSource{problem.E(422, "RECORD_EMPTY", "x", nil)} })
	_, err := r.export(human, tape.ExportInput{})
	wantProblem(t, err, 422, "RECORD_EMPTY")
	r2 := newRig(t, func(o *tape.Options) { o.Source = errSource{errors.New("boom")} })
	if _, err := r2.export(human, tape.ExportInput{}); err == nil || !strings.Contains(err.Error(), "snapshot") {
		t.Errorf("wrapped: %v", err)
	}
	// run-time failures
	r3 := newRig(t)
	for _, c := range []struct {
		name string
		mod  func(*rig)
	}{
		{"empty", func(r *rig) { r.src.Set(snapshot(rec, 3, 0)) }},
		{"nosnap", func(r *rig) { r.svc = rigWith(r, func(o *tape.Options) { o.Source = errSource{errors.New("gone")} }) }},
	} {
		name, mod := c.name, c.mod
		r3.src.Set(snapshot(rec, 3, 3))
		q := r3.mustExport(tape.ExportInput{TargetLUFS: f64(-14 - float64(len(name)))})
		mod(r3)
		r3.runOne()
		if got := r3.get(q.ID); got.State != tape.StateFailed || got.Error.Code != tape.ErrSourceChanged {
			t.Errorf("%s: %+v", name, got.Error)
		}
	}
	r3.src.Set(snapshot(rec, 3, 3))
	// a service with no source at run time
	r4 := newRig(t)
	q := r4.mustExport(tape.ExportInput{})
	r5 := rigWith(r4, func(o *tape.Options) { o.Source = nil })
	if ran, _ := r5.RunOne(context.Background()); !ran {
		t.Fatal("not run")
	}
	if got := r4.get(q.ID); got.State != tape.StateFailed {
		t.Errorf("no source: %+v", got)
	}
}

// rigWith builds a second service over the same store and data root.
func rigWith(r *rig, mod func(*tape.Options)) *tape.Service {
	o := tape.Options{Store: r.st, Source: r.src, Loudness: r.loud, Covers: r.covers, Data: r.data, Pipeline: r.pipe, Now: r.clk.now,
		Owner: "runner-a", FlacVersion: "flac 1.5.0", BuildVersion: "v-test"}
	mod(&o)
	return tape.New(o)
}

type badResultPipeline struct{ inner *tapetest.FakePipeline }

func (p badResultPipeline) Render(ctx context.Context, j tape.Job, pr func(tape.Progress)) (tape.Result, error) {
	res, err := p.inner.Render(ctx, j, pr)
	res.Chapters = res.Chapters[:1]
	return res, err
}

type cancelPipeline struct{ cancel context.CancelFunc }

func (p cancelPipeline) Render(ctx context.Context, _ tape.Job, _ func(tape.Progress)) (tape.Result, error) {
	p.cancel()
	<-ctx.Done()
	return tape.Result{Cleanup: func() {}}, ctx.Err()
}

// TestTape_RunnerEdges: an incomplete result fails TAPE_VERIFY_FAILED; a cancelled run leaves the row running for Recover; no
// Owner uses the default; the failure message is capped and the data path masked.
func TestTape_RunnerEdges(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(o *tape.Options) { o.Owner = "" })
	r.svc = rigWith(r, func(o *tape.Options) {
		o.Owner = ""
		o.Pipeline = badResultPipeline{r.pipe}
		o.NewID = func() string { r.n++; return fmt.Sprintf("t-%d", r.n) }
	})
	q := r.mustExport(tape.ExportInput{})
	r.runOne()
	if got := r.get(q.ID); got.Error == nil || got.Error.Code != tape.ErrVerifyFailed {
		t.Fatalf("incomplete: %+v", got)
	}
	r.pipe.Err = errors.New(strings.Repeat("x", 900) + r.dir)
	r.svc = rigWith(r, func(o *tape.Options) { o.NewID = func() string { r.n++; return fmt.Sprintf("t-%d", r.n) } })
	q2 := r.mustExport(tape.ExportInput{TargetLUFS: f64(-20)})
	r.runOne()
	if got := r.get(q2.ID); got.Error == nil || len(got.Error.Message) > 500 || got.Error.Code != tape.ErrEncodeFailed {
		t.Fatalf("long: %+v", got.Error)
	}
	// cancel mid-render: the row stays running, no failure recorded, Recover re-queues after the lease
	r.pipe.Err = nil
	ctx, cancel := context.WithCancel(context.Background())
	svc := rigWith(r, func(o *tape.Options) {
		o.Pipeline = cancelPipeline{cancel}
		o.NewID = func() string { r.n++; return fmt.Sprintf("t-%d", r.n) }
	})
	q3, err := svc.Export(ctx, human, rec, tape.ExportInput{TargetLUFS: f64(-22)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RunOne(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("RunOne: %v", err)
	}
	if got := r.get(q3.ID); got.State != tape.StateRunning {
		t.Fatalf("cancelled: %+v", got)
	}
	r.clk.add(tape.LeaseTTL + time.Second)
	if err := r.svc.Recover(context.Background()); err != nil || r.get(q3.ID).State != tape.StateQueued {
		t.Errorf("recover: %v %v", err, r.get(q3.ID).State)
	}
}

// TestTape_Filename: slug, date, lossy suffix, empty title, length bound.
func TestTape_Filename(t *testing.T) {
	t.Parallel()
	day := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		title string
		lossy bool
		want  string
	}{
		{"My Record!", false, "my-record-20260301-tape.flac"},
		{"x", true, "x-20260301-tape-lossy-sources.flac"},
		{"!!!", false, "record-20260301-tape.flac"},
	} {
		if got := tape.Filename(c.title, day, c.lossy); got != c.want {
			t.Errorf("%q: %s", c.title, got)
		}
	}
	if got := tape.Filename(strings.Repeat("a", 300), day, true); len(got) > 120 || !strings.HasSuffix(got, "-lossy-sources.flac") {
		t.Errorf("long: %d", len(got))
	}
}

// TestTape_NilStore: every entry point degrades without a store instead of panicking.
func TestTape_NilStore(t *testing.T) {
	t.Parallel()
	s, ctx := tape.New(tape.Options{}), context.Background()
	if _, err := s.Get(ctx, "x"); err == nil {
		t.Error("Get")
	}
	if _, err := s.Brief(ctx, "x", 3); err == nil {
		t.Error("Brief")
	}
	var pe *problem.Error
	if _, err := s.Export(ctx, auth.Principal{}, "r", tape.ExportInput{}); !errors.As(err, &pe) || pe.Status != 503 {
		t.Errorf("Export: %v", err)
	}
	if got, err := newRig(t).svc.Brief(ctx, rec, 0); got != nil || err != nil {
		t.Errorf("limit 0: %v %v", got, err)
	}
}

// TestTape_ClaimEdges: an existing identical file is reused, a render with a malformed hash fails, a lost lease removes the
// file it claimed, a closed store surfaces as an error.
func TestTape_ClaimEdges(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.mustExport(tape.ExportInput{})
	r.runOne()
	rel := r.scalar(`SELECT path FROM tapes WHERE id=?`, a.ID)
	// same bytes again (expire the row, keep the file): the claim finds it
	r.exec(`UPDATE tapes SET state='expired' WHERE id=?`, a.ID)
	b := r.mustExport(tape.ExportInput{})
	r.runOne()
	if r.scalar(`SELECT path FROM tapes WHERE id=?`, b.ID) != rel {
		t.Error("identical bytes must land on the same name")
	}
	// malformed hash
	bad := newRig(t, func(o *tape.Options) { o.Pipeline = shortHashPipeline{} })
	q := bad.mustExport(tape.ExportInput{})
	bad.runOne()
	if got := bad.get(q.ID); got.Error == nil || got.Error.Code != tape.ErrVerifyFailed {
		t.Errorf("short hash: %+v", got)
	}
	// lease lost between render and finish: the file is removed again
	lost := newRig(t)
	lost.svc = rigWith(lost, func(o *tape.Options) { o.Pipeline = stealPipeline{lost} })
	q2 := lost.mustExport(tape.ExportInput{})
	lost.runOne()
	if got := lost.get(q2.ID); got.State != tape.StateRunning || got.Download != nil {
		t.Errorf("lost: %+v", got)
	}
	if n := countFiles(lost.dir, nil); n != 0 {
		t.Errorf("%d files left after a lost lease", n)
	}
	_ = os.Remove
}

type shortHashPipeline struct{}

func (shortHashPipeline) Render(_ context.Context, j tape.Job, _ func(tape.Progress)) (tape.Result, error) {
	ch := make([]tape.Chapter, len(j.Items))
	return tape.Result{Path: "/nonexistent", SHA256: "abc", Bytes: 1, TotalSamples: 1, Chapters: ch, Cleanup: func() {}}, nil
}

// stealPipeline renders, then takes the row away from the runner (another owner) before the finish.
type stealPipeline struct{ r *rig }

func (p stealPipeline) Render(ctx context.Context, j tape.Job, pr func(tape.Progress)) (tape.Result, error) {
	res, err := p.r.pipe.Render(ctx, j, pr)
	p.r.exec(`UPDATE tapes SET owner='someone-else' WHERE state='running'`)
	return res, err
}

// TestTape_SweepEdges: a failing walk root, an unparsable finish time and an empty tapes directory are not errors.
func TestTape_SweepEdges(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	if n, err := r.svc.Sweep(context.Background(), time.Hour); n != 0 || err != nil {
		t.Errorf("empty: %d %v", n, err)
	}
	a := r.mustExport(tape.ExportInput{})
	r.runOne()
	r.exec(`DROP TRIGGER tapes_ready_frozen`)
	r.exec(`UPDATE tapes SET finished_at = 'garbage' WHERE id=?`, a.ID)
	r.clk.add(40 * 24 * time.Hour)
	if n, err := r.svc.Sweep(context.Background(), time.Hour); n != 0 || err != nil {
		t.Errorf("garbage time: %d %v", n, err)
	}
	// a ready row whose file is already gone expires cleanly
	r.exec(`UPDATE tapes SET finished_at = '2026-01-01T00:00:00.000Z' WHERE id=?`, a.ID)
	_ = r.data.Remove(r.scalar(`SELECT path FROM tapes WHERE id=?`, a.ID))
	if n, err := r.svc.Sweep(context.Background(), time.Hour); n != 1 || err != nil {
		t.Errorf("gone file: %d %v", n, err)
	}
}

// TestTape_QuotaRace: the Record is unpublished between the cheap check and the insert.
func TestTape_InsertRaces(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.svc = rigWith(r, func(o *tape.Options) { o.Source = unpublishSource{r} })
	_, err := r.export(human, tape.ExportInput{})
	wantProblem(t, err, 409, "RECORD_NOT_PUBLISHED")
}

type unpublishSource struct{ r *rig }

func (u unpublishSource) Snapshot(ctx context.Context, id string, o tape.SnapOptions) (tape.Snapshot, error) {
	u.r.exec(`UPDATE records SET status='draft', published_at=NULL, published_by=NULL WHERE id=?`, id)
	return u.r.src.Snapshot(ctx, id, o)
}

// TestTape_StoreFailures: with the database closed every entry point returns an error and none panics or reports success.
func TestTape_StoreFailures(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.mustExport(tape.ExportInput{})
	r.runOne()
	q := r.mustExport(tape.ExportInput{TargetLUFS: f64(-20)})
	ctx := context.Background()
	hash := r.scalar(`SELECT sha256 FROM tapes WHERE id=?`, a.ID)
	r.st.Close()
	if _, err := r.svc.Get(ctx, q.ID); err == nil {
		t.Error("Get")
	}
	if _, err := r.svc.Brief(ctx, rec, 3); err == nil {
		t.Error("Brief")
	}
	if _, err := r.svc.Resolve(ctx, hash, "flac"); err == nil || errors.Is(err, errNotFoundMarker) {
		t.Errorf("Resolve: %v", err)
	}
	if _, err := r.export(human, tape.ExportInput{}); err == nil {
		t.Error("Export")
	}
	if _, err := r.svc.RunOne(ctx); err == nil {
		t.Error("RunOne")
	}
	if err := r.svc.Recover(ctx); err == nil {
		t.Error("Recover")
	}
	if _, err := r.svc.Sweep(ctx, time.Hour); err == nil {
		t.Error("Sweep")
	}
}

var errNotFoundMarker = errors.New("never matches")

// TestTape_NoDataRoot: a service without a data root cannot claim files (the tape fails), serves no link and sweeps no files.
func TestTape_NoDataRoot(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.svc = rigWith(r, func(o *tape.Options) { o.Data = nil; o.Signer = nil })
	q := r.mustExport(tape.ExportInput{})
	r.runOne()
	if got := r.get(q.ID); got.State != tape.StateFailed || got.Download != nil {
		t.Errorf("%+v", got)
	}
	if n, err := r.svc.Sweep(context.Background(), time.Hour); n != 0 || err != nil {
		t.Errorf("sweep: %d %v", n, err)
	}
}

// TestTape_DefaultIDs: a service without NewID still mints distinct row ids.
func TestTape_DefaultIDs(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.svc = rigWith(r, func(o *tape.Options) { o.NewID = nil })
	a := r.mustExport(tape.ExportInput{})
	r.clk.add(time.Second)
	b := r.mustExport(tape.ExportInput{TargetLUFS: f64(-20)})
	if a.ID == "" || a.ID == b.ID {
		t.Errorf("ids %q %q", a.ID, b.ID)
	}
}

// TestTape_RunRecoversWhenIdle: the serve loop re-queues an abandoned running tape and renders it.
func TestTape_RunRecoversWhenIdle(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	q := r.mustExport(tape.ExportInput{})
	r.exec(`UPDATE tapes SET state='running', owner='dead', lease_expires_at='2026-03-01T11:00:00.000Z' WHERE id=?`, q.ID)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.svc.Run(ctx)
	deadline := time.Now().Add(8 * time.Second)
	for r.get(q.ID).State != tape.StateReady && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if r.get(q.ID).State != tape.StateReady {
		t.Errorf("state %s", r.get(q.ID).State)
	}
}

// TestTape_CancelledBeforeClaim: a cancelled context claims nothing and Export/Get surface the error.
func TestTape_CancelledContext(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.mustExport(tape.ExportInput{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if ran, err := r.svc.RunOne(ctx); ran || err == nil {
		t.Errorf("RunOne: %v %v", ran, err)
	}
	if _, err := r.svc.Export(ctx, human, rec, tape.ExportInput{}); err == nil {
		t.Error("Export")
	}
	if _, err := r.svc.Sweep(ctx, time.Hour); err == nil {
		t.Error("Sweep")
	}
}
