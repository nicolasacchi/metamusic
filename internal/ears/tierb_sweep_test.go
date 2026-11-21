package ears_test

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/ears/tierb/tierbtest"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

// seedJob inserts a job of the given kind next to a seeded one and returns its id. state "queued" is kept; "completed" and
// "failed" are forced through the (dropped) transition guard, as a finished job would be.
func (r *rig) seedJob(name, kind, needs, source, track, state string) string {
	r.t.Helper()
	seed := lt.SeedJob(r.t, r.st, r.now, lt.JobSeed{Name: name})
	id := seed.JobID[:len(seed.JobID)-1] + "f"
	r.exec(`INSERT INTO jobs(id,batch_id,take_index,kind,engine,backend,recipe_id,source_track_id,operator,mutation_json,title,expected_audio_s,expected_s,timeout_s,
			expected_micro_usd,reserved_micro_usd,state,next_attempt_at,queued_at,created_at,updated_at,needs_json)
		SELECT ?, batch_id, 7, ?, engine, backend, recipe_id, ?, operator, mutation_json, title, expected_audio_s, expected_s, timeout_s, expected_micro_usd,
		       reserved_micro_usd, 'queued', next_attempt_at, queued_at, created_at, updated_at, ? FROM jobs WHERE id = ?`, id, kind, nullIfEmpty(source), needs, seed.JobID)
	switch state {
	case "completed":
		r.dropTriggers("jobs_transition")
		r.exec(`UPDATE jobs SET state = 'completed', outcome = 'clean', finished_at = created_at, track_id = ? WHERE id = ?`, track, id)
	case "failed":
		r.dropTriggers("jobs_transition")
		r.exec(`UPDATE jobs SET state = 'failed', error_code = 'WORKER_FAILED', finished_at = created_at WHERE id = ?`, id)
	}
	return id
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// TestTierBState: the state machine of C337.4 over the cases a take can be in.
func TestTierBState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("none for a take of a job without ears", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, withTierARows())
		r.seedJob("plain", "generate", `["v2"]`, "", lt.TrackLuna, "completed")
		if got, err := r.svc.TierBState(ctx, lt.TrackLuna); err != nil || got != "none" {
			t.Errorf("state = %q, %v", got, err)
		}
		if got, _ := r.svc.TierBState(ctx, lt.TrackCitta); got != "none" {
			t.Errorf("import state = %q", got)
		}
	})
	t.Run("missing when ears was needed and no document arrived", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, withTierARows())
		r.seedJob("ears", "generate", `["ears","v2"]`, "", lt.TrackLuna, "completed")
		if got, _ := r.svc.TierBState(ctx, lt.TrackLuna); got != "missing" {
			t.Errorf("state = %q", got)
		}
		d, err := r.svc.Dossier(ctx, lt.TrackLuna)
		if err != nil {
			t.Fatal(err)
		}
		var ev map[string]any
		for _, f := range d.Flags {
			if f.Code == "listening_check_failed" {
				ev = f.Evidence
			}
		}
		if ev["state"] != "missing" {
			t.Errorf("flags = %+v, want listening_check_failed with state missing", d.Flags)
		}
		if _, err := r.svc.TierB(ctx, lt.TrackLuna); err == nil {
			t.Error("TierB of a missing listening check answered 200")
		}
	})
	t.Run("pending while an analyze job is live, none once it failed", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, withTierARows())
		r.seedJob("analyze", "analyze", `["analyze"]`, lt.TrackCitta, "", "queued")
		if got, _ := r.svc.TierBState(ctx, lt.TrackCitta); got != "pending" {
			t.Errorf("state = %q", got)
		}
		r2 := newRig(t, withTierARows())
		r2.seedJob("analyze", "analyze", `["analyze"]`, lt.TrackCitta, "", "failed")
		if got, _ := r2.svc.TierBState(ctx, lt.TrackCitta); got != "none" {
			t.Errorf("failed job state = %q", got)
		}
	})
	t.Run("pending while a document waits for its analysis, then ok", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, withTierARows())
		r.putDoc(lt.TrackLuna, "analysis.json", tierbtest.FromLyrics(lunaLyrics, tierbtest.Opts{}), time.Hour)
		if got, _ := r.svc.TierBState(ctx, lt.TrackLuna); got != "pending" {
			t.Errorf("state = %q", got)
		}
		r.analyze(lt.TrackLuna)
		if got, _ := r.svc.TierBState(ctx, lt.TrackLuna); got != "ok" {
			t.Errorf("state = %q", got)
		}
	})
	t.Run("error and skipped come from the document", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, withTierARows())
		r.putDoc(lt.TrackLuna, "analysis.json", tierbtest.FromLyrics(lunaLyrics, tierbtest.Opts{Status: "error", Code: "ASR_FAILED"}), time.Hour)
		r.analyze(lt.TrackLuna)
		if got, _ := r.svc.TierBState(ctx, lt.TrackLuna); got != "error" {
			t.Errorf("state = %q", got)
		}
		// a document whose recogniser never ran: stages.asr is skipped
		doc := []byte(`{"contract":"mm.ears-b/1","status":"error","code":"SEPARATION_FAILED","stages":{"separate":"error","asr":"skipped","transcribe":"skipped"},` +
			`"models":{"separator":null,"asr":null,"pitch":null},"input":null,"timings":{"load_s":0,"separate_s":0,"asr_s":0,"transcribe_s":0,"total_s":0},` +
			`"resources":{"device":"x","peak_vram_mib":0,"parent_reserved_mib":null},"asr":null,"pitch":null,"vocals":null,"errors":[{"stage":"separate","code":"SEPARATION_FAILED","message":"x"}]}`)
		r.putDoc(lt.TrackCitta, "analysis.json", doc, time.Hour)
		r.analyze(lt.TrackCitta)
		if got, _ := r.svc.TierBState(ctx, lt.TrackCitta); got != "skipped" {
			t.Errorf("state = %q", got)
		}
	})
	t.Run("unknown track and unwired service", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		if _, err := r.svc.TierBState(ctx, "nope"); err == nil {
			t.Error("unknown track answered")
		}
		if _, err := ears.New(ears.Options{}).TierBState(ctx, "x"); err == nil {
			t.Error("unwired service answered")
		}
	})
}

