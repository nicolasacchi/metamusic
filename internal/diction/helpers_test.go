package diction_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/diction"
	"github.com/nicolasacchi/metamusic/internal/ears/tierb"
	"github.com/nicolasacchi/metamusic/internal/ears/tierb/tierbtest"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/store"
)

var epoch = time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)

// env is a real SQLite store with a diction service and an advanceable clock.
type env struct {
	t     *testing.T
	st    *store.Store
	svc   *diction.Service
	now   atomic.Int64 // unix milliseconds
	takes int
}

var (
	alice = auth.Principal{Kind: auth.KindHuman, ID: "alice", Name: "alice", User: "alice"}
	robot = auth.Principal{Kind: auth.KindAgent, ID: "tok1", Name: "robot"}
)

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "mm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e := &env{t: t, st: st}
	e.now.Store(epoch.UnixMilli())
	clock := func() time.Time { return time.UnixMilli(e.now.Load()).UTC() }
	e.svc = diction.New(diction.Options{Store: st, Library: library.New(st, nil, clock), Now: clock})
	return e
}

func (e *env) advance(d time.Duration) { e.now.Add(d.Milliseconds()) }

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.st.W.Exec(q, args...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

// addTake inserts a recipe and a take with the given lyrics and returns the take id.
func (e *env) addTake(lyrics string) string {
	e.t.Helper()
	e.takes++
	n := e.takes
	id := fmt.Sprintf("01900000-0000-7000-8000-%012d", n)
	rid := fmt.Sprintf("01900000-0000-7000-9000-%012d", n)
	req := fmt.Sprintf(`{"id":"r%d","seed":%d}`, n, n)
	now := store.FormatTime(epoch)
	e.exec(`INSERT INTO recipes(id, engine, request_id, request_json, style, lyrics, seed, cot, hash, core_hash, created_by, created_at)
		VALUES (?, 'yue2', ?, ?, 'pop', ?, ?, 'full', ?, ?, 'test', ?)`, rid, fmt.Sprintf("r%d", n), req, lyrics, n, fmt.Sprintf("h%d", n), fmt.Sprintf("c%d", n), now)
	e.exec(`INSERT INTO tracks(id, slug, title, recipe_id, root_track_id, generation, status, status_set_by, origin, duration_s, sample_rate, bits_per_sample,
		channels, total_samples, master_path, master_sha256, master_bytes, source_sha256, frames_sha256, audio_md5, import_rule, recorded_at, created_at)
		VALUES (?, ?, ?, ?, ?, 0, 'draft', 'import', 'job', 30, 44100, 16, 2, ?, ?, ?, 1, 's', ?, 'm', 'job', ?, ?)`,
		id, fmt.Sprintf("take-%d", n), fmt.Sprintf("Take %d", n), rid, id, n, fmt.Sprintf("m/%d.flac", n), fmt.Sprintf("sha%d", n), fmt.Sprintf("f%d", n), now, now)
	return id
}

// addDiction stores the tier_b_raw and tier_b_diction rows a listening check of the take would have written, from the synthetic
// recognition of its lyrics.
func (e *env) addDiction(trackID, lyrics string, o tierbtest.Opts) {
	e.t.Helper()
	raw, err := tierb.Parse(tierbtest.FromLyrics(lyrics, o))
	if err != nil {
		e.t.Fatal(err)
	}
	res := tierb.Diction(tierb.RefWords(lyrics), raw)
	type sec struct {
		Lines []tierb.LineResult `json:"lines"`
	}
	doc := struct {
		Sections []sec `json:"sections"`
	}{}
	for _, s := range res.Sections {
		doc.Sections = append(doc.Sections, sec{Lines: s.Lines})
	}
	b, _ := json.Marshal(doc)
	at := store.FormatTime(time.UnixMilli(e.now.Load()))
	e.exec(`INSERT INTO analyses(track_id, analyzer, version, tier, metrics_json, created_at) VALUES (?, 'tier_b_diction', 'mm-diction/1 go1 abc', 'B', ?, ?)`, trackID, string(b), at)
	lang := o.Lang
	if lang == "" {
		lang = "it"
	}
	e.exec(`INSERT INTO analyses(track_id, analyzer, version, tier, metrics_json, created_at) VALUES (?, 'tier_b_raw', 'mm-tierb-raw/1 x', 'B', ?, ?)`,
		trackID, fmt.Sprintf(`{"language":%q}`, lang), at)
}

// addTimed gives the take a timed section source (plan) for the proportional basis.
func (e *env) addTimed(trackID string, src string, idx int, start, end float64) {
	e.t.Helper()
	e.exec(`INSERT INTO sections(track_id, source, idx, kind, label, raw_label, start_s, end_s) VALUES (?, ?, ?, 'verse', 'verse', '[Verse]', ?, ?)`,
		trackID, src, idx, start, end)
}

const chiave = "[Verse]\nla chiave del mare"

// sixTakes builds the C344.3 corpus: four takes hear "kiave", one hears "male", one drops "chiave".
func (e *env) sixTakes() []string {
	var ids []string
	opts := []tierbtest.Opts{
		{Subs: map[string]string{"chiave": "kiave"}}, {Subs: map[string]string{"chiave": "kiave"}},
		{Subs: map[string]string{"chiave": "kiave"}}, {Subs: map[string]string{"chiave": "kiave"}},
		{Subs: map[string]string{"mare": "male"}}, {Drops: map[string]bool{"chiave": true}},
	}
	for _, o := range opts {
		id := e.addTake(chiave)
		e.addDiction(id, chiave, o)
		ids = append(ids, id)
	}
	return ids
}

func diction2Opts() tierbtest.Opts { return tierbtest.Opts{} }
