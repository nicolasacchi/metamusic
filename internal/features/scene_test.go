package features_test

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

// recipe inserts a yue2 recipe with a style and an optional inline ABC score.
func (w *world) recipe(style, abc string) string {
	w.t.Helper()
	id, seed := w.newID(), 100+int(w.n.Add(1))
	m := map[string]any{"id": "req-" + id[24:], "seed": seed, "style": style, "lyrics": "[Verse 1]\nline"}
	if abc != "" {
		m["abc"] = abc
	}
	rj, _ := json.Marshal(m)
	w.exec(`INSERT INTO recipes(id,engine,request_id,request_json,style,lyrics,seed,cot,hash,core_hash,created_by,created_at)
		VALUES(?,'yue2',?,?,?,?,?,'full',?,?,'import',?)`, id, "req-"+id[24:], string(rj), style, "[Verse 1]\nline", seed, librarytest.Sha("h"+id), librarytest.Sha("c"+id), "2026-10-06T12:00:00Z")
	return id
}

// stubRecipe inserts a reference (engine 'unknown') recipe.
func (w *world) stubRecipe() string {
	w.t.Helper()
	id := w.newID()
	h := librarytest.Sha("stub" + id)
	rj, _ := json.Marshal(map[string]any{"id": "ref-" + id[24:], "seed": 0, "title": "Synthetic Reference"})
	w.exec(`INSERT INTO recipes(id,engine,request_id,request_json,style,lyrics,seed,cot,hash,core_hash,created_by,created_at)
		VALUES(?,'unknown',?,?,'','',0,'',?,?,'import',?)`, id, "ref-"+id[24:], string(rj), h, h, "2026-10-06T12:00:00Z")
	return id
}

type trackOpts struct {
	Parent   string
	Declared *int
	AgeMin   int
	Ref      bool
	Dur      float64
}

// track inserts a take; the recorded_at is FixedNow minus AgeMin minutes.
func (w *world) track(recipe string, o trackOpts) string {
	w.t.Helper()
	id := w.newID()
	h := librarytest.Sha("t" + id)
	at := librarytest.FixedNow.Add(-time.Duration(o.AgeMin) * time.Minute).Format(time.RFC3339)
	rule := "slug"
	if o.Ref {
		rule = "reference"
	}
	if o.Dur == 0 {
		o.Dur = 10
	}
	var parent, root, gen any = nil, id, 0
	if o.Parent != "" {
		parent, gen = o.Parent, 1
		root = w.str(`SELECT root_track_id FROM tracks WHERE id = ?`, o.Parent)
	}
	var decl any
	if o.Declared != nil {
		decl = *o.Declared
	}
	w.exec(`INSERT INTO tracks(id,slug,title,recipe_id,parent_track_id,root_track_id,generation,status,status_set_by,origin,duration_s,sample_rate,bits_per_sample,channels,total_samples,
		master_path,master_sha256,master_bytes,source_sha256,frames_sha256,audio_md5,bpm_declared,import_rule,recorded_at,created_at)
		VALUES(?,'slug','Synthetic Take',?,?,?,?,'draft','import','import',?,44100,16,2,?,?,?,1,'s',?,'m',?,?,?,?)`,
		id, recipe, parent, root, gen, o.Dur, 1000+int(w.n.Add(1)), "masters/"+id+".flac", h, h, decl, rule, at, at)
	return id
}

func (w *world) analysis(track, analyzer, version, metrics string) {
	w.t.Helper()
	w.exec(`INSERT INTO analyses(track_id,analyzer,version,tier,metrics_json,created_at) VALUES(?,?,?,'A',?,?)`, track, analyzer, version, metrics, "2026-10-06T12:00:00Z")
}

func (w *world) tempo(track string, bpm float64) {
	w.t.Helper()
	w.analysis(track, "tempo", "mm-tempo/1 ffmpeg n9", fmt.Sprintf(`{"bpm":%v,"confidence":3.1,"octave_ambiguous":false}`, bpm))
}

func (w *world) key(track string, tonic int, mode string) {
	w.t.Helper()
	w.analysis(track, "key", "mm-key/1 ffmpeg n9", fmt.Sprintf(`{"key":"x","tonic":%d,"mode":%q,"r":0.8,"margin":0.1,"ambiguous_relative":false}`, tonic, mode))
}

func (w *world) energy(track string, e float64) {
	w.t.Helper()
	w.analysis(track, "energy", "mm-energy/1 ffmpeg n9", fmt.Sprintf(`{"energy":%v}`, e))
}

// song returns the live Song id of a take after a settle.
func (w *world) songOf(track string) string {
	w.t.Helper()
	return w.str(`SELECT song_id FROM take_songs WHERE track_id = ?`, track)
}
