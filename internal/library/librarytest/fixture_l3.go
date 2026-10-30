package librarytest

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/muses"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// The L3 fixture (C146.1) is SQL-only on top of FixtureL2: no service of L3 exists yet and the Reconciler has not run (the
// queue is as the triggers left it; a Settle derives Sounds, and later the features). It adds Muse M5 with the Songs
// T1-T8, Muse M3 with three draft Songs, Muse M4 with a one-take Song and a draft Song, and a Muse-less draft Song d4.
// Every text is synthetic. The expected feature rows of these Songs are Go literals (L3TakeFeatures, L3SongFeatures).

// l3ID is a fixed id of an L3 row; the kinds are spaced 0x1000 apart (the range 0xd3xxxx is free of every other fixture).
func l3ID(space, n int) string {
	return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xd30000+space*0x1000+n)
}

const (
	l3SpTake = iota + 1
	l3SpSong
	l3SpMuse
	l3SpEvent
	l3SpRecipe
	l3SpDraft
	l3Spaces
)

// The synthetic Muse text of the L3 fixture.
const (
	L3Muse3Title  = "Synthetic Agent Muse 3"
	L3Muse3Artist = "Synthetic Agent Artist 3"
	L3Muse4Title  = "Synthetic Agent Muse 4"
	L3Muse4Artist = "Synthetic Agent Artist 4"
	L3Muse5Title  = "Synthetic Agent Muse 5"
	L3Muse5Artist = "Synthetic Agent Artist 5"
)

// L3Info lists the L3 rows by their C146.1 labels, on top of L2Info (whose maps also hold them): Muses "M1".."M5", Songs
// "S1".."S7", "T1".."T8", "u1", "u2", "d1".."d4", Takes the L2 labels plus "T1".."T8" and "u1".
type L3Info struct {
	L2Info
	// Drafts3 maps "d1".."d4" and "u2" to the id of the draft of that Song.
	Drafts3 map[string]string
}

// l3Take is one rendered take of the L3 world: its Song label, style, measured analyses and human stars.
type l3Take struct {
	label, style string
	muse         string  // "M5" or "M4"
	stars        int     // 0 = unrated
	bpm          float64 // measured tempo; 0 = no Ears analysis at all
	camelot      string
	energy       float64
	lufs         float64
	dur          float64
	age          time.Duration
}

// l3Takes is the take table: T1-T6 are exactly the six-Song set of the sequencer fixture (tempo, key, energy), T7 and T8 are
// the rest of Muse M5, u1 has no Ears measurement. The styles declare the measured tempo, so no Song has a tempo mismatch.
var l3Takes = []l3Take{
	{"T1", "reggae, chill, male vocal, 96bpm", "M5", 5, 96, "8A", 0.30, -14.0, 200, 3 * time.Hour},
	{"T2", "reggae, upbeat, female vocal, 98bpm", "M5", 4, 98, "8A", 0.45, -14.5, 205, 3*time.Hour + 5*time.Minute},
	{"T3", "pop, warm, female vocal, 100bpm", "M5", 5, 100, "9A", 0.55, -13.8, 210, 3*time.Hour + 10*time.Minute},
	{"T4", "pop, energetic, male vocal, 104bpm", "M5", 4, 104, "9A", 0.70, -13.0, 215, 3*time.Hour + 15*time.Minute},
	{"T5", "dance, energetic, female vocal, 108bpm", "M5", 5, 108, "9B", 0.90, -11.5, 220, 3*time.Hour + 20*time.Minute},
	{"T6", "funk, upbeat, male vocal, 102bpm", "M5", 4, 102, "8B", 0.40, -14.2, 225, 3*time.Hour + 25*time.Minute},
	{"T7", "jazz, 80s, mellow, male vocal, 90bpm", "M5", 3, 90, "5A", 0.35, -15.0, 230, 3*time.Hour + 30*time.Minute},
	{"T8", "rock, aggressive, male vocal, 120bpm", "M5", 0, 120, "12B", 0.80, -12.0, 235, 3*time.Hour + 35*time.Minute},
	{"u1", "pop, mellow, 100bpm", "M4", 0, 0, "", 0, -14.0, 190, 3*time.Hour + 40*time.Minute},
}

// l3Draft is one draft Song; every style carries the unmapped term `skank` (five Songs hold it).
type l3Draft struct{ label, style, muse, angle string }

var l3Drafts = []l3Draft{
	{"d1", "reggae, chill, skank, 76bpm", "M3", "remake-long"},
	{"d2", "dance, energetic, skank, 132bpm", "M3", "alt-take-long"},
	{"d3", "reggae, skank, 90bpm", "M3", "long"},
	{"u2", "synthpop, skank, 120bpm", "M4", ""},
	{"d4", "rock, dark, skank, 140bpm", "", ""},
}

