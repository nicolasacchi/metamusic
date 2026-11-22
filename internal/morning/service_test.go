package morning

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/schedule"
)

func ids(cs []Candidate) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.TrackID)
	}
	return out
}

func code(err error) string {
	var pe *problem.Error
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

// TestEligible_SQL: the one query keeps new, unrated, undiscarded job takes that no earlier crate carried, and adds the included ones.
func TestEligible_SQL(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	fresh := r.take("fresh", 3*time.Hour)
	fresh2 := r.take("fresh2", 1*time.Hour)
	rated := r.take("rated", 2*time.Hour)
	r.rate(rated, 3)
	discarded := r.take("discarded", 2*time.Hour)
	r.exec(`UPDATE tracks SET status = 'discarded' WHERE id = ?`, discarded)
	old := r.take("old", 48*time.Hour)
	inCrate := r.take("incrate", 2*time.Hour)
	r.exec(`INSERT INTO morning_crates(id,trigger,state,seed,options_json,created_by,created_at,built_at,takes) VALUES('c0','manual','ready',1,'{}','human:alice',?,?,1)`,
		"2026-11-21T07:00:00.000Z", "2026-11-21T07:01:00.000Z")
	r.exec(`INSERT INTO morning_crate_items(crate_id,n,side_index,track_id,anchor,source,start_frame,audio_start_frame,end_frame,gain_cdb,limited) VALUES('c0',1,0,?,0,'job',0,10,100,0,0)`, inCrate)
	imported := r.take("imported", 2*time.Hour)
	r.exec(`DROP TRIGGER tracks_frozen`) // the template of a real take is an import; only this test needs the origin changed
	r.exec(`UPDATE tracks SET origin = 'import' WHERE id = ?`, imported)
	manualOld := r.take("manual-old", 90*time.Hour)
	r.rate(manualOld, 5)
	manualDiscarded := r.take("manual-discarded", 90*time.Hour)
	r.exec(`UPDATE tracks SET status = 'discarded' WHERE id = ?`, manualDiscarded)

	got, err := r.svc.Eligible(ctx, []string{manualOld, manualDiscarded}, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{fresh2, fresh, manualOld} // newest first; the included take sorts by its age too
	slices.Sort(want)
	gs := ids(got)
	slices.Sort(gs)
	if !slices.Equal(gs, want) {
		t.Fatalf("eligible = %v, want %v", gs, want)
	}
	for _, c := range got {
		wantSrc := SourceJob
		if c.TrackID == manualOld {
			wantSrc = SourceManual
		}
		if c.Source != wantSrc {
			t.Errorf("%s source = %s, want %s", c.TrackID, c.Source, wantSrc)
		}
		if c.LUFS != -14.2 || c.TruePeak != -1.4 || c.SampleRate != 48000 || c.TotalSamples != 9_600_000 || c.Channels != 2 {
			t.Errorf("candidate %+v", c)
		}
	}
	_ = old
}

// TestEligible_SinceIsThePreviousScheduledCrate: with a previous scheduled crate, only later takes count; a failed one is skipped.
func TestEligible_SinceIsThePreviousScheduledCrate(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	r.take("a", 30*time.Hour) // older than the previous crate and than 24 h
	r.take("b", 5*time.Hour)
	c := r.take("c", 1*time.Hour)
	r.exec(`INSERT INTO morning_crates(id,schedule_key,trigger,state,seed,options_json,created_by,created_at,built_at) VALUES('p1','morning:2026-11-21','scheduled','empty',1,'{}','system:morning',?,?)`,
		ts(dawn.Add(-3*time.Hour)), ts(dawn.Add(-3*time.Hour)))
	got, err := r.svc.Eligible(ctx, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if g := ids(got); len(g) != 1 || g[0] != c {
		t.Fatalf("eligible = %v, want only c (%s)", g, c)
	}
	// a failed scheduled crate does not move the window
	r2 := newRig(t)
	b := r2.take("b", 5*time.Hour)
	r2.exec(`INSERT INTO morning_crates(id,schedule_key,trigger,state,seed,options_json,created_by,created_at,built_at,error_code) VALUES('p1','morning:2026-11-21','scheduled','failed',1,'{}','system:morning',?,?,'TOOL_FAILED')`,
		ts(dawn.Add(-3*time.Hour)), ts(dawn.Add(-3*time.Hour)))
	got, _ = r2.svc.Eligible(ctx, nil, "")
	if g := ids(got); len(g) != 1 || !slices.Contains(g, b) {
		t.Fatalf("after a failed crate eligible = %v, want b within the 24 h default", g)
	}
}

func ts(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// TestEligible_NightSource: a take of tonight's batch enters as night_shift, and the plan keeps the source order.
func TestEligible_NightSource(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	night := r.take("night", 2*time.Hour)
	other := r.take("other", 2*time.Hour)
	r.nightJob("bat-1", night)
	got, err := r.svc.Eligible(context.Background(), nil, "bat-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range got {
		want := SourceJob
		if c.TrackID == night {
			want = SourceNightShift
		}
		if c.Source != want {
			t.Errorf("%s source %s, want %s", c.TrackID, c.Source, want)
		}
	}
	_ = other
}

// nightJob records a batch with one job that made track.
func (r *rig) nightJob(batch, track string) {
	r.t.Helper()
	ts := ts(dawn)
	r.exec(`INSERT INTO estimates(id,principal_id,principal_kind,engine,backend,items_json,recipe_hashes,n_takes,quote_json,
		expected_micro_usd,max_micro_usd,retry_allowance_micro_usd,ceiling_micro_usd,price_pph_micro_usd,workers_max,idle_s,
		requires_approval,approval_reasons,token_hash,created_at,expires_at,consumed_at,consumed_batch_id)
		VALUES(?,'agent:nightshift','agent','yue2','fake','[]','[]',1,'{}',10,10,0,10,690000,2,20,0,'[]',?,?,?,?,?)`, "e-"+batch, "h"+batch, ts, ts, ts, batch)
	r.exec(`INSERT INTO batches(id,estimate_id,principal_id,principal_kind,principal_name,via,engine,backend,n_takes,rationale,
		expected_micro_usd,max_micro_usd,retry_allowance_micro_usd,ceiling_micro_usd,price_pph_micro_usd,attempt_overhead_s,kill_slack_s,
		workers_max,idle_s,state,created_at) VALUES(?,?,'agent:nightshift','agent','nightshift','api','yue2','fake',1,NULL,10,10,0,10,690000,200,15,2,20,'active',?)`,
		batch, "e-"+batch, ts)
	r.exec(`INSERT INTO jobs(id,batch_id,take_index,kind,engine,backend,recipe_id,operator,mutation_json,title,expected_audio_s,expected_s,timeout_s,
		expected_micro_usd,reserved_micro_usd,state,next_attempt_at,created_at,updated_at,track_id)
		SELECT ?,?,0,'generate','yue2','fake',recipe_id,'new_seed','{}','t',100,100,600,10,10,'queued',?,?,?,? FROM tracks WHERE id = ?`,
		"job-"+batch, batch, ts, ts, ts, track, track)
}

// TestMorning_BuildsAReadyCrate: a manual build chooses, builds the sides, records items and sides and answers a blind view.
func TestMorning_BuildsAReadyCrate(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	for i := range 6 {
		r.take("Secret "+string(rune('A'+i)), time.Duration(i+1)*time.Hour)
	}
	c, err := r.svc.Build(ctx, owner, BuildIn{})
	if err != nil {
		t.Fatal(err)
	}
	if c.State != StateBuilding || c.Trigger != TriggerManual {
		t.Fatalf("started crate %+v", c)
	}
	c = r.built(c)
	if c.State != StateReady || c.Takes != 6 || len(c.Chapters) != 6 || len(c.Sides) == 0 {
		t.Fatalf("crate %+v", c)
	}
	var secs int64
	for _, s := range c.Sides {
		if s.URL == nil || s.ExpiresAt == nil || s.Removed || s.Bytes == 0 {
			t.Errorf("side %+v", s)
		}
		secs += s.DurationMS
	}
	if c.DurationMS != secs || secs != 6*201_500 {
		t.Errorf("duration %d, sides %d", c.DurationMS, secs)
	}
	for _, ch := range c.Chapters {
		if ch.Verdict != VerdictUnheard || ch.Track != nil || ch.Anchor != nil || ch.GainDB != nil || ch.Limited != nil || ch.LUFSAfter != nil || ch.Stars != nil {
			t.Errorf("chapter %+v", ch)
		}
		if ch.AudioStartMS != ch.StartMS+1500 || ch.EndMS <= ch.AudioStartMS {
			t.Errorf("positions %+v", ch)
		}
	}
	if len(r.build.calls) != len(c.Sides) {
		t.Errorf("builder calls %d, sides %d", len(r.build.calls), len(c.Sides))
	}
	if n := r.scalar(`SELECT COUNT(*) FROM morning_crate_items WHERE crate_id = ?`, c.ID); n != "6" {
		t.Errorf("items = %s", n)
	}
	// the crate carries its takes now: a second build finds nothing new
	c2, err := r.svc.Build(ctx, owner, BuildIn{})
	if err != nil {
		t.Fatal(err)
	}
	if c2 = r.built(c2); c2.State != StateEmpty || c2.Takes != 0 || len(c2.Sides) != 0 {
		t.Fatalf("second crate %+v", c2)
	}
}

// TestMorning_NeverSpends: a build reads takes and writes only morning_ rows: no estimate, batch, job, ledger or approval row appears.
func TestMorning_NeverSpends(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	for i := range 3 {
		r.take("t", time.Duration(i+1)*time.Hour)
	}
	count := func() string {
		return r.scalar(`SELECT (SELECT COUNT(*) FROM estimates) || '/' || (SELECT COUNT(*) FROM batches) || '/' || (SELECT COUNT(*) FROM jobs) || '/' ||
			(SELECT COUNT(*) FROM ledger) || '/' || (SELECT COUNT(*) FROM approvals) || '/' || (SELECT COUNT(*) FROM tracks)`)
	}
	before := count()
	c, err := r.svc.Build(context.Background(), owner, BuildIn{})
	if err != nil {
		t.Fatal(err)
	}
	if r.built(c).State != StateReady {
		t.Fatal("not ready")
	}
	if after := count(); after != before {
		t.Fatalf("a build changed the money or take tables: %s -> %s", before, after)
	}
}

func TestMorning_EmptyState(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	c, err := r.svc.Build(context.Background(), owner, BuildIn{})
	if err != nil {
		t.Fatal(err)
	}
	c = r.built(c)
	if c.State != StateEmpty || c.BuiltAt == nil || c.Takes != 0 || c.Sides == nil || c.Chapters == nil || len(r.build.calls) != 0 {
		t.Fatalf("empty crate %+v, builder calls %d", c, len(r.build.calls))
	}
}

func TestMorning_BuildFailureStoresTheCode(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"loudness", &BuildError{Code: CodeLoudness, Msg: "x"}, CodeLoudness},
		{"samples", &BuildError{Code: CodeSamples, Msg: "x"}, CodeSamples},
		{"plain", errors.New("boom"), CodeToolFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.take("a", time.Hour)
			r.build.err = tc.err
			c, err := r.svc.Build(context.Background(), owner, BuildIn{})
			if err != nil {
				t.Fatal(err)
			}
			c = r.built(c)
			if c.State != StateFailed || c.ErrorCode == nil || *c.ErrorCode != tc.want || len(c.Chapters) != 0 {
				t.Fatalf("crate %+v", c)
			}
			// a failed build leaves no item, so its takes stay eligible
			if n := r.scalar(`SELECT COUNT(*) FROM morning_crate_items`); n != "0" {
				t.Fatalf("items = %s", n)
			}
		})
	}
}

func TestMorning_NoBuilderFails(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.take("a", time.Hour)
	r.svc.o.Builder = nil
	c, _ := r.svc.Build(context.Background(), owner, BuildIn{})
	if c = r.built(c); c.State != StateFailed || *c.ErrorCode != CodeToolFailed {
		t.Fatalf("%+v", c)
	}
}

func TestMorning_BuildInProgress(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.take("a", time.Hour)
	r.build.gate = make(chan struct{})
	r.build.started = make(chan struct{}, 1)
	c, err := r.svc.Build(context.Background(), owner, BuildIn{})
	if err != nil {
		t.Fatal(err)
	}
	<-r.build.started
	if _, err := r.svc.Build(context.Background(), owner, BuildIn{}); code(err) != "MORNING_CRATE_BUILD_IN_PROGRESS" {
		t.Fatalf("second build err = %v", err)
	}
	// another process sees the row, not the mutex
	other := New(ServiceOptions{Store: r.st, Data: r.data, Builder: r.build, Clock: r.clk, Loc: time.UTC})
	if _, err := other.Build(context.Background(), owner, BuildIn{}); code(err) != "MORNING_CRATE_BUILD_IN_PROGRESS" {
		t.Fatalf("other process err = %v", err)
	}
	close(r.build.gate)
	if r.built(c).State != StateReady {
		t.Fatal("first build did not finish")
	}
	if _, err := r.svc.Build(context.Background(), owner, BuildIn{}); err != nil {
		t.Fatalf("after it finished: %v", err)
	}
	r.svc.Wait()
}

// TestMorning_InterruptedBuildFails: a build cut short by shutdown is recorded as INTERRUPTED, and a building row left by a dead
// process is failed once it is older than 30 minutes.
func TestMorning_InterruptedBuildFails(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.take("a", time.Hour)
	r.build.gate = make(chan struct{})
	r.build.started = make(chan struct{}, 1)
	c, err := r.svc.Build(context.Background(), owner, BuildIn{})
	if err != nil {
		t.Fatal(err)
	}
	<-r.build.started
	r.svc.Close()
	got, err := r.svc.Get(context.Background(), owner, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateFailed || got.ErrorCode == nil || *got.ErrorCode != CodeInterrupted {
		t.Fatalf("crate %+v", got)
	}

	// a row left by a process that died
	r.exec(`INSERT INTO morning_crates(id,trigger,state,seed,options_json,created_by,created_at) VALUES('dead','manual','building',1,'{}','human:alice',?)`, ts(dawn.Add(-31*time.Minute)))
	r.exec(`INSERT INTO morning_crates(id,trigger,state,seed,options_json,created_by,created_at) VALUES('live','manual','building',1,'{}','human:alice',?)`, ts(dawn.Add(-5*time.Minute)))
	if err := r.svc.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := r.scalar(`SELECT state || ':' || COALESCE(error_code,'') FROM morning_crates WHERE id = 'dead'`); s != "failed:INTERRUPTED" {
		t.Errorf("dead = %s", s)
	}
	if s := r.scalar(`SELECT state FROM morning_crates WHERE id = 'live'`); s != "building" {
		t.Errorf("live = %s", s)
	}
}

func TestMorning_ManualBuildValidation(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.svc.Build(ctx, owner, BuildIn{IncludeTrackIDs: make([]string, 31)}); code(err) != "VALIDATION" {
		t.Errorf("31 ids: %v", err)
	}
	if _, err := r.svc.Build(ctx, owner, BuildIn{Minutes: 4}); code(err) != "MORNING_SETTINGS_INVALID" {
		t.Errorf("minutes 4: %v", err)
	}
	if _, err := r.svc.Build(ctx, owner, BuildIn{IncludeTrackIDs: []string{"nope"}}); code(err) != "UNKNOWN_TRACK" {
		t.Errorf("unknown id: %v", err)
	}
	// the mutex was released by every refusal
	if _, err := r.svc.Build(ctx, owner, BuildIn{Minutes: 20}); err != nil {
		t.Errorf("valid build refused: %v", err)
	}
	r.svc.Wait()
	if o := r.scalar(`SELECT json_extract(options_json,'$.minutes') FROM morning_crates`); o != "20" {
		t.Errorf("stored minutes = %s", o)
	}
}

// TestMorning_ManualInclude: a take named by id enters as manual even when it was rated long ago.
func TestMorning_ManualInclude(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	old := r.take("old", 100*time.Hour)
	r.rate(old, 2)
	c, err := r.svc.Build(context.Background(), owner, BuildIn{IncludeTrackIDs: []string{old, old}})
	if err != nil {
		t.Fatal(err)
	}
	c = r.built(c)
	if c.Takes != 1 || r.scalar(`SELECT source FROM morning_crate_items WHERE track_id = ?`, old) != "manual" {
		t.Fatalf("crate %+v", c)
	}
}

func TestMorning_AnchorsFromRatedTakes(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.take("new1", time.Hour)
	r.take("new2", 2*time.Hour)
	r.setting("anchors", 1)
	five := r.take("beloved", 200*time.Hour)
	r.rate(five, 5)
	c, _ := r.svc.Build(context.Background(), owner, BuildIn{})
	c = r.built(c)
	if c.Takes != 3 || c.Anchors != 1 {
		t.Fatalf("crate %+v", c)
	}
	if n := r.scalar(`SELECT COUNT(*) FROM morning_crate_items WHERE track_id = ? AND anchor = 1`, five); n != "1" {
		t.Errorf("anchor rows = %s", n)
	}
}

// TestMorning_ScheduledOncePerDay: two processes over one database build one crate for the day.
func TestMorning_ScheduledOncePerDay(t *testing.T) {
	t.Parallel()
	r1 := newRig(t)
	r1.take("a", time.Hour)
	r2 := newRigAt(t, r1.dbPath(), r1.dir, r1.clk)
	k := schedule.Key{Day: "2026-11-22", At: "07:00", Planned: dawn}
	ctx := context.Background()
	done := make(chan struct{}, 2)
	for _, r := range []*rig{r1, r2} {
		go func() { r.svc.RunScheduled(ctx, k); done <- struct{}{} }()
	}
	<-done
	<-done
	r1.svc.Wait()
	if n := r1.scalar(`SELECT COUNT(*) FROM morning_crates`); n != "1" {
		t.Fatalf("crates = %s", n)
	}
	if s := r1.scalar(`SELECT schedule_key || '/' || trigger || '/' || state || '/' || created_by FROM morning_crates`); s != "morning:2026-11-22/scheduled/ready/system:morning" {
		t.Fatalf("row = %s", s)
	}
	// a later run of the same day does nothing
	r1.svc.RunScheduled(ctx, k)
	if n := r1.scalar(`SELECT COUNT(*) FROM morning_crates`); n != "1" {
		t.Fatalf("crates after rerun = %s", n)
	}
}

func (r *rig) dbPath() string {
	var seq int
	var name, file string
	if err := r.st.R.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &file); err != nil {
		r.t.Fatal(err)
	}
	return file
}

func TestMorning_DisabledDoesNotRun(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.take("a", time.Hour)
	r.setting("enabled", 0)
	r.svc.RunScheduled(context.Background(), schedule.Key{Day: "2026-11-22", At: "07:00", Planned: dawn})
	if n := r.scalar(`SELECT COUNT(*) FROM morning_crates`); n != "0" {
		t.Fatalf("crates = %s", n)
	}
}

// TestMorning_CatchUpTwoHours: a restart at 08:30 builds the missed 07:00 crate, one at 09:30 skips the day.
func TestMorning_CatchUpTwoHours(t *testing.T) {
	for _, tc := range []struct {
		name  string
		hh    int
		mm    int
		built bool
	}{{"08:30 builds", 8, 30, true}, {"09:30 skips", 9, 30, false}, {"before 07:00 waits", 6, 0, false}} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				start := time.Date(2026, 11, 22, tc.hh, tc.mm, 0, 0, time.UTC)
				time.Sleep(time.Until(start)) // the bubble starts at 2000-01-01; move its clock to the scenario
				r := newRigAt(t, t.TempDir()+"/s.db", t.TempDir(), nil)
				r.take("a", time.Hour)
				ctx, cancel := context.WithCancel(context.Background())
				d := r.svc.Daily()
				d.Clock = nil // the real (virtual in the bubble) clock
				r.svc.o.Clock = realClockForTest{}
				go d.Loop(ctx)
				synctest.Wait()
				cancel()
				synctest.Wait()
				r.svc.Wait()
				n := r.scalar(`SELECT COUNT(*) FROM morning_crates`)
				if (n == "1") != tc.built {
					t.Fatalf("crates = %s, built wanted %v", n, tc.built)
				}
			})
		})
	}
}

