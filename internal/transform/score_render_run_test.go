package transform_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/score/synth"
	"github.com/nicolasacchi/metamusic/internal/transform"
	"github.com/nicolasacchi/metamusic/internal/transform/transformtest"
)

const runTune = "X:1\nT:Synthetic run\nM:4/4\nL:1/4\nQ:1/4=120\nK:C\nCDEF|GABc|\n"

// fakeSynth stands in for fluidsynth: it records the request and writes a placeholder out.wav (FakeMedia decodes by name).
type fakeSynth struct {
	mu    sync.Mutex
	calls []synth.Request
	err   error
	info  synth.Info
}

func (f *fakeSynth) Render(_ context.Context, r synth.Request) (synth.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r)
	if f.err != nil {
		return synth.Info{}, f.err
	}
	if err := os.WriteFile(r.Out, []byte("RIFF-fake"), 0o600); err != nil {
		return synth.Info{}, err
	}
	return f.info, nil
}

func (f *fakeSynth) last() synth.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

func (f *fakeSynth) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

// scoreRunRig is the rig with a fake synth, one soundfont ("orchestra") and every tool present.
func scoreRunRig(t *testing.T) (*rig, *fakeSynth) {
	t.Helper()
	fs := &fakeSynth{info: synth.Info{Name: "fluidsynth", Version: "FluidSynth runtime version 2.3.4"}}
	sfDir := t.TempDir()
	sf := filepath.Join(sfDir, "orchestra.sf2")
	if err := os.WriteFile(sf, []byte("synthetic soundfont bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := newRig(t, false, func(o *transform.Options) {
		o.ToolStatus = func() transform.ToolStatus {
			return transform.ToolStatus{FFmpeg: true, FLAC: true, Fluidsynth: true, FluidsynthVersion: fs.info.Version}
		}
		o.Score = &transform.ScoreSupport{Synth: fs, Info: fs.info, Fonts: synth.NewSet([]synth.Font{{Name: "orchestra", Path: sf}}, nil)}
	})
	r.media.Sources["out.wav"] = transformtest.FakeSrc{Frames: 48000 * 6, Channels: 2}
	return r, fs
}

func scoreRequest(abc, extra string) transform.Request {
	b, _ := json.Marshal(abc)
	return transform.Request{Kind: transform.KindScoreRender, Members: map[transform.Kind]json.RawMessage{
		transform.KindScoreRender: json.RawMessage(fmt.Sprintf(`{"score":{"abc":%s}%s}`, b, extra))}}
}

func (r *rig) createScore(abc, extra string) string {
	r.t.Helper()
	res, err := r.svc.Create(context.Background(), human, scoreRequest(abc, extra), nil)
	if err != nil {
		r.t.Fatalf("create: %v", err)
	}
	return res.View.ID
}

