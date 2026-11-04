package features_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/atlas"
	"github.com/nicolasacchi/metamusic/internal/features"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

// aceRecipe inserts an acestep15 recipe; req overrides or extends the default request fields.
func (w *world) aceRecipe(caption string, req map[string]any) string {
	w.t.Helper()
	id := w.newID()
	m := map[string]any{"id": "req-" + id[24:], "task": "text2music", "caption": caption, "lyrics": "[Verse]\nla", "seed": 7}
	for k, v := range req {
		m[k] = v
	}
	rj, _ := json.Marshal(m)
	w.exec(`INSERT INTO recipes(id,engine,request_id,request_json,style,lyrics,seed,cot,hash,core_hash,created_by,created_at)
		VALUES(?,'acestep15',?,?,?,?,7,'',?,?,'job',?)`, id, "req-"+id[24:], string(rj), caption, "[Verse]\nla", librarytest.Sha("h"+id), librarytest.Sha("c"+id), "2026-10-06T12:00:00Z")
	return id
}

// TestFeatures_ACEStepDeclaredKeyAndTempo: an ACE-Step take has no score and no style term; declared tempo comes from
// tracks.bpm_declared (or request.bpm), declared key from request.keyscale, expected duration from duration_ds (C164).
func TestFeatures_ACEStepDeclaredKeyAndTempo(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	bpm96 := 96
	cases := []struct {
		name     string
		req      map[string]any
		declared *int
		setup    func(id string)
		check    func(t *testing.T, r features.Row)
	}{
		{"declared column and keyscale", map[string]any{"bpm": 96, "keyscale": "F# Minor", "duration_ds": 1200}, &bpm96, func(string) {}, func(t *testing.T, r features.Row) {
			if *r.BPMx100 != 9600 || r.BPMBasis != "declared" || *r.BPMRequestedx100 != 9600 {
				t.Errorf("tempo %+v", r)
			}
			if r.Key == nil || *r.Key != (features.Camelot{Num: 11, Letter: "A"}) || r.KeyBasis != "declared" {
				t.Errorf("key %+v", r)
			}
			if r.DurationExpectedS == nil || *r.DurationExpectedS != 120 {
				t.Errorf("expected duration %v", r.DurationExpectedS)
			}
		}},
		{"request bpm without the column", map[string]any{"bpm": 120, "keyscale": "C Major"}, nil, func(string) {}, func(t *testing.T, r features.Row) {
			if *r.BPMx100 != 12000 || r.BPMBasis != "declared" {
				t.Errorf("tempo %+v", r)
			}
			if r.Key == nil || *r.Key != (features.Camelot{Num: 8, Letter: "B"}) {
				t.Errorf("key %+v", r)
			}
		}},
		{"column wins over request bpm", map[string]any{"bpm": 120}, &bpm96, func(string) {}, func(t *testing.T, r features.Row) {
			if *r.BPMRequestedx100 != 9600 {
				t.Errorf("tempo %+v", r)
			}
		}},
		{"measured key beats declared, relation recorded", map[string]any{"keyscale": "A Minor"}, nil, func(id string) { w.key(id, 0, "major") }, func(t *testing.T, r features.Row) {
			if r.KeyBasis != "measured" || *r.Key != (features.Camelot{Num: 8, Letter: "B"}) || !strings.Contains(string(r.Detail), `"declared":"8A"`) || !strings.Contains(string(r.Detail), `"relation":"relative"`) {
				t.Errorf("%+v %s", r, r.Detail)
			}
		}},
		{"keyscale wins over a caption key", map[string]any{"keyscale": "D Major"}, nil, func(string) {}, func(t *testing.T, r features.Row) {
			if *r.Key != (features.Camelot{Num: 10, Letter: "B"}) {
				t.Errorf("key %+v", r.Key)
			}
		}},
		{"no keyscale, no bpm: measured values only", map[string]any{}, nil, func(id string) { w.tempo(id, 101.5) }, func(t *testing.T, r features.Row) {
			if r.Key != nil || r.BPMRequestedx100 != nil || r.BPMBasis != "measured" || *r.BPMx100 != 10150 {
				t.Errorf("%+v", r)
			}
		}},
		{"unreadable keyscale and out-of-range bpm are ignored", map[string]any{"keyscale": "H lydian", "bpm": 5}, nil, func(string) {}, func(t *testing.T, r features.Row) {
			if r.Key != nil || r.BPMx100 != nil {
				t.Errorf("%+v", r)
			}
		}},
		{"repaint carries no expected duration", map[string]any{"task": "repaint", "duration_ds": 600}, nil, func(string) {}, func(t *testing.T, r features.Row) {
			if r.DurationExpectedS != nil {
				t.Errorf("expected duration %v", *r.DurationExpectedS)
			}
		}},
	}
	ids := make([]string, len(cases))
	for i, c := range cases {
		caption := "chill pop"
		if strings.HasPrefix(c.name, "keyscale wins") {
			caption = "chill pop, in A minor"
		}
		ids[i] = w.track(w.aceRecipe(caption, c.req), trackOpts{Declared: c.declared, AgeMin: i + 1})
		c.setup(ids[i])
	}
	w.settle()
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, ok, err := w.svc.Take(context.Background(), ids[i])
			if err != nil || !ok {
				t.Fatalf("Take: %v %v", ok, err)
			}
			c.check(t, r)
		})
	}
	w.verifyEmpty()
}

// TestFeatures_ACEStepMixedSong: a yue2 root with an ACE-Step port child, beside a lossy reference import; every take
// gets a row, the ACE take keeps its declared values and the yue2 take is unaffected.
func TestFeatures_ACEStepMixedSong(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	root := w.track(w.recipe("pop, in A minor, 100bpm", ""), trackOpts{AgeMin: 30})
	ref := w.track(w.stubRecipe(), trackOpts{Ref: true, AgeMin: 20})
	port := w.track(w.aceRecipe("pop", map[string]any{"bpm": 100, "keyscale": "A Minor"}), trackOpts{Parent: root, AgeMin: 10})
	w.settle()
	if w.songOf(root) != w.songOf(port) {
		t.Fatalf("port child is in another Song")
	}
	for _, id := range []string{root, ref, port} {
		if _, ok, err := w.svc.Take(context.Background(), id); err != nil || !ok {
			t.Errorf("take %s: ok=%v err=%v", id, ok, err)
		}
	}
	r, _, _ := w.svc.Take(context.Background(), port)
	if r.KeyBasis != "declared" || r.Key == nil || r.Key.Num != 8 {
		t.Errorf("port take %+v", r)
	}
	rr, _, _ := w.svc.Take(context.Background(), ref)
	if rr.Key != nil || rr.BPMRequestedx100 != nil {
		t.Errorf("reference take picked up declared values: %+v", rr)
	}
	w.verifyEmpty()
}

// TestAdherence_ACEStepPair: get_adherence gets a requested-vs-got pair for an ACE-Step take from request.bpm.
func TestAdherence_ACEStepPair(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	tr := w.track(w.aceRecipe("pop", map[string]any{"bpm": 100, "keyscale": "A Minor"}), trackOpts{})
	w.tempo(tr, 102)
	w.settle()
	got, err := atlas.New(w.st, w.now).Adherence(context.Background(), atlas.AdherenceQuery{MinN: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Items[0].TakeID != tr || got.Items[0].RequestedBPM != 100 || got.Items[0].MeasuredBPM != 102 {
		t.Errorf("items %+v", got.Items)
	}
}
