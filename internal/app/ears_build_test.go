package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/app"
	"github.com/nicolasacchi/metamusic/internal/ears/tierb"
	"github.com/nicolasacchi/metamusic/internal/ears/tierb/tierbtest"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/subsonic"
)

// TestWireEars_RegisteredAtInit: a real Build carries the Tier B sweep loop (the build hook is registered at init).
func TestWireEars_RegisteredAtInit(t *testing.T) {
	t.Parallel()
	s, _ := build(t, app.Options{})
	for _, l := range s.Loops {
		if l.Name == "ears-tierb-sweep" {
			return
		}
	}
	t.Errorf("Build did not register the sweep loop: %+v", s.Loops)
}

// TestWireEars_DictionReachesLintAndContext: after Build the draft lint warns about a word the lexicon says was misheard, and
// the generation seam is wired (the seam itself is tested in internal/generation).
func TestWireEars_DictionReachesLintAndContext(t *testing.T) {
	t.Parallel()
	s, st := build(t, app.Options{})
	const lyrics = "[Verse]\nla chiave del mare"
	now := store.FormatTime(time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC))
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("01900000-0000-7000-8000-%012d", i)
		rid := fmt.Sprintf("01900000-0000-7000-9000-%012d", i)
		exec := func(q string, args ...any) {
			t.Helper()
			if _, err := st.W.Exec(q, args...); err != nil {
				t.Fatal(err)
			}
		}
		exec(`INSERT INTO recipes(id, engine, request_id, request_json, style, lyrics, seed, cot, hash, core_hash, created_by, created_at)
			VALUES (?, 'yue2', ?, ?, 'pop', ?, ?, 'full', ?, ?, 'test', ?)`, rid, fmt.Sprintf("r%d", i), fmt.Sprintf(`{"id":"r%d","seed":%d}`, i, i), lyrics, i, fmt.Sprintf("h%d", i), fmt.Sprintf("c%d", i), now)
		exec(`INSERT INTO tracks(id, slug, title, recipe_id, root_track_id, generation, status, status_set_by, origin, duration_s, sample_rate, bits_per_sample,
			channels, total_samples, master_path, master_sha256, master_bytes, source_sha256, frames_sha256, audio_md5, import_rule, recorded_at, created_at)
			VALUES (?, ?, ?, ?, ?, 0, 'draft', 'import', 'job', 30, 44100, 16, 2, ?, ?, ?, 1, 's', ?, 'm', 'job', ?, ?)`,
			id, fmt.Sprintf("take-%d", i), fmt.Sprintf("Take %d", i), rid, id, i, fmt.Sprintf("m/%d.flac", i), fmt.Sprintf("sha%d", i), fmt.Sprintf("f%d", i), now, now)
		raw, err := tierb.Parse(tierbtest.FromLyrics(lyrics, tierbtest.Opts{Subs: map[string]string{"chiave": "kiave"}}))
		if err != nil {
			t.Fatal(err)
		}
		res := tierb.Diction(tierb.RefWords(lyrics), raw)
		type sec struct {
			Lines []tierb.LineResult `json:"lines"`
		}
		doc := struct {
			Sections []sec `json:"sections"`
		}{}
		for _, sc := range res.Sections {
			doc.Sections = append(doc.Sections, sec{Lines: sc.Lines})
		}
		b, _ := json.Marshal(doc)
		exec(`INSERT INTO analyses(track_id, analyzer, version, tier, metrics_json, created_at) VALUES (?, 'tier_b_diction', 'mm-diction/1 go1 abc', 'B', ?, ?)`, id, string(b), now)
		exec(`INSERT INTO analyses(track_id, analyzer, version, tier, metrics_json, created_at) VALUES (?, 'tier_b_raw', 'mm-tierb-raw/1 x', 'B', '{"language":"it"}', ?)`, id, now)
	}
	req, _ := json.Marshal(map[string]any{"id": "x", "style": "italian", "lyrics": lyrics, "cot": "full", "seed": 3})
	rep, err := s.Deps.Drafts.Lint(context.Background(), []yue2.LintItem{{Request: req}}, false)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range rep.Items[0].Warnings {
		found = found || (w.Code == "diction_risk" && w.Message == "'chiave' was heard as 'kiave' in 3/3 takes")
	}
	if !found {
		t.Errorf("warnings = %+v", rep.Items[0].Warnings)
	}
}

// TestWireEars_SubsonicHandlerReachable: a build hook sees the built Subsonic handler, and the Ears hook has attached the
// Diction Lab to it (C348); UseDiction refuses any other handler.
func TestWireEars_SubsonicHandlerReachable(t *testing.T) {
	t.Parallel()
	var seen, attached bool
	build(t, app.Options{Hooks: []app.BuildHook{func(env *app.BuildEnv) error {
		seen = env.Subsonic != nil
		attached = subsonic.HasDiction(env.Subsonic)
		return nil
	}}})
	if !seen {
		t.Error("BuildEnv.Subsonic is not set")
	}
	if !attached {
		t.Error("the Ears hook did not attach the Diction Lab to the Subsonic handler")
	}
	if subsonic.UseDiction(http.NotFoundHandler(), nil) {
		t.Error("UseDiction accepted a foreign handler")
	}
}