// TestScoreRender_RunnerTake: a score render runs through the runner to an ordinary rootless Take with the three artifacts and no
// scores row, and the synth was asked for exactly the spec (soundfont path from the set, rate, gain, an SMF in the work dir).
func TestScoreRender_RunnerTake(t *testing.T) {
	r, fs := scoreRunRig(t)
	id := r.createScore(runTune, `,"gain":0.7`)
	if !r.runOne() {
		t.Fatal("nothing was claimed")
	}
	if row := r.row(id); row.State != "done" || row.PCM == "" {
		t.Fatalf("row %+v", row)
	}
	req := fs.last()
	if req.Rate != 48000 || req.Gain != 0.7 || !strings.HasSuffix(req.Soundfont, "orchestra.sf2") || !filepath.IsAbs(req.SMF) || !filepath.IsAbs(req.Out) {
		t.Errorf("synth request %+v", req)
	}
	var parent, root string
	var gen int
	if err := r.st.R.QueryRow(`SELECT COALESCE(parent_track_id,''), root_track_id, generation FROM tracks WHERE id = ?`, id).Scan(&parent, &root, &gen); err != nil {
		t.Fatal(err)
	}
	if parent != "" || root != id || gen != 0 {
		t.Errorf("a score render is a rootless Take: parent %q root %q generation %d", parent, root, gen)
	}
	if n := r.count(`SELECT count(*) FROM scores WHERE track_id = ?`, id); n != 0 {
		t.Errorf("a score render wrote %d scores rows", n)
	}
	if n := r.count(`SELECT count(*) FROM mutations WHERE child_track_id = ? AND operator = 'score_render' AND parent_track_id IS NULL`, id); n != 1 {
		t.Errorf("ledger rows = %d", n)
	}
	got, err := r.data.ReadFile("takes/" + id + "/score.abc")
	if err != nil || string(got) != runTune {
		t.Errorf("score.abc = %q, %v", got, err)
	}
	if mid, err := r.data.ReadFile("takes/" + id + "/score.mid"); err != nil || len(mid) < 14 || string(mid[:4]) != "MThd" {
		t.Errorf("score.mid = %q, %v", mid, err)
	}
	tj, err := r.data.ReadFile("takes/" + id + "/transform.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Synth struct{ Name, Version, SoundfontSHA string } `json:"-"`
		Raw   map[string]json.RawMessage                   `json:"-"`
	}
	_ = json.Unmarshal(tj, &doc.Raw)
	var sy map[string]string
	_ = json.Unmarshal(doc.Raw["synth"], &sy)
	if sy["name"] != "fluidsynth" || sy["version"] != "FluidSynth runtime version 2.3.4" || len(sy["soundfont_sha256"]) != 64 {
		t.Errorf("transform.json synth = %v", sy)
	}
	var voices []map[string]any
	if err := json.Unmarshal(doc.Raw["voices"], &voices); err != nil || len(voices) != 1 || voices[0]["notes"] != 8.0 {
		t.Errorf("transform.json voices = %s (%v)", doc.Raw["voices"], err)
	}
	if r.exists("incoming/transform-" + id) {
		t.Error("the work dir survived")
	}
	// the same spec again is the same Take
	res, err := r.svc.Create(context.Background(), human, scoreRequest(runTune, `,"gain":0.7`), nil)
	if err != nil || !res.View.Reused || res.View.ID != id {
		t.Errorf("second create: %+v %v", res, err)
	}
}

// TestScoreRender_SynthFailureFailsTheRow: a failing synth fails the row with SYNTH_FAILED (no path in the text) and leaves no take.
func TestScoreRender_SynthFailureFailsTheRow(t *testing.T) {
	r, fs := scoreRunRig(t)
	fs.err = &synth.RenderError{ExitCode: 1}
	id := r.createScore(runTune, "")
	r.runOne()
	row := r.row(id)
	if row.State != "failed" || row.ErrCode != transform.ErrSynthFailed || strings.Contains(row.ErrText, "/") {
		t.Errorf("row %+v", row)
	}
	if n := r.count(`SELECT count(*) FROM tracks WHERE id = ?`, id); n != 0 {
		t.Error("a failed render left a take")
	}
	if r.exists("incoming/transform-" + id) {
		t.Error("the work dir survived")
	}
}

// TestScoreRender_TimeoutMapsToTimeout: the synth's own time limit is TRANSFORM_TIMEOUT, not a synth failure.
func TestScoreRender_TimeoutMapsToTimeout(t *testing.T) {
	r, fs := scoreRunRig(t)
	fs.err = &synth.RenderError{ExitCode: -1, Err: context.DeadlineExceeded}
	id := r.createScore(runTune, "")
	r.runOne()
	if row := r.row(id); row.State != "failed" || row.ErrCode != transform.ErrTimeout {
		t.Errorf("row %+v", row)
	}
}

