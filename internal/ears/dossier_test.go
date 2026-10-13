package ears_test

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/ears/earstest"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name string, v any) {
	t.Helper()
	got, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if diff := cmp.Diff(string(want), string(got)); diff != "" {
		t.Errorf("%s differs (-want +got):\n%s", name, diff)
	}
}

func TestDossier_Golden(t *testing.T) {
	t.Parallel()
	r := newRig(t, withTierARows())
	for _, c := range []struct{ file, id string }{{"dossier_loop.golden.json", lt.TrackLuna}, {"dossier_clip.golden.json", lt.TrackCitta}} {
		d, err := r.svc.Dossier(context.Background(), c.id)
		if err != nil {
			t.Fatal(err)
		}
		if d.Tier != "A" || !d.Complete || len(d.Missing) != 0 {
			t.Errorf("%s: tier %q complete %v missing %v", c.id, d.Tier, d.Complete, d.Missing)
		}
		golden(t, c.file, d)
	}
}

func TestDossier_PartialWhenMissing(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	d, err := r.svc.Dossier(context.Background(), lt.TrackTwinA)
	if err != nil {
		t.Fatal(err)
	}
	if d.Tier != "0" || d.Complete {
		t.Errorf("tier %q complete %v", d.Tier, d.Complete)
	}
	if want := []string{"silence", "clipping", "spectrogram"}; !slices.Equal(d.Missing, want) {
		t.Errorf("missing = %v, want %v", d.Missing, want)
	}
	if d.Loudness == nil || d.Spectrum == nil || d.Silence != nil || d.Clipping != nil || d.TokenLoops != nil || d.Spectrogram.Available {
		t.Errorf("blocks wrong: %+v", d)
	}
	if !slices.Contains(d.BlindSpots, "silence, clipping and token loops (Tier A not run yet)") {
		t.Errorf("blind spots = %v", d.BlindSpots)
	}
	if d.Flags == nil || d.Missing == nil || d.Structure.Sections == nil {
		t.Error("lists must be empty, not nil")
	}
	if !strings.Contains(d.Summary, "Tier A incomplete (missing: silence, clipping, spectrogram)") {
		t.Errorf("summary = %q", d.Summary)
	}
	if _, err := r.svc.Dossier(context.Background(), "nope"); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("unknown track: %v", err)
	}
}