// TestTierBSweep_RecomputesFromArtifact: rows lost or superseded are recomputed from the stored document, CPU only.
func TestTierBSweep_RecomputesFromArtifact(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := newRig(t, withTierARows())
	r.putDoc(lt.TrackLuna, "analysis.json", tierbtest.FromLyrics(lunaLyrics, tierbtest.Opts{}), time.Hour)
	jobsBefore := r.count(`SELECT count(*) FROM jobs`)

	rep, err := r.svc.TierBSweep(ctx)
	if err != nil || rep.Candidates != 1 || rep.Analysed != 1 || len(rep.Failed) != 0 {
		t.Fatalf("first sweep = %+v, %v", rep, err)
	}
	if n := len(r.tierBRows(lt.TrackLuna)); n != 2 {
		t.Fatalf("rows = %d, want raw and diction", n)
	}
	if rep, _ := r.svc.TierBSweep(ctx); rep.Candidates != 0 {
		t.Errorf("a settled library has candidates: %+v", rep)
	}
	// the diction definition is superseded: the old row stays (immutable), a new one is written
	r.dropTriggers("analyses_immutable")
	r.exec(`UPDATE analyses SET version = 'mm-diction/0 go0' WHERE track_id = ? AND analyzer = 'tier_b_diction'`, lt.TrackLuna)
	rep, err = r.svc.TierBSweep(ctx)
	if err != nil || rep.Candidates != 1 || rep.Analysed != 1 {
		t.Fatalf("sweep after a definition bump = %+v, %v", rep, err)
	}
	if n := r.count(`SELECT count(*) FROM analyses WHERE track_id = ? AND analyzer = 'tier_b_diction'`, lt.TrackLuna); n != 2 {
		t.Errorf("diction rows = %d, want the old and the new", n)
	}
	// an adherence row of a superseded definition is not a candidate while no writer exists (EARS-F2)
	r.exec(`INSERT INTO analyses(track_id, analyzer, version, tier, metrics_json, created_at) VALUES (?, 'tier_b_adherence', 'mm-adherence/0 go0', 'B', '{}', ?)`, lt.TrackLuna, storeNow(r))
	if rep, _ := r.svc.TierBSweep(ctx); rep.Candidates != 0 {
		t.Errorf("superseded adherence is a candidate: %+v", rep)
	}
	// the raw row is gone entirely (a restore without derived rows)
	r.dropTriggers("analyses_no_delete")
	r.exec(`DELETE FROM analyses WHERE track_id = ? AND tier = 'B'`, lt.TrackLuna)
	if rep, _ := r.svc.TierBSweep(ctx); rep.Analysed != 1 {
		t.Errorf("sweep after the rows were lost = %+v", rep)
	}
	if n := r.count(`SELECT count(*) FROM jobs`); n != jobsBefore {
		t.Errorf("the sweep created %d jobs", n-jobsBefore)
	}
}

func storeNow(r *rig) string { return r.now.UTC().Format(time.RFC3339) }

// TestTierBSweep_FailureIsReportedAndSkipped: a corrupt document does not stop the pass.
func TestTierBSweep_FailureIsReportedAndSkipped(t *testing.T) {
	t.Parallel()
	r := newRig(t, withTierARows())
	r.putDoc(lt.TrackCitta, "analysis.json", []byte("{}"), time.Hour)
	r.putDoc(lt.TrackLuna, "analysis.json", tierbtest.FromLyrics(lunaLyrics, tierbtest.Opts{}), time.Hour)
	rep, err := r.svc.TierBSweep(context.Background())
	if err != nil || rep.Candidates != 2 || rep.Analysed != 1 || len(rep.Failed) != 1 || rep.Failed[0] != lt.TrackCitta {
		t.Fatalf("sweep = %+v, %v", rep, err)
	}
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.svc.TierBSweep(cctx); err == nil {
		t.Error("a cancelled context did not end the pass")
	}
	if _, err := ears.New(ears.Options{}).TierBSweep(context.Background()); err == nil {
		t.Error("unwired service swept")
	}
}

