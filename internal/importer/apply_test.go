package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/media/ingest"
)

// three recipes, three takes
func simpleInbox(e *env) {
	e.put("r.jsonl", lines(req("ap-1", 1), req("ap-2", 2), req("ap-3", 3)))
	for _, n := range []string{"ap_1", "ap_2", "ap_3"} {
		e.flac(n+".flac", n)
	}
}

func TestApply_Idempotent(t *testing.T) {
	e := newEnv(t)
	simpleInbox(e)
	e.put("pilot.jsonl", lines(req("ap-1", 1), req("ap-2", 2)))
	e.put("idx.csv", "variant_id,seed,variant_type\nap-1,1,x\n")
	e.applyAll()
	before := e.counts()
	if before["tracks"] != 3 || before["recipe_sources"] != 5 {
		t.Fatalf("setup counts = %v", before)
	}
	_, res := e.applyAll()
	after := e.counts()
	for k := range before {
		want := before[k]
		if k == "import_runs" {
			want++
		}
		if after[k] != want {
			t.Errorf("%s: %d -> %d", k, before[k], after[k])
		}
	}
	if res.TracksImported != 0 || res.State != "completed" {
		t.Errorf("run 2 = %+v", res)
	}
}

func TestApply_PartialFailureContinues(t *testing.T) {
	e := newEnv(t)
	simpleInbox(e)
	e.ing.fail = func(r ingest.Request) error {
		if r.RequestID == "ap-2" {
			return &ingest.Error{Code: ingest.CodeToolFailed, Detail: "flac exited 1"}
		}
		return nil
	}
	_, res := e.applyAll()
	if res.State != "completed" || res.Errors != 1 || res.TracksImported != 2 || res.TracksFailed != 1 {
		t.Fatalf("result = %+v", res)
	}
	if e.count("tracks") != 2 {
		t.Errorf("tracks = %d", e.count("tracks"))
	}
	if st := e.scalar(`SELECT state FROM import_runs`); st != "completed" {
		t.Errorf("run state = %s", st)
	}
	er := res.Report.Errors[0]
	if er.Code != ingest.CodeToolFailed || er.Path != "ap_2.flac" {
		t.Errorf("error = %+v", er)
	}
	if got := e.drafts()["ap-2#2"].Status; got != "backlog" {
		t.Errorf("failed take's draft = %s", got)
	}
	// the next run retries the failed take
	e.ing.fail = nil
	_, res2 := e.applyAll()
	if res2.TracksImported != 1 || e.count("tracks") != 3 || e.drafts()["ap-2#2"].Status != "fulfilled" {
		t.Errorf("retry: %+v tracks=%d", res2, e.count("tracks"))
	}
}

func TestApply_FailedTakeKeepsDraftAndCollection(t *testing.T) {
	e := newEnv(t)
	a, b := req("pk-1", 1), req("pk-2", 2)
	e.put("all.jsonl", lines(a, b))
	e.put("pilot.jsonl", lines(a))
	e.flac("pk_1.flac", "pk1")
	e.ing.fail = func(r ingest.Request) error { return &ingest.Error{Code: ingest.CodeInvalidAudio} }
	_, res := e.applyAll()
	if res.TracksFailed != 1 || e.count("tracks") != 0 {
		t.Fatalf("result = %+v", res)
	}
	d := e.drafts()
	if d["pk-1#1"].Status != "backlog" || d["pk-1#1"].FulfilledBy != "" {
		t.Errorf("draft = %+v", d["pk-1#1"])
	}
	if n := e.scalar(`SELECT count(*) FROM draft_collections WHERE draft_id=?`, d["pk-1#1"].ID); n != "1" {
		t.Errorf("collection memberships = %s", n)
	}
}

