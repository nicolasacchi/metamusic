package acceptance_test

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/config"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/clean/cleantest"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// The U1-INT-B canaries: synthetic text planted in every private place, so a leak shows as a substring anywhere in a body.
const (
	canaryNote    = "CANARY-NOTE-900"
	canaryMuse    = "CANARY-MUSE-900"
	canaryVerdict = "CANARY-VERDICT-900"
	canaryLiner   = "CANARY-LINER-900"
)

// intbSecret is one value that must never reach a role that may not see it. Num matches as a whole number token (a seed or
// a money amount), so a longer number that merely contains it is not a hit.
type intbSecret struct {
	Kind, Value string
	Num         bool
	Word        bool     // match as a whole word (a short name)
	Ops         []string // when set, the value is secret only in the answers of these operations
}

// intbWorld is the fixture library of the sweeps: the L1 to L3 fixtures, masters and mp3_320 files that carry every kind of
// hidden metadata, and Owner-side private state of every kind (comments, Muse text, other users' ratings, a Record liner
// with a built tape, discarded takes, a Song whose takes are all discarded, smart Crates with hidden filters).
type intbWorld struct {
	R               *servertest.Rig
	Own, Bob, Carol *servertest.Client
	Taint           map[string]cleantest.Taint

	Discarded    []string // every discarded take id
	Visible      []string // take ids a Listener may open
	GoneSong     string   // the Song whose takes are all discarded
	Songs        []string
	Records      []string // published, visible Records
	Draft        string   // a draft Record
	Playlists    []string
	Crates       []string
	SmartBad     string // a smart Crate (Songs) whose filter holds a hidden filter
	SmartBadTake string // the same for Takes
	Tape         string // a ready tape of a published Record built with the liner
	LinerDesc    string // the DESCRIPTION text the Owner's tape carries
	TapeNoLiner  string // a ready tape of the same Record built without the liner
	BobBatch     string // the batch the Editor requested: its own cost is the Editor's to see (C310.5)
}

