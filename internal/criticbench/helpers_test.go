package criticbench_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/criticbench"
	"github.com/nicolasacchi/metamusic/internal/store"
)

var epoch = time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)

type env struct {
	t     *testing.T
	st    *store.Store
	svc   *criticbench.Service
	n     int
	ticks int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "mm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e := &env{t: t, st: st}
	e.svc = criticbench.New(criticbench.Options{Store: st, Now: func() time.Time { return epoch }})
	e.exec(`INSERT INTO recipes(id, engine, request_id, request_json, style, lyrics, seed, cot, hash, core_hash, created_by, created_at)
		VALUES ('rec', 'yue2', 'r', '{"id":"r","seed":1}', 'pop', 'la', 1, 'full', 'h', 'c', 'test', ?)`, store.FormatTime(epoch))
	return e
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.st.W.Exec(q, args...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

// tick is a strictly increasing timestamp.
func (e *env) tick() string {
	e.ticks++
	return store.FormatTime(epoch.Add(time.Duration(e.ticks) * time.Minute))
}

// take inserts a take; root "" makes it a lineage root.
func (e *env) take(root string) string {
	e.t.Helper()
	e.n++
	id := fmt.Sprintf("01900000-0000-7000-8000-%012d", e.n)
	if root == "" {
		root = id
	}
	e.exec(`INSERT INTO tracks(id, slug, title, recipe_id, root_track_id, generation, status, status_set_by, origin, duration_s, sample_rate, bits_per_sample,
		channels, total_samples, master_path, master_sha256, master_bytes, source_sha256, frames_sha256, audio_md5, import_rule, recorded_at, created_at)
		VALUES (?, ?, ?, 'rec', ?, 0, 'draft', 'import', 'job', 30, 44100, 16, 2, ?, ?, ?, 1, 's', ?, 'm', 'job', ?, ?)`,
		id, fmt.Sprintf("take-%d", e.n), fmt.Sprintf("Take %d", e.n), root, e.n, fmt.Sprintf("m/%d.flac", e.n), fmt.Sprintf("sha%d", e.n),
		fmt.Sprintf("f%d", e.n), store.FormatTime(epoch), store.FormatTime(epoch))
	return id
}

func (e *env) ownerStars(track string, stars int) {
	e.t.Helper()
	e.rating(track, "human", "owner", "human", "human:alice", "alice", stars)
}

func (e *env) bobStars(track string, stars int) {
	e.t.Helper()
	e.rating(track, "human", "bob", "human", "human:bob", "bob", stars)
}

func (e *env) agentStars(track, tok string, stars int) {
	e.t.Helper()
	e.rating(track, "agent", "critic", "agent", "agent:"+tok, tok, stars)
}

func (e *env) rating(track, source, rater, kind, pid, name string, stars int) {
	e.t.Helper()
	ts := e.tick()
	e.exec(`INSERT INTO ratings(track_id, source, rater, stars, principal_kind, principal_id, principal_name, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(track_id, source, rater) DO UPDATE SET stars = excluded.stars, principal_kind = excluded.principal_kind,
		principal_id = excluded.principal_id, principal_name = excluded.principal_name, updated_at = excluded.updated_at`,
		track, source, rater, stars, kind, pid, name, ts, ts)
}

// analysis stores a Tier A row.
func (e *env) analysis(track, analyzer, version, json string) {
	e.t.Helper()
	e.exec(`INSERT INTO analyses(track_id, analyzer, version, tier, metrics_json, created_at) VALUES (?,?,?, 'A', ?, ?)`,
		track, analyzer, version, json, e.tick())
}

// duel stores a duel of a (slot A) and b (slot B), unswapped.
func (e *env) duel(a, b string, blind bool) string {
	e.t.Helper()
	e.n++
	id := fmt.Sprintf("duel-%d", e.n)
	bl := 0
	if blind {
		bl = 1
	}
	e.exec(`INSERT INTO duels(id, slot_a_track_id, slot_b_track_id, swapped, blind, from_ms, to_ms, clip_def, tool_version, match, gain_a_cdb, gain_b_cdb,
		created_by, created_by_kind, created_at, expires_at) VALUES (?,?,?,0,?,0,1000,'mm-clip/1','ffmpeg x','off',0,0,'human:alice','human',?,?)`,
		id, a, b, bl, e.tick(), store.FormatTime(epoch.Add(24*time.Hour)))
	return id
}

// verdict records a verdict on a duel: source human (the Owner) or critic with the given principal.
func (e *env) verdict(duel, a, b, source, pid, verdict string, sawReveal bool) {
	e.t.Helper()
	e.n++
	var win any
	switch verdict {
	case "a":
		win = a
	case "b":
		win = b
	}
	kind := "human"
	if source == "critic" {
		kind = "agent"
	}
	sr := 0
	if sawReveal {
		sr = 1
	}
	e.exec(`INSERT INTO duel_verdicts(id, duel_id, source, principal_kind, principal_id, verdict, winner_track_id, saw_reveal, created_at)
		VALUES (?,?,?,?,?,?,?,?,?)`, fmt.Sprintf("v-%d", e.n), duel, source, kind, pid, verdict, win, sr, e.tick())
}

func (e *env) bench(q criticbench.Query) *criticbench.Bench {
	e.t.Helper()
	b, err := e.svc.Bench(context.Background(), q)
	if err != nil {
		e.t.Fatal(err)
	}
	return b
}

func find(b *criticbench.Bench, id string) *criticbench.Critic {
	for i := range b.Critics {
		if b.Critics[i].ID == id {
			return &b.Critics[i]
		}
	}
	return nil
}

// lineages rates `lines` lineages of `per` takes each (stars 1..per) as the Owner and returns the takes by lineage.
func (e *env) lineages(lines, per int) [][]string {
	e.t.Helper()
	var out [][]string
	for l := 0; l < lines; l++ {
		var ids []string
		root := ""
		for k := 0; k < per; k++ {
			id := e.take(root)
			if k == 0 {
				root = id
			}
			e.ownerStars(id, k+1)
			ids = append(ids, id)
		}
		out = append(out, ids)
	}
	return out
}
