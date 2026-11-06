package jobs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/synth"
)

// The attach ingest (E2b, C166): a finished extract job attaches its stem to the source take, a text2music take keeps a valid
// lyrics.lrc and drops an invalid one with an event. Fixtures are synthetic.

// aceDeliverWith is aceDeliver with extra artifact bodies (lyrics.lrc) uploaded and reported besides the three common ones.
func (b *bridgeRig) aceDeliverWith(spec provider.TakeSpec, flac []byte, extra map[string][]byte, mutate func(*provider.Output)) provider.Output {
	b.t.Helper()
	out := b.aceDeliver(spec, flac, func(o *provider.Output) {
		for name, body := range extra {
			if _, ok := spec.Uploads[name]; !ok {
				b.t.Fatalf("the ACE-Step spec has no upload target %s", name)
			}
			if code := b.put(spec.Uploads[name].URL, body); code != 201 {
				b.t.Fatalf("upload %s: %d", name, code)
			}
			o.Artifacts[name] = provider.ArtifactDigest{SHA256: sha256hex(body), Bytes: int64(len(body))}
		}
		if mutate != nil {
			mutate(o)
		}
	})
	return out
}

// runACEWith drives one ACE-Step job through the real bridge with extra artifacts.
func (b *bridgeRig) runACEWith(job string, flac []byte, extra map[string][]byte) provider.Output {
	b.t.Helper()
	spec := b.specOf(job)
	out := b.aceDeliverWith(spec, flac, extra, nil)
	b.setStatus(b.refOf(job, 1), completed(out, 5000))
	for range 12 {
		b.step()
		if IsTerminal(b.state(job)) {
			break
		}
		b.advance(3e9)
	}
	return out
}

// stemFixture is a bridge rig with one finished 10 s text2music take, the source of the stem jobs.
type stemFixture struct {
	*bridgeRig
	parent string
}

func newStemFixture(t *testing.T) *stemFixture {
	t.Helper()
	b := newACERig(t, true, aceEngines("complete", "extract"))
	job := b.seedACE(b.aceRecipe(aceRequest(t, nil)))
	b.runACE(job, b.longFLAC(10, 80), nil)
	b.wantState(job, StateCompleted)
	return &stemFixture{bridgeRig: b, parent: b.str(`SELECT track_id FROM jobs WHERE id = ?`, job)}
}

var stemSeq atomic.Int64

// seedStem seeds an extract job of the stem called name on the fixture's take.
func (s *stemFixture) seedStem(name string) string {
	s.t.Helper()
	req := aceRequest(s.t, map[string]any{"id": fmt.Sprintf("syn-stem-%s-%d", name, stemSeq.Add(1)), "task": "extract", "tracks": []string{name}, "duration_ds": nil, "lyrics": nil,
		"src": map[string]any{"track_id": s.parent, "master_sha256": s.str(`SELECT master_sha256 FROM tracks WHERE id = ?`, s.parent), "duration_ds": 100}})
	batch := s.seed(batchSpec{recipe: s.aceRecipe(req), source: s.parent, operator: "extract_stems",
		mutation: `{"operator":"extract_stems","params":{"mode":"stems","tracks":["` + name + `"]},"principal_id":"agent:t1"}`})
	s.exec(`DROP TRIGGER IF EXISTS jobs_frozen`)
	s.exec(`DROP TRIGGER IF EXISTS batches_frozen`)
	s.exec(`UPDATE jobs SET engine = 'acestep15' WHERE id = ?`, batch.Jobs[0])
	s.exec(`UPDATE batches SET engine = 'acestep15' WHERE id = ?`, batch.Batch)
	return batch.Jobs[0]
}

// library renders the row counts of everything a stem must leave alone.
func (s *stemFixture) library() string {
	return s.str(`SELECT (SELECT COUNT(*) FROM tracks) || '|' || (SELECT COUNT(*) FROM mutations) || '|' || (SELECT COUNT(*) FROM songs) || '|' ||
		(SELECT COUNT(*) FROM song_picks) || '|' || (SELECT COUNT(*) FROM pick_events) || '|' || (SELECT COUNT(*) FROM renditions) || '|' ||
		(SELECT COUNT(*) FROM track_tags) || '|' || (SELECT COUNT(*) FROM recipe_keys)`)
}

// longFLAC renders a distinct synthetic FLAC of the given length.
func (b *bridgeRig) longFLAC(seconds float64, variant int) []byte {
	b.t.Helper()
	dst := filepath.Join(b.t.TempDir(), "long.flac")
	if err := synth.FLACUncalibrated(b.t.Context(), b.tools, dst, synth.Tone{Seconds: seconds, Variant: variant}, synth.StyleFFmpeg); err != nil {
		b.t.Fatal(err)
	}
	raw, err := os.ReadFile(dst)
	if err != nil {
		b.t.Fatal(err)
	}
	return raw
}