func TestApply_InsertOrder(t *testing.T) {
	e := newEnv(t)
	a, b := req("io-1", 1), req("io-2", 2)
	e.put("all.jsonl", lines(a, b))
	e.put("pilot.jsonl", lines(a, b))
	e.flac("io_1.flac", "i1")
	e.flac("io_2.flac", "i2")
	var drafts, members, tracks = -1, -1, -1
	var once sync.Once
	e.ing.fail = func(r ingest.Request) error { // runs when the first ingest starts
		once.Do(func() {
			drafts, members, tracks = e.count("recipe_drafts"), e.count("draft_collections"), e.count("tracks")
		})
		return nil
	}
	e.applyAll()
	if drafts != 2 || members != 2 || tracks != 0 {
		t.Errorf("before the first take: drafts=%d collection rows=%d tracks=%d", drafts, members, tracks)
	}
	var bad int
	e.st.R.QueryRow(`SELECT count(*) FROM draft_collections c LEFT JOIN recipe_drafts d ON d.id=c.draft_id WHERE d.id IS NULL`).Scan(&bad)
	if bad != 0 {
		t.Error("dangling collection rows")
	}
}

func TestApply_TxFailureDiscards(t *testing.T) {
	e := newEnv(t)
	e.put("r.jsonl", lines(req("tx-1", 1), req("tx-2", 2)))
	e.flac("Boom.flac", "boom")
	e.flac("tx_2.flac", "tx2")
	e.mapping("audio,request_id,seed\nBoom.flac,tx-1,1\n")
	if _, err := e.st.W.Exec(`CREATE TRIGGER boom BEFORE INSERT ON tracks WHEN NEW.title='Boom' BEGIN SELECT RAISE(ABORT,'boom'); END`); err != nil {
		t.Fatal(err)
	}
	_, res := e.applyAll()
	if res.State != "completed" || res.TracksFailed != 1 || res.TracksImported != 1 || res.Errors != 1 {
		t.Fatalf("result = %+v", res)
	}
	if len(e.ing.discarded) != 1 || len(e.ing.discarded[0].Created) == 0 {
		t.Fatalf("Discard calls = %+v", e.ing.discarded)
	}
	if !strings.Contains(e.ing.discarded[0].MasterRelPath, "tx-1") && !strings.Contains(e.ing.discarded[0].MasterRelPath, "boom") {
		// the master name carries the track slug
		t.Logf("discarded master %s", e.ing.discarded[0].MasterRelPath)
	}
	if e.count("tracks") != 1 || e.count("renditions") != 2 || e.count("analyses") != 2 || e.count("artifacts") != 0 {
		t.Errorf("rows: tracks=%d renditions=%d analyses=%d", e.count("tracks"), e.count("renditions"), e.count("analyses"))
	}
	if res.Report.Errors[0].Code != "DB_INSERT" {
		t.Errorf("error = %+v", res.Report.Errors[0])
	}
}

func TestApply_ConcurrentRunRefused(t *testing.T) {
	e := newEnv(t)
	simpleInbox(e)
	if _, err := e.lib.BeginRun(context.Background(), "cli", "inbox", "x"); err != nil {
		t.Fatal(err)
	}
	im, inv := e.inventory()
	_, err := im.Apply(context.Background(), inv, ApplyOptions{ExpectDigest: inv.PlanDigest()})
	if !errors.Is(err, ErrImportInProgress) || !errors.Is(err, library.ErrImportInProgress) {
		t.Fatalf("err = %v", err)
	}
	if e.count("recipes") != 0 || e.count("import_runs") != 1 {
		t.Errorf("something was written: %v", e.counts())
	}
}

func TestApply_StaleHeartbeatMarkedFailed(t *testing.T) {
	e := newEnv(t)
	simpleInbox(e)
	fresh := e.now
	e.now = fresh.Add(-3 * time.Minute)
	staleID, err := e.lib.BeginRun(context.Background(), "cli", "inbox", "x")
	if err != nil {
		t.Fatal(err)
	}
	e.now = fresh
	_, res := e.applyAll()
	if res.State != "completed" || res.TracksImported != 3 {
		t.Fatalf("result = %+v", res)
	}
	if st, msg := e.scalar(`SELECT state FROM import_runs WHERE id=?`, staleID), e.scalar(`SELECT error FROM import_runs WHERE id=?`, staleID); st != "failed" || msg != "stale: no heartbeat" {
		t.Errorf("stale run = %s / %s", st, msg)
	}
}