// TestTierB_NoAnalysisOfOldTakes: the library of imports and plain takes is never swept into a listening check: no row, no job,
// no spend (D-EARS-OP-5).
func TestTierB_NoAnalysisOfOldTakes(t *testing.T) {
	t.Parallel()
	r := newRig(t, withTierARows())
	r.seedJob("plain", "generate", `["v2"]`, "", lt.TrackLuna, "completed")
	r.seedJob("ears-no-doc", "generate", `["ears","v2"]`, "", lt.TrackCitta, "completed") // needs ears, no document arrived
	jobs, rows := r.count(`SELECT count(*) FROM jobs`), r.count(`SELECT count(*) FROM analyses`)
	rep, err := r.svc.TierBSweep(context.Background())
	if err != nil || rep.Candidates != 0 || rep.Analysed != 0 {
		t.Fatalf("sweep = %+v, %v", rep, err)
	}
	if n := r.count(`SELECT count(*) FROM jobs`); n != jobs {
		t.Errorf("jobs %d -> %d", jobs, n)
	}
	if n := r.count(`SELECT count(*) FROM analyses`); n != rows {
		t.Errorf("analyses %d -> %d", rows, n)
	}
	if n := r.count(`SELECT count(*) FROM analyses WHERE tier = 'B'`); n != 0 {
		t.Errorf("tier B rows = %d", n)
	}
}

// TestRunTierBSweep_RunsAtStartAndEveryInterval: the loop body sweeps once at start, again each interval and returns when its
// context ends.
func TestRunTierBSweep_RunsAtStartAndEveryInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t, withTierARows())
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { r.svc.RunTierBSweep(ctx, ears.TierBSweepInterval); close(done) }()
		synctest.Wait()
		// a document arrives after the first pass: the next interval picks it up
		r.putDoc(lt.TrackLuna, "analysis.json", tierbtest.FromLyrics(lunaLyrics, tierbtest.Opts{}), time.Hour)
		if n := len(r.tierBRows(lt.TrackLuna)); n != 0 {
			t.Fatalf("rows before the interval = %d", n)
		}
		time.Sleep(ears.TierBSweepInterval + time.Second)
		synctest.Wait()
		if n := len(r.tierBRows(lt.TrackLuna)); n != 2 {
			t.Errorf("rows after one interval = %d, want 2", n)
		}
		cancel()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Error("the loop did not return after its context ended")
		}
	})
}

// TestTierBSweep_BadDocumentsDoNotStarve: more than a pass worth of unparseable documents neither repeat every pass nor hide the
// good track behind them.
func TestTierBSweep_BadDocumentsDoNotStarve(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := newRig(t, withTierARows())
	// Luna and Citta sort first or last by id; the bad ones are the tracks before the good one.
	r.putDoc(lt.TrackCitta, "analysis.json", []byte("{}"), time.Hour)
	r.putDoc(lt.TrackLuna, "analysis.json", tierbtest.FromLyrics(lunaLyrics, tierbtest.Opts{}), time.Hour)
	rep, _ := r.svc.TierBSweep(ctx)
	if rep.Analysed != 1 || len(rep.Failed) != 1 {
		t.Fatalf("first pass = %+v", rep)
	}
	if rep, _ := r.svc.TierBSweep(ctx); rep.Candidates != 0 || len(rep.Failed) != 0 {
		t.Errorf("a known-bad document is retried: %+v", rep)
	}
	// a newer document of the same track is tried again
	r.putDoc(lt.TrackCitta, "analysis-2.json", []byte("{}"), time.Minute)
	if rep, _ := r.svc.TierBSweep(ctx); len(rep.Failed) != 1 {
		t.Errorf("a new document was not retried: %+v", rep)
	}
}

// TestTierBState_RunningRenderIsNotMissing: a render that has not completed is not yet a missing document (C337.4).
func TestTierBState_RunningRenderIsNotMissing(t *testing.T) {
	t.Parallel()
	r := newRig(t, withTierARows())
	id := r.seedJob("ears-running", "generate", `["ears","v2"]`, "", lt.TrackLuna, "queued")
	r.exec(`UPDATE jobs SET track_id = ? WHERE id = ?`, lt.TrackLuna, id)
	if got, err := r.svc.TierBState(context.Background(), lt.TrackLuna); err != nil || got != "none" {
		t.Errorf("state = %q, %v, want none", got, err)
	}
}