func intbSQLStrings(tb testing.TB, r *servertest.Rig, q string, args ...any) []string {
	tb.Helper()
	rows, err := r.Store.R.Query(q, args...)
	if err != nil {
		tb.Fatalf("%s: %v", q, err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s sql.NullString
		if err := rows.Scan(&s); err != nil {
			tb.Fatal(err)
		}
		if s.Valid {
			out = append(out, s.String)
		}
	}
	return out
}

// newIntbWorld builds the world once per test (each sweep gets its own rig: a sweep revokes its devices).
func newIntbWorld(t *testing.T) *intbWorld {
	t.Helper()
	if testing.Short() {
		t.Skip("heavy acceptance test")
	}
	r := servertest.New(t, servertest.Options{Backend: "fake", Fixture: true, M2c: true, M3: true, M4a: true, L1: true, L2: true, L3: true, RealFeatures: true, Files: true})
	root, err := os.OpenRoot(r.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	w := &intbWorld{R: r, Own: r.Human(t), Bob: r.User(t, servertest.UserEditor, config.RoleEditor), Carol: r.User(t, servertest.UserListener, config.RoleListener)}
	w.Taint = cleantest.Rewrite(t, r.Store, root, lt.TrackTwinA, lt.TrackTwinB, lt.TrackRefFlac, lt.TrackLossy, lt.TrackLuna, lt.TrackCitta, lt.TrackShort)
	// the Editor's own generation request: awaiting the Owner, with its own estimate and cost
	est := obj(w.Bob.OK(t, "POST", "/api/generations/estimate", map[string]any{"items": []map[string]any{{"mode": "new_seed", "parent_track_id": lt.TrackLuna, "n_seeds": 1}}}))
	bb := obj(w.Bob.OK(t, "POST", "/api/generations", map[string]any{"confirmation_token": est["confirmation_token"], "rationale": "bob asks"}, "Idempotency-Key", "intb-bob-batch"))
	w.BobBatch = bb["id"].(string)
	// recipes carry short seeds: give each a distinctive one so the value sweep can tell a leaked seed from any other number
	if _, err := r.Store.W.Exec(`DROP TRIGGER IF EXISTS recipes_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Store.W.Exec(`UPDATE recipes SET seed = 700000000 + rowid, request_json = json_set(request_json, '$.seed', 700000000 + rowid)`); err != nil {
		t.Fatal(err)
	}
	r.Settle(t)
	own := w.Own

	// the Owner's opt-ins that put Muse names into tape files and Subsonic groupings
	own.OK(t, "PUT", "/human/api/curation/settings", map[string]any{"export_muse_names": true, "subsonic_muse_names": true})

	// Muse text, assigned to Songs
	w.Songs = intbSQLStrings(t, r, `SELECT id FROM songs ORDER BY id`)
	mu := obj(own.OK(t, "POST", "/api/muses", map[string]any{"kind": "brief", "title": canaryMuse + " title", "brief": canaryMuse + " brief text"}, "Idempotency-Key", "intb-muse"))
	own.OK(t, "POST", "/api/muses/"+mu["id"].(string)+"/songs", map[string]any{"song_ids": w.Songs[:min(6, len(w.Songs))]}, "Idempotency-Key", "intb-muse-assign")

	// private notes and other users' ratings, with canary verdicts
	for i, id := range []string{lt.TrackLuna, lt.TrackCitta, lt.TrackTwinA, lt.TrackRefFlac} {
		own.OK(t, "POST", "/api/tracks/"+id+"/comments", map[string]any{"body": fmt.Sprintf("%s comment %d", canaryNote, i)}, "Idempotency-Key", fmt.Sprintf("intb-comment-%d", i))
		own.OK(t, "PUT", "/api/tracks/"+id+"/rating", map[string]any{"stars": 4, "verdict": "owner " + canaryVerdict})
		w.Bob.OK(t, "PUT", "/api/tracks/"+id+"/rating", map[string]any{"stars": 2, "verdict": "bob " + canaryVerdict})
		w.Carol.OK(t, "PUT", "/api/tracks/"+id+"/rating", map[string]any{"stars": 5, "verdict": "carol " + canaryVerdict})
	}

	// discarded takes: one inside a Song that keeps others, and every take of one Song
	if err := r.Store.R.QueryRow(`SELECT song_id FROM take_songs WHERE track_id = ?`, lt.TrackJobChild).Scan(&w.GoneSong); err != nil {
		t.Fatal(err)
	}
	for _, id := range intbSQLStrings(t, r, `SELECT track_id FROM take_songs WHERE song_id = ?`, w.GoneSong) {
		own.OK(t, "PATCH", "/api/tracks/"+id, map[string]any{"status": "discarded"})
	}
	own.OK(t, "PATCH", "/api/tracks/"+lt.TrackTwinB, map[string]any{"status": "discarded"})
	r.Settle(t)
	w.Discarded = intbSQLStrings(t, r, `SELECT id FROM tracks WHERE status = 'discarded' ORDER BY id`)
	w.Visible = intbSQLStrings(t, r, `SELECT id FROM tracks WHERE status <> 'discarded' ORDER BY id`)

	// Records: a liner canary on a published Record, a built tape, a draft; Playlists and Crates, one smart with a hidden filter
	w.Records = intbSQLStrings(t, r, `SELECT id FROM records WHERE status = 'published' AND hidden_at IS NULL ORDER BY id`)
	w.Draft = intbSQLStrings(t, r, `SELECT id FROM records WHERE status = 'draft' ORDER BY id LIMIT 1`)[0]
	rec := obj(own.OK(t, "GET", "/api/records/"+w.Records[0], nil))
	own.OK(t, "PATCH", "/api/records/"+w.Records[0], map[string]any{"revision": obj(rec["record"])["revision"], "liner_note": canaryLiner + " liner note"})
	tp := obj(own.OK(t, "POST", "/api/records/"+w.Records[0]+"/tapes", map[string]any{"include_liner": true, "accept_warnings": true}, "Idempotency-Key", "intb-tape"))
	w.Tape = tp["id"].(string)
	for r.Tapes.RunOne(t) {
	}
	if d := obj(obj(own.OK(t, "GET", "/api/tapes/"+w.Tape, nil))["download"]); d == nil {
		t.Fatalf("setup: the tape is not ready")
	}
	var tapePath string
	if err := r.Store.R.QueryRow(`SELECT path FROM tapes WHERE id = ?`, w.Tape).Scan(&tapePath); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(r.DataDir + "/" + tapePath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := flacmeta.Read(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range info.Comments {
		if v, ok := strings.CutPrefix(c, "DESCRIPTION="); ok {
			w.LinerDesc = v
		}
	}
	if w.LinerDesc == "" {
		t.Fatalf("setup: the Owner's tape carries no DESCRIPTION: %q", info.Comments)
	}
	tp2 := obj(own.OK(t, "POST", "/api/records/"+w.Records[0]+"/tapes", map[string]any{"include_liner": false, "accept_warnings": true}, "Idempotency-Key", "intb-tape-2"))
	w.TapeNoLiner = tp2["id"].(string)
	for r.Tapes.RunOne(t) {
	}
	w.Playlists = intbSQLStrings(t, r, `SELECT id FROM playlists ORDER BY id`)
	w.Crates = intbSQLStrings(t, r, `SELECT id FROM crates ORDER BY id`)
	c := obj(own.OK(t, "POST", "/api/crates", map[string]any{"name": "hidden-filter-crate", "kind": "smart", "subject": "song", "filter": map[string]any{"angle": "variant-a"}}, "Idempotency-Key", "intb-smart"))
	if id := obj(c["crate"])["id"]; id != nil {
		w.SmartBad = id.(string)
	} else {
		w.SmartBad = c["id"].(string)
	}
	ct := obj(own.OK(t, "POST", "/api/crates", map[string]any{"name": "hidden-filter-take-crate", "kind": "smart", "subject": "take", "filter": map[string]any{"has_open_comments": true}}, "Idempotency-Key", "intb-smart-take"))
	if id := obj(ct["crate"])["id"]; id != nil {
		w.SmartBadTake = id.(string)
	} else {
		w.SmartBadTake = ct["id"].(string)
	}
	w.Crates = append(w.Crates, w.SmartBad, w.SmartBadTake)
	return w
}

// secrets collects every value a Listener (and, for the money and Owner-private kinds, an Editor) must never be shown,
// from the database, so a value the shaping rules forgot still fails the sweep.
func (w *intbWorld) secrets(tb testing.TB) (listener, editor []intbSecret) {
	tb.Helper()
	addOps := func(dst *[]intbSecret, kind string, num bool, ops []string, vals ...string) {
		for _, v := range vals {
			if num && len(v) < 5 || !num && len(v) < 6 { // a short value cannot be told from ordinary text; its key is banned instead
				continue
			}
			*dst = append(*dst, intbSecret{Kind: kind, Value: v, Num: num, Ops: ops})
		}
	}
	add := func(dst *[]intbSecret, kind string, num bool, vals ...string) { addOps(dst, kind, num, nil, vals...) }
	q := func(q string) []string { return intbSQLStrings(tb, w.R, q) }
	// money: every integer micro-USD column of every table, whatever it is named
	var money []string
	for _, tbl := range q(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`) {
		for _, col := range q(`SELECT name FROM pragma_table_info('` + tbl + `') WHERE name LIKE '%micro_usd'`) {
			money = append(money, q(`SELECT DISTINCT CAST(`+col+` AS TEXT) FROM `+tbl+` WHERE `+col+` IS NOT NULL AND `+col+` >= 10000`)...)
		}
	}
	// a money amount that equals an ordinary number of the library (a bandwidth, a duration) cannot be told apart by value:
	// the key rules cover it, the value sweep keeps the amounts nobody else shares
	ordinary := map[string]bool{}
	for _, tbl := range q(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name NOT LIKE '%fts%'`) {
		for _, col := range q(`SELECT name FROM pragma_table_info('` + tbl + `') WHERE name NOT LIKE '%micro_usd' AND (type LIKE 'INT%' OR type = 'REAL')`) {
			for _, v := range q(`SELECT DISTINCT CAST(CAST(` + col + ` AS INTEGER) AS TEXT) FROM ` + tbl + ` WHERE ` + col + ` IS NOT NULL`) {
				ordinary[v] = true
			}
		}
	}
	money = slices.DeleteFunc(money, func(v string) bool { return ordinary[v] })
	// the Editor's own batch is its to see (C310.5): its amounts are not secret to it
	var own []string
	for _, qq := range []string{
		`SELECT CAST(%s AS TEXT) FROM batches WHERE id = '` + w.BobBatch + `'`,
		`SELECT CAST(%s AS TEXT) FROM jobs WHERE batch_id = '` + w.BobBatch + `'`,
		`SELECT CAST(%s AS TEXT) FROM estimates WHERE id IN (SELECT estimate_id FROM batches WHERE id = '` + w.BobBatch + `')`,
	} {
		tbl := qq[strings.Index(qq, "FROM ")+5:]
		tbl = tbl[:strings.Index(tbl, " ")]
		for _, col := range q(`SELECT name FROM pragma_table_info('` + tbl + `') WHERE name LIKE '%micro_usd'`) {
			own = append(own, q(fmt.Sprintf(qq, col))...)
		}
	}
	editorMoney := slices.DeleteFunc(slices.Clone(money), func(v string) bool { return slices.Contains(own, v) })
	add(&editor, "money", true, editorMoney...)
	add(&listener, "money", true, money...)
	// hashes and ids that identify a master or a request
	hashes := append(q(`SELECT master_sha256 FROM tracks`), q(`SELECT sha256 FROM renditions WHERE kind NOT LIKE '%clean'`)...)
	hashes = append(hashes, q(`SELECT audio_md5 FROM tracks WHERE audio_md5 <> '00000000000000000000000000000000'`)...)
	hashes = append(hashes, q(`SELECT hash FROM recipes`)...)
	hashes = append(hashes, q(`SELECT core_hash FROM recipes`)...)
	hashes = append(hashes, q(`SELECT frames_sha256 FROM tracks`)...)
	hashes = append(hashes, q(`SELECT source_sha256 FROM tracks`)...)
	add(&listener, "hash", false, hashes...)
	add(&listener, "master path", false, q(`SELECT master_path FROM tracks`)...)
	add(&listener, "hidden tag marker", false, cleantest.Marker)
	add(&listener, "seed", true, q(`SELECT CAST(seed AS TEXT) FROM recipes`)...)
	add(&listener, "request id", false, q(`SELECT request_id FROM recipes`)...)
	add(&listener, "recipe style", false, q(`SELECT style FROM recipes`)...)
	add(&listener, "job id", false, q(`SELECT id FROM jobs`)...)
	add(&listener, "batch id", false, q(`SELECT id FROM batches`)...)
	add(&listener, "discarded take", false, w.Discarded...)
	// Muse text and ids
	add(&listener, "muse id", false, q(`SELECT id FROM muses`)...)
	add(&listener, "muse text", false, q(`SELECT title FROM muses`)...)
	add(&listener, "muse text", false, q(`SELECT artist FROM muses`)...)
	add(&listener, "muse text", false, q(`SELECT brief FROM muses`)...)
	add(&listener, "muse canary", false, canaryMuse)
	// private notes
	add(&listener, "comment", false, q(`SELECT body FROM comments`)...)
	add(&listener, "note canary", false, canaryNote)
	add(&listener, "liner", false, canaryLiner, w.LinerDesc)
	add(&listener, "draft", false, q(`SELECT id FROM recipe_drafts`)...)
	add(&listener, "session", false, q(`SELECT id FROM sessions`)...)
	// other users' identity and verdicts: the Owner's and the Editor's names, and their verdicts
	for _, n := range []string{"alice", "bob"} {
		listener = append(listener, intbSecret{Kind: "username", Value: n, Word: true})
	}
	add(&listener, "other verdict", false, "owner "+canaryVerdict, "bob "+canaryVerdict)
	// the Editor must not see the Owner's private state either: other users' verdicts are Owner-only, the Muse text is Owner-side
	add(&editor, "other verdict", false, "owner "+canaryVerdict, "carol "+canaryVerdict)
	// the Editor reads recipes and Muses, but the context and stats blocks carry no Muse text for it (C305, Revision 13)
	ctxOps := []string{"get_track_context", "get_stats"}
	addOps(&editor, "muse text", false, ctxOps, q(`SELECT title FROM muses`)...)
	addOps(&editor, "muse text", false, ctxOps, q(`SELECT brief FROM muses`)...)
	addOps(&editor, "muse canary", false, ctxOps, canaryMuse)
	return listener, editor
}
