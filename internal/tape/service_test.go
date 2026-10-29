package tape_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/media/cover"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/tape"
	"github.com/nicolasacchi/metamusic/internal/tape/tapetest"
)

var (
	human = auth.Principal{Kind: auth.KindHuman, ID: "op", Name: "op", Via: "local"}
	agent = auth.Principal{Kind: auth.KindAgent, ID: "ag1", Name: "synthetic-agent", Via: "token"}
)

const (
	rec        = "rec-1"
	museCanary = "Synthetic Muse Canary"
	titleOne   = "Synthetic Take Title One"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeCovers answers a cover key derived from the Record id, so a different Record gets a different key.
type fakeCovers struct{ calls int }

func (f *fakeCovers) EnsureMosaic(_ context.Context, in cover.Input, size int) (serve.Object, string, error) {
	f.calls++
	return serve.Object{}, fmt.Sprintf("cover-%d", size), nil
}

type rig struct {
	t      *testing.T
	st     *store.Store
	data   *os.Root
	dir    string
	src    *tapetest.FakeSource
	loud   *tapetest.FakeLoudness
	pipe   *tapetest.FakePipeline
	covers *fakeCovers
	clk    *clock
	svc    *tape.Service
	n      int
}

func newRig(t *testing.T, mod ...func(*tape.Options)) *rig {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "t.db")
	if err := os.WriteFile(dbPath, migratedImage(t), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	r := &rig{t: t, st: st, data: root, dir: dir, src: tapetest.NewFakeSource(), loud: tapetest.NewFakeLoudness(),
		pipe: tapetest.NewFakePipeline(t.TempDir()), covers: &fakeCovers{}, clk: &clock{t: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}}
	r.insertRecord(rec, "published", 3)
	r.src.Set(snapshot(rec, 3, 3))
	o := tape.Options{Store: st, Source: r.src, Loudness: r.loud, Covers: r.covers, Data: root, Pipeline: r.pipe,
		Signer: serve.NewSigner([]byte("synthetic-key"), r.clk.now), Now: r.clk.now, Owner: "runner-a", FlacVersion: "flac 1.5.0", BuildVersion: "v-test",
		NewID: func() string { r.n++; return fmt.Sprintf("tape-%03d", r.n) }}
	for _, m := range mod {
		m(&o)
	}
	r.svc = tape.New(o)
	return r
}

var (
	imageOnce sync.Once
	imageDB   []byte
)

// migratedImage is an empty, migrated database built once per process; every rig starts from a copy.
func migratedImage(t *testing.T) []byte {
	imageOnce.Do(func() {
		dir, err := os.MkdirTemp("", "mm-tape-image-")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(dir)
		p := filepath.Join(dir, "i.db")
		st, err := store.Open(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.W.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			t.Fatal(err)
		}
		st.Close()
		if imageDB, err = os.ReadFile(p); err != nil {
			t.Fatal(err)
		}
	})
	if imageDB == nil {
		t.Fatal("the database image could not be built")
	}
	return imageDB
}

func (r *rig) insertRecord(id, status string, revision int) {
	r.t.Helper()
	pub := "NULL"
	if status == "published" {
		pub = "'2026-01-01T00:00:00.000Z'"
	}
	pubBy := "NULL"
	if status == "published" {
		pubBy = "'human:op'"
	}
	q := fmt.Sprintf(`INSERT INTO records(id, title, cover_seed, status, revision, created_by, created_at, updated_by, updated_at, published_by, published_at, first_published_at)
		VALUES (?, 'Synthetic Record', 1, ?, ?, 'human:op', '2026-01-01T00:00:00.000Z', 'human:op', '2026-01-01T00:00:00.000Z', %s, %s, %s)`, pubBy, pub, pub)
	if _, err := r.st.W.Exec(q, id, status, revision); err != nil {
		r.t.Fatal(err)
	}
}

// snapshot is a published Record of n resolved Takes, 180 s each, measured at -14 LUFS / -3 dBTP.
func snapshot(id string, revision, n int) tape.Snapshot {
	s := tape.Snapshot{RecordID: id, Revision: revision, Status: "published", Title: "Synthetic Record: Side A",
		Tags:        []flacmeta.Comment{{Key: "TITLE", Value: "Synthetic Record"}, {Key: "ARTIST", Value: "metamusic"}},
		Description: "Synthetic liner."}
	for i := 1; i <= n; i++ {
		title := fmt.Sprintf("Synthetic Take Title %d", i)
		if i == 1 {
			title = titleOne
		}
		s.Items = append(s.Items, tape.Item{SlotID: fmt.Sprintf("slot-%d", i), Position: i, TakeID: fmt.Sprintf("take-%d", i), Title: title,
			MasterSHA256: strings.Repeat(fmt.Sprintf("%02x", i), 32), SampleRate: 48000, Bits: 24, Channels: 2, DurationS: 180,
			Measure: tape.Measure{LUFS: -14, TruePeakDBTP: -3, OK: true}})
	}
	return s
}

func (r *rig) export(p auth.Principal, in tape.ExportInput) (tape.Tape, error) {
	r.t.Helper()
	in.IncludeLiner = true
	return r.svc.Export(context.Background(), p, rec, in)
}

func (r *rig) mustExport(in tape.ExportInput) tape.Tape {
	r.t.Helper()
	tp, err := r.export(human, in)
	if err != nil {
		r.t.Fatalf("export: %v", err)
	}
	return tp
}

func (r *rig) runOne() bool {
	r.t.Helper()
	ran, err := r.svc.RunOne(context.Background())
	if err != nil {
		r.t.Fatalf("RunOne: %v", err)
	}
	return ran
}

func (r *rig) get(id string) tape.Tape {
	r.t.Helper()
	tp, err := r.svc.Get(auth.WithPrincipal(context.Background(), human), id)
	if err != nil {
		r.t.Fatal(err)
	}
	return tp
}

func (r *rig) exec(q string, args ...any) {
	r.t.Helper()
	if _, err := r.st.W.Exec(q, args...); err != nil {
		r.t.Fatalf("%s: %v", q, err)
	}
}

func (r *rig) scalar(q string, args ...any) string {
	r.t.Helper()
	var v any
	if err := r.st.R.QueryRow(q, args...).Scan(&v); err != nil {
		r.t.Fatalf("%s: %v", q, err)
	}
	return fmt.Sprint(v)
}

func wantProblem(t *testing.T, err error, status int, code string) *problem.Error {
	t.Helper()
	var pe *problem.Error
	if !errors.As(err, &pe) || pe.Status != status || pe.Code != code {
		t.Fatalf("err = %v, want %d %s", err, status, code)
	}
	return pe
}

func f64(v float64) *float64 { return &v }
func ip(v int) *int          { return &v }

// TestTape_ExportRules: the check order of C128.1 and every refusal.
func TestTape_ExportRules(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.svc.Export(ctx, human, "nope", tape.ExportInput{}); err == nil {
		t.Error("unknown record")
	} else {
		wantProblem(t, err, 404, "NOT_FOUND")
	}
	r.insertRecord("rec-draft", "draft", 1)
	_, err := r.svc.Export(ctx, human, "rec-draft", tape.ExportInput{})
	wantProblem(t, err, 409, "RECORD_NOT_PUBLISHED")
	_, err = r.export(human, tape.ExportInput{Revision: ip(2)})
	if pe := wantProblem(t, err, 409, "STALE_REVISION"); pe.Details["revision"] != 3 {
		t.Errorf("details = %v", pe.Details)
	}
	empty := snapshot(rec, 3, 0)
	r.src.Set(empty)
	_, err = r.export(human, tape.ExportInput{})
	wantProblem(t, err, 422, "RECORD_EMPTY")
	r.src.Set(snapshot(rec, 3, 61))
	_, err = r.export(human, tape.ExportInput{})
	wantProblem(t, err, 422, tape.ErrTooLong)
	long := snapshot(rec, 3, 2)
	long.Items[0].DurationS, long.Items[1].DurationS = 3600, 3600.5
	r.src.Set(long)
	_, err = r.export(human, tape.ExportInput{})
	wantProblem(t, err, 422, tape.ErrTooLong)
	r.src.Set(snapshot(rec, 3, 3))
	for _, bad := range []float64{-30.1, -8.9} {
		_, err = r.export(human, tape.ExportInput{TargetLUFS: f64(bad)})
		wantProblem(t, err, 422, "VALIDATION_FAILED")
	}
	_, err = r.export(agent, tape.ExportInput{AcceptWarnings: true})
	wantProblem(t, err, 403, "HUMAN_ONLY")
	if tp, err := r.export(human, tape.ExportInput{Revision: ip(3), TargetLUFS: f64(-30)}); err != nil || tp.State != tape.StateQueued || tp.TargetLUFS != -30 {
		t.Errorf("edge target: %+v %v", tp, err)
	}
	// no flac: only when the version probe at startup found nothing
	r2 := newRig(t, func(o *tape.Options) { o.FlacVersion = "" })
	_, err = r2.export(human, tape.ExportInput{})
	if pe := wantProblem(t, err, 503, "UNAVAILABLE"); pe.Details["component"] != "flac" {
		t.Errorf("details = %v", pe.Details)
	}
	if _, err := tape.New(tape.Options{}).Export(ctx, human, rec, tape.ExportInput{}); err == nil {
		t.Error("a service without collaborators must refuse, not panic")
	}
}

// TestTape_ReviewGate: an undecided or ineligible slot needs a human's accept_warnings.
func TestTape_ReviewGate(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	s := snapshot(rec, 3, 3)
	s.Warnings = []tape.SlotWarning{{SlotID: "slot-2", Code: "undecided_pick"}, {SlotID: "slot-3", Code: "pin_ineligible"}}
	r.src.Set(s)
	_, err := r.export(human, tape.ExportInput{})
	pe := wantProblem(t, err, 409, tape.ErrNeedsReview)
	if slots, _ := pe.Details["slots"].([]map[string]string); len(slots) != 2 || slots[0]["slot_id"] != "slot-2" || slots[1]["code"] != "pin_ineligible" {
		t.Errorf("details = %v", pe.Details)
	}
	_, err = r.export(agent, tape.ExportInput{AcceptWarnings: true})
	wantProblem(t, err, 403, "HUMAN_ONLY")
	tp, err := r.export(human, tape.ExportInput{AcceptWarnings: true})
	if err != nil || !tp.AcceptedWarnings || len(tp.Warnings) != 2 {
		t.Fatalf("human accept: %+v %v", tp, err)
	}
	if n := r.scalar(`SELECT count(*) FROM tapes`); n != "1" {
		t.Errorf("rows = %s", n)
	}
}

// TestTape_Reuse: the key decides identity (C128.5).
func TestTape_Reuse(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.mustExport(tape.ExportInput{})
	if a.Reused {
		t.Error("first export reused")
	}
	b := r.mustExport(tape.ExportInput{})
	if !b.Reused || b.ID != a.ID {
		t.Fatalf("same snapshot twice: %s reused=%v, want %s", b.ID, b.Reused, a.ID)
	}
	// the Record revision alone does not change the key
	r.exec(`UPDATE records SET revision = 4 WHERE id = ?`, rec)
	s := snapshot(rec, 4, 3)
	r.src.Set(s)
	if c := r.mustExport(tape.ExportInput{}); c.ID != a.ID || !c.Reused {
		t.Errorf("revision alone: %s reused=%v", c.ID, c.Reused)
	}
	// a Pick change that swaps a Take (other master) is a new key and row
	s2 := snapshot(rec, 4, 3)
	s2.Items[1].MasterSHA256 = strings.Repeat("ee", 32)
	r.src.Set(s2)
	if c := r.mustExport(tape.ExportInput{}); c.ID == a.ID || c.Reused {
		t.Errorf("take change: %s reused=%v", c.ID, c.Reused)
	}
	// a title change is a new key (the chapter names are part of it)
	s3 := snapshot(rec, 4, 3)
	s3.Items[0].Title = "Synthetic Renamed Take"
	r.src.Set(s3)
	d := r.mustExport(tape.ExportInput{})
	if d.ID == a.ID || d.Reused {
		t.Errorf("title change: %s reused=%v", d.ID, d.Reused)
	}
	if n := r.scalar(`SELECT count(*) FROM tapes`); n != "3" {
		t.Errorf("rows = %s, want 3", n)
	}
}

// TestTape_KeyTable: what is and what is not in the input key.
func TestTape_KeyTable(t *testing.T) {
	t.Parallel()
	same := func(mod func(*tape.Snapshot), in tape.ExportInput) bool { return keyChange(t, mod, in, nil) }
	if !same(func(s *tape.Snapshot) { s.Revision = 9 }, tape.ExportInput{}) {
		t.Error("revision is in the key")
	}
	if !same(nil, tape.ExportInput{TargetLUFS: f64(-14.0)}) || !same(nil, tape.ExportInput{TargetLUFS: f64(-14.04)}) {
		t.Error("-14.04 and -14.0 are not one key")
	}
	if !same(nil, tape.ExportInput{AcceptWarnings: true}) {
		t.Error("accept_warnings is in the key")
	}
	for name, mod := range map[string]func(*tape.Snapshot){
		"master":      func(s *tape.Snapshot) { s.Items[0].MasterSHA256 = strings.Repeat("fe", 32) },
		"tag":         func(s *tape.Snapshot) { s.Tags[0].Value = "Other" },
		"description": func(s *tape.Snapshot) { s.Description = "Other." },
		"muse":        func(s *tape.Snapshot) { s.MuseNames = true },
		"resampled":   func(s *tape.Snapshot) { s.Items[0].SampleRate = 44100 },
		"upmixed":     func(s *tape.Snapshot) { s.Items[0].Channels = 1 },
		"downmixed":   func(s *tape.Snapshot) { s.Items[0].Channels = 6 },
		"loudness":    func(s *tape.Snapshot) { s.Items[0].Measure.LUFS = -20 },
		"order":       func(s *tape.Snapshot) { s.Items[0], s.Items[1] = s.Items[1], s.Items[0] },
	} {
		if same(mod, tape.ExportInput{}) {
			t.Errorf("%s is not in the key", name)
		}
	}
	if same(nil, tape.ExportInput{TargetLUFS: f64(-16)}) {
		t.Error("target is not in the key")
	}
	// the encoder version, the build version and the cover are in the key too
	if keyChange(t, nil, tape.ExportInput{}, func(o *tape.Options) { o.FlacVersion = "flac 1.4.3" }) {
		t.Error("flac version is not in the key")
	}
	if keyChange(t, nil, tape.ExportInput{}, func(o *tape.Options) { o.BuildVersion = "v-other" }) {
		t.Error("build version is not in the key")
	}
	if keyChange(t, nil, tape.ExportInput{}, func(o *tape.Options) { o.Covers = coverOf("cover-other") }) {
		t.Error("cover is not in the key")
	}
}

type coverOf string

func (c coverOf) EnsureMosaic(context.Context, cover.Input, int) (serve.Object, string, error) {
	return serve.Object{}, string(c), nil
}

// keyChange reports whether the export of the modified inputs lands on the same key as the baseline. Snapshot variants share
// one rig (each tape is failed after its key is read, which frees the quota); option variants get their own service.
func keyChange(t *testing.T, mod func(*tape.Snapshot), in tape.ExportInput, opt func(*tape.Options)) bool {
	t.Helper()
	keyRigOnce.Do(func() {
		keyRig = newRig(t)
		keyBase = exportKey(keyRig, nil, tape.ExportInput{})
	})
	r := keyRig
	if opt != nil {
		r = newRig(t, opt)
	}
	return keyBase == exportKey(r, mod, in)
}

var (
	keyRigOnce sync.Once
	keyRig     *rig
	keyBase    string
	keyMu      sync.Mutex
)

func exportKey(r *rig, mod func(*tape.Snapshot), in tape.ExportInput) string {
	keyMu.Lock()
	defer keyMu.Unlock()
	s := snapshot(rec, 3, 3)
	if mod != nil {
		mod(&s)
	}
	r.src.Set(s)
	tp := r.mustExport(in)
	k := keyOf(r, tp.ID)
	r.exec(`UPDATE tapes SET state='failed', error_code='TAPE_ENCODE_FAILED', finished_at='2026-03-01T12:00:00.000Z' WHERE id=?`, tp.ID)
	return k
}

func keyOf(r *rig, id string) string { return r.scalar(`SELECT key FROM tapes WHERE id = ?`, id) }

// TestTape_Quotas: three live tapes per Record, 8 GiB of ready tapes in total; a reused key is never blocked.
func TestTape_Quotas(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	for i := 1; i <= tape.MaxLivePerRecord; i++ {
		r.mustExport(tape.ExportInput{TargetLUFS: f64(-14 - float64(i))})
	}
	_, err := r.export(human, tape.ExportInput{TargetLUFS: f64(-25)})
	if pe := wantProblem(t, err, 409, tape.ErrQuota); pe.Details["scope"] != "record" {
		t.Errorf("details = %v", pe.Details)
	}
	if again := r.mustExport(tape.ExportInput{TargetLUFS: f64(-15)}); !again.Reused {
		t.Error("a reused key must not be blocked by the quota")
	}
	// a failed tape leaves the quota
	r.exec(`UPDATE tapes SET state='failed', error_code='TAPE_ENCODE_FAILED', finished_at='2026-03-01T12:00:00.000Z' WHERE id='tape-001'`)
	if tp := r.mustExport(tape.ExportInput{TargetLUFS: f64(-25)}); tp.State != tape.StateQueued {
		t.Errorf("after a failure: %+v", tp)
	}
	// total: ready bytes at the cap, on a second Record
	r2 := newRig(t)
	big := func(id, state string) {
		r2.exec(`INSERT INTO tapes(id, record_id, record_revision, key, state, params_json, plan_json, contains_lossy, lossy_sources, total_samples, bytes, sha256, path, filename,
			principal_id, created_at, finished_at) VALUES (?,'rec-1',1,?,?,'{}','{}',0,0,10,?,?, 'derived/tapes/aa/x.flac','x.flac','human:op','2026-03-01T11:00:00.000Z','2026-03-01T11:00:00.000Z')`,
			id, strings.Repeat(id[len(id)-1:], 64), state, int64(tape.MaxReadyBytes), strings.Repeat(id[len(id)-1:], 64))
	}
	big("big1", "expired")
	r2.mustExport(tape.ExportInput{}) // an expired tape does not count
	big("big2", "ready")
	_, err = r2.export(human, tape.ExportInput{TargetLUFS: f64(-20)})
	if pe := wantProblem(t, err, 409, tape.ErrQuota); pe.Details["scope"] != "total" {
		t.Errorf("details = %v", pe.Details)
	}
}

// TestTape_Runner_States: queued, running, ready; a failure keeps no file; a failed snapshot re-exported is a new row.
func TestTape_Runner_States(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	if r.runOne() {
		t.Fatal("nothing is queued")
	}
	q := r.mustExport(tape.ExportInput{})
	if q.State != tape.StateQueued || q.Progress != nil || q.Download != nil || q.Chapters != nil {
		t.Fatalf("queued: %+v", q)
	}
	var seen []tape.State
	r.pipe.Phases = []string{"decode", "encode", "verify"}
	if !r.runOne() {
		t.Fatal("RunOne found no tape")
	}
	seen = append(seen, r.get(q.ID).State)
	done := r.get(q.ID)
	if seen[0] != tape.StateReady || done.SHA256 == nil || done.Bytes == nil || *done.Bytes <= 0 || done.FinishedAt == nil || done.StartedAt == nil {
		t.Fatalf("ready: %+v", done)
	}
	if len(done.Chapters) != 3 || done.Chapters[1].StartMS != 180000 || done.TotalMS == nil || *done.TotalMS != 540000 {
		t.Errorf("chapters %+v total %v", done.Chapters, done.TotalMS)
	}
	if done.Download == nil || !strings.HasPrefix(done.Download.URL, "/media/") || !strings.Contains(done.Download.URL, ".flac") ||
		done.Download.Filename != "synthetic-record--side-a-20260301-tape.flac" {
		t.Errorf("download %+v", done.Download)
	}
	rel := r.scalar(`SELECT path FROM tapes WHERE id = ?`, q.ID)
	info, err := r.data.Stat(rel)
	if err != nil || info.Mode().Perm() != 0o444 || !strings.HasPrefix(rel, "derived/tapes/"+(*done.SHA256)[:2]+"/") {
		t.Errorf("file %s: %v %v", rel, info, err)
	}
	// the pipeline got the plan: chapter names, tags and cover key, and no row of titles was stored
	job := r.pipe.Jobs()[0]
	if len(job.Chapters) != 3 || job.Chapters[0] != titleOne || job.CoverKey != "cover-512" || len(job.Tags) != 2 || job.TargetLUFS != -14 {
		t.Errorf("job %+v", job)
	}
	if plan := r.scalar(`SELECT plan_json FROM tapes WHERE id = ?`, q.ID); strings.Contains(plan, "Synthetic Take Title") || strings.Contains(plan, "Synthetic Record") {
		t.Errorf("plan_json holds a title: %s", plan)
	}
	// a second identical tape finds the same bytes under the same name: expire the first, export again
	// failure: no file, a code, no leftover
	r.pipe.Err = fmt.Errorf("TAPE_CLIPPED: item 2: samples clipped at %s", r.dir)
	r.exec(`UPDATE tapes SET state='expired' WHERE id=?`, q.ID)
	f := r.mustExport(tape.ExportInput{})
	if f.ID == q.ID {
		t.Fatal("an expired tape must not be reused")
	}
	r.runOne()
	fd := r.get(f.ID)
	if fd.State != tape.StateFailed || fd.Error == nil || fd.Error.Code != tape.ErrClipped || strings.Contains(fd.Error.Message, r.dir) || fd.Download != nil {
		t.Fatalf("failed: %+v", fd)
	}
	if r.scalar(`SELECT coalesce(path, 'none') FROM tapes WHERE id = ?`, f.ID) != "none" || r.scalar(`SELECT coalesce(owner, 'none') FROM tapes WHERE id = ?`, f.ID) != "none" {
		t.Error("a failed tape keeps no file and no lease")
	}
	// re-exporting after the failure is a new row
	r.pipe.Err = nil
	again := r.mustExport(tape.ExportInput{})
	if again.ID == f.ID || again.Reused || again.State != tape.StateQueued {
		t.Fatalf("after a failure: %+v", again)
	}
	r.runOne()
	if got := r.get(again.ID); got.State != tape.StateReady {
		t.Fatalf("second run: %+v", got)
	}
	// the other failure codes
	for msg, want := range map[string]string{"plain failure": tape.ErrEncodeFailed, "TAPE_DECODE_FAILED: item 1": tape.ErrDecodeFailed} {
		r.pipe.Err = errors.New(msg)
		x := r.mustExport(tape.ExportInput{TargetLUFS: f64(-20 - float64(len(msg)%7))})
		r.runOne()
		if got := r.get(x.ID); got.Error == nil || got.Error.Code != want {
			t.Errorf("%q: %+v", msg, got.Error)
		}
		r.exec(`UPDATE tapes SET state = 'failed' WHERE id = ? AND state <> 'failed'`, x.ID)
	}
}

// TestTape_RunnerSourceChanged: a Record that changed between the export and the run is not rendered (the key is the check).
func TestTape_RunnerSourceChanged(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	q := r.mustExport(tape.ExportInput{})
	s := snapshot(rec, 3, 3)
	s.Items[2].Title = "Synthetic Retitled"
	r.src.Set(s)
	r.runOne()
	if got := r.get(q.ID); got.State != tape.StateFailed || got.Error.Code != tape.ErrSourceChanged || len(r.pipe.Jobs()) != 0 {
		t.Errorf("%+v jobs=%d", got, len(r.pipe.Jobs()))
	}
	r.src.Set(snapshot(rec, 3, 3))
	q2 := r.mustExport(tape.ExportInput{})
	unp := snapshot(rec, 3, 3)
	unp.Status = "draft"
	r.src.Set(unp)
	r.runOne()
	if got := r.get(q2.ID); got.State != tape.StateFailed || got.Error.Code != tape.ErrSourceChanged {
		t.Errorf("unpublished: %+v", got)
	}
}

// TestTape_Recover: an expired lease re-queues up to twice, the third expiry fails; progress writes never reset it.
func TestTape_Recover(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	q := r.mustExport(tape.ExportInput{})
	ctx := context.Background()
	claim := func() {
		t.Helper()
		// claim as the runner would, then die before rendering: use a pipeline that blocks until the context ends
		r.exec(`UPDATE tapes SET state='running', owner='dead', lease_expires_at=?, started_at=? WHERE id=?`,
			store.FormatTime(r.clk.now().Add(tape.LeaseTTL)), store.FormatTime(r.clk.now()), q.ID)
	}
	for want := 1; want <= 2; want++ {
		claim()
		if err := r.svc.Recover(ctx); err != nil || r.get(q.ID).State != tape.StateRunning {
			t.Fatalf("a live lease must stay: %v %v", err, r.get(q.ID).State)
		}
		r.exec(`UPDATE tapes SET progress_json='{"phase":"encode","item":1,"items":3}' WHERE id=?`, q.ID) // a progress rewrite
		r.clk.add(tape.LeaseTTL + time.Second)
		if err := r.svc.Recover(ctx); err != nil {
			t.Fatal(err)
		}
		if got := r.get(q.ID); got.State != tape.StateQueued || r.scalar(`SELECT attempts FROM tapes WHERE id=?`, q.ID) != fmt.Sprint(want) ||
			r.scalar(`SELECT coalesce(owner,'-') FROM tapes WHERE id=?`, q.ID) != "-" {
			t.Fatalf("expiry %d: %+v attempts=%s", want, got, r.scalar(`SELECT attempts FROM tapes WHERE id=?`, q.ID))
		}
	}
	claim()
	r.clk.add(tape.LeaseTTL + time.Second)
	if err := r.svc.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if got := r.get(q.ID); got.State != tape.StateFailed || got.Error.Code != tape.ErrInterrupted {
		t.Fatalf("third expiry: %+v", got)
	}
	// a recovered tape renders
	r2 := newRig(t)
	q2 := r2.mustExport(tape.ExportInput{})
	r2.exec(`UPDATE tapes SET state='running', owner='dead', lease_expires_at='2026-03-01T11:00:00.000Z' WHERE id=?`, q2.ID)
	if r2.runOne() {
		t.Fatal("a running tape is not claimable")
	}
	if err := r2.svc.Recover(ctx); err != nil || !r2.runOne() || r2.get(q2.ID).State != tape.StateReady {
		t.Errorf("recovered run: %+v", r2.get(q2.ID))
	}
}

// TestTape_LeaseRenewed: a slow render keeps its lease alive and a lost lease stops the render without a failure row of ours.
func TestTape_LeaseRenewed(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	slow := &slowPipeline{inner: r.pipe, started: make(chan struct{}), release: make(chan struct{})}
	r2 := newRig(t, func(o *tape.Options) { o.Pipeline = slow })
	tape.SetTimings(r2.svc, 10*time.Millisecond, time.Second)
	q2 := r2.mustExport(tape.ExportInput{})
	done := make(chan struct{})
	go func() { r2.runOne(); close(done) }()
	<-slow.started
	first := r2.scalar(`SELECT lease_expires_at FROM tapes WHERE id=?`, q2.ID)
	r2.clk.add(30 * time.Second)
	deadline := time.Now().Add(5 * time.Second)
	for r2.scalar(`SELECT lease_expires_at FROM tapes WHERE id=?`, q2.ID) == first && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if r2.scalar(`SELECT lease_expires_at FROM tapes WHERE id=?`, q2.ID) == first {
		t.Error("the lease was not renewed")
	}
	close(slow.release)
	<-done
	if r2.get(q2.ID).State != tape.StateReady {
		t.Errorf("state %s", r2.get(q2.ID).State)
	}
}

type slowPipeline struct {
	inner   tape.Pipeline
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *slowPipeline) Render(ctx context.Context, j tape.Job, pr func(tape.Progress)) (tape.Result, error) {
	p.once.Do(func() { close(p.started) })
	select {
	case <-p.release:
	case <-ctx.Done():
		return tape.Result{}, ctx.Err()
	}
	return p.inner.Render(ctx, j, pr)
}

// TestTape_Sweep: the 14-day rule, the file goes after the commit, orphans older than a day go and younger ones stay.
func TestTape_Sweep(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	a := r.mustExport(tape.ExportInput{})
	r.runOne()
	r.clk.add(12 * 24 * time.Hour)
	b := r.mustExport(tape.ExportInput{TargetLUFS: f64(-20)})
	r.runOne()
	relA, relB := r.scalar(`SELECT path FROM tapes WHERE id=?`, a.ID), r.scalar(`SELECT path FROM tapes WHERE id=?`, b.ID)
	// orphans: one old, one young
	for _, p := range []string{"derived/tapes/zz/old.flac", "derived/tapes/zz/young.flac"} {
		if err := r.data.MkdirAll("derived/tapes/zz", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := r.data.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := r.clk.now().Add(-25 * time.Hour)
	if err := os.Chtimes(filepath.Join(r.dir, "derived/tapes/zz/old.flac"), old, old); err != nil {
		t.Fatal(err)
	}
	r.clk.add(3 * 24 * time.Hour) // A is 15 days old, B 3
	n, err := r.svc.Sweep(ctx, tape.RetentionDays*24*time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("Sweep = %d, %v", n, err)
	}
	if st := r.get(a.ID).State; st != tape.StateExpired {
		t.Errorf("A is %s", st)
	}
	if st := r.get(b.ID).State; st != tape.StateReady {
		t.Errorf("B is %s", st)
	}
	if exp := r.get(a.ID); exp.Download != nil || exp.SHA256 == nil {
		t.Errorf("an expired tape has no link: %+v", exp.Download)
	}
	for p, want := range map[string]bool{relA: false, relB: true, "derived/tapes/zz/old.flac": false, "derived/tapes/zz/young.flac": true} {
		if _, err := r.data.Stat(p); (err == nil) != want {
			t.Errorf("%s exists=%v, want %v", p, err == nil, want)
		}
	}
	if n, err := r.svc.Sweep(ctx, tape.RetentionDays*24*time.Hour); err != nil || n != 0 {
		t.Errorf("second sweep = %d, %v", n, err)
	}
	// the expired tape can be exported again: a new row
	if again := r.mustExport(tape.ExportInput{}); again.ID == a.ID || again.Reused {
		t.Errorf("re-export after expiry: %+v", again)
	}
}

// TestTape_Resolve: only a ready tape's sha answers, with its download name; master shas never do.
func TestTape_Resolve(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	q := r.mustExport(tape.ExportInput{})
	if _, err := r.svc.Resolve(ctx, strings.Repeat("a", 64), "flac"); !errors.Is(err, serve.ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
	r.runOne()
	done := r.get(q.ID)
	obj, err := r.svc.Resolve(ctx, *done.SHA256, "flac")
	if err != nil || obj.ContentType != "audio/flac" || obj.ETag != *done.SHA256 || obj.DownloadName != done.Download.Filename ||
		obj.RelPath != r.scalar(`SELECT path FROM tapes WHERE id=?`, q.ID) {
		t.Fatalf("Resolve = %+v, %v", obj, err)
	}
	if _, err := r.svc.Resolve(ctx, *done.SHA256, "mp3"); !errors.Is(err, serve.ErrNotFound) {
		t.Errorf("other extension: %v", err)
	}
	// the master of an item is not a tape
	if _, err := r.svc.Resolve(ctx, strings.Repeat("01", 32), "flac"); !errors.Is(err, serve.ErrNotFound) {
		t.Errorf("master sha: %v", err)
	}
	// a ready tape whose file vanished does not resolve and gets no link
	if err := r.data.Remove(obj.RelPath); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Resolve(ctx, *done.SHA256, "flac"); !errors.Is(err, serve.ErrNotFound) {
		t.Errorf("missing file: %v", err)
	}
	if got := r.get(q.ID); got.Download != nil {
		t.Errorf("link for a missing file: %+v", got.Download)
	}
	r.exec(`UPDATE tapes SET state='expired' WHERE id=?`, q.ID)
	if _, err := r.svc.Resolve(ctx, *done.SHA256, "flac"); !errors.Is(err, serve.ErrNotFound) {
		t.Errorf("expired: %v", err)
	}
}

// TestTape_LinksSigned: the link verifies as sha.flac and its lifetime follows the principal.
func TestTape_LinksSigned(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	q := r.mustExport(tape.ExportInput{})
	r.runOne()
	signer := serve.NewSigner([]byte("synthetic-key"), r.clk.now)
	for _, tc := range []struct {
		p   auth.Principal
		ttl time.Duration
	}{{human, 12 * time.Hour}, {agent, 15 * time.Minute}} {
		got, err := r.svc.Get(auth.WithPrincipal(context.Background(), tc.p), q.ID)
		if err != nil || got.Download == nil {
			t.Fatal(got.Download, err)
		}
		u := got.Download.URL
		_, query, _ := strings.Cut(u, "?")
		var exp, sig string
		for _, kv := range strings.Split(query, "&") {
			k, v, _ := strings.Cut(kv, "=")
			switch k {
			case "exp", "e":
				exp = v
			case "sig", "s":
				sig = v
			}
		}
		if !strings.Contains(u, *got.SHA256+".flac") || exp == "" || sig == "" {
			t.Fatalf("url %s", u)
		}
		if err := signer.Verify(*got.SHA256, "flac", exp, sig); err != nil {
			t.Errorf("verify: %v", err)
		}
		if at, _ := time.Parse(time.RFC3339, got.Download.ExpiresAt); at.Sub(r.clk.now()) > tc.ttl+time.Hour || at.Before(r.clk.now()) {
			t.Errorf("%v expires %s", tc.p.Kind, got.Download.ExpiresAt)
		}
	}
	if _, err := r.svc.Get(context.Background(), "nope"); err == nil {
		t.Error("unknown tape")
	} else {
		wantProblem(t, err, 404, "NOT_FOUND")
	}
	q2 := r.mustExport(tape.ExportInput{TargetLUFS: f64(-20)})
	if got := r.get(q2.ID); got.Download != nil {
		t.Error("a queued tape has no link")
	}
}

// TestTape_NoMuseByDefault: the service adds no Muse text anywhere and passes the flag the snapshot honoured on; nothing
// Muse-shaped is stored on a row even when the snapshot carries it (it can only reach the file, through the tags).
func TestTape_NoMuseByDefault(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	s := snapshot(rec, 3, 3)
	r.src.Set(s)
	q := r.mustExport(tape.ExportInput{})
	r.runOne()
	for _, col := range []string{"params_json", "plan_json", "coalesce(error_message,'')"} {
		if v := r.scalar(`SELECT `+col+` FROM tapes WHERE id=?`, q.ID); strings.Contains(v, museCanary) || strings.Contains(v, "Synthetic Take Title") {
			t.Errorf("%s: %s", col, v)
		}
	}
	if !strings.Contains(r.scalar(`SELECT params_json FROM tapes WHERE id=?`, q.ID), `"muse_names":false`) {
		t.Error("muse_names not recorded")
	}
	s2 := snapshot(rec, 3, 3)
	s2.MuseNames = true
	s2.Tags = append(s2.Tags, flacmeta.Comment{Key: "LINER_NOTES", Value: museCanary})
	r.src.Set(s2)
	q2 := r.mustExport(tape.ExportInput{})
	if q2.ID == q.ID {
		t.Error("flipping the flag must be a new key")
	}
	for _, col := range []string{"params_json", "plan_json"} {
		if v := r.scalar(`SELECT `+col+` FROM tapes WHERE id=?`, q2.ID); strings.Contains(v, museCanary) {
			t.Errorf("%s holds the Muse text: %s", col, v)
		}
	}
}

// TestTape_LossyLabel_Table: the chapter name of a lossy source, whitespace collapsed, and the plan flags and statuses.
func TestTape_LossyLabel_Table(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	s := snapshot(rec, 3, 4)
	s.Items[0].Lossy = true
	s.Items[1].Title = "  Spaced \t Out  "
	s.Items[2].Channels, s.Items[2].SampleRate = 1, 44100
	s.Items[3].Measure = tape.Measure{}
	r.src.Set(s)
	q := r.mustExport(tape.ExportInput{})
	if !q.ContainsLossy || q.LossySources != 1 {
		t.Errorf("lossy %v %d", q.ContainsLossy, q.LossySources)
	}
	r.runOne()
	job := r.pipe.Jobs()[0]
	want := []string{titleOne + " (lossy source)", "Spaced Out", "Synthetic Take Title 3", "Synthetic Take Title 4"}
	for i, w := range want {
		if job.Chapters[i] != w {
			t.Errorf("chapter %d = %q, want %q", i, job.Chapters[i], w)
		}
	}
	it := r.get(q.ID).Items
	if !it[0].Lossy || !it[2].Upmixed || !it[2].Resampled || it[3].Status != tape.GainUnmeasured || it[1].Status != tape.GainMatched {
		t.Errorf("items %+v", it)
	}
	if !job.Items[2].Upmixed || !job.Items[2].Resampled || job.Items[0].Resampled {
		t.Errorf("job items %+v", job.Items)
	}
	if got := r.get(q.ID); got.SpreadLU == nil || got.SpreadLU.Before != 0 || got.SpreadLU.After != 0 {
		t.Errorf("spread %+v", got.SpreadLU)
	}
}

// TestTape_Loudness: missing measures are asked for; a failure only leaves the item unmeasured.
func TestTape_Loudness(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	s := snapshot(rec, 3, 2)
	s.Items[1].Measure = tape.Measure{}
	r.src.Set(s)
	r.loud.Set("take-2", tape.Measure{LUFS: -20, TruePeakDBTP: -6, OK: true})
	q := r.mustExport(tape.ExportInput{})
	if it := q.Items[1]; it.Status != tape.GainMatched || it.GainCDB != 300 {
		t.Errorf("ensured item: %+v", it)
	}
	if q.SpreadLU == nil || q.SpreadLU.Before != 6 {
		t.Errorf("spread %+v", q.SpreadLU)
	}
	r2 := newRig(t, func(o *tape.Options) { o.Loudness = failingLoudness{} })
	r2.src.Set(s)
	q2 := r2.mustExport(tape.ExportInput{})
	if q2.Items[1].Status != tape.GainUnmeasured || q2.Items[1].GainCDB != 0 {
		t.Errorf("failed analysis: %+v", q2.Items[1])
	}
}

type failingLoudness struct{}

func (failingLoudness) Ensure(context.Context, []string) (map[string]tape.Measure, error) {
	return nil, errors.New("analysis exploded")
}

// TestTape_Brief: newest first, the expiry of a ready tape, the limit.
func TestTape_Brief(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.mustExport(tape.ExportInput{})
	r.runOne()
	r.clk.add(time.Minute)
	b := r.mustExport(tape.ExportInput{TargetLUFS: f64(-20)})
	got, err := r.svc.Brief(context.Background(), rec, 5)
	if err != nil || len(got) != 2 || got[0].ID != b.ID || got[1].ID != a.ID {
		t.Fatalf("Brief = %+v, %v", got, err)
	}
	if got[1].ExpiresAt == nil || got[1].FinishedAt == nil || got[1].Bytes == nil || got[0].ExpiresAt != nil || got[0].State != tape.StateQueued {
		t.Errorf("fields %+v", got)
	}
	if one, _ := r.svc.Brief(context.Background(), rec, 1); len(one) != 1 {
		t.Errorf("limit: %d", len(one))
	}
	if none, err := r.svc.Brief(context.Background(), "other", 3); err != nil || len(none) != 0 {
		t.Errorf("other record: %v %v", none, err)
	}
}

// TestTape_Run_WakesOnExport: the serve loop renders a tape without polling for it, and stops with its context.
func TestTape_Run_WakesOnExport(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.svc.Run(ctx); close(done) }()
	time.Sleep(20 * time.Millisecond) // the loop is idle
	q := r.mustExport(tape.ExportInput{})
	deadline := time.Now().Add(4 * time.Second)
	for r.get(q.ID).State != tape.StateReady && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if r.get(q.ID).State != tape.StateReady {
		t.Error("the runner did not pick the tape up")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	// nil collaborators: harmless
	idle, stop := context.WithCancel(context.Background())
	fin := make(chan struct{})
	go func() { tape.New(tape.Options{}).Run(idle); close(fin) }()
	stop()
	<-fin
	if ran, err := tape.New(tape.Options{}).RunOne(context.Background()); ran || err != nil {
		t.Errorf("RunOne: %v %v", ran, err)
	}
	if err := tape.New(tape.Options{}).Recover(context.Background()); err != nil {
		t.Error(err)
	}
	if n, err := tape.New(tape.Options{}).Sweep(context.Background(), time.Hour); n != 0 || err != nil {
		t.Error(n, err)
	}
	if _, err := tape.New(tape.Options{}).Resolve(context.Background(), strings.Repeat("a", 64), "flac"); !errors.Is(err, serve.ErrNotFound) {
		t.Error(err)
	}
}

// linkPipeline renders its file inside the data root, so the runner links it instead of copying.
type linkPipeline struct {
	data    *os.Root
	dir     string
	inner   *tapetest.FakePipeline
	rewrote string
}

func (p *linkPipeline) Render(ctx context.Context, j tape.Job, pr func(tape.Progress)) (tape.Result, error) {
	res, err := p.inner.Render(ctx, j, pr)
	if err != nil {
		return res, err
	}
	b, _ := os.ReadFile(res.Path)
	res.Cleanup()
	if err := p.data.MkdirAll("incoming/tape-x", 0o700); err != nil {
		return tape.Result{}, err
	}
	if err := p.data.WriteFile("incoming/tape-x/final.flac", b, 0o600); err != nil {
		return tape.Result{}, err
	}
	res.Path = filepath.Join(p.dir, "incoming/tape-x/final.flac")
	res.Cleanup = func() { _ = p.data.RemoveAll("incoming/tape-x") }
	return res, nil
}

// TestTape_ClaimFile: a file inside the data root is linked (one inode), a file outside is copied and re-hashed, and a
// render whose bytes do not match its hash fails TAPE_VERIFY_FAILED.
func TestTape_ClaimFile(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	lp := &linkPipeline{data: r.data, dir: r.dir, inner: r.pipe}
	r2 := newRig(t, func(o *tape.Options) { o.Pipeline = lp })
	lp.data, lp.dir = r2.data, r2.dir
	q := r2.mustExport(tape.ExportInput{})
	r2.runOne()
	if got := r2.get(q.ID); got.State != tape.StateReady {
		t.Fatalf("linked run: %+v", got)
	}
	rel := r2.scalar(`SELECT path FROM tapes WHERE id=?`, q.ID)
	if info, err := r2.data.Stat(rel); err != nil || info.Mode().Perm() != 0o444 {
		t.Errorf("linked file: %v %v", info, err)
	}
	if _, err := r2.data.Stat("incoming/tape-x"); err == nil {
		t.Error("the work dir was not cleaned up")
	}
	// a hash that does not match the copied bytes
	bad := newRig(t, func(o *tape.Options) { o.Pipeline = lyingPipeline{inner: r.pipe} })
	q3 := bad.mustExport(tape.ExportInput{})
	bad.runOne()
	if got := bad.get(q3.ID); got.State != tape.StateFailed || got.Error.Code != tape.ErrVerifyFailed {
		t.Errorf("lying pipeline: %+v", got)
	}
	if entries, _ := os.ReadDir(filepath.Join(bad.dir, "derived/tapes")); countFiles(bad.dir, entries) != 0 {
		t.Error("a failed claim left a file")
	}
}

func countFiles(root string, top []os.DirEntry) int {
	n := 0
	_ = filepath.WalkDir(filepath.Join(root, "derived/tapes"), func(_ string, d os.DirEntry, _ error) error {
		if d != nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

type lyingPipeline struct{ inner *tapetest.FakePipeline }

func (p lyingPipeline) Render(ctx context.Context, j tape.Job, pr func(tape.Progress)) (tape.Result, error) {
	res, err := p.inner.Render(ctx, j, pr)
	res.SHA256 = strings.Repeat("c", 64)
	return res, err
}
