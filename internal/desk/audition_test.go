package desk_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/desk"
	"github.com/nicolasacchi/metamusic/internal/desk/desktest"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/score/synth"
	"github.com/nicolasacchi/metamusic/internal/transform"
)

type auditionEnv struct {
	*env
	r   *desktest.Renderer
	svc *desk.Service
	enc atomic.Int64
}

func newAuditionEnv(t *testing.T, mod ...func(*desk.Options)) *auditionEnv {
	t.Helper()
	e := newEnv(t)
	r := desktest.New(t, e.dir)
	a := &auditionEnv{env: e, r: r}
	o := desk.Options{Store: e.st, Data: e.root, DataDir: e.dir, Transform: r,
		Now:   func() time.Time { return time.UnixMilli(e.now.Load()).UTC() },
		NewID: func() string { return "01900000-0000-7000-a000-" + pad12(e.ids.Add(1)) },
		Encode: func(ctx context.Context, pcm, out string) error {
			a.enc.Add(1)
			return desktest.Encode(ctx, pcm, out)
		}}
	for _, m := range mod {
		m(&o)
	}
	a.svc = desk.New(o)
	return a
}

func (a *auditionEnv) planID() string {
	a.t.Helper()
	pl, _, err := a.svc.FromTake(context.Background(), alice, a.takeWithScore())
	if err != nil {
		a.t.Fatal(err)
	}
	return pl.ID
}

// TestAudition_CacheHitIsFree: the second request of the same plan and settings renders nothing, makes no row and answers
// created=false; another soundfont or chord mode is another audition.
func TestAudition_CacheHitIsFree(t *testing.T) {
	t.Parallel()
	a := newAuditionEnv(t)
	id := a.planID()
	ctx := context.Background()
	first, created, err := a.svc.Audition(ctx, alice, id, desk.AuditionIn{})
	if err != nil || !created {
		t.Fatalf("first: created %v err %v", created, err)
	}
	if first.Soundfont != "gm" || first.Chords != "block" || first.SynthVersion != "2.5.4" || first.Seconds != 12.5 || len(first.SoundfontSHA12) != 12 {
		t.Errorf("first = %+v", first)
	}
	again, created, err := a.svc.Audition(ctx, bob, id, desk.AuditionIn{Soundfont: "gm", Chords: "block"})
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("again: %+v created %v err %v", again, created, err)
	}
	if a.r.Renders.Load() != 1 || a.enc.Load() != 1 {
		t.Errorf("renders %d encodes %d, want 1 and 1", a.r.Renders.Load(), a.enc.Load())
	}
	if _, created, err = a.svc.Audition(ctx, alice, id, desk.AuditionIn{Chords: "none"}); err != nil || !created {
		t.Fatalf("chords none: created %v err %v", created, err)
	}
	if _, created, err = a.svc.Audition(ctx, alice, id, desk.AuditionIn{Soundfont: "alt"}); err != nil || !created {
		t.Fatalf("alt soundfont: created %v err %v", created, err)
	}
	tail := 1.0
	if _, created, err = a.svc.Audition(ctx, alice, id, desk.AuditionIn{TailS: &tail}); err != nil || !created {
		t.Fatalf("tail: created %v err %v", created, err)
	}
	if n := a.scalar(`SELECT COUNT(*) FROM desk_auditions`); n != "4" {
		t.Errorf("%s rows, want 4", n)
	}
	// the file is content-addressed under derived/auditions, read-only, and the work directory is gone
	rel := a.scalar(`SELECT mp3_path FROM desk_auditions WHERE id = ?`, first.ID)
	st, err := a.root.Stat(rel)
	if err != nil || st.Mode().Perm() != 0o444 || !strings.HasPrefix(rel, "derived/auditions/") {
		t.Errorf("file %s: %v %v", rel, st, err)
	}
	ents, _ := os.ReadDir(filepath.Join(a.dir, "derived", "auditions"))
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "work-") {
			t.Errorf("work directory %s left behind", e.Name())
		}
	}
	got, err := a.svc.GetAudition(ctx, alice, id, first.ID)
	if err != nil || got.ID != first.ID {
		t.Fatalf("GetAudition: %v %+v", err, got)
	}
	_, err = a.svc.GetAudition(ctx, alice, id, "nope")
	wantProblem(t, err, 404, "NOT_FOUND")
	other := a.scalar(`SELECT id FROM desk_plans LIMIT 1`)
	_, err = a.svc.GetAudition(ctx, alice, other+"x", first.ID)
	wantProblem(t, err, 404, "NOT_FOUND")
}

