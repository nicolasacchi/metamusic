package ears_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/synth"
)

var allNames = []string{"silence", "clipping", "token_loops", "spectrogram"}

// addSemantic gives a track a real semantic.npy artifact (a clean random token stream).
func (r *rig) addSemantic(id string, seconds float64) string {
	r.t.Helper()
	rel := "takes/" + id + "/semantic.npy"
	if err := os.MkdirAll(filepath.Join(r.dir, "takes", id), 0o750); err != nil {
		r.t.Fatal(err)
	}
	abs := filepath.Join(r.dir, rel)
	if err := synth.SemanticNPY(abs, synth.NPYOptions{TokensPerS: 50, Seconds: seconds, Vocab: 1000, Seed: 7}); err != nil {
		r.t.Fatal(err)
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		r.t.Fatal(err)
	}
	r.exec(`INSERT INTO artifacts(track_id, name, sha256, bytes, path, created_at) VALUES (?, 'semantic.npy', ?, ?, ?, ?)`,
		id, library.SHA256Hex(raw), len(raw), rel, r.now.Format(time.RFC3339))
	return rel
}

func (r *rig) spectroSHA(id string) string {
	r.t.Helper()
	return r.str(`SELECT sha256 FROM renditions WHERE track_id = ? AND kind = 'spectrogram'`, id)
}