func TestIngest_StemArtifact(t *testing.T) {
	s := newStemFixture(t)
	before := s.library()
	job := s.seedStem("drums")
	out := s.runACE(job, s.longFLAC(10, 91), nil)
	s.wantState(job, StateCompleted)

	// the job completes against the source take, clean, with the worker's environment
	if got := s.str(`SELECT track_id || '|' || outcome || '|' || (env_id IS NOT NULL) || '|' || (audio_s > 0) FROM jobs WHERE id = ?`, job); got != s.parent+"|clean|1|1" {
		t.Errorf("job row %q", got)
	}
	if n := s.count(`SELECT COUNT(*) FROM env_fingerprints e JOIN jobs j ON j.env_id = e.env_id WHERE j.id = ?`, job); n != 1 {
		t.Error("the job's env fingerprint is missing")
	}
	// the stem is an artifact of the source take, frozen at 0444, byte-identical to the upload
	var path, sha string
	var bytes int64
	if err := s.st.R.QueryRow(`SELECT path, sha256, bytes FROM artifacts WHERE track_id = ? AND name = 'stem-drums.flac'`, s.parent).Scan(&path, &sha, &bytes); err != nil {
		t.Fatalf("no stem artifact: %v", err)
	}
	if want := out.Artifacts["audio.flac"]; sha != want.SHA256 || bytes != want.Bytes {
		t.Errorf("artifact %s/%d, upload %s/%d", sha, bytes, want.SHA256, want.Bytes)
	}
	if path != "takes/"+s.parent+"/stem-drums.flac" {
		t.Errorf("path %q", path)
	}
	st, err := os.Stat(filepath.Join(s.dataDir, path))
	if err != nil || st.Mode().Perm() != 0o444 || st.Size() != bytes {
		t.Errorf("stem file: %v %v", st, err)
	}
	// nothing else changed: no tracks row, no mutation, no Song, Pick, recipe key, rendition or tag
	if after := s.library(); after != before {
		t.Errorf("library rows changed:\nbefore %s\nafter  %s", before, after)
	}
	if n := s.count(`SELECT COUNT(*) FROM job_events WHERE job_id = ? AND event = 'attached'`, job); n != 1 {
		t.Errorf("%d attached events", n)
	}
	// the uploads were consumed and the work file is gone
	if left, _ := filepath.Glob(filepath.Join(s.dataDir, "incoming", "attach-*")); len(left) != 0 {
		t.Errorf("work files left: %v", left)
	}
	if n := s.count(`SELECT COUNT(*) FROM uploads WHERE state <> 'consumed' AND attempt_id IN (SELECT id FROM job_attempts WHERE job_id = ?)`, job); n != 0 {
		t.Errorf("%d uploads not consumed", n)
	}
}

func TestIngest_StemArtifact_AddsNoTrackRow(t *testing.T) {
	s := newStemFixture(t)
	before := s.count(`SELECT COUNT(*) FROM tracks`)
	job := s.seedStem("bass")
	s.runACE(job, s.longFLAC(10, 92), nil)
	s.wantState(job, StateCompleted)
	if n := s.count(`SELECT COUNT(*) FROM tracks`); n != before {
		t.Errorf("tracks %d -> %d", before, n)
	}
	if n := s.count(`SELECT COUNT(*) FROM tracks WHERE job_id = ?`, job); n != 0 {
		t.Error("a track points at the stem job")
	}
}

// TestIngest_StemArtifact_Numbered: a repeated stem name is stored as stem-<name>-<n>.flac (the estimate warned stem_exists).
func TestIngest_StemArtifact_Numbered(t *testing.T) {
	s := newStemFixture(t)
	for i, want := range []string{"stem-vocals.flac", "stem-vocals-2.flac", "stem-vocals-3.flac"} {
		job := s.seedStem("vocals")
		s.runACE(job, s.longFLAC(10, 93+i), nil)
		s.wantState(job, StateCompleted)
		if n := s.count(`SELECT COUNT(*) FROM artifacts WHERE track_id = ? AND name = ?`, s.parent, want); n != 1 {
			t.Errorf("round %d: %s missing; artifacts: %s", i, want, s.str(`SELECT group_concat(name) FROM artifacts WHERE track_id = ? AND name LIKE 'stem-%'`, s.parent))
		}
	}
}

