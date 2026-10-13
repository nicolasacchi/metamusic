package library_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
)

func TestTrackRefs(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	got, err := e.svc.TrackRefs(ctx, []string{lt.TrackLuna, lt.TrackTwinA, lt.TrackTwinB, "nope"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("refs=%d (unknown ids must be absent): %v", len(got), got)
	}
	if _, ok := got["nope"]; ok {
		t.Error("unknown id present")
	}
	luna, a, b := got[lt.TrackLuna], got[lt.TrackTwinA], got[lt.TrackTwinB]
	if luna.ID != lt.TrackLuna || luna.Title != "Luna Park" || luna.RequestID != "fx-luna" || luna.Seed != 101 ||
		luna.DurationS != 12 || luna.SampleRate != 48000 || luna.Channels != 2 || luna.TotalSamples <= 0 ||
		len(luna.MasterSHA256) != 64 || !strings.HasPrefix(luna.MasterPath, "audio/masters/") || luna.Status == "" || luna.RecordedAt.IsZero() {
		t.Errorf("luna: %+v", luna)
	}
	if luna.RootTrackID != lt.TrackLuna || luna.ParentTrackID != "" || luna.ParentOperator != "" {
		t.Errorf("root lineage: %+v", luna)
	}
	if a.ParentTrackID != "" || a.ParentOperator != "" || a.RootTrackID != lt.TrackTwinA {
		t.Errorf("twin a: %+v", a)
	}
	if b.RootTrackID != lt.TrackTwinA || b.ParentTrackID != lt.TrackTwinA || b.ParentOperator != "inferred_sibling" {
		t.Errorf("twin b: %+v", b)
	}
	empty, err := e.svc.TrackRefs(ctx, nil)
	if err != nil || len(empty) != 0 {
		t.Errorf("empty: %v %v", empty, err)
	}
	// more ids than SQLite's variable limit still work
	many := make([]string, 2000)
	for i := range many {
		many[i] = "x" + string(rune('a'+i%26)) + strings.Repeat("0", i%7)
	}
	many = append(many, lt.TrackLuna)
	got, err = e.svc.TrackRefs(ctx, many)
	if err != nil || len(got) != 1 {
		t.Errorf("many: %d %v", len(got), err)
	}
}

func TestLineageTracks_Order(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	got, err := e.svc.LineageTracks(ctx, lt.TrackTwinA)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range got {
		ids = append(ids, r.ID)
	}
	if diff := cmp.Diff([]string{lt.TrackTwinA, lt.TrackTwinB}, ids); diff != "" {
		t.Errorf("order (-want +got):\n%s", diff)
	}
	if got, err := e.svc.LineageTracks(ctx, lt.TrackLuna); err != nil || len(got) != 1 {
		t.Errorf("single: %v %v", got, err)
	}
	if _, err := e.svc.LineageTracks(ctx, "nope"); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("unknown root: %v", err)
	}
	if _, err := e.svc.LineageTracks(ctx, lt.TrackTwinB); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("non-root id: %v", err)
	}
}