// TestMorning_MovedAtBuildsOnce: the time changed from 07:00 to 07:30 after 07:00 had already built: the day has one crate.
func TestMorning_MovedAtBuildsOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		time.Sleep(time.Until(time.Date(2026, 11, 22, 6, 59, 0, 0, time.UTC)))
		r := newRigAt(t, t.TempDir()+"/s.db", t.TempDir(), nil)
		r.take("a", time.Hour)
		d := r.svc.Daily()
		d.Clock = nil
		r.svc.o.Clock = realClockForTest{}
		ctx, cancel := context.WithCancel(context.Background())
		go d.Loop(ctx)
		time.Sleep(2 * time.Minute) // 07:01: built
		synctest.Wait()
		r.setting("at", "07:30")
		time.Sleep(time.Hour) // past 07:30
		synctest.Wait()
		cancel()
		synctest.Wait()
		r.svc.Wait()
		if n := r.scalar(`SELECT COUNT(*) FROM morning_crates`); n != "1" {
			t.Fatalf("crates = %s, want 1", n)
		}
	})
}

// TestMorning_WaitsForNightBatch: the scheduled run waits for an unfinished night batch (re-checking every five minutes) and builds
// with what completed once wait_minutes is spent; a finished batch builds at once, and the night takes enter as night_shift.
func TestMorning_WaitsForNightBatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		time.Sleep(time.Until(dawn))
		r := newRigAt(t, t.TempDir()+"/s.db", t.TempDir(), nil)
		r.svc.o.Clock = realClockForTest{}
		night := r.take("night", 2*time.Hour)
		r.nightJob("bat-n", night)
		r.setting("wait_minutes", 45)
		polls := 0
		r.night = func(ctx context.Context, day string) (string, bool, bool, error) {
			polls++
			if day != "2026-11-22" {
				t.Errorf("night asked for %s", day)
			}
			return "bat-n", polls >= 4, true, nil // finished at the 4th look: 15 minutes in
		}
		start := time.Now()
		r.svc.RunScheduled(context.Background(), schedule.Key{Day: "2026-11-22", At: "07:00", Planned: dawn})
		r.svc.Wait()
		if polls != 4 || time.Since(start) != 15*time.Minute {
			t.Fatalf("polls %d after %v, want 4 after 15m", polls, time.Since(start))
		}
		if s := r.scalar(`SELECT source FROM morning_crate_items WHERE track_id = ?`, night); s != SourceNightShift {
			t.Errorf("source = %s", s)
		}

		// never finishing: builds after wait_minutes anyway
		r2 := newRigAt(t, t.TempDir()+"/s.db", t.TempDir(), nil)
		r2.svc.o.Clock = realClockForTest{}
		r2.take("x", time.Hour)
		r2.setting("wait_minutes", 20)
		n := 0
		r2.night = func(context.Context, string) (string, bool, bool, error) { n++; return "bat-x", false, true, nil }
		start = time.Now()
		r2.svc.RunScheduled(context.Background(), schedule.Key{Day: "2026-11-22", At: "07:00", Planned: dawn})
		r2.svc.Wait()
		if d := time.Since(start); d != 20*time.Minute || r2.scalar(`SELECT state FROM morning_crates`) != "ready" {
			t.Fatalf("waited %v, polls %d, state %s", d, n, r2.scalar(`SELECT state FROM morning_crates`))
		}

		// no night batch at all: no waiting
		r3 := newRigAt(t, t.TempDir()+"/s.db", t.TempDir(), nil)
		r3.svc.o.Clock = realClockForTest{}
		r3.take("y", time.Hour)
		start = time.Now()
		r3.svc.RunScheduled(context.Background(), schedule.Key{Day: "2026-11-22", At: "07:00", Planned: dawn})
		r3.svc.Wait()
		if time.Since(start) != 0 {
			t.Errorf("waited %v with no night batch", time.Since(start))
		}
	})
}