// TestAudition_Rebuild: a row whose file was lost is rebuilt in place on the next request (the verify warning D4's promise).
func TestAudition_Rebuild(t *testing.T) {
	t.Parallel()
	a := newAuditionEnv(t)
	id := a.planID()
	ctx := context.Background()
	first, _, err := a.svc.Audition(ctx, alice, id, desk.AuditionIn{})
	if err != nil {
		t.Fatal(err)
	}
	rel := a.scalar(`SELECT mp3_path FROM desk_auditions`)
	if err := a.root.Remove(rel); err != nil {
		t.Fatal(err)
	}
	again, created, err := a.svc.Audition(ctx, alice, id, desk.AuditionIn{})
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("rebuild: %+v created %v err %v", again, created, err)
	}
	if _, err := a.root.Stat(rel); err != nil {
		t.Errorf("file not rebuilt: %v", err)
	}
	if n := a.scalar(`SELECT COUNT(*) FROM desk_auditions`); n != "1" {
		t.Errorf("%s rows after rebuild", n)
	}
}

// TestAudition_Limit: the 201st new audition of one person in a local day is refused with the reset time; cache hits and other
// principals are not counted; the next day starts again.
func TestAudition_Limit(t *testing.T) {
	t.Parallel()
	a := newAuditionEnv(t, func(o *desk.Options) { o.AuditionsPerDay = 2 })
	id := a.planID()
	ctx := context.Background()
	g1, g2 := 0.5, 0.7
	for _, g := range []*float64{&g1, &g2} {
		if _, _, err := a.svc.Audition(ctx, alice, id, desk.AuditionIn{Gain: g}); err != nil {
			t.Fatal(err)
		}
	}
	if _, created, err := a.svc.Audition(ctx, alice, id, desk.AuditionIn{Gain: &g1}); err != nil || created {
		t.Fatalf("a hit is free: created %v err %v", created, err)
	}
	g3 := 0.9
	_, _, err := a.svc.Audition(ctx, alice, id, desk.AuditionIn{Gain: &g3})
	wantProblem(t, err, 429, "AUDITION_LIMIT")
	if _, _, err := a.svc.Audition(ctx, bob, id, desk.AuditionIn{Gain: &g3}); err != nil {
		t.Errorf("bob has his own allowance: %v", err)
	}
	a.advance(24 * time.Hour)
	g4 := 1.1 // bob made g3, so it is a hit now; a new setting is the real check
	if _, created, err := a.svc.Audition(ctx, alice, id, desk.AuditionIn{Gain: &g4}); err != nil || !created {
		t.Errorf("next day: created %v err %v", created, err)
	}
}

// TestAudition_Busy: a slot that stays taken answers 409 AUDITION_BUSY with retry_after_s and stores nothing.
func TestAudition_Busy(t *testing.T) {
	t.Parallel()
	a := newAuditionEnv(t)
	id := a.planID()
	a.r.Busy = true
	_, _, err := a.svc.Audition(context.Background(), alice, id, desk.AuditionIn{})
	pe := wantProblem(t, err, 409, "AUDITION_BUSY")
	if pe.Details["retry_after_s"] == nil {
		t.Errorf("details = %v", pe.Details)
	}
	if n := a.scalar(`SELECT COUNT(*) FROM desk_auditions`); n != "0" || a.r.Renders.Load() != 0 {
		t.Errorf("rows %s renders %d", n, a.r.Renders.Load())
	}
}