func TestSections_SelectionRule(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()

	// aligned plan: timed, lines of the plan rows, structure from the lyrics rows, expected duration = bars x spb
	s, err := e.svc.Sections(ctx, lt.TrackLuna)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Timed || s.Source != "plan" || len(s.Timeline) != 3 || s.Confidence == nil || *s.Confidence != 0.7 {
		t.Fatalf("luna: %+v", s)
	}
	if s.Timeline[0].StartS != 0 || s.Timeline[0].EndS != 4 || s.Timeline[2].EndS != 12 || s.Timeline[1].Kind != "chorus" {
		t.Errorf("timeline: %+v", s.Timeline)
	}
	if len(s.Timeline[0].Lines) == 0 {
		t.Errorf("timeline lines missing: %+v", s.Timeline[0])
	}
	if len(s.Structure) != 3 || s.ExpectedDurationS == nil || *s.ExpectedDurationS != 12 {
		t.Errorf("structure=%v expected=%v", s.Structure, s.ExpectedDurationS)
	}

	// unaligned / no score: untimed, structure only
	for _, id := range []string{lt.TrackCitta, lt.TrackShort} {
		s, err := e.svc.Sections(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if s.Timed || s.Source != "" || len(s.Timeline) != 0 || len(s.Structure) == 0 || s.ExpectedDurationS != nil || s.Confidence != nil {
			t.Errorf("%s: %+v", id, s)
		}
	}

	// manual outranks plan when all its rows are timed
	exec(t, e, `INSERT INTO sections(track_id, source, idx, kind, label, raw_label, start_s, end_s, confidence)
		VALUES (?, 'manual', 0, 'verse', 'verse', 'verse', 0, 5, 1.0), (?, 'manual', 1, 'chorus', 'chorus', 'chorus', 5, 12, 1.0)`, lt.TrackLuna, lt.TrackLuna)
	s, _ = e.svc.Sections(ctx, lt.TrackLuna)
	if !s.Timed || s.Source != "manual" || len(s.Timeline) != 2 || s.Timeline[0].EndS != 5 {
		t.Errorf("manual: %+v", s)
	}

	// agreement with Track(): same source, timed flag and rows
	for _, id := range []string{lt.TrackLuna, lt.TrackCitta, lt.TrackTwinA, lt.TrackShort} {
		d, err := e.svc.Track(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		s, err := e.svc.Sections(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if d.Timed != s.Timed {
			t.Errorf("%s: Track.Timed=%v Sections.Timed=%v", id, d.Timed, s.Timed)
		}
		if s.Timed {
			if len(d.Sections) != len(s.Timeline) || d.Sections[0].Source != s.Source {
				t.Errorf("%s: Track sections %d/%s vs timeline %d/%s", id, len(d.Sections), d.Sections[0].Source, len(s.Timeline), s.Source)
			}
		}
	}
	if _, err := e.svc.Sections(ctx, "nope"); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("unknown track: %v", err)
	}
}

func TestArtifact_NotFound(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	a, err := e.svc.Artifact(ctx, lt.TrackLuna, "request.json")
	if err != nil || a.Name != "request.json" || len(a.SHA256) != 64 || a.Bytes <= 0 || !strings.HasPrefix(a.Path, "takes/"+lt.TrackLuna+"/") {
		t.Fatalf("artifact: %+v %v", a, err)
	}
	if _, err := os.Stat(filepath.Join(e.dir, a.Path)); err != nil {
		t.Errorf("artifact file: %v", err)
	}
	for _, c := range [][2]string{{lt.TrackLuna, "semantic.npy"}, {lt.TrackLuna, ""}, {"nope", "request.json"}} {
		if _, err := e.svc.Artifact(ctx, c[0], c[1]); !errors.Is(err, library.ErrNotFound) {
			t.Errorf("artifact %v: %v", c, err)
		}
	}
}

func TestResolve_JPG(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	sha := strings.Repeat("9a", 32)
	exec(t, e, `INSERT INTO renditions(track_id, kind, sha256, bytes, path, params_json, created_at)
		VALUES (?, 'spectrogram', ?, 10, ?, '{}', '2026-10-06T12:00:00Z')`, lt.TrackLuna, sha, "derived/9a/"+sha+".spectro.jpg")
	obj, err := e.svc.Resolve(ctx, sha, "jpg")
	if err != nil || obj.ContentType != "image/jpeg" || obj.ETag != sha || obj.RelPath != "derived/9a/"+sha+".spectro.jpg" || obj.ModTime.IsZero() {
		t.Fatalf("resolve jpg: %+v %v", obj, err)
	}
	for _, c := range [][2]string{{strings.Repeat("0", 64), "jpg"}, {sha, "flac"}, {sha, "mp3"}} {
		if _, err := e.svc.Resolve(ctx, c[0], c[1]); !errors.Is(err, serve.ErrNotFound) {
			t.Errorf("resolve %v: %v", c, err)
		}
	}
}