func TestAnalyze_RunsMissingOnly(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	id := lt.TrackTwinA // 8 s, 48 kHz, no Tier A rows
	r.addSemantic(id, 8)
	ctx := context.Background()

	out, err := r.svc.AnalyzeTrack(ctx, id, ears.AnalyzeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(allNames, out.Ran); diff != "" || len(out.Failed) != 0 || len(out.Skipped) != 0 {
		t.Fatalf("first run: %+v\n%s", out, diff)
	}
	if got := r.count(`SELECT count(*) FROM analyses WHERE track_id = ? AND tier = 'A'`, id); got != 3 {
		t.Errorf("tier A analyses = %d, want 3 (silence, clipping, token_loops)", got)
	}
	if got := r.count(`SELECT count(*) FROM renditions WHERE track_id = ? AND kind = 'spectrogram'`, id); got != 1 {
		t.Errorf("spectrogram renditions = %d, want 1", got)
	}
	for _, an := range []string{"silence", "clipping", "token_loops"} {
		v := r.str(`SELECT version FROM analyses WHERE track_id = ? AND analyzer = ?`, id, an)
		if !strings.HasPrefix(v, "mm-"+strings.ReplaceAll(an, "_", "-")+"/1 ") {
			t.Errorf("%s version = %q", an, v)
		}
	}
	sig, pic := r.tier.Calls()
	if sig != 1 || pic < 1 {
		t.Fatalf("tool calls after first run = %d signal, %d picture", sig, pic)
	}
	sha := r.spectroSHA(id)

	out, err = r.svc.AnalyzeTrack(ctx, id, ears.AnalyzeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Ran) != 0 || !slices.Equal(out.Skipped, allNames) {
		t.Fatalf("second run: %+v, want everything skipped", out)
	}
	if s2, p2 := r.tier.Calls(); s2 != sig || p2 != pic {
		t.Errorf("second run called the tools: %d/%d -> %d/%d", sig, pic, s2, p2)
	}

	out, err = r.svc.AnalyzeTrack(ctx, id, ears.AnalyzeOptions{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(out.Ran, allNames) || len(out.Failed) != 0 {
		t.Fatalf("forced run: %+v", out)
	}
	if got := r.count(`SELECT count(*) FROM analyses WHERE track_id = ? AND tier = 'A'`, id); got != 3 {
		t.Errorf("forced run with the same tool version changed the analyses: %d rows", got)
	}
	if got := r.spectroSHA(id); got != sha {
		t.Errorf("forced run changed the spectrogram: %s -> %s", sha, got)
	}
}

func TestAnalyze_WithoutSemanticSkipsTokenLoops(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	out, err := r.svc.AnalyzeTrack(context.Background(), lt.TrackCitta, ears.AnalyzeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"silence", "clipping", "spectrogram"}; !slices.Equal(out.Ran, want) {
		t.Errorf("ran %v, want %v", out.Ran, want)
	}
	if got := r.count(`SELECT count(*) FROM analyses WHERE analyzer = 'token_loops'`); got != 0 {
		t.Errorf("token_loops rows = %d", got)
	}
}

func TestAnalyze_UnknownTrack(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	if _, err := r.svc.AnalyzeTrack(context.Background(), "no-such-track", ears.AnalyzeOptions{}); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("err = %v, want library.ErrNotFound", err)
	}
}

func TestAnalyze_StaleOverlayRerenders(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	id := lt.TrackLuna // aligned score: timed plan sections
	ctx := context.Background()
	if _, err := r.svc.AnalyzeTrack(ctx, id, ears.AnalyzeOptions{}); err != nil {
		t.Fatal(err)
	}
	oldSHA := r.spectroSHA(id)
	oldPath := r.str(`SELECT path FROM renditions WHERE track_id = ? AND kind = 'spectrogram'`, id)

	// measured section times outrank the plan: the overlay changes
	for i, s := range []struct {
		label string
		a, b  float64
	}{{"verse 1", 0, 4}, {"chorus 1", 4, 8}, {"outro 1", 8, 12}} {
		r.exec(`INSERT INTO sections(track_id, source, idx, kind, label, raw_label, start_s, end_s, confidence) VALUES (?, 'measured', ?, 'other', ?, ?, ?, ?, 0.9)`,
			id, i, s.label, s.label, s.a, s.b)
	}
	sig, _ := r.tier.Calls()
	out, err := r.svc.AnalyzeTrack(ctx, id, ears.AnalyzeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"spectrogram"}; !slices.Equal(out.Ran, want) {
		t.Fatalf("ran %v, want only %v", out.Ran, want)
	}
	if s2, _ := r.tier.Calls(); s2 != sig {
		t.Errorf("a stale overlay re-ran the signal pass")
	}
	if got := r.spectroSHA(id); got == oldSHA {
		t.Error("spectrogram sha did not change")
	}
	if n := r.count(`SELECT count(*) FROM renditions WHERE track_id = ? AND kind = 'spectrogram'`, id); n != 1 {
		t.Errorf("rendition rows = %d, want the one row updated", n)
	}
	if _, err := os.Stat(filepath.Join(r.dir, oldPath)); err != nil {
		t.Errorf("old spectrogram file must stay as an orphan: %v", err)
	}
	if out, _ = r.svc.AnalyzeTrack(ctx, id, ears.AnalyzeOptions{}); len(out.Ran) != 0 {
		t.Errorf("fresh overlay re-rendered: %+v", out)
	}
}

func TestAnalyze_PartialFailure(t *testing.T) {
	t.Parallel()
	t.Run("spectrogram fails", func(t *testing.T) {
		r := newRig(t)
		r.tier.PicErr = errors.New("ffmpeg exploded")
		out, err := r.svc.AnalyzeTrack(context.Background(), lt.TrackCitta, ears.AnalyzeOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if out.Failed["spectrogram"] != "TOOL_FAILED" || len(out.Failed) != 1 {
			t.Errorf("failed = %v, want only spectrogram: TOOL_FAILED", out.Failed)
		}
		if want := []string{"silence", "clipping"}; !slices.Equal(out.Ran, want) {
			t.Errorf("ran %v, want %v", out.Ran, want)
		}
		if n := r.count(`SELECT count(*) FROM analyses WHERE track_id = ? AND tier = 'A'`, lt.TrackCitta); n != 2 {
			t.Errorf("committed analyses = %d, want silence and clipping", n)
		}
		if n := r.count(`SELECT count(*) FROM renditions WHERE kind = 'spectrogram'`); n != 0 {
			t.Errorf("a failed spectrogram left a rendition row")
		}
	})
	t.Run("signal pass fails", func(t *testing.T) {
		r := newRig(t)
		r.tier.SignalErr = errors.New("ffmpeg exploded")
		out, err := r.svc.AnalyzeTrack(context.Background(), lt.TrackCitta, ears.AnalyzeOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if out.Failed["silence"] != "TOOL_FAILED" || out.Failed["clipping"] != "TOOL_FAILED" || len(out.Failed) != 2 {
			t.Errorf("failed = %v", out.Failed)
		}
		if n := r.count(`SELECT count(*) FROM renditions WHERE kind = 'spectrogram'`); n != 1 {
			t.Errorf("spectrogram must still be stored: %d rows", n)
		}
	})
	t.Run("unreadable semantic.npy", func(t *testing.T) {
		r := newRig(t)
		rel := r.addSemantic(lt.TrackTwinA, 8)
		if err := os.WriteFile(filepath.Join(r.dir, rel), []byte("not an npy file"), 0o640); err != nil {
			t.Fatal(err)
		}
		out, err := r.svc.AnalyzeTrack(context.Background(), lt.TrackTwinA, ears.AnalyzeOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if out.Failed["token_loops"] != "NPY_UNSUPPORTED" || len(out.Failed) != 1 {
			t.Errorf("failed = %v", out.Failed)
		}
	})
}

func TestAnalyze_TierAMissingReportedOnce(t *testing.T) {
	t.Parallel()
	missing := fmt.Errorf("%w: filter showspectrumpic", analysis.ErrToolMissing)
	r := newRig(t, func(_ *rig, o *ears.Options) { o.Missing = missing })
	ctx := context.Background()
	before := r.count(`SELECT count(*) FROM analyses`)

	out, err := r.svc.AnalyzeTrack(ctx, lt.TrackCitta, ears.AnalyzeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Failed["tier_a"] != "TOOL_MISSING" || len(out.Ran) != 0 {
		t.Errorf("outcome = %+v", out)
	}
	rep, err := r.svc.Backfill(ctx, ears.BackfillOptions{TrackIDs: []string{lt.TrackCitta, lt.TrackLuna, lt.TrackTwinA}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rep.ToolMissing, "showspectrumpic") {
		t.Errorf("ToolMissing = %q, want the capability named", rep.ToolMissing)
	}
	if rep.Analysed != 0 || len(rep.Failures) != 0 {
		t.Errorf("report = %+v, want nothing analysed and no per-track failures", rep)
	}
	if sig, pic := r.tier.Calls(); sig != 0 || pic != 0 {
		t.Errorf("tools were called: %d/%d", sig, pic)
	}
	if got := r.count(`SELECT count(*) FROM analyses`); got != before {
		t.Errorf("analyses changed: %d -> %d", before, got)
	}
}

func TestAnalyze_NilAnalyzerIsMissing(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(_ *rig, o *ears.Options) { o.Analyzer = nil })
	out, err := r.svc.AnalyzeTrack(context.Background(), lt.TrackCitta, ears.AnalyzeOptions{})
	if err != nil || out.Failed["tier_a"] != "TOOL_MISSING" {
		t.Errorf("out = %+v, err = %v", out, err)
	}
}

type fileState struct {
	sha  string
	mode os.FileMode
	mod  time.Time
}

func (r *rig) masterState(id string) fileState {
	r.t.Helper()
	path := r.str(`SELECT master_path FROM tracks WHERE id = ?`, id)
	st, err := os.Stat(filepath.Join(r.dir, path))
	if err != nil {
		r.t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(r.dir, path))
	if err != nil {
		r.t.Fatal(err)
	}
	return fileState{library.SHA256Hex(raw), st.Mode(), st.ModTime()}
}

func TestAnalyze_MasterUntouched(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	// frozen masters are 0444 in production
	path := filepath.Join(r.dir, r.str(`SELECT master_path FROM tracks WHERE id = ?`, lt.TrackCitta))
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	before := r.masterState(lt.TrackCitta)
	if _, err := r.svc.AnalyzeTrack(context.Background(), lt.TrackCitta, ears.AnalyzeOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if after := r.masterState(lt.TrackCitta); after != before {
		t.Errorf("master changed: %+v -> %+v", before, after)
	}
	st, _ := os.Stat(path)
	if sys, ok := nlink(st); ok && sys != 1 {
		t.Errorf("master still has %d links: the work-dir link leaked", sys)
	}
}

func TestAnalyze_WorkDirRemoved(t *testing.T) {
	t.Parallel()
	left := func(r *rig) []string {
		ents, err := os.ReadDir(filepath.Join(r.dir, "incoming"))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range ents {
			out = append(out, e.Name())
		}
		return out
	}
	t.Run("success", func(t *testing.T) {
		r := newRig(t)
		if _, err := r.svc.AnalyzeTrack(context.Background(), lt.TrackCitta, ears.AnalyzeOptions{}); err != nil {
			t.Fatal(err)
		}
		if l := left(r); len(l) != 0 {
			t.Errorf("incoming/ still holds %v", l)
		}
	})
	t.Run("failure", func(t *testing.T) {
		r := newRig(t)
		r.tier.PicErr, r.tier.SignalErr = errors.New("x"), errors.New("y")
		if _, err := r.svc.AnalyzeTrack(context.Background(), lt.TrackCitta, ears.AnalyzeOptions{}); err != nil {
			t.Fatal(err)
		}
		if l := left(r); len(l) != 0 {
			t.Errorf("incoming/ still holds %v", l)
		}
	})
	t.Run("missing master", func(t *testing.T) {
		r := newRig(t)
		if err := os.Remove(filepath.Join(r.dir, r.str(`SELECT master_path FROM tracks WHERE id = ?`, lt.TrackCitta))); err != nil {
			t.Fatal(err)
		}
		out, err := r.svc.AnalyzeTrack(context.Background(), lt.TrackCitta, ears.AnalyzeOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if out.Failed["silence"] != "IO" || out.Failed["spectrogram"] != "IO" {
			t.Errorf("failed = %v, want IO", out.Failed)
		}
		if l := left(r); len(l) != 0 {
			t.Errorf("incoming/ still holds %v", l)
		}
	})
}

func TestAnalyze_WritesTwice(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	for i := range 2 {
		out, err := r.svc.AnalyzeTrack(ctx, lt.TrackCitta, ears.AnalyzeOptions{Force: true})
		if err != nil || len(out.Failed) != 0 {
			t.Fatalf("run %d: %+v %v", i, out, err)
		}
	}
	if n := r.count(`SELECT count(*) FROM analyses WHERE tier = 'A'`); n != 2 {
		t.Errorf("analyses = %d", n)
	}
	if n := r.count(`SELECT count(*) FROM renditions WHERE kind = 'spectrogram'`); n != 1 {
		t.Errorf("renditions = %d", n)
	}
}

func TestBackfill_Report(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	dry, err := r.svc.Backfill(ctx, ears.BackfillOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if dry.Tracks != 5 || dry.Ran["silence"] != 5 || dry.Ran["spectrogram"] != 5 || dry.Ran["token_loops"] != 0 {
		t.Errorf("dry run = %+v", dry)
	}
	if sig, pic := r.tier.Calls(); sig != 0 || pic != 0 || r.count(`SELECT count(*) FROM analyses WHERE tier = 'A'`) != 0 {
		t.Error("a dry run touched tools or the database")
	}
	rep, err := r.svc.Backfill(ctx, ears.BackfillOptions{Jobs: 3})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Tracks != 5 || rep.Analysed != 5 || rep.Failed != 0 || rep.Ran["clipping"] != 5 {
		t.Errorf("report = %+v", rep)
	}
	if n := r.count(`SELECT count(*) FROM analyses WHERE tier = 'A'`); n != 10 {
		t.Errorf("analyses = %d, want 10", n)
	}
	again, err := r.svc.Backfill(ctx, ears.BackfillOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if again.Analysed != 0 || again.Tracks != 5 || len(again.Ran) != 0 {
		t.Errorf("second backfill = %+v", again)
	}
	// failures are counted and listed
	r2 := newRig(t)
	r2.tier.PicErr = errors.New("x")
	bad, err := r2.svc.Backfill(ctx, ears.BackfillOptions{TrackIDs: []string{lt.TrackCitta, lt.TrackLuna}})
	if err != nil {
		t.Fatal(err)
	}
	if bad.Failed != 2 || bad.Analysed != 0 || len(bad.Failures) != 2 || bad.Failures[0].Failed["spectrogram"] != "TOOL_FAILED" {
		t.Errorf("failure report = %+v", bad)
	}
}