// TestRenderScore_Direct: the Desk entry point renders into a work dir the caller names and reports the engine; its errors are
// *ScoreError with the stable codes.
func TestRenderScore_Direct(t *testing.T) {
	r, fs := scoreRunRig(t)
	j := transform.ScoreJob{Score: []byte(runTune), Dialect: "standard", Chords: "none", TailS: 1, Gain: 0.5, TargetLUFS: -16, Soundfont: "orchestra"}
	out, err := r.svc.RenderScore(context.Background(), j, "incoming/audition-x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.PCMSHA256 == "" || out.Frames != 48000*6 || out.Seconds != 6 || out.Synth.Version != fs.info.Version || len(out.SoundfontSHA256) != 64 || len(out.Report) == 0 {
		t.Errorf("out %+v", out)
	}
	for _, f := range []string{"score.abc", "score.mid", "out.pcm24", "report.json"} {
		if !r.exists("incoming/audition-x/" + f) {
			t.Errorf("%s missing from the work dir", f)
		}
	}
	// refusals
	for name, tc := range map[string]struct {
		mod  func(*transform.ScoreJob)
		code string
	}{
		"bad score":    {func(j *transform.ScoreJob) { j.Score = []byte("X:1\nM:abc\nK:C\nC|") }, transform.ErrScoreInvalid},
		"no notes":     {func(j *transform.ScoreJob) { j.Score = []byte("X:1\nK:C\n") }, transform.ErrScoreInvalid},
		"unknown font": {func(j *transform.ScoreJob) { j.Soundfont = "nope" }, transform.ErrSoundfontUnknown},
		"too long":     {func(j *transform.ScoreJob) { j.MaxOutS = 3 }, transform.ErrInvalidTransform},
	} {
		jj := j
		tc.mod(&jj)
		_, err := r.svc.RenderScore(context.Background(), jj, "incoming/audition-y", nil)
		var se *transform.ScoreError
		if !errors.As(err, &se) || se.Code != tc.code {
			t.Errorf("%s: %v, want code %s", name, err, tc.code)
		}
	}
	fs.err = errors.New("exec failed: /usr/bin/fluidsynth -F /srv/secret/path")
	_, err = r.svc.RenderScore(context.Background(), j, "incoming/audition-z", nil)
	var se *transform.ScoreError
	if !errors.As(err, &se) || se.Code != transform.ErrSynthFailed || strings.Contains(se.Message, "/srv") {
		t.Errorf("synth error %v", err)
	}
}

// TestExclusive_SharesRunnerSlot: the runner does not claim while an audition holds the slot, and an audition is refused while a
// transform runs.
func TestExclusive_SharesRunnerSlot(t *testing.T) {
	r, _ := scoreRunRig(t)
	id := r.createScore(runTune, "")
	held, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- r.svc.Exclusive(context.Background(), time.Second, func(context.Context) error { close(held); <-release; return nil })
	}()
	<-held
	ran := make(chan bool, 1)
	go func() { ran <- r.runOne() }()
	select {
	case <-ran:
		t.Fatal("the runner ran while an audition held the slot")
	case <-time.After(150 * time.Millisecond):
	}
	if got := r.row(id).State; got != "queued" {
		t.Errorf("state while held = %s, want queued (not claimed)", got)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !<-ran {
		t.Fatal("the runner did not run after the slot was released")
	}
	if got := r.row(id).State; got != "done" {
		t.Errorf("state = %s", got)
	}

	// the other way: a running transform keeps an audition out
	r.child.Hang = true
	r.createScore(runTune, `,"gain":0.9`)
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	go func() {
		close(started)
		_, _ = r.svc.RunOne(ctx)
	}()
	<-started
	deadline := time.Now().Add(5 * time.Second)
	var err error
	for time.Now().Before(deadline) {
		err = r.svc.Exclusive(context.Background(), 40*time.Millisecond, func(context.Context) error { return nil })
		if errors.Is(err, transform.ErrBusy) {
			break
		}
	}
	if !errors.Is(err, transform.ErrBusy) {
		t.Errorf("an audition ran beside a transform: %v", err)
	}
	cancel()
}