func TestApply_HeartbeatRefreshed(t *testing.T) {
	e := newEnv(t)
	simpleInbox(e)
	e.lib = library.New(e.st, e.data, time.Now)
	var beats []string
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		e.ing.fail = func(ingest.Request) error {
			for i := 0; i < 3; i++ {
				time.Sleep(31 * time.Second)
				beats = append(beats, e.scalar(`SELECT heartbeat_at FROM import_runs`))
			}
			return nil
		}
		im, inv := e.inventory(func(o *Options) { o.Now = time.Now; o.Jobs = 1 })
		if _, err := im.Apply(context.Background(), inv, ApplyOptions{ExpectDigest: inv.PlanDigest()}); err != nil {
			t.Fatal(err)
		}
		_ = start
	})
	if len(beats) < 3 {
		t.Fatalf("beats = %v", beats)
	}
	seen := map[string]bool{}
	for _, b := range beats[:3] {
		seen[b] = true
	}
	if len(seen) != 3 {
		t.Errorf("heartbeat not refreshed every 30 s: %v", beats[:3])
	}
	t0, _ := time.Parse(time.RFC3339, beats[0])
	t1, _ := time.Parse(time.RFC3339, beats[1])
	if d := t1.Sub(t0); d != 30*time.Second {
		t.Errorf("heartbeat step = %v", d)
	}
}

func TestApply_PlanDigestBinding(t *testing.T) {
	e := newEnv(t)
	simpleInbox(e)
	im, inv := e.inventory()
	digest := inv.PlanDigest()
	if len(digest) != 64 {
		t.Fatalf("digest = %q", digest)
	}
	// unchanged inbox => identical digest, across dry-runs and across report writes
	if _, err := im.WriteDryRun(inv); err != nil {
		t.Fatal(err)
	}
	_, inv2 := e.inventory()
	if inv2.PlanDigest() != digest {
		t.Error("digest changed between dry-runs of an unchanged inbox")
	}
	// a file arrives after the reviewed dry-run
	e.put("late.jsonl", lines(req("late-1", 9)))
	im3, inv3 := e.inventory()
	if inv3.PlanDigest() == digest {
		t.Fatal("digest ignores the new recipe")
	}
	_, err := im3.Apply(context.Background(), inv3, ApplyOptions{ExpectDigest: digest})
	var pc *PlanChangedError
	if !errors.Is(err, ErrPlanChanged) || !errors.As(err, &pc) || pc.Expected != digest || pc.Actual != inv3.PlanDigest() {
		t.Fatalf("err = %v", err)
	}
	if got := e.counts(); got["import_runs"] != 0 || got["recipes"] != 0 {
		t.Errorf("PLAN_CHANGED wrote something: %v", got)
	}
	added, removed := DiffPlans(inv.PlanEntries(), inv3.PlanEntries())
	if len(added) == 0 || len(removed) != 0 || !strings.Contains(strings.Join(added, "\n"), "late-1") && !strings.Contains(strings.Join(added, "\n"), `"k":"recipe"`) {
		t.Errorf("diff = +%v -%v", added, removed)
	}
	// no digest and no force is refused as well
	if _, err := im3.Apply(context.Background(), inv3, ApplyOptions{}); !errors.Is(err, ErrPlanChanged) {
		t.Errorf("empty digest: %v", err)
	}
	// Force applies the current plan
	res, err := im3.Apply(context.Background(), inv3, ApplyOptions{ExpectDigest: digest, Force: true})
	if err != nil || res.TracksImported != 3 || e.count("recipes") != 4 {
		t.Fatalf("force: %v %+v", err, res)
	}
	// the report of the applied run carries the digest it executed
	if res.Report.PlanDigest != inv3.PlanDigest() || e.scalar(`SELECT plan_digest FROM import_runs`) != inv3.PlanDigest() {
		t.Error("run does not record the applied digest")
	}
}