// camelotKey is the table of C135.1 read backwards: Camelot code to (tonic pitch class, mode, name). It is repeated here
// on purpose: the fixture shares no code with `features`.
var camelotKey = map[string]struct {
	tonic      int
	mode, name string
}{
	"8A": {9, "minor", "A minor"}, "9A": {4, "minor", "E minor"}, "5A": {0, "minor", "C minor"},
	"9B": {7, "major", "G major"}, "8B": {0, "major", "C major"}, "12B": {4, "major", "E major"},
}

type l3Builder struct {
	tb  testing.TB
	st  *store.Store
	now time.Time
	tx  *sql.Tx
	out L3Info
	n   [l3Spaces]int
}

func (b *l3Builder) id(space int) string       { b.n[space]++; return l3ID(space, b.n[space]) }
func (b *l3Builder) ts(d time.Duration) string { return store.FormatTime(b.now.Add(-d)) }

func (b *l3Builder) put(q string, args ...any) {
	b.tb.Helper()
	if _, err := b.tx.ExecContext(context.Background(), q, args...); err != nil {
		b.tb.Fatalf("fixture L3: %v\n%s", err, q)
	}
}

func (b *l3Builder) begin() { b.tx = begin(b.tb, b.st) }
func (b *l3Builder) commit() {
	b.tb.Helper()
	check(b.tb, b.tx.Commit())
	b.tx = nil
}
func (b *l3Builder) abort() {
	if b.tx != nil {
		_ = b.tx.Rollback()
	}
}

// muses creates M3, M4 and M5 through muses.Service (the only writer of Muse rows); the ordinal trigger ranks them 3, 4, 5.
func (b *l3Builder) muses() {
	b.tb.Helper()
	svc := muses.New(b.st, func() time.Time { return b.now.Add(-9 * time.Hour) }, func() string { return b.id(l3SpMuse) })
	for _, m := range []struct{ label, title, artist string }{
		{"M3", L3Muse3Title, L3Muse3Artist}, {"M4", L3Muse4Title, L3Muse4Artist}, {"M5", L3Muse5Title, L3Muse5Artist},
	} {
		artist := m.artist
		got, err := svc.Create(context.Background(), principalCLI(), muses.NewMuse{Kind: muses.KindReference, Title: m.title, Artist: &artist})
		check(b.tb, err)
		b.out.Muses[m.label] = got.ID
	}
}

// takes inserts the nine rendered takes through the library write path (recipes, style terms, loudness analyses).
func (b *l3Builder) takes() {
	ctx := context.Background()
	svc := library.New(b.st, nil, func() time.Time { return b.now })
	svc.UseIDFunc(func() string { return b.id(l3SpRecipe) })
	for _, tk := range l3Takes {
		id := b.id(l3SpTake)
		b.out.Takes[tk.label] = id
		tx := begin(b.tb, b.st)
		title := "Synthetic Take " + tk.label
		slug := yue2.Slug(title)
		h := sha(fmt.Appendf(nil, "l3-take/%s", tk.label))
		peaks := []library.NewRendition{{Kind: "peaks", SHA256: sha([]byte("l3-peaks/" + tk.label)), Bytes: 32, Path: "derived/l3/" + h[:16] + ".peaks.json", Params: map[string]any{"format": analysis.PeaksFormat}}}
		series := make([]float64, int(tk.dur))
		for i := range series {
			series[i] = tk.lufs
		}
		nt := library.NewTake{
			ID: id, Title: title, Slug: slug, PrincipalID: "cli", RecordedAt: b.now.Add(-tk.age),
			DurationS: tk.dur, SampleRate: 48000, BitsPerSample: 24, Channels: 2, TotalSamples: int64(tk.dur * 48000),
			MasterPath: "audio/masters/" + slug + "__" + h[:8] + ".flac", MasterSHA256: h, MasterBytes: 64,
			SourceSHA256: sha([]byte("l3-src/" + tk.label)), FramesSHA256: sha([]byte("l3-frames/" + tk.label)), AudioMD5: h[:32],
			Loudness: analysis.Loudness{IntegratedLUFS: tk.lufs, LRA: 5, LRALowLUFS: tk.lufs - 4, LRAHighLUFS: tk.lufs + 1.5, TruePeakDBTP: -3, SamplePeakDBFS: -3.3,
				ShortTermMaxLUFS: tk.lufs + 1, ShortTerm1Hz: series},
			Spectrum:    analysis.Spectrum{BandwidthHz: bptrInt(16000), RefDB: -40, ThresholdDB: -110, Frames: 30},
			ToolVersion: "ffmpeg n9.0.1", ReplayGainDB: -18 - tk.lufs, Renditions: peaks,
		}
		reqID := "l3_" + tk.label
		p := mustParse(b.tb, reqJSON(reqID, tk.style, "[Verse 1]\nfixture line\n[Chorus]\nfixture hook", int64(8100+len(b.out.Takes))))
		rid, _, err := svc.InsertRecipe(ctx, tx, p, "import")
		check(b.tb, err)
		nt.RecipeID, nt.ImportRule = rid, "slug"
		check(b.tb, svc.InsertTake(ctx, tx, nt))
		check(b.tb, tx.Commit())
	}
}