// TestAudition_TooLong: a score over the output limit is refused before any render, and a render that reports more is refused too.
func TestAudition_TooLong(t *testing.T) {
	t.Parallel()
	a := newAuditionEnv(t)
	id := a.planID()
	ctx := context.Background()
	// 3 s tail + the fixture's 12 s fits; a renderer that reports 700 s does not
	a.r.Secs = 700
	_, _, err := a.svc.Audition(ctx, alice, id, desk.AuditionIn{})
	wantProblem(t, err, 422, "AUDITION_TOO_LONG")
	a.r.Secs = 0
	a.r.Err = &transform.ScoreError{Code: transform.ErrInvalidTransform, Message: "the render would be 900 s"}
	_, _, err = a.svc.Audition(ctx, alice, id, desk.AuditionIn{})
	wantProblem(t, err, 422, "AUDITION_TOO_LONG")
	if n := a.scalar(`SELECT COUNT(*) FROM desk_auditions`); n != "0" {
		t.Errorf("%s rows", n)
	}
	// the pre-render check: a long score never reaches the renderer
	a.r.Secs, a.r.Err = 0, nil
	renders := a.r.Renders.Load()
	long := "X:1\nT:t\nM:4/4\nL:1/4\nQ:1/4=20\nK:C\n" + strings.Repeat("CDEF|", 60) + "]\n"
	a.writeScoreOver(a.scalar(`SELECT score_path FROM desk_plans WHERE id = ?`, id), long)
	_, _, err = a.svc.Audition(ctx, alice, id, desk.AuditionIn{})
	pe := wantProblem(t, err, 422, "AUDITION_TOO_LONG")
	if pe.Details["max"] == nil || pe.Details["seconds"] == nil || a.r.Renders.Load() != renders {
		t.Errorf("details %v renders %d -> %d", pe.Details, renders, a.r.Renders.Load())
	}
}

// TestAudition_CancelStoresNothing: a request cancelled while the renderer works stores no row and no file, and removes its work
// directory.
func TestAudition_CancelStoresNothing(t *testing.T) {
	t.Parallel()
	a := newAuditionEnv(t)
	id := a.planID()
	a.r.Block = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, _, err := a.svc.Audition(ctx, alice, id, desk.AuditionIn{}); done <- err }()
	for a.r.Renders.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("cancelled audition succeeded")
	}
	if n := a.scalar(`SELECT COUNT(*) FROM desk_auditions`); n != "0" {
		t.Errorf("%s rows", n)
	}
	var files []string
	_ = filepath.WalkDir(filepath.Join(a.dir, "derived"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, p)
		}
		return nil
	})
	if len(files) != 0 {
		t.Errorf("files left: %v", files)
	}
	if ents, _ := os.ReadDir(filepath.Join(a.dir, "derived", "auditions")); len(ents) != 0 {
		t.Errorf("work directory left: %v", ents)
	}
}