func TestApply_SourceChanged(t *testing.T) {
	e := newEnv(t)
	simpleInbox(e)
	im, inv := e.inventory()
	// ap_2 is replaced between the dry-run and the apply
	e.flac("ap_2.flac", "ap_2-but-longer-frames-than-before")
	res, err := im.Apply(context.Background(), inv, ApplyOptions{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.TracksImported != 2 || res.TracksFailed != 1 || res.Report.Errors[0].Code != ingest.CodeSourceChanged {
		t.Fatalf("result = %+v / %+v", res, res.Report.Errors)
	}
	if _, ok := e.ing.requestFor("ap-2", 2); ok {
		t.Error("ingest ran on a changed file")
	}
}

func TestApply_ParentFailed(t *testing.T) {
	e := newEnv(t)
	e.put("r.jsonl", lines(req("pf-1", 1), req("pf-1", 2), req("pf-1", 3)))
	for i, n := range []string{"a", "b", "c"} {
		e.flac(n+".flac", n)
		e.age(n+".flac", e.now.Add(-time.Duration(10-i)*time.Hour))
	}
	e.mapping("audio,request_id,seed\na.flac,pf-1,1\nb.flac,pf-1,2\nc.flac,pf-1,3\n")
	e.ing.fail = func(r ingest.Request) error {
		if r.Seed == 1 {
			return &ingest.Error{Code: ingest.CodeIO}
		}
		return nil
	}
	_, res := e.applyAll()
	if res.TracksImported != 2 || res.TracksFailed != 1 {
		t.Fatalf("result = %+v", res)
	}
	if !hasCode(res.Report.Warnings, "parent_failed") {
		t.Errorf("warnings = %v", issueCodes(res.Report.Warnings))
	}
	tr := e.tracks()
	// siblings hang under the (failed) root, so each survivor becomes its own root
	for _, k := range []string{"pf-1#2", "pf-1#3"} {
		if x := tr[k]; x.Parent != "" || x.Root != x.ID || x.Gen != 0 || x.Rule != "" {
			t.Errorf("%s must be a root: %+v", k, x)
		}
	}
}

func TestApply_JobsBoundConcurrency(t *testing.T) {
	e := newEnv(t)
	var ls []string
	for i := 1; i <= 8; i++ {
		id := fmt.Sprintf("job-%d", i)
		ls = append(ls, req(id, i))
		e.flac(fmt.Sprintf("job_%d.flac", i), id)
	}
	e.put("r.jsonl", lines(ls...))
	e.ing.delay = 40 * time.Millisecond
	e.applyAll(func(o *Options) { o.Jobs = 3 })
	if e.ing.peak < 2 || e.ing.peak > 3 {
		t.Errorf("peak concurrency = %d, want 2..3", e.ing.peak)
	}
	if e.count("tracks") != 8 {
		t.Errorf("tracks = %d", e.count("tracks"))
	}
}

func TestApply_RunRecordAndReportFile(t *testing.T) {
	e := newEnv(t)
	simpleInbox(e)
	_, res := e.applyAll()
	var state, summary, report, inbox string
	if err := e.st.R.QueryRow(`SELECT state, summary_json, report_json, inbox_label FROM import_runs WHERE id=?`, res.RunID).Scan(&state, &summary, &report, &inbox); err != nil {
		t.Fatal(err)
	}
	if state != "completed" || inbox != "inbox" {
		t.Errorf("run = %s %s", state, inbox)
	}
	var sum map[string]any
	if err := json.Unmarshal([]byte(summary), &sum); err != nil || sum["tracks_new"] != float64(3) || sum["tracks_imported"] != float64(3) {
		t.Errorf("summary = %s", summary)
	}
	var rep Report
	if err := json.Unmarshal([]byte(report), &rep); err != nil || rep.ReportID != res.RunID || rep.DryRun || rep.Result == nil || rep.Result.TracksImported != 3 {
		t.Errorf("report = %v %+v", err, rep.Result)
	}
	fi, err := os.Stat(filepath.Join(e.dataDir, res.ReportPath))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("report file: %v %v", fi, err)
	}
	if ok, _ := regexp.MatchString(`^import/reports/\d{8}T\d{6}Z-`+res.RunID+`\.json$`, res.ReportPath); !ok {
		t.Errorf("report path = %s", res.ReportPath)
	}
	if di, _ := os.Stat(filepath.Join(e.dataDir, "import")); di.Mode().Perm() != 0o700 {
		t.Errorf("import/ mode = %v", di.Mode())
	}
}

func TestApply_Cancelled(t *testing.T) {
	e := newEnv(t)
	simpleInbox(e)
	ctx, cancel := context.WithCancel(context.Background())
	e.ing.fail = func(ingest.Request) error { cancel(); return nil }
	e.ing.delay = 20 * time.Millisecond
	im, inv := e.inventory(func(o *Options) { o.Jobs = 2 })
	_, err := im.Apply(ctx, inv, ApplyOptions{Force: true})
	if err == nil {
		t.Fatal("expected a context error")
	}
	// ingests that succeeded but were never committed must be discarded
	e.ing.mu.Lock()
	nd := len(e.ing.discarded)
	e.ing.mu.Unlock()
	if nd == 0 || e.count("tracks") != 0 {
		t.Errorf("discarded = %d, tracks = %d", nd, e.count("tracks"))
	}
	if st := e.scalar(`SELECT state FROM import_runs`); st != "failed" {
		t.Errorf("run state = %s", st)
	}
}

func TestApply_SweepsStaleWorkDirsOnly(t *testing.T) {
	e := newEnv(t)
	simpleInbox(e)
	for _, d := range []string{"incoming/ingest-old", "incoming/ingest-new", "incoming/other-old"} {
		if err := e.data.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	old := e.now.Add(-48 * time.Hour)
	for _, d := range []string{"ingest-old", "other-old"} {
		if err := os.Chtimes(filepath.Join(e.dataDir, "incoming", d), old, old); err != nil {
			t.Fatal(err)
		}
	}
	im, inv := e.inventory()
	if _, err := im.WriteDryRun(inv); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"ingest-old", "ingest-new", "other-old"} {
		if _, err := os.Stat(filepath.Join(e.dataDir, "incoming", d)); err != nil {
			t.Fatalf("dry-run removed %s", d)
		}
	}
	if _, err := im.Apply(context.Background(), inv, ApplyOptions{ExpectDigest: inv.PlanDigest()}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.dataDir, "incoming", "ingest-old")); err == nil {
		t.Error("stale work dir survived apply")
	}
	for _, d := range []string{"ingest-new", "other-old"} {
		if _, err := os.Stat(filepath.Join(e.dataDir, "incoming", d)); err != nil {
			t.Errorf("apply removed %s", d)
		}
	}
}