func TestMorning_NightBatchErrorBuildsAnyway(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.take("a", time.Hour)
	r.night = func(context.Context, string) (string, bool, bool, error) { return "", false, false, errors.New("boom") }
	r.svc.RunScheduled(context.Background(), schedule.Key{Day: "2026-11-22", At: "07:00", Planned: dawn})
	if s := r.scalar(`SELECT state FROM morning_crates`); s != "ready" {
		t.Fatalf("state = %s", s)
	}
}

// TestMorning_BlindRedaction: before the reveal nothing in the JSON of a crate names a take, for the Owner, the Owner's device or an
// agent; the Editor and Listener get nothing at all; after the reveal the titles appear.
func TestMorning_BlindRedaction(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	takes := map[string]string{}
	for _, title := range []string{"Zebra Anthem", "Quokka Blues", "Narwhal Waltz"} {
		takes[r.take(title, time.Hour)] = title
	}
	r.setting("anchors", 1)
	five := r.take("Beloved Elephant", 300*time.Hour)
	r.rate(five, 5)
	takes[five] = "Beloved Elephant"
	roots := map[string]bool{}
	for id := range takes {
		roots[id] = true
	}
	c, err := r.svc.Build(ctx, owner, BuildIn{})
	if err != nil {
		t.Fatal(err)
	}
	c = r.built(c)
	if c.State != StateReady || c.Takes != 4 {
		t.Fatalf("crate %+v", c)
	}
	// some events, so stars and verdicts exist
	r.exec(`INSERT INTO morning_crate_events(id,crate_id,side_index,chapter,kind,stars,side_ms,latency_ms,client_at,received_at,received_by) VALUES('e1',?,0,2,'rate',4,1000,0,?,?,'human:alice')`, c.ID, ts(dawn), ts(dawn))
	r.exec(`INSERT INTO morning_crate_events(id,crate_id,side_index,chapter,kind,side_ms,latency_ms,client_at,received_at,received_by) VALUES('e2',?,0,2,'strong_marker',1200,0,?,?,'human:alice')`, c.ID, ts(dawn), ts(dawn))

	secret := func(js string) {
		t.Helper()
		for id, title := range takes {
			if strings.Contains(js, id) || strings.Contains(js, title) || strings.Contains(strings.ToLower(js), strings.ToLower(strings.Fields(title)[0])) {
				t.Fatalf("a blind crate names %s / %q: %s", id, title, js)
			}
		}
		for _, banned := range []string{"root_track_id", `"title"`, "lineage", "seed", "cover"} {
			if strings.Contains(js, banned) && banned != "root_track_id" && banned != `"title"` {
				t.Fatalf("a blind crate mentions %s", banned)
			}
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(js), &m); err != nil {
			t.Fatal(err)
		}
		for _, ch := range m["chapters"].([]any) {
			ch := ch.(map[string]any)
			for _, f := range []string{"track", "anchor", "gain_db", "limited", "lufs_after"} {
				if ch[f] != nil {
					t.Fatalf("chapter field %s = %v before the reveal", f, ch[f])
				}
			}
		}
		if m["playlist_id"] != nil {
			t.Fatal("playlist before reveal")
		}
	}
	for name, who := range map[string]struct{ get func() (Crate, error) }{
		"owner":  {func() (Crate, error) { return r.svc.Get(ctx, owner, c.ID) }},
		"device": {func() (Crate, error) { return r.svc.Get(ctx, ownerDev, c.ID) }},
		"agent":  {func() (Crate, error) { return r.svc.Get(ctx, agent, c.ID) }},
	} {
		got, err := who.get()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		b, _ := json.Marshal(got)
		secret(string(b))
		if name == "owner" {
			var stars *int
			var verdict string
			for _, ch := range got.Chapters {
				if ch.N == 2 {
					stars, verdict = ch.Stars, ch.Verdict
				}
			}
			if stars == nil || *stars != 4 || verdict != VerdictKept {
				t.Errorf("chapter 2 stars %v verdict %s, want 4 / kept", stars, verdict)
			}
		}
	}
	pg, err := r.svc.List(ctx, agent, ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(pg)
	if len(pg.Items) != 1 {
		t.Fatalf("list %s", b)
	}
	for id, title := range takes {
		if strings.Contains(string(b), id) || strings.Contains(string(b), title) {
			t.Fatalf("the list names a take: %s", b)
		}
	}
	// people who are not the Owner never see a crate, revealed or not
	for _, p := range []string{"editor", "listener"} {
		pr := editor
		if p == "listener" {
			pr = listener
		}
		if _, err := r.svc.Get(ctx, pr, c.ID); code(err) != "OWNER_ONLY" {
			t.Errorf("%s Get err = %v", p, err)
		}
		if _, err := r.svc.List(ctx, pr, ListFilter{}); code(err) != "OWNER_ONLY" {
			t.Errorf("%s List err = %v", p, err)
		}
	}
	// the reveal (C4's transaction, set directly here) turns the names on for the Owner's side
	r.exec(`UPDATE morning_crates SET revealed_at = ?, revealed_by = 'human:alice' WHERE id = ?`, ts(dawn), c.ID)
	got, err := r.svc.Get(ctx, owner, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	named := 0
	for _, ch := range got.Chapters {
		if ch.Track == nil || ch.Anchor == nil || ch.GainDB == nil || ch.Limited == nil {
			t.Fatalf("revealed chapter %+v", ch)
		}
		if takes[ch.Track.ID] != ch.Track.Title {
			t.Errorf("track %+v", ch.Track)
		}
		named++
	}
	if named != 4 || !got.Revealed || got.RevealedAt == nil {
		t.Fatalf("revealed crate %+v", got)
	}
	if _, err := r.svc.Get(ctx, editor, c.ID); code(err) != "OWNER_ONLY" {
		t.Errorf("editor after reveal: %v", err)
	}
}

// TestMorning_SideLinksOutliveASide: a link lasts at least 6 h and never more than the 14 h the verifier accepts, for every viewer.
func TestMorning_SideLinksOutliveASide(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.take("a", time.Hour)
	c, _ := r.svc.Build(context.Background(), owner, BuildIn{})
	r.built(c)
	for name, pr := range map[string]struct {
		get func() (Crate, error)
	}{
		"owner": {func() (Crate, error) { return r.svc.Get(context.Background(), owner, c.ID) }},
		"agent": {func() (Crate, error) { return r.svc.Get(context.Background(), agent, c.ID) }},
	} {
		got, err := pr.get()
		if err != nil || len(got.Sides) == 0 {
			t.Fatalf("%s: %v %+v", name, err, got)
		}
		s := got.Sides[0]
		left := s.ExpiresAt.Sub(dawn)
		if left < 6*time.Hour || left > 14*time.Hour {
			t.Errorf("%s: link lives %v", name, left)
		}
		// the signer accepts it now and 5 h later, not 7 h later than its expiry
		sg := serve.NewSigner([]byte("k"), r.clk.Now)
		_ = sg
		u := *s.URL
		exp := u[strings.Index(u, "exp=")+4 : strings.Index(u, "&sig=")]
		sig := u[strings.Index(u, "&sig=")+5:]
		if err := sg.Verify(s.SHA256, "flac", exp, sig); err != nil {
			t.Errorf("%s: verify: %v", name, err)
		}
	}
}

func TestMorning_Settings(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	s, err := r.svc.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r.setting("anchors", 1) // the rig runs without anchors
	s, _ = r.svc.Settings(ctx)
	if !s.Enabled || s.At != "07:00" || s.Minutes != 40 || s.SideMinutes != 15 || s.MaxTakes != 14 || s.Anchors != 1 || s.IncludeTruncated || s.KeepDays != 7 || s.WaitMinutes != 45 || s.UpdatedBy != "system:migration" {
		t.Fatalf("defaults %+v", s)
	}
	at, off, mins := "06:15", false, 30
	got, err := r.svc.UpdateSettings(ctx, owner, SettingsPatch{At: &at, Enabled: &off, Minutes: &mins})
	if err != nil {
		t.Fatal(err)
	}
	if got.At != "06:15" || got.Enabled || got.Minutes != 30 || got.UpdatedBy != "human:alice" || !got.UpdatedAt.Equal(dawn) || got.KeepDays != 7 {
		t.Fatalf("updated %+v", got)
	}
	if o := got.Options(); o.Minutes != 30 || o.SlateMS != 1500 || o.MaxSideBytes != 200<<20 {
		t.Errorf("options %+v", o)
	}
	for _, tc := range []struct {
		name  string
		patch SettingsPatch
		field string
	}{
		{"at 24:00", SettingsPatch{At: ptr("24:00")}, "at"}, {"at junk", SettingsPatch{At: ptr("7:00")}, "at"}, {"at sign", SettingsPatch{At: ptr("+7:00")}, "at"},
		{"minutes 4", SettingsPatch{Minutes: ptrI(4)}, "minutes"}, {"minutes 121", SettingsPatch{Minutes: ptrI(121)}, "minutes"},
		{"side 16", SettingsPatch{SideMinutes: ptrI(16)}, "side_minutes"}, {"takes 0", SettingsPatch{MaxTakes: ptrI(0)}, "max_takes"},
		{"takes 31", SettingsPatch{MaxTakes: ptrI(31)}, "max_takes"}, {"anchors 3", SettingsPatch{Anchors: ptrI(3)}, "anchors"},
		{"keep 0", SettingsPatch{KeepDays: ptrI(0)}, "keep_days"}, {"keep 31", SettingsPatch{KeepDays: ptrI(31)}, "keep_days"},
		{"wait 121", SettingsPatch{WaitMinutes: ptrI(121)}, "wait_minutes"},
	} {
		_, err := r.svc.UpdateSettings(ctx, owner, tc.patch)
		var pe *problem.Error
		if !errors.As(err, &pe) || pe.Code != "MORNING_SETTINGS_INVALID" || pe.Details["field"] != tc.field {
			t.Errorf("%s: err = %v", tc.name, err)
		}
	}
	if s, _ := r.svc.Settings(ctx); s.At != "06:15" || s.Minutes != 30 {
		t.Errorf("a refused patch changed the settings: %+v", s)
	}
	for name, p := range map[string]string{"editor": "e", "listener": "l"} {
		pr := editor
		if p == "l" {
			pr = listener
		}
		if _, err := r.svc.UpdateSettings(ctx, pr, SettingsPatch{Enabled: ptrB(true)}); code(err) != "OWNER_ONLY" {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := r.svc.UpdateSettings(ctx, agent, SettingsPatch{Enabled: ptrB(true)}); err != nil {
		t.Errorf("an agent of the Owner may change behaviour settings: %v", err)
	}
	if s, _ := r.svc.Settings(ctx); s.UpdatedBy != "agent:ag1" {
		t.Errorf("updated_by = %s", s.UpdatedBy)
	}
	if _, err := r.svc.UpdateSettings(ctx, auth0, SettingsPatch{}); code(err) != "OWNER_ONLY" {
		t.Errorf("anonymous: %v", err)
	}
	// the schedule reads them
	at2, en, err := r.svc.dailyAt(ctx)
	if err != nil || at2 != "06:15" || !en {
		t.Errorf("dailyAt = %s %v %v", at2, en, err)
	}
}

func ptr(s string) *string { return &s }
func ptrI(i int) *int      { return &i }
func ptrB(b bool) *bool    { return &b }

func TestMorning_ListPagesAndFilters(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	for i, st := range []string{"empty", "failed", "empty", "empty"} {
		built, code := "NULL", "NULL"
		if st != "building" {
			built = "'" + ts(dawn.Add(time.Duration(i)*time.Minute)) + "'"
		}
		if st == "failed" {
			code = "'TOOL_FAILED'"
		}
		r.exec(`INSERT INTO morning_crates(id,trigger,state,seed,options_json,created_by,created_at,built_at,error_code) VALUES(?,'manual',?,1,'{}','human:alice',?,`+built+`,`+code+`)`,
			"c"+string(rune('0'+i)), st, ts(dawn.Add(time.Duration(i)*time.Minute)))
	}
	pg, err := r.svc.List(ctx, owner, ListFilter{Limit: 3})
	if err != nil || len(pg.Items) != 3 || pg.NextCursor == "" || pg.Items[0].ID != "c3" || pg.Items[2].ID != "c1" {
		t.Fatalf("page 1 %+v %v", pg, err)
	}
	if pg.Items[2].ErrorCode == nil || *pg.Items[2].ErrorCode != "TOOL_FAILED" {
		t.Errorf("failed summary %+v", pg.Items[2])
	}
	pg2, err := r.svc.List(ctx, owner, ListFilter{Limit: 3, Cursor: pg.NextCursor})
	if err != nil || len(pg2.Items) != 1 || pg2.Items[0].ID != "c0" || pg2.NextCursor != "" {
		t.Fatalf("page 2 %+v %v", pg2, err)
	}
	pg3, _ := r.svc.List(ctx, owner, ListFilter{State: "failed"})
	if len(pg3.Items) != 1 || pg3.Items[0].ID != "c1" {
		t.Errorf("filter %+v", pg3)
	}
	if _, err := r.svc.List(ctx, owner, ListFilter{State: "bogus"}); code(err) != "VALIDATION" {
		t.Errorf("bad state: %v", err)
	}
	if _, err := r.svc.List(ctx, owner, ListFilter{Cursor: "junk"}); code(err) != "VALIDATION" {
		t.Errorf("bad cursor: %v", err)
	}
	if _, err := r.svc.Get(ctx, owner, "missing"); code(err) != "NOT_FOUND" {
		t.Errorf("missing: %v", err)
	}
	if _, err := r.svc.Get(ctx, owner, "c1"); err != nil {
		t.Errorf("failed crate get: %v", err)
	}
}

func TestMorning_BuildOwnerSideOnly(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	if _, err := r.svc.Build(context.Background(), editor, BuildIn{}); code(err) != "OWNER_ONLY" {
		t.Errorf("editor: %v", err)
	}
	if _, err := r.svc.Build(context.Background(), listener, BuildIn{}); code(err) != "OWNER_ONLY" {
		t.Errorf("listener: %v", err)
	}
	if _, err := r.svc.Build(context.Background(), agent, BuildIn{}); err != nil {
		t.Errorf("agent: %v", err)
	}
	r.svc.Wait()
}

func TestMorning_VerdictOrder(t *testing.T) {
	t.Parallel()
	sk := int64(5)
	for _, tc := range []struct {
		a    *evAgg
		want string
	}{
		{nil, VerdictUnheard}, {&evAgg{}, VerdictUnheard}, {&evAgg{played: true}, VerdictHeard}, {&evAgg{haveStars: true}, VerdictHeard},
		{&evAgg{markers: 1, played: true}, VerdictMarked}, {&evAgg{markers: 1, skippedAt: &sk}, VerdictSkipped},
		{&evAgg{strong: 1, skippedAt: &sk, markers: 2}, VerdictKept},
	} {
		if got := verdictOf(tc.a); got != tc.want {
			t.Errorf("%+v = %s, want %s", tc.a, got, tc.want)
		}
	}
}
