package sounds_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/provenance"
	"github.com/nicolasacchi/metamusic/internal/sounds"
	"github.com/nicolasacchi/metamusic/internal/store"
)

var (
	agentP = auth.Principal{Kind: auth.KindAgent, ID: "tok-1", Name: "agent-one", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}, Via: "token"}
	humanP = auth.Principal{Kind: auth.KindHuman, ID: "op", Name: "op", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}, Via: "local"}
)

var (
	seedOnce sync.Once
	seedImg  []byte
	seedErr  error
)

func seededImage(t *testing.T) []byte {
	t.Helper()
	seedOnce.Do(func() {
		dir, err := os.MkdirTemp("", "mm-sounds-seed-")
		if err != nil {
			seedErr = err
			return
		}
		defer os.RemoveAll(dir)
		p := filepath.Join(dir, "s.db")
		st, err := store.Open(context.Background(), p)
		if err != nil {
			seedErr = err
			return
		}
		sv := sounds.New(st, func() time.Time { return librarytest.FixedNow }, nil)
		if seedErr = sv.EnsureSeed(context.Background()); seedErr == nil {
			seedErr = st.Close()
		}
		if seedErr == nil {
			seedImg, seedErr = os.ReadFile(p)
		}
	})
	if seedErr != nil {
		t.Fatal(seedErr)
	}
	return seedImg
}

// world is one empty library with the Sound service and a Reconciler that runs it.
type world struct {
	t    *testing.T
	path string
	st   *store.Store
	sv   *sounds.Service
	prov *provenance.Service
	n    int64
	tick atomic.Int64
}