// TestIngest_StemArtifact_Rejected: a stem whose length is not the source's, a stem that is not a FLAC, and a recipe that is not a
// one-stem extract of the job's source end INGEST_FAILED and leave the take untouched.
func TestIngest_StemArtifact_Rejected(t *testing.T) {
	s := newStemFixture(t)
	long := func() []byte { // 14 s against a 10 s source
		s.t.Helper()
		return s.longFLAC(14, 1)
	}
	for _, c := range []struct {
		name, code string
		flac       func() []byte
	}{
		{"length", codeStemLength, long},
		{"not a flac", "INVALID_AUDIO", func() []byte { return []byte("not a flac at all") }},
	} {
		job := s.seedStem("guitar")
		s.runACE(job, c.flac(), nil)
		s.wantState(job, StateFailed)
		s.wantCode(job, CodeIngestFailed)
		if got := s.str(`SELECT json_extract(detail_json, '$.ingest_code') FROM job_events WHERE job_id = ? AND event = 'failed' ORDER BY id DESC LIMIT 1`, job); got != c.code {
			t.Errorf("%s: ingest_code %q, want %q", c.name, got, c.code)
		}
	}
	if n := s.count(`SELECT COUNT(*) FROM artifacts WHERE track_id = ? AND name LIKE 'stem-%'`, s.parent); n != 0 {
		t.Errorf("%d stem artifacts after rejected stems", n)
	}
	if left, _ := filepath.Glob(filepath.Join(s.dataDir, "takes", s.parent, "stem-*")); len(left) != 0 {
		t.Errorf("stem files left: %v", left)
	}
}

// TestIngest_StemArtifact_RecipeMismatch: an extract job whose request names another source than the job's is refused.
func TestIngest_StemArtifact_RecipeMismatch(t *testing.T) {
	s := newStemFixture(t)
	job := s.seedStem("synth")
	s.exec(`UPDATE jobs SET source_track_id = NULL WHERE id = ?`, job) // jobs_frozen is dropped by seedStem
	s.runACE(job, s.longFLAC(10, 95), nil)
	s.wantState(job, StateFailed)
	if got := s.str(`SELECT json_extract(detail_json, '$.ingest_code') FROM job_events WHERE job_id = ? AND event = 'failed' ORDER BY id DESC LIMIT 1`, job); got != codeStemRequest {
		t.Errorf("ingest_code %q", got)
	}
}

// TestStemJob_CommittedAttachCompletes: the attach transaction committed and the process stopped before the job did; the next
// ingest pass completes the job from the attached event instead of reading uploads that were consumed.
func TestStemJob_CommittedAttachCompletes(t *testing.T) {
	s := newStemFixture(t)
	job := s.seedStem("keyboard")
	jr, ar, files, out := s.captureACE(job, s.longFLAC(10, 96))
	owner := "runner-1"
	if _, err := s.bridge.IngestTake(WithLeaseOwner(t.Context(), owner), jr, ar, files, out); err != nil {
		t.Fatal(err)
	}
	done, err := s.run.completeCommitted(t.Context(), job)
	if err != nil || !done {
		t.Fatalf("completeCommitted = %v, %v", done, err)
	}
	s.wantState(job, StateCompleted)
	if got := s.str(`SELECT track_id || '|' || outcome FROM jobs WHERE id = ?`, job); got != s.parent+"|clean" {
		t.Errorf("job row %q", got)
	}
}

// TestStemJob_ProvenanceUnchanged: a stem job marks its batch dirty (jobs.track_id is set, as for any completed job) but no track
// and no Song: the provenance queue names no row of the library that did not exist before.
func TestStemJob_ProvenanceUnchanged(t *testing.T) {
	s := newStemFixture(t)
	job := s.seedStem("woodwinds")
	before := s.count(`SELECT COUNT(*) FROM provenance_dirty WHERE kind <> 'batch'`)
	s.runACE(job, s.longFLAC(10, 97), nil)
	s.wantState(job, StateCompleted)
	if n := s.count(`SELECT COUNT(*) FROM provenance_dirty WHERE kind <> 'batch'`); n != before {
		t.Errorf("non-batch dirty rows %d -> %d", before, n)
	}
}