// songs writes the Songs, their roots, the Pick history, the current Picks, the human stars and the Ears analyses.
func (b *l3Builder) songs() {
	b.begin()
	defer b.abort()
	system := [3]string{"system:provenance", "system", "provenance"}
	for _, tk := range l3Takes {
		sid := b.id(l3SpSong)
		b.out.Songs[tk.label] = sid
		created := b.ts(tk.age + time.Hour)
		take := b.out.Takes[tk.label]
		b.put(`INSERT INTO songs(id,root_track_id,draft_id,muse_id,muse_source,title,origin,created_by,created_at,updated_by,updated_at)
			VALUES(?,?,NULL,?,'manual',?,'root','system:provenance',?,'system:provenance',?)`, sid, take, b.out.Muses[tk.muse], "Synthetic Song "+tk.label, created, created)
		b.put(`INSERT INTO song_roots(root_track_id,song_id,via,attached_by,attached_at) VALUES(?,?,'own','system:provenance',?)`, take, sid, created)
		rec := store.FormatTime(b.now.Add(-tk.age))
		e1 := b.id(l3SpEvent)
		ev1 := fmt.Sprintf(`{"def":"mm.pick/1","rule":"newest","eligible":1,"recorded_at":%q}`, rec)
		b.put(`INSERT INTO pick_events(id,song_id,seq,at,from_take_id,to_take_id,from_source,to_source,reason,actor,actor_kind,actor_name,evidence_json)
			VALUES(?,?,1,?,NULL,?,'none','newest','initial',?,?,?,?)`, e1, sid, rec, take, system[0], system[1], system[2], ev1)
		b.put(`INSERT INTO song_picks(song_id,take_id,source,evidence_json,last_event_id,changed_at,computed_at) VALUES(?,?,'newest',?,?,?,?)`, sid, take, ev1, e1, rec, rec)
		if tk.stars > 0 { // a human rating moves the Pick to the stars rule, as a Settle would
			at := b.ts(tk.age - 30*time.Minute)
			b.put(`INSERT INTO ratings(track_id,source,stars,principal_kind,principal_id,principal_name,created_at,updated_at) VALUES(?,'human',?,'human',?,'fixture',?,?)`,
				take, tk.stars, FixtureHuman, at, at)
			e2 := b.id(l3SpEvent)
			ev2 := fmt.Sprintf(`{"def":"mm.pick/1","rule":"stars","eligible":1,"stars":%d,"tied":1}`, tk.stars)
			b.put(`INSERT INTO pick_events(id,song_id,seq,at,from_take_id,to_take_id,from_source,to_source,reason,actor,actor_kind,actor_name,evidence_json)
				VALUES(?,?,2,?,?,?,'newest','stars','stars',?,?,?,?)`, e2, sid, at, take, take, system[0], system[1], system[2], ev2)
			b.put(`UPDATE song_picks SET source='stars', evidence_json=?, last_event_id=?, changed_at=?, computed_at=? WHERE song_id=?`, ev2, e2, at, at, sid)
		}
		if tk.bpm > 0 {
			ck := camelotKey[tk.camelot]
			at := b.ts(tk.age - time.Hour)
			b.put(`INSERT INTO analyses(track_id,analyzer,version,tier,metrics_json,created_at) VALUES(?,'tempo','mm-tempo/1 ffmpeg n9.0.1','A',?,?)`,
				take, fmt.Sprintf(`{"bpm":%.1f,"confidence":3.2,"analyzed_s":%.0f}`, tk.bpm, tk.dur), at)
			b.put(`INSERT INTO analyses(track_id,analyzer,version,tier,metrics_json,created_at) VALUES(?,'key','mm-key/1 ffmpeg n9.0.1','A',?,?)`,
				take, fmt.Sprintf(`{"key":%q,"tonic":%d,"mode":%q,"camelot":%q,"r":0.82,"margin":0.11,"ambiguous_relative":false,"tuning_cents":0,"alternatives":[],"analyzed_s":%.0f}`,
					ck.name, ck.tonic, ck.mode, tk.camelot, tk.dur), at)
			b.put(`INSERT INTO analyses(track_id,analyzer,version,tier,metrics_json,created_at) VALUES(?,'energy','mm-energy/1 ffmpeg n9.0.1','A',?,?)`,
				take, fmt.Sprintf(`{"energy":%.4f,"onset_rate":2.5,"centroid_hz":1500,"lufs":%.1f,"components":{"onset":0.5,"centroid":0.5,"loudness":0.5},"analyzed_s":%.0f}`,
					tk.energy, tk.lufs, tk.dur), at)
		}
	}
	for _, d := range l3Drafts {
		sid, did := b.id(l3SpSong), b.id(l3SpDraft)
		b.out.Songs[d.label], b.out.Drafts3[d.label] = sid, did
		created := b.ts(2 * time.Hour)
		b.put(`INSERT INTO recipe_drafts(id,origin,group_key,label,request_id,request_json,status,created_at,updated_at)
			VALUES(?,'import_backlog',?,'v1',?,?,'backlog',?,?)`, did, "l3_"+d.label, "l3_"+d.label+"_v1", reqJSON("l3_"+d.label+"_v1", d.style, "[Verse 1]\nfixture draft line", int64(8200+len(b.out.Drafts3))), created, created)
		var muse, museSrc, angle, angleSrc any
		if d.muse != "" {
			muse, museSrc = b.out.Muses[d.muse], "manual"
		}
		if d.angle != "" {
			angle, angleSrc = d.angle, "manual"
		}
		b.put(`INSERT INTO songs(id,root_track_id,draft_id,muse_id,muse_source,angle,angle_source,title,origin,created_by,created_at,updated_by,updated_at)
			VALUES(?,NULL,?,?,?,?,?,?,'draft','system:provenance',?,'system:provenance',?)`, sid, did, muse, museSrc, angle, angleSrc, "Synthetic Song "+d.label, created, created)
	}
	b.commit()
}