func TestDossier_RowSelection(t *testing.T) {
	t.Parallel()
	r := newRig(t, withTierARows())
	id := lt.TrackCitta
	at := func(d time.Duration) string { return r.now.Add(d).Format(time.RFC3339) }
	ins := func(version string, internal float64, created string) {
		r.exec(`INSERT INTO analyses(track_id, analyzer, version, tier, metrics_json, created_at) VALUES (?, 'silence', ?, 'A', ?, ?)`,
			id, version, fmt.Sprintf(`{"threshold_db":-50,"min_duration_s":1,"spans":[],"spans_truncated":false,"leading_s":0,"trailing_s":0,"internal_s":%g,"total_s":0,"duration_s":6}`, internal), created)
	}
	get := func() (string, float64) {
		d, err := r.svc.Dossier(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		return d.Silence.Version, d.Silence.InternalS
	}
	if v, _ := get(); v != "mm-silence/1 "+earstest.Tool {
		t.Fatalf("fixture version %q", v)
	}
	ins("mm-silence/1 ffmpeg n9.0.2", 1.25, at(-30*time.Minute)) // newer created_at than the fixture row (-1h)
	if v, in := get(); v != "mm-silence/1 ffmpeg n9.0.2" || in != 1.25 {
		t.Errorf("newest created_at must win: %q %v", v, in)
	}
	ins("mm-silence/0 ffmpeg n8.0", 9, at(-time.Minute)) // another definition: ignored even though newest
	if v, _ := get(); v != "mm-silence/1 ffmpeg n9.0.2" {
		t.Errorf("a row of another definition was selected: %q", v)
	}
	ins("mm-silence/1 ffmpeg n9.0.3", 2.5, at(-30*time.Minute)) // same created_at: the greatest version wins
	if v, in := get(); v != "mm-silence/1 ffmpeg n9.0.3" || in != 2.5 {
		t.Errorf("tie must go to the greatest version: %q %v", v, in)
	}
	d, _ := r.svc.Dossier(context.Background(), id)
	if d.Provenance.Analyzers["silence"] != "mm-silence/1 ffmpeg n9.0.3" || d.Provenance.Analyzers["loudness"] == "" || d.Provenance.Analyzers["spectrogram"] == "" {
		t.Errorf("provenance = %v", d.Provenance.Analyzers)
	}
}

func TestDossier_TierRule(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("token loops are required only with a semantic.npy", func(t *testing.T) {
		r := newRig(t, withTierARows())
		d, err := r.svc.Dossier(ctx, lt.TrackCitta) // no semantic.npy, no token_loops row
		if err != nil || d.Tier != "A" || d.TokenLoops != nil || len(d.Missing) != 0 {
			t.Fatalf("clip track: tier %q missing %v err %v", d.Tier, d.Missing, err)
		}
		d, err = r.svc.Dossier(ctx, lt.TrackLuna)
		if err != nil || d.Tier != "A" || d.TokenLoops == nil {
			t.Fatalf("loop track: tier %q tl=%v err %v", d.Tier, d.TokenLoops, err)
		}
		if d.TokenLoops.ArtifactSHA256 != r.ears.SemanticSHA256 {
			t.Errorf("artifact sha = %q, want the artifact row's %q", d.TokenLoops.ArtifactSHA256, r.ears.SemanticSHA256)
		}
		r.dropTriggers("analyses_no_delete")
		r.exec(`DELETE FROM analyses WHERE analyzer = 'token_loops'`)
		d, _ = r.svc.Dossier(ctx, lt.TrackLuna)
		if d.Tier != "0" || !slices.Equal(d.Missing, []string{"token_loops"}) {
			t.Errorf("without the token_loops row: tier %q missing %v", d.Tier, d.Missing)
		}
		if !slices.Contains(d.BlindSpots, "silence, clipping and token loops (Tier A not run yet)") {
			t.Errorf("blind spots = %v", d.BlindSpots)
		}
	})
	t.Run("an older spectrogram definition is not Tier A", func(t *testing.T) {
		r := newRig(t, withTierARows())
		r.exec(`UPDATE renditions SET params_json = json_set(params_json, '$.def', 'mm-spectro/0') WHERE kind = 'spectrogram' AND track_id = ?`, lt.TrackCitta)
		d, _ := r.svc.Dossier(ctx, lt.TrackCitta)
		if d.Tier != "0" || d.Complete || !slices.Equal(d.Missing, []string{"spectrogram"}) || d.Spectrogram.Available {
			t.Errorf("tier %q missing %v available %v", d.Tier, d.Missing, d.Spectrogram.Available)
		}
	})
}

func TestDossier_Summary(t *testing.T) {
	t.Parallel()
	r := newRig(t, withTierARows())
	d1, _ := r.svc.Dossier(context.Background(), lt.TrackCitta)
	d2, _ := r.svc.Dossier(context.Background(), lt.TrackCitta)
	if d1.Summary != d2.Summary {
		t.Errorf("summary is not deterministic: %q vs %q", d1.Summary, d2.Summary)
	}
	if utf8.RuneCountInString(d1.Summary) > 300 {
		t.Errorf("summary is %d characters", utf8.RuneCountInString(d1.Summary))
	}
	const want = "-15.5 LUFS, true peak -1.2 dBTP, bandwidth 12.0 kHz; 2 warnings: clipping 2.0 s at 0:02; internal silence 2.5 s at 0:04"
	if d1.Summary != want {
		t.Errorf("summary = %q\nwant      %q", d1.Summary, want)
	}
	dl, _ := r.svc.Dossier(context.Background(), lt.TrackLuna)
	if !strings.Contains(dl.Summary, "token loop suspected at 0:03") {
		t.Errorf("loop summary = %q", dl.Summary)
	}
}