func TestLRC_Reason(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, body, want string }{
		{"good", "[00:00.00] la\n[00:04.50] la la\n", ""},
		{"crlf and blank lines", "[00:00.00] a\r\n\r\n[01:02.345] b\r\n", ""},
		{"three-digit minutes", "[100:00.00] x\n", ""},
		{"empty", "", "empty"},
		{"blank only", "\n \n", "empty"},
		{"no time stamp", "[00:00.00] ok\nplain line\n", "bad_line"},
		{"one-digit seconds fraction", "[00:00.0] x\n", "bad_line"},
		{"not utf-8", "[00:00.00] \xff\xfe\n", "not_utf8"},
		{"nul", "[00:00.00] a\x00b\n", "not_utf8"},
		{"too large", "[00:00.00] " + strings.Repeat("a", lrcMaxBytes) + "\n", "too_large"},
	} {
		if got := lrcReason([]byte(c.body)); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestIngest_LRC_Stored(t *testing.T) {
	b := newACERig(t, true, aceEngines())
	job := b.seedACE(b.aceRecipe(aceRequest(t, map[string]any{"lrc": true})))
	body := []byte("[00:00.00] la la la\n[00:04.00] la\n")
	b.runACEWith(job, b.flac(81), map[string][]byte{"lyrics.lrc": body})
	b.wantState(job, StateCompleted)
	track := b.str(`SELECT track_id FROM jobs WHERE id = ?`, job)
	path := b.str(`SELECT path FROM artifacts WHERE track_id = ? AND name = 'lyrics.lrc'`, track)
	if path == "" {
		t.Fatal("lyrics.lrc was not stored")
	}
	if got, err := os.ReadFile(filepath.Join(b.dataDir, path)); err != nil || string(got) != string(body) {
		t.Errorf("stored lrc %q (%v)", got, err)
	}
	if n := b.count(`SELECT COUNT(*) FROM job_events WHERE job_id = ? AND event = 'lrc_dropped'`, job); n != 0 {
		t.Error("a valid lrc was dropped")
	}
}

func TestIngest_LRC_InvalidDropped(t *testing.T) {
	b := newACERig(t, true, aceEngines())
	job := b.seedACE(b.aceRecipe(aceRequest(t, map[string]any{"lrc": true})))
	b.runACEWith(job, b.flac(82), map[string][]byte{"lyrics.lrc": []byte("this is not an lrc\n")})
	b.wantState(job, StateCompleted) // never fails the take
	track := b.str(`SELECT track_id FROM jobs WHERE id = ?`, job)
	if n := b.count(`SELECT COUNT(*) FROM artifacts WHERE track_id = ? AND name = 'lyrics.lrc'`, track); n != 0 {
		t.Error("an invalid lrc was kept")
	}
	if n := b.count(`SELECT COUNT(*) FROM artifacts WHERE track_id = ?`, track); n < 3 {
		t.Errorf("the take's other artifacts are missing (%d)", n)
	}
	if got := b.str(`SELECT json_extract(detail_json, '$.reason') FROM job_events WHERE job_id = ? AND event = 'lrc_dropped'`, job); got != "bad_line" {
		t.Errorf("lrc_dropped reason %q", got)
	}
	if left, _ := filepath.Glob(filepath.Join(b.dataDir, "takes", track, "lyrics.lrc")); len(left) != 0 {
		t.Error("the invalid lrc file is on disk")
	}
}

// TestJobView_TaskAndStem: the job view names the task of an acestep15 job and the stem of an extract job (C166); yue2 jobs
// have neither.
func TestJobView_TaskAndStem(t *testing.T) {
	s := newStemFixture(t)
	job := s.seedStem("percussion")
	s.runACE(job, s.longFLAC(10, 98), nil)
	s.wantState(job, StateCompleted)
	svc := s.service(t.Context())
	j, err := svc.Job(t.Context(), auth.Principal{}, job)
	if err != nil {
		t.Fatal(err)
	}
	if j.Task == nil || *j.Task != "extract" || j.Stem == nil || *j.Stem != "percussion" {
		t.Errorf("stem job: task %v stem %v", deref(j.Task), deref(j.Stem))
	}
	parentJob := s.str(`SELECT job_id FROM tracks WHERE id = ?`, s.parent)
	p, err := svc.Job(t.Context(), auth.Principal{}, parentJob)
	if err != nil {
		t.Fatal(err)
	}
	if p.Task == nil || *p.Task != "text2music" || p.Stem != nil {
		t.Errorf("take job: task %v stem %v", deref(p.Task), deref(p.Stem))
	}
	page, err := svc.ListJobs(t.Context(), auth.Principal{}, JobQuery{TrackID: s.parent})
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, it := range page.Items {
		if it.ID == job {
			seen = it.Stem != nil && *it.Stem == "percussion"
		}
	}
	if !seen {
		t.Errorf("the stem job is not listed with its stem under its source take: %d items", len(page.Items))
	}

}

// TestJobView_TaskAndStem_Yue2: a yue2 job names neither.
func TestJobView_TaskAndStem_Yue2(t *testing.T) {
	y := newBridgeRig(t, true)
	yj := y.seed(batchSpec{recipe: y.newRecipe("pop")}).Jobs[0]
	y.run1(yj, y.flac(1), [2]bool{}, nil)
	yv, err := y.service(t.Context()).Job(t.Context(), auth.Principal{}, yj)
	if err != nil {
		t.Fatal(err)
	}
	if yv.Task != nil || yv.Stem != nil {
		t.Errorf("a yue2 job names task %v stem %v", deref(yv.Task), deref(yv.Stem))
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