func newWorld(t *testing.T, seed bool) *world {
	t.Helper()
	path := filepath.Join(t.TempDir(), "w.db")
	if seed { // a seeded image is built once per process: seeding ~500 terms row by row costs seconds under -race
		if err := os.WriteFile(path, seededImage(t), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	w := &world{t: t, path: path, st: st}
	w.sv = sounds.New(st, w.now, w.newID)
	if seed {
		if err := w.sv.EnsureSeed(context.Background()); err != nil { // already applied: a no-op that proves idempotence
			t.Fatal(err)
		}
	}
	w.prov = provenance.New(provenance.Options{Store: st, Sounds: w.sv, Now: w.now, NewID: w.newID})
	return w
}

// now is a strictly increasing clock so a rewrite always changes computed_at.
func (w *world) now() time.Time {
	return librarytest.FixedNow.Add(time.Duration(w.tick.Add(1)) * time.Second)
}

func (w *world) newID() string {
	return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xc00000+atomic.AddInt64(&w.n, 1))
}

func (w *world) exec(q string, args ...any) {
	w.t.Helper()
	if _, err := w.st.W.Exec(q, args...); err != nil {
		w.t.Fatalf("%v\n%s", err, q)
	}
}

func (w *world) count(q string, args ...any) int {
	w.t.Helper()
	var n int
	if err := w.st.R.QueryRow(q, args...).Scan(&n); err != nil {
		w.t.Fatalf("%v\n%s", err, q)
	}
	return n
}

func (w *world) str(q string, args ...any) string {
	w.t.Helper()
	var s sql.NullString
	if err := w.st.R.QueryRow(q, args...).Scan(&s); err != nil && err != sql.ErrNoRows {
		w.t.Fatalf("%v\n%s", err, q)
	}
	return s.String
}

func (w *world) settle() {
	w.t.Helper()
	if _, err := w.prov.Settle(context.Background()); err != nil {
		w.t.Fatalf("settle: %v", err)
	}
}

// recipe inserts a yue2 recipe with a style and lyrics.
func (w *world) recipe(style, lyrics string) string {
	w.t.Helper()
	id, seed := w.newID(), 100+int(w.n)
	rj, _ := json.Marshal(map[string]any{"id": "req-" + id[24:], "seed": seed, "style": style, "lyrics": lyrics})
	w.exec(`INSERT INTO recipes(id,engine,request_id,request_json,style,lyrics,seed,cot,hash,core_hash,created_by,created_at)
		VALUES(?,'yue2',?,?,?,?,?,'full',?,?,'import',?)`, id, "req-"+id[24:], string(rj), style, lyrics, seed, librarytest.Sha("h"+id), librarytest.Sha("c"+id), "2026-10-06T12:00:00Z")
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

// trackOpts shapes a take.
type trackOpts struct {
	Parent   string
	Declared *int
	Status   string
	AgeMin   int // recorded_at = FixedNow - AgeMin minutes
	Ref      bool
}

func (w *world) track(recipe string, o trackOpts) string {
	w.t.Helper()
	id := w.newID()
	h := librarytest.Sha("t" + id)
	at := librarytest.FixedNow.Add(-time.Duration(o.AgeMin) * time.Minute).Format(time.RFC3339)
	if o.Status == "" {
		o.Status = "draft"
	}
	rule := "slug"
	if o.Ref {
		rule = "reference"
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
		VALUES(?,'slug','Synthetic Take',?,?,?,?,?,'import','import',10.0,44100,16,2,?,?,?,1,'s',?,'m',?,?,?,?)`,
		id, recipe, parent, root, gen, o.Status, 1000+int(w.n), "masters/"+id+".flac", h, h, decl, rule, at, at)
	return id
}

func (w *world) score(track string, bpm float64) {
	w.t.Helper()
	w.exec(`INSERT INTO scores(track_id,abc_path,abc_sha256,bars,meter,q,bpm,seconds_per_bar,aligned,source,created_at) VALUES(?,'takes/x/score.abc','x',8,'4/4','1/4=90',?,2.5,1,'import',?)`,
		track, bpm, "2026-10-06T12:00:00Z")
}

// tempo records an Ears tempo analysis; bpm 0 means "no tempo found".
func (w *world) tempo(track string, bpm float64) {
	w.t.Helper()
	m := map[string]any{"bpm": nil, "confidence": 3.1}
	if bpm > 0 {
		m["bpm"] = bpm
	}
	b, _ := json.Marshal(m)
	w.exec(`INSERT INTO analyses(track_id,analyzer,version,tier,metrics_json,created_at) VALUES(?,'tempo','mm-tempo/1 ffmpeg n9','A',?,?)`, track, string(b), "2026-10-06T12:00:00Z")
}

// draft inserts a motherless backlog draft with a style and lyrics.
func (w *world) draft(style string) string {
	w.t.Helper()
	id := w.newID()
	rj, _ := json.Marshal(map[string]any{"id": "d-" + id[24:], "seed": 5, "style": style, "lyrics": "[Verse 1]\nline"})
	w.exec(`INSERT INTO recipe_drafts(id,origin,request_id,request_json,status,created_at,updated_at) VALUES(?,'import_backlog',?,?,'backlog',?,?)`,
		id, "d-"+id[24:], string(rj), "2026-10-06T12:00:00Z", "2026-10-06T12:00:00Z")
	return id
}

func (w *world) songOfTrack(track string) string {
	return w.str(`SELECT song_id FROM take_songs WHERE track_id = ?`, track)
}

func (w *world) songOfDraft(draft string) string {
	return w.str(`SELECT id FROM songs WHERE draft_id = ? AND retired_at IS NULL`, draft)
}

// pick sets the Pick of a Song to a take through the event chain the triggers demand (first pick only).
func (w *world) pick(song, take string) {
	w.t.Helper()
	ev := w.newID()
	w.exec(`INSERT INTO pick_events(id,song_id,seq,at,from_take_id,to_take_id,from_source,to_source,reason,actor,actor_kind,actor_name,evidence_json)
		VALUES(?,?,1,?,NULL,?,'none','newest','newest','system:provenance','system','provenance','{}')`, ev, song, "2026-10-06T12:00:00Z", take)
	w.exec(`INSERT INTO song_picks(song_id,take_id,source,evidence_json,last_event_id,changed_at,computed_at) VALUES(?,?,'newest','{}',?,?,?)`,
		song, take, ev, "2026-10-06T12:00:00Z", "2026-10-06T12:00:00Z")
}

func (w *world) facetValues(table, idCol, id, facet string) []string {
	w.t.Helper()
	rows, err := w.st.R.Query(`SELECT value FROM `+table+` WHERE `+idCol+` = ? AND facet = ? ORDER BY value`, id, facet)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			w.t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}

// rowsOf returns "facet/value/origin" strings of a subject, sorted.
func (w *world) rowsOf(table, idCol, id string) []string {
	w.t.Helper()
	rows, err := w.st.R.Query(`SELECT facet || '/' || value || '/' || origin FROM `+table+` WHERE `+idCol+` = ? ORDER BY 1`, id)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			w.t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}

func (w *world) computedAt(kind, id string) string {
	return w.str(`SELECT computed_at FROM sound_state WHERE subject_kind = ? AND subject_id = ?`, kind, id)
}

// compute runs ComputeTx for the given subjects in one transaction.
func (w *world) compute(takes, songs []string) sounds.Changes {
	w.t.Helper()
	tx, err := w.st.W.Begin()
	if err != nil {
		w.t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	ch, err := w.sv.ComputeTx(context.Background(), tx, sounds.Env{Now: w.now(), NewID: w.newID, Actor: "system:provenance"}, takes, songs)
	if err != nil {
		w.t.Fatalf("ComputeTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		w.t.Fatal(err)
	}
	return ch
}

func ip(v int) *int { return &v }

func soundsEnv(w *world) sounds.Env {
	return sounds.Env{Now: w.now(), NewID: w.newID, Actor: "system:provenance"}
}

// song gives a root take its own Song straight through SQL (the Reconciler's result, without running it).
func (w *world) song(track string) string {
	w.t.Helper()
	id := w.newID()
	w.exec(`INSERT INTO songs(id,root_track_id,title,origin,created_by,created_at,updated_by,updated_at) VALUES(?,?,'Synthetic','root','system:provenance',?,'system:provenance',?)`,
		id, track, "2026-10-06T12:00:00Z", "2026-10-06T12:00:00Z")
	w.exec(`INSERT INTO song_roots(root_track_id,song_id,via,attached_by,attached_at) VALUES(?,?,'own','system:provenance',?)`, track, id, "2026-10-06T12:00:00Z")
	return id
}