// TestAudition_Refusals: unavailable pipeline parts, bad settings, an unknown plan, renderer failures.
func TestAudition_Refusals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bad := 99.0
	t.Run("no transform", func(t *testing.T) {
		a := newAuditionEnv(t, func(o *desk.Options) { o.Transform = nil })
		_, _, err := a.svc.Audition(ctx, alice, a.planID(), desk.AuditionIn{})
		pe := wantProblem(t, err, 503, "UNAVAILABLE")
		if pe.Details["component"] != "transform" {
			t.Errorf("details %v", pe.Details)
		}
	})
	t.Run("no soundfont", func(t *testing.T) {
		a := newAuditionEnv(t)
		a.r.Support.Fonts = synth.NewSet(nil, nil)
		_, _, err := a.svc.Audition(ctx, alice, a.planID(), desk.AuditionIn{})
		if pe := wantProblem(t, err, 503, "UNAVAILABLE"); pe.Details["component"] != "soundfont" {
			t.Errorf("details %v", pe.Details)
		}
	})
	t.Run("no fluidsynth", func(t *testing.T) {
		a := newAuditionEnv(t)
		a.r.Support.Synth = nil
		_, _, err := a.svc.Audition(ctx, alice, a.planID(), desk.AuditionIn{})
		if pe := wantProblem(t, err, 503, "UNAVAILABLE"); pe.Details["component"] != "fluidsynth" {
			t.Errorf("details %v", pe.Details)
		}
	})
	t.Run("unwired", func(t *testing.T) {
		_, _, err := (*desk.Service)(nil).Audition(ctx, alice, "x", desk.AuditionIn{})
		wantProblem(t, err, 503, "UNAVAILABLE")
	})
	t.Run("settings", func(t *testing.T) {
		a := newAuditionEnv(t)
		id := a.planID()
		for _, in := range []desk.AuditionIn{{Soundfont: "nope"}, {Chords: "wild"}, {TailS: &bad}, {Gain: &bad}} {
			if _, _, err := a.svc.Audition(ctx, alice, id, in); err == nil {
				t.Errorf("%+v accepted", in)
			}
		}
		_, _, err := a.svc.Audition(ctx, alice, id, desk.AuditionIn{Soundfont: "nope"})
		wantProblem(t, err, 422, "SOUNDFONT_UNKNOWN")
		_, _, err = a.svc.Audition(ctx, alice, "no-plan", desk.AuditionIn{})
		wantProblem(t, err, 404, "NOT_FOUND")
	})
	t.Run("renderer failures", func(t *testing.T) {
		a := newAuditionEnv(t)
		id := a.planID()
		a.r.Err = &transform.ScoreError{Code: transform.ErrSynthFailed, Message: "the synthesizer failed"}
		_, _, err := a.svc.Audition(ctx, alice, id, desk.AuditionIn{})
		wantProblem(t, err, 500, "AUDITION_FAILED")
		a.r.Err = &transform.ScoreError{Code: transform.ErrScoreInvalid, Message: "x"}
		_, _, err = a.svc.Audition(ctx, alice, id, desk.AuditionIn{})
		wantProblem(t, err, 422, "SCORE_INVALID")
		a.r.Err = &transform.ScoreError{Code: transform.ErrSoundfontUnknown, Message: "x"}
		_, _, err = a.svc.Audition(ctx, alice, id, desk.AuditionIn{})
		wantProblem(t, err, 422, "SOUNDFONT_UNKNOWN")
		a.r.Err = errors.New("boom")
		if _, _, err = a.svc.Audition(ctx, alice, id, desk.AuditionIn{}); err == nil {
			t.Error("plain error swallowed")
		}
	})
	t.Run("encode failure", func(t *testing.T) {
		a := newAuditionEnv(t, func(o *desk.Options) {
			o.Encode = func(context.Context, string, string) error { return errors.New("ffmpeg died") }
		})
		_, _, err := a.svc.Audition(ctx, alice, a.planID(), desk.AuditionIn{})
		wantProblem(t, err, 500, "AUDITION_FAILED")
	})
	t.Run("unparsable score", func(t *testing.T) {
		a := newAuditionEnv(t)
		id := a.planID()
		path := a.scalar(`SELECT score_path FROM desk_plans WHERE id = ?`, id)
		a.writeScoreOver(path, "X:1\nK:C\n\"unterminated chord")
		_, _, err := a.svc.Audition(ctx, alice, id, desk.AuditionIn{})
		wantProblem(t, err, 422, "SCORE_INVALID")
	})
}