func newL3Builder(tb testing.TB, st *store.Store, now time.Time, base L2Info) *l3Builder {
	tb.Helper()
	b := &l3Builder{tb: tb, st: st, now: now, out: L3Info{L2Info: base, Drafts3: map[string]string{}}}
	for _, m := range []*map[string]string{&b.out.Muses, &b.out.Songs, &b.out.Takes} {
		if *m == nil {
			*m = map[string]string{}
		}
	}
	return b
}

func (b *l3Builder) world() L3Info {
	b.muses()
	b.takes()
	b.songs()
	for _, tk := range l3Takes {
		b.out.Tracks = append(b.out.Tracks, b.out.Takes[tk.label])
	}
	for _, d := range l3Drafts {
		b.out.Drafts = append(b.out.Drafts, b.out.Drafts3[d.label])
	}
	return b.out
}

// FixtureL3 is FixtureL2 plus the C146.1 world (SQL-only): Muse M5 with the Songs T1-T8 (T1-T6 the strong set, one take each,
// human stars 5,4,5,4,5,4, T7 3 stars, T8 unrated; tempo, key, energy and loudness analyses), Muse M3 with the draft Songs
// d1-d3, Muse M4 with the unrated one-take Song u1 (no Ears analysis) and the draft Song u2, and the Muse-less draft Song d4;
// the term `skank` is unmapped in the styles of d1, d2, d3, u2 and d4. No Record holds T1-T8. The queue is not drained: a Settle
// changes no Pick. It needs schema 10 or later and writes no L3 table.
func FixtureL3(tb testing.TB, st *store.Store, now time.Time) L3Info {
	tb.Helper()
	return newL3Builder(tb, st, now, FixtureL2(tb, st, now)).world()
}

// ApplyL3 adds the L3 world to a library that already holds FixtureM3's and FixtureL1's rows (servertest uses it).
func ApplyL3(tb testing.TB, st *store.Store, now time.Time) {
	tb.Helper()
	b := newL3Builder(tb, st, now, L2Info{})
	b.out.Muses, b.out.Songs, b.out.Takes = map[string]string{}, map[string]string{}, map[string]string{}
	b.world()
}

// FixtureL3Upgrade builds the data at schema 10 (a store opened with store.OpenRaw and store.MigrateTo(..., 10)) for
// TestMigration0011_UpgradeFromL2: the L2 world plus the L3 world, no L3 table and no song_sounds.ord.
func FixtureL3Upgrade(tb testing.TB, st *store.Store, now time.Time) L3Info {
	tb.Helper()
	return FixtureL3(tb, st, now)
}
