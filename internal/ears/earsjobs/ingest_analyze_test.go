package earsjobs_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/ears/earsjobs"
	"github.com/nicolasacchi/metamusic/internal/ears/tierb/tierbtest"
	"github.com/nicolasacchi/metamusic/internal/jobs"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/upload"
)

const lunaLyrics = "[Verse 1]\nla la luna\n[Chorus]\nsing it loud\n[Outro]\nfade away"

func TestMain(m *testing.M) {
	store.UseMigratedTemplateForTests()
	os.Exit(m.Run())
}

type ingestRig struct {
	t     *testing.T
	st    *store.Store
	root  *os.Root
	dir   string
	svc   *ears.Service
	ing   *earsjobs.AnalyzeIngester
	now   time.Time
	owner string
}

func newIngestRig(t *testing.T) *ingestRig {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	lt.WriteFiles(t, dir)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	now := lt.FixedNow
	lt.FixtureM3(t, st, now)
	lib := library.New(st, root, func() time.Time { return now })
	svc := ears.New(ears.Options{Store: st, Library: lib, Data: root, DataDir: dir, Now: func() time.Time { return now }})
	r := &ingestRig{t: t, st: st, root: root, dir: dir, svc: svc, ing: earsjobs.NewAnalyzeIngester(svc), now: now, owner: "runner-1"}
	if _, err := st.W.Exec(`INSERT INTO runner_lease(id, owner, acquired_at, heartbeat_at, expires_at) VALUES (1, ?, ?, ?, ?)`,
		r.owner, store.FormatTime(now), store.FormatTime(now), store.FormatTime(now.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *ingestRig) count(q string, a ...any) (n int) {
	r.t.Helper()
	if err := r.st.R.QueryRow(q, a...).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

// attemptID is an attempt row of the fixture's job (the event's foreign key), "" when it has none.
func (r *ingestRig) attemptID() string {
	var id sql.NullString
	_ = r.st.R.QueryRow(`SELECT id FROM job_attempts WHERE job_id = ? LIMIT 1`, lt.JobDone).Scan(&id)
	return id.String
}

// ingest runs the kind ingester the way the bridge does: in one transaction under the lease fence, with the AfterKindCommit after it.
// The job is the fixture's finished job (any job row satisfies the event's foreign key); the analysed track is the argument.
func (r *ingestRig) ingest(track string, doc []byte, commit bool) (*string, string, error) {
	r.t.Helper()
	name := "up-" + hex.EncodeToString(doc[:min(len(doc), 4)]) + ".json"
	if err := r.root.MkdirAll("uploads", 0o750); err != nil {
		r.t.Fatal(err)
	}
	if err := r.root.WriteFile("uploads/"+name, doc, 0o600); err != nil {
		r.t.Fatal(err)
	}
	sum := sha256.Sum256(doc)
	sha := hex.EncodeToString(sum[:])
	in := jobs.KindIngest{
		Job:     jobs.JobRow{ID: lt.JobDone, Kind: jobs.KindAnalyze, SourceTrackID: &track},
		Attempt: jobs.AttemptRow{ID: r.attemptID(), JobID: lt.JobDone},
		Files: []upload.Received{
			{UploadID: "u1", Artifact: provider.AnalysisArtifact, SHA256: sha, Bytes: int64(len(doc)), Path: "uploads/" + name},
			{UploadID: "u2", Artifact: provider.ResultArtifact, SHA256: "ff", Bytes: 2, Path: "uploads/none"},
		},
		Output: provider.Output{Kind: provider.KindAnalyze, Status: "ok", Artifacts: map[string]provider.ArtifactDigest{provider.AnalysisArtifact: {SHA256: sha, Bytes: int64(len(doc))}}},
		Fence:  store.LeaseFence{Owner: r.owner, Now: r.now},
	}
	tx, err := r.st.W.BeginTx(context.Background(), nil)
	if err != nil {
		r.t.Fatal(err)
	}
	id, outcome, err := r.ing.IngestKind(context.Background(), tx, in)
	if err != nil || !commit {
		_ = tx.Rollback()
		return id, outcome, err
	}
	if err := tx.Commit(); err != nil {
		r.t.Fatal(err)
	}
	r.ing.AfterKindCommit(context.Background(), in)
	return id, outcome, nil
}

// TestAnalyzeIngest_StoresArtifactAndRows: the document is attached to the analysed take (frozen file, artifacts row), the ingester
// answers the take's id with outcome clean, and after the commit the listening check is stored: raw and diction rows and measured
// sections. The take's tracks row count and recipes are untouched.
func TestAnalyzeIngest_StoresArtifactAndRows(t *testing.T) {
	t.Parallel()
	r := newIngestRig(t)
	tracks, recipes := r.count(`SELECT COUNT(*) FROM tracks`), r.count(`SELECT COUNT(*) FROM recipes`)
	id, outcome, err := r.ingest(lt.TrackLuna, tierbtest.FromLyrics(lunaLyrics, tierbtest.Opts{}), true)
	if err != nil {
		t.Fatal(err)
	}
	if id == nil || *id != lt.TrackLuna || outcome != "clean" {
		t.Fatalf("ingest = %v %q", id, outcome)
	}
	if r.count(`SELECT COUNT(*) FROM artifacts WHERE track_id = ? AND name = 'analysis.json'`, lt.TrackLuna) != 1 {
		t.Error("no analysis.json artifact")
	}
	st, err := os.Stat(filepath.Join(r.dir, "takes", lt.TrackLuna, "analysis.json"))
	if err != nil || st.Mode().Perm() != 0o444 {
		t.Errorf("file: %v %v", st, err)
	}
	for _, a := range []string{"tier_b_raw", "tier_b_diction"} {
		if r.count(`SELECT COUNT(*) FROM analyses WHERE track_id = ? AND analyzer = ?`, lt.TrackLuna, a) != 1 {
			t.Errorf("%s rows != 1", a)
		}
	}
	if r.count(`SELECT COUNT(*) FROM sections WHERE track_id = ? AND source = 'measured'`, lt.TrackLuna) == 0 {
		t.Error("no measured sections")
	}
	if r.count(`SELECT COUNT(*) FROM tracks`) != tracks || r.count(`SELECT COUNT(*) FROM recipes`) != recipes {
		t.Error("the analyze ingest wrote a track or a recipe")
	}
}

// TestAnalyzeIngest_ReferenceTrackGetsArtifact: a reference (an import with no recipe, no takes directory) receives the document too;
// the transcript has no lyrics to score, so only the raw row exists.
func TestAnalyzeIngest_ReferenceTrackGetsArtifact(t *testing.T) {
	t.Parallel()
	r := newIngestRig(t)
	if err := os.RemoveAll(filepath.Join(r.dir, "takes", lt.TrackRefFlac)); err != nil {
		t.Fatal(err)
	}
	id, _, err := r.ingest(lt.TrackRefFlac, tierbtest.FromLyrics("[Verse]\nsotto il cielo di sera", tierbtest.Opts{}), true)
	if err != nil || id == nil || *id != lt.TrackRefFlac {
		t.Fatalf("ingest = %v %v", id, err)
	}
	if r.count(`SELECT COUNT(*) FROM artifacts WHERE track_id = ? AND name = 'analysis.json'`, lt.TrackRefFlac) != 1 {
		t.Error("the reference has no artifact")
	}
	if r.count(`SELECT COUNT(*) FROM analyses WHERE track_id = ? AND analyzer = 'tier_b_raw'`, lt.TrackRefFlac) != 1 {
		t.Error("the reference has no tier_b_raw row")
	}
	if r.count(`SELECT COUNT(*) FROM analyses WHERE track_id = ? AND analyzer = 'tier_b_diction'`, lt.TrackRefFlac) != 0 {
		t.Error("a reference with no lyrics got a diction row")
	}
}

// TestAnalyzeIngest_GarbageIsWorkerContract: a document that is not an mm.ears-b/1 document fails the job with WORKER_CONTRACT, and
// the rolled-back transaction leaves no row.
func TestAnalyzeIngest_GarbageIsWorkerContract(t *testing.T) {
	t.Parallel()
	r := newIngestRig(t)
	_, _, err := r.ingest(lt.TrackLuna, []byte(`{"contract":"mm.ears-b/0","garbage":true}`), true)
	var cv *jobs.ContractViolation
	if !errors.As(err, &cv) || cv.Code != "WORKER_CONTRACT" {
		t.Fatalf("error = %v, want a WORKER_CONTRACT violation", err)
	}
	if r.count(`SELECT COUNT(*) FROM artifacts WHERE track_id = ? AND name LIKE 'analysis%'`, lt.TrackLuna) != 0 {
		t.Error("a garbage document was stored")
	}
}

// TestAnalyzeIngest_ErrorDocumentCompletes: a well-formed document whose status is error (the recogniser failed) completes the job and
// is stored: the raw row says error, no diction row, no measured sections (D-EARS-10).
func TestAnalyzeIngest_ErrorDocumentCompletes(t *testing.T) {
	t.Parallel()
	r := newIngestRig(t)
	id, outcome, err := r.ingest(lt.TrackLuna, tierbtest.FromLyrics(lunaLyrics, tierbtest.Opts{Status: "error"}), true)
	if err != nil || id == nil || outcome != "clean" {
		t.Fatalf("ingest = %v %q %v", id, outcome, err)
	}
	if r.count(`SELECT COUNT(*) FROM analyses WHERE track_id = ? AND analyzer = 'tier_b_raw'`, lt.TrackLuna) != 1 ||
		r.count(`SELECT COUNT(*) FROM analyses WHERE track_id = ? AND analyzer = 'tier_b_diction'`, lt.TrackLuna) != 0 ||
		r.count(`SELECT COUNT(*) FROM sections WHERE track_id = ? AND source = 'measured'`, lt.TrackLuna) != 0 {
		t.Error("an error document stored more than its raw row")
	}
	if state, err := r.svc.TierBState(context.Background(), lt.TrackLuna); err != nil || state != ears.TierBError {
		t.Errorf("state = %q %v, want error", state, err)
	}
}

// TestAnalyzeIngest_DuplicateRace: the race loser is stored as analysis-2.json with a job event analysis_duplicate and is not analysed
// (the winner's rows stand); the job still completes.
func TestAnalyzeIngest_DuplicateRace(t *testing.T) {
	t.Parallel()
	r := newIngestRig(t)
	if _, _, err := r.ingest(lt.TrackLuna, tierbtest.FromLyrics(lunaLyrics, tierbtest.Opts{}), true); err != nil {
		t.Fatal(err)
	}
	rows := r.count(`SELECT COUNT(*) FROM analyses WHERE track_id = ? AND tier = 'B'`, lt.TrackLuna)
	id, outcome, err := r.ingest(lt.TrackLuna, tierbtest.FromLyrics(lunaLyrics, tierbtest.Opts{StartS: 2}), true)
	if err != nil || id == nil || outcome != "clean" {
		t.Fatalf("second ingest = %v %q %v", id, outcome, err)
	}
	if r.count(`SELECT COUNT(*) FROM artifacts WHERE track_id = ? AND name = 'analysis-2.json'`, lt.TrackLuna) != 1 {
		t.Error("the loser is not stored as analysis-2.json")
	}
	if got := r.count(`SELECT COUNT(*) FROM analyses WHERE track_id = ? AND tier = 'B'`, lt.TrackLuna); got != rows {
		t.Errorf("the duplicate was analysed: %d -> %d rows", rows, got)
	}
	var detail sql.NullString
	if err := r.st.R.QueryRow(`SELECT detail_json FROM job_events WHERE job_id = ? AND event = 'analysis_duplicate'`, lt.JobDone).Scan(&detail); err != nil {
		t.Fatalf("no analysis_duplicate event: %v", err)
	}
}

// TestAnalyzeIngest_RolledBackDuplicateLeavesNoStaleFlag: an ingest attempt that detects the duplicate and then rolls back sets the
// per-job duplicate flag; the NEXT attempt of the same job (here on a take with no document yet, so no duplicate) must start with the
// flag cleared and be analysed after its commit, not skipped as if it were the rolled-back duplicate.
func TestAnalyzeIngest_RolledBackDuplicateLeavesNoStaleFlag(t *testing.T) {
	t.Parallel()
	r := newIngestRig(t)
	if _, _, err := r.ingest(lt.TrackLuna, tierbtest.FromLyrics(lunaLyrics, tierbtest.Opts{}), true); err != nil {
		t.Fatal(err)
	}
	// the loser of the race: the duplicate is detected (flag set), the transaction rolls back, AfterKindCommit never runs
	if _, outcome, err := r.ingest(lt.TrackLuna, tierbtest.FromLyrics(lunaLyrics, tierbtest.Opts{StartS: 2}), false); err != nil || outcome != "clean" {
		t.Fatalf("rolled-back duplicate = %q %v", outcome, err)
	}
	if r.count(`SELECT COUNT(*) FROM artifacts WHERE track_id = ? AND name = 'analysis-2.json'`, lt.TrackLuna) != 0 {
		t.Fatal("the rolled-back duplicate left an artifact")
	}
	// the retry of the same job (same job id) on a take that has no document: a normal ingest
	if err := os.RemoveAll(filepath.Join(r.dir, "takes", lt.TrackRefFlac)); err != nil {
		t.Fatal(err)
	}
	if _, outcome, err := r.ingest(lt.TrackRefFlac, tierbtest.FromLyrics("[Verse]\nsotto il cielo di sera", tierbtest.Opts{}), true); err != nil || outcome != "clean" {
		t.Fatalf("next ingest = %q %v", outcome, err)
	}
	if r.count(`SELECT COUNT(*) FROM analyses WHERE track_id = ? AND analyzer = 'tier_b_raw'`, lt.TrackRefFlac) != 1 {
		t.Error("the next attempt was not analysed: a stale duplicate flag survived the rollback")
	}
}

// TestAnalyzeJob_NoTrackNoMutation: the ingester never creates a track, a mutation, a recipe or a song, and refuses a job with no source
// take or one for another kind.
func TestAnalyzeJob_NoTrackNoMutation(t *testing.T) {
	t.Parallel()
	r := newIngestRig(t)
	tables := []string{"tracks", "mutations", "recipes", "songs", "song_picks", "pick_events"}
	before := map[string]int{}
	for _, tb := range tables {
		before[tb] = r.count(`SELECT COUNT(*) FROM ` + tb) //nolint:gosec // fixed table names
	}
	if _, _, err := r.ingest(lt.TrackLuna, tierbtest.FromLyrics(lunaLyrics, tierbtest.Opts{}), true); err != nil {
		t.Fatal(err)
	}
	for _, tb := range tables {
		if got := r.count(`SELECT COUNT(*) FROM ` + tb); got != before[tb] { //nolint:gosec // fixed table names
			t.Errorf("%s: %d -> %d rows", tb, before[tb], got)
		}
	}
	in := jobs.KindIngest{Job: jobs.JobRow{ID: lt.JobDone, Kind: jobs.KindAnalyze}, Fence: store.LeaseFence{Owner: r.owner, Now: r.now}}
	tx, err := r.st.W.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, _, err := r.ing.IngestKind(context.Background(), tx, in); err == nil {
		t.Error("an analyze job with no source take was ingested")
	}
}

// TestAnalyzeIngest_Refusals: every way the received files can contradict the job is refused without a row: no analysis.json among
// the uploads (ARTIFACT_MISSING), a report that differs from the received file (ARTIFACT_MISMATCH), a file that changed after it was
// reported, an unknown take, and a lost lease while the duplicate event is written.
func TestAnalyzeIngest_Refusals(t *testing.T) {
	t.Parallel()
	r := newIngestRig(t)
	doc := tierbtest.FromLyrics(lunaLyrics, tierbtest.Opts{})
	if err := r.root.MkdirAll("uploads", 0o750); err != nil {
		t.Fatal(err)
	}
	if err := r.root.WriteFile("uploads/r.json", doc, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(doc)
	sha := hex.EncodeToString(sum[:])
	track := lt.TrackLuna
	base := func() jobs.KindIngest {
		return jobs.KindIngest{
			Job:     jobs.JobRow{ID: lt.JobDone, Kind: jobs.KindAnalyze, SourceTrackID: &track},
			Files:   []upload.Received{{UploadID: "u1", Artifact: "analysis.json", SHA256: sha, Bytes: int64(len(doc)), Path: "uploads/r.json"}},
			Output:  provider.Output{Artifacts: map[string]provider.ArtifactDigest{"analysis.json": {SHA256: sha, Bytes: int64(len(doc))}}},
			Fence:   store.LeaseFence{Owner: r.owner, Now: r.now},
			Attempt: jobs.AttemptRow{ID: r.attemptID()},
		}
	}
	unknown := "01900000-0000-7000-8000-00000000ffff"
	for name, c := range map[string]struct {
		edit func(in *jobs.KindIngest)
		code string // the ContractViolation code, "" = another error
	}{
		"no document": {func(in *jobs.KindIngest) { in.Files = nil }, "ARTIFACT_MISSING"},
		"unreported":  {func(in *jobs.KindIngest) { in.Output.Artifacts = nil }, "ARTIFACT_MISMATCH"},
		"report differs": {func(in *jobs.KindIngest) {
			in.Output.Artifacts["analysis.json"] = provider.ArtifactDigest{SHA256: sha, Bytes: 1}
		}, "ARTIFACT_MISMATCH"},
		"file changed": {func(in *jobs.KindIngest) {
			in.Files[0].SHA256 = sha[:63] + "0"
			in.Output.Artifacts["analysis.json"] = provider.ArtifactDigest{SHA256: sha[:63] + "0", Bytes: int64(len(doc))}
		}, "ARTIFACT_MISMATCH"},
		"unknown take":     {func(in *jobs.KindIngest) { in.Job.SourceTrackID = &unknown }, ""},
		"another kind":     {func(in *jobs.KindIngest) { in.Job.Kind = jobs.KindPlan }, ""},
		"missing received": {func(in *jobs.KindIngest) { in.Files[0].Path = "uploads/gone.json" }, ""},
	} {
		in := base()
		c.edit(&in)
		tx, err := r.st.W.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = r.ing.IngestKind(context.Background(), tx, in)
		_ = tx.Rollback()
		var cv *jobs.ContractViolation
		switch {
		case err == nil:
			t.Errorf("%s: accepted", name)
		case c.code != "" && (!errors.As(err, &cv) || cv.Code != c.code):
			t.Errorf("%s: error = %v, want a %s violation", name, err, c.code)
		case c.code == "" && errors.As(err, &cv):
			t.Errorf("%s: error = %v, want an infrastructure error, not a violation", name, err)
		}
	}
	if r.count(`SELECT COUNT(*) FROM artifacts WHERE track_id = ? AND name LIKE 'analysis%'`, lt.TrackLuna) != 0 {
		t.Error("a refused ingest left an artifact")
	}

	// a duplicate whose lease is gone: the event cannot be written, the whole ingest fails with ErrLeaseLost
	if _, _, err := r.ingest(lt.TrackLuna, doc, true); err != nil {
		t.Fatal(err)
	}
	in := base()
	in.Fence = store.LeaseFence{Owner: "another-runner", Now: r.now}
	tx, err := r.st.W.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, _, err := r.ing.IngestKind(context.Background(), tx, in); !errors.Is(err, store.ErrLeaseLost) {
		t.Errorf("duplicate under a lost lease: %v, want ErrLeaseLost", err)
	}
}

// TestAnalyzeIngest_AfterCommitFailureIsLogged: a failed analysis after the commit never fails the job (the sweep retries), and a job
// without a source take analyses nothing.
func TestAnalyzeIngest_AfterCommitFailureIsLogged(t *testing.T) {
	t.Parallel()
	s := &spyAttacher{err: errors.New("boom")}
	ing := earsjobs.NewAnalyzeIngester(s)
	track := "t1"
	ing.AfterKindCommit(context.Background(), jobs.KindIngest{Job: jobs.JobRow{ID: "j1", SourceTrackID: &track}})
	ing.AfterKindCommit(context.Background(), jobs.KindIngest{Job: jobs.JobRow{ID: "j2"}})
	if len(s.analyzed) != 1 || s.analyzed[0] != "t1" {
		t.Errorf("analyses = %v, want only t1", s.analyzed)
	}
}

type spyAttacher struct {
	err      error
	analyzed []string
}

func (s *spyAttacher) AttachAnalysis(context.Context, *sql.Tx, string, string, string, int64) (ears.AttachedAnalysis, error) {
	return ears.AttachedAnalysis{}, s.err
}

func (s *spyAttacher) AnalyzeTierB(_ context.Context, id string) (ears.TierBOutcome, error) {
	s.analyzed = append(s.analyzed, id)
	return ears.TierBOutcome{}, s.err
}