// TestVerify_ScoreRenderMatch: a re-render of a score render reproduces pcm_sha256.
func TestVerify_ScoreRenderMatch(t *testing.T) {
	r, fs := scoreRunRig(t)
	id := r.createScore(runTune, "")
	r.runOne()
	calls := fs.count()
	rep, err := r.svc.Verify(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Match || rep.Inconclusive || rep.Got != rep.Expected || fs.count() != calls+1 {
		t.Errorf("report %+v, synth calls %d -> %d", rep, calls, fs.count())
	}
	if r.exists("incoming/verify-" + id) {
		t.Error("the verify scratch dir survived")
	}
}

// TestVerify_ScoreRenderTakesRenderSlot: a score re-render is CPU work like a runner attempt, so Verify waits for the render slot
// (C355.3). While an audition holds it the synth is not called; a cancelled context ends the wait with the context error; once the
// slot is free the verify completes. The fake synth seam (scoreRunRig) is used, so no fluidsynth is needed: the slot is taken
// after staging and before the synth, and the synth call count shows nothing rendered while it was held.
func TestVerify_ScoreRenderTakesRenderSlot(t *testing.T) {
	r, fs := scoreRunRig(t)
	id := r.createScore(runTune, "")
	r.runOne()
	calls := fs.count()

	held, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- r.svc.Exclusive(context.Background(), time.Second, func(context.Context) error { close(held); <-release; return nil })
	}()
	<-held

	// a cancelled context ends the wait with the context error, before any synth work
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.svc.Verify(cctx, id); !errors.Is(err, context.Canceled) {
		t.Errorf("Verify with a cancelled context while the slot is held: %v, want context.Canceled", err)
	}
	if fs.count() != calls {
		t.Errorf("synth ran while the slot was held: calls %d -> %d", calls, fs.count())
	}

	// a live context blocks until the slot is released
	type res struct {
		rep transform.VerifyReport
		err error
	}
	out := make(chan res, 1)
	go func() {
		rep, err := r.svc.Verify(context.Background(), id)
		out <- res{rep, err}
	}()
	select {
	case v := <-out:
		t.Fatalf("Verify finished while an audition held the slot: %+v", v)
	case <-time.After(150 * time.Millisecond):
	}
	if fs.count() != calls {
		t.Errorf("synth ran while the slot was held: calls %d -> %d", calls, fs.count())
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case v := <-out:
		if v.err != nil || !v.rep.Match || fs.count() != calls+1 {
			t.Errorf("after release: %+v err %v, synth calls %d -> %d", v.rep, v.err, calls, fs.count())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Verify did not finish after the slot was released")
	}
}

// TestVerify_ScoreRenderInconclusiveOnVersion: a different fluidsynth major.minor, or a soundfont that is not configured, cannot
// confirm a render, and nothing is rendered.
func TestVerify_ScoreRenderInconclusiveOnVersion(t *testing.T) {
	r, fs := scoreRunRig(t)
	id := r.createScore(runTune, "")
	r.runOne()
	calls := fs.count()
	tjPath := "takes/" + id + "/transform.json"
	orig, err := r.data.ReadFile(tjPath)
	if err != nil {
		t.Fatal(err)
	}
	patch := func(mod func(sy map[string]any)) {
		var doc map[string]any
		if err := json.Unmarshal(orig, &doc); err != nil {
			t.Fatal(err)
		}
		mod(doc["synth"].(map[string]any))
		b, _ := json.Marshal(doc)
		_ = r.data.Remove(tjPath)
		if err := r.data.WriteFile(tjPath, b, 0o444); err != nil {
			t.Fatal(err)
		}
	}
	for name, mod := range map[string]func(map[string]any){
		"minor differs":   func(sy map[string]any) { sy["version"] = "FluidSynth runtime version 2.4.0" },
		"soundfont other": func(sy map[string]any) { sy["soundfont_sha256"] = strings.Repeat("0", 64) },
	} {
		patch(mod)
		rep, err := r.svc.Verify(context.Background(), id)
		if err != nil || !rep.Inconclusive || rep.Match || rep.Reason == "" {
			t.Errorf("%s: report %+v err %v", name, rep, err)
		}
		if strings.Contains(rep.Reason, "/") {
			t.Errorf("%s: reason names a path: %s", name, rep.Reason)
		}
	}
	// a patch version change is the same synth
	patch(func(sy map[string]any) { sy["version"] = "FluidSynth runtime version 2.3.9" })
	if rep, err := r.svc.Verify(context.Background(), id); err != nil || !rep.Match {
		t.Errorf("patch version: %+v %v", rep, err)
	}
	if fs.count() != calls+1 {
		t.Errorf("synth calls %d, want %d (only the matching one renders)", fs.count(), calls+1)
	}
}