func (e *env) writeScoreOver(rel, text string) {
	e.t.Helper()
	_ = e.root.Chmod(rel, 0o644)
	if err := e.root.WriteFile(rel, []byte(text), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// TestResolve_OnlyAuditionMP3s: the resolver answers the MP3 of an audition row and nothing else: not another extension, not a
// hash without a row, not a malformed key, not a row whose path is not a regular file.
func TestResolve_OnlyAuditionMP3s(t *testing.T) {
	t.Parallel()
	a := newAuditionEnv(t)
	id := a.planID()
	ctx := context.Background()
	au, _, err := a.svc.Audition(ctx, alice, id, desk.AuditionIn{})
	if err != nil {
		t.Fatal(err)
	}
	sha := a.scalar(`SELECT mp3_sha256 FROM desk_auditions WHERE id = ?`, au.ID)
	obj, err := a.svc.Resolve(ctx, sha, "mp3")
	if err != nil || obj.ContentType != "audio/mpeg" || obj.ETag != sha || !strings.HasPrefix(obj.RelPath, "derived/auditions/") {
		t.Fatalf("resolve: %+v %v", obj, err)
	}
	for _, c := range []struct{ sha, ext string }{
		{sha, "flac"}, {sha, ""}, {lt256("nothing"), "mp3"}, {"../../etc/passwd", "mp3"}, {strings.ToUpper(sha), "mp3"}, {sha[:10], "mp3"},
	} {
		if _, err := a.svc.Resolve(ctx, c.sha, c.ext); !errors.Is(err, serve.ErrNotFound) {
			t.Errorf("Resolve(%q, %q) = %v, want not found", c.sha, c.ext, err)
		}
	}
	// a master or any other derived file with a hash that is no audition is unreachable even when the file exists
	a.writeScore("derived/auditions/ff/"+lt256("stray")+".mp3", "stray")
	if _, err := a.svc.Resolve(ctx, lt256("stray"), "mp3"); !errors.Is(err, serve.ErrNotFound) {
		t.Errorf("stray file: %v", err)
	}
	// the row of a lost file is not found; a directory in its place is not served
	rel := a.scalar(`SELECT mp3_path FROM desk_auditions WHERE id = ?`, au.ID)
	_ = a.root.Remove(rel)
	if _, err := a.svc.Resolve(ctx, sha, "mp3"); !errors.Is(err, serve.ErrNotFound) {
		t.Errorf("lost file: %v", err)
	}
	if err := a.root.Mkdir(rel, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := a.svc.Resolve(ctx, sha, "mp3"); !errors.Is(err, serve.ErrNotFound) {
		t.Errorf("directory: %v", err)
	}
	if _, err := (*desk.Service)(nil).Resolve(ctx, sha, "mp3"); !errors.Is(err, serve.ErrNotFound) {
		t.Errorf("nil service: %v", err)
	}
}

// TestAudition_LinkSignedForCaller: the audition's url names its own MP3 and is signed for the principal who asked.
func TestAudition_LinkSignedForCaller(t *testing.T) {
	t.Parallel()
	signer := serve.NewSigner([]byte("0123456789abcdef0123456789abcdef"), func() time.Time { return epoch })
	a := newAuditionEnv(t, func(o *desk.Options) { o.Signer = signer })
	au, _, err := a.svc.Audition(context.Background(), alice, a.planID(), desk.AuditionIn{})
	if err != nil {
		t.Fatal(err)
	}
	sha := a.scalar(`SELECT mp3_sha256 FROM desk_auditions`)
	if !strings.Contains(au.URL, sha) || !strings.Contains(au.URL, ".mp3") {
		t.Errorf("url = %q", au.URL)
	}
}

// TestAudition_DefaultZoneDay: the day window follows the configured zone.
func TestAudition_ZoneDay(t *testing.T) {
	t.Parallel()
	z := time.FixedZone("x", 12*3600)
	a := newAuditionEnv(t, func(o *desk.Options) { o.AuditionsPerDay = 1; o.Zone = z })
	id := a.planID()
	ctx := context.Background()
	g1, g2 := 0.5, 0.7
	if _, _, err := a.svc.Audition(ctx, alice, id, desk.AuditionIn{Gain: &g1}); err != nil {
		t.Fatal(err)
	}
	// 10:00 UTC is 22:00 at +12: two hours later the local day has turned
	a.advance(2 * time.Hour)
	if _, _, err := a.svc.Audition(ctx, alice, id, desk.AuditionIn{Gain: &g2}); err != nil {
		t.Errorf("the new local day: %v", err)
	}
}

// TestAudition_SameBytesOtherSettings: two settings that happen to encode to the very same MP3 are one stored file: the second
// request answers the first row (mp3_sha256 is unique) and leaves no stray file.
func TestAudition_SameBytesOtherSettings(t *testing.T) {
	t.Parallel()
	a := newAuditionEnv(t, func(o *desk.Options) {
		o.Encode = func(_ context.Context, _, out string) error { return os.WriteFile(out, []byte("constant mp3"), 0o600) }
	})
	id := a.planID()
	ctx := context.Background()
	g := 0.5
	first, _, err := a.svc.Audition(ctx, alice, id, desk.AuditionIn{})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := a.svc.Audition(ctx, alice, id, desk.AuditionIn{Gain: &g})
	if err != nil || second.ID != first.ID {
		t.Fatalf("second = %+v err %v", second, err)
	}
	n := 0
	_ = filepath.WalkDir(filepath.Join(a.dir, "derived", "auditions"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	if n != 1 {
		t.Errorf("%d files, want 1", n)
	}
}