func TestApply_NeedsWriteSide(t *testing.T) {
	e := newEnv(t)
	simpleInbox(e)
	im, err := New(nil, nil, e.data, nil, Options{Inbox: e.importer().o.Inbox})
	if err != nil {
		t.Fatal(err)
	}
	inv, err := im.Inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := im.Apply(context.Background(), inv, ApplyOptions{Force: true}); err == nil {
		t.Error("apply without store/ingester must fail")
	}
	if _, err := New(nil, nil, e.data, nil, Options{}); err == nil {
		t.Error("New without an inbox must fail")
	}
}

func TestApply_ErrorDetailNotInReport(t *testing.T) {
	e := newEnv(t)
	simpleInbox(e)
	e.ing.fail = func(r ingest.Request) error {
		if r.RequestID == "ap-2" {
			return &ingest.Error{Code: ingest.CodeToolFailed, Detail: "flac: cannot open " + e.dataDir + "/incoming/x/in.flac"}
		}
		return nil
	}
	_, res := e.applyAll()
	b, _ := json.Marshal(res.Report)
	if strings.Contains(string(b), e.dataDir) || strings.Contains(e.scalar(`SELECT report_json FROM import_runs`), e.dataDir) {
		t.Errorf("report leaks an absolute path: %s", b)
	}
	if res.TracksFailed != 1 {
		t.Errorf("result = %+v", res)
	}
}
