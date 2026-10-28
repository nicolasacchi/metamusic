package librarytest

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/muses"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// The L2 fixture (C132.2, C132.2a) is SQL-only: no service of L2 and no Syncer writes it, and the Reconciler has not run
// (the queue is as the triggers left it; Rig.Settle drains it). It builds seven Songs S1-S7 with eight Takes on top of
// FixtureL1, the Pick state those Songs would have after a Settle, two Records and four Crates. Every text is synthetic.

// l2ID is a fixed id of an L2 row; the kinds are spaced 0x1000 apart (the range 0xd2xxxx is free of every other fixture).
func l2ID(space, n int) string {
	return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xd20000+space*0x1000+n)
}

const (
	l2SpTake = iota + 1
	l2SpSong
	l2SpMuse
	l2SpRecord
	l2SpSlot
	l2SpChange
	l2SpCrate
	l2SpItem
	l2SpEvent
	l2SpRecipe
	l2SpDraft
	l2Spaces
)

// L2Info lists the L2 rows by their C132.2a labels.
type L2Info struct {
	L1Info
	// Muses is "M1" and "M2" to Muse ids; Songs "S1".."S7" to Song ids; Takes the take labels ("s1a", "a1", "b1", "b2",
	// "c1", "c2", "e1", "s6a") to track ids; Records "R1" and "R2"; Slots "A".."E" (R1, in order) and "R2.1", "R2.2";
	// Crates "manual_song", "manual_take", "smart_song", "smart_take". S7Draft is the draft of the takes-less Song S7.
	Muses, Songs, Takes, Records, Slots, Crates map[string]string
	S7Draft                                     string
}

// The synthetic Muse text of the L2 fixture (chosen not to collide with FixtureL1's index Muses).
const (
	L2Muse1Title  = "Synthetic Curation Muse 1"
	L2Muse1Artist = "Synthetic Curation Artist 1"
	L2Muse2Title  = "Synthetic Curation Muse 2"
	L2Muse2Artist = "Synthetic Curation Artist 2"
)

// The styles of the two Sound families the Songs use, and the two smart filters.
const (
	l2Reggae = "roots reggae, laid-back groove, male vocal, 76bpm"
	l2Dance  = "eurodance, club, energetic, female vocal, 128bpm"
	// L2SmartSongFilter is the worked example of C125.5 (D-L2-36): its result set on the fixture is {S1, S6}.
	L2SmartSongFilter = `{"sound":["genre_family:reggae"],"unheard":true,"min_duration_s":240,"pick_decided":false}`
	// L2SmartTakeFilter is the stored filter of the fixture's smart Take Crate.
	L2SmartTakeFilter = `{"status":["draft"],"unheard":true}`
)

// l2CoverSeed is records.CoverSeed, repeated here so that no non-test fixture code imports a package whose own tests
// import this one (an import cycle); fixture_l2_test.go pins the equality.
func l2CoverSeed(id string, rerolls int) int64 {
	sum := sha256.Sum256([]byte("mm-cover-seed/1|" + id + "|" + strconv.Itoa(rerolls)))
	return int64(binary.BigEndian.Uint64(sum[:8]) >> 11)
}

type l2Spec struct {
	label, title, style string
	parent              string // the label of the parent take; "" for a root
	dur                 float64
	age                 time.Duration // recorded this long before now
	lufs, tp            float64
	lossy               bool
	seed                int64
}

// The eight Takes of C132.2a. A Song's root is its first Take; a child is a newer Take of the same request.
var l2Specs = []l2Spec{
	{label: "s1a", title: "Synthetic Take S1A", style: l2Reggae, dur: 300, age: 2 * time.Hour, lufs: -14.0, tp: -3.0, seed: 7101},
	{label: "a1", title: "Synthetic Take A1", style: l2Reggae, dur: 300, age: 3 * time.Hour, lufs: -15.5, tp: -2.0, seed: 7102},
	{label: "b2", title: "Synthetic Take B2", style: l2Reggae, dur: 180, age: 4 * time.Hour, lufs: -14.2, tp: -3.0, seed: 7103},
	{label: "b1", title: "Synthetic Take B1", style: l2Reggae, parent: "b2", dur: 180, age: 3*time.Hour + 30*time.Minute, lufs: -13.0, tp: -1.5, seed: 7104},
	{label: "c1", title: "Synthetic Take C1", style: l2Dance, dur: 300, age: 5 * time.Hour, lufs: -14.5, tp: -3.0, seed: 7105},
	{label: "c2", title: "Synthetic Take C2", style: l2Dance, parent: "c1", dur: 240, age: 4*time.Hour + 30*time.Minute, lufs: -11.8, tp: 0.6, seed: 7106},
	{label: "e1", title: "Synthetic Take E1", dur: 300, age: 6 * time.Hour, lufs: -14.0, tp: -3.0, lossy: true, seed: 7107},
	{label: "s6a", title: "Synthetic Take S6A", style: l2Reggae, dur: 240, age: time.Hour, lufs: -14.0, tp: -3.0, seed: 7108},
}

// l2Event is one pick_events row of a Song; the first row of a Song has no from.
type l2Event struct {
	to, source, reason string
	age                time.Duration // before now
	actor              [3]string     // actor, kind, name
	evidence           string
}

type l2Song struct {
	label, title string
	muse         string // "M1", "M2" or ""
	takes        []string
	events       []l2Event
	pinned       string // the pinned take label
}

var l2System = [3]string{"system:provenance", "system", "provenance"}

func l2Newest(eligible int, at time.Time) string {
	return fmt.Sprintf(`{"def":"mm.pick/1","rule":"newest","eligible":%d,"recorded_at":%q}`, eligible, store.FormatTime(at))
}

// l2Songs is the Song table of C132.2a with the Pick history each Song would have after a Settle.
func l2Songs(now time.Time) []l2Song {
	rec := func(label string) time.Time {
		for _, s := range l2Specs {
			if s.label == label {
				return now.Add(-s.age)
			}
		}
		panic("unknown take " + label)
	}
	human := [3]string{FixtureHuman, "human", "fixture"}
	return []l2Song{
		{label: "S1", title: "Synthetic Song S1", muse: "M1", takes: []string{"s1a"}, events: []l2Event{
			{"s1a", "newest", "initial", 2 * time.Hour, l2System, l2Newest(1, rec("s1a"))}}},
		{label: "S2", title: "Synthetic Song S2", muse: "M1", takes: []string{"a1"}, events: []l2Event{
			{"a1", "newest", "initial", 3 * time.Hour, l2System, l2Newest(1, rec("a1"))}}},
		{label: "S3", title: "Synthetic Song S3", muse: "M1", takes: []string{"b2", "b1"}, events: []l2Event{
			{"b2", "newest", "initial", 4 * time.Hour, l2System, l2Newest(1, rec("b2"))},
			{"b1", "newest", "newest", 3*time.Hour + 30*time.Minute, l2System, l2Newest(2, rec("b1"))}}},
		{label: "S4", title: "Synthetic Song S4", muse: "M2", takes: []string{"c1", "c2"}, events: []l2Event{
			{"c1", "newest", "initial", 5 * time.Hour, l2System, l2Newest(1, rec("c1"))},
			{"c2", "newest", "newest", 4*time.Hour + 30*time.Minute, l2System, l2Newest(2, rec("c2"))},
			{"c1", "stars", "stars", 90 * time.Minute, l2System, `{"def":"mm.pick/1","rule":"stars","eligible":2,"stars":4,"tied":1}`}}},
		{label: "S5", title: "Synthetic Song S5", muse: "M2", takes: []string{"e1"}, pinned: "e1", events: []l2Event{
			{"e1", "newest", "initial", 6 * time.Hour, l2System, l2Newest(1, rec("e1"))},
			{"e1", "pinned", "pin", 100 * time.Minute, human, ""}}},
		{label: "S6", title: "Synthetic Song S6", takes: []string{"s6a"}, events: []l2Event{
			{"s6a", "newest", "initial", time.Hour, l2System, l2Newest(1, rec("s6a"))},
			{"s6a", "stars", "stars", 80 * time.Minute, l2System, `{"def":"mm.pick/1","rule":"stars","eligible":1,"stars":3,"tied":1}`}}},
		{label: "S7", title: "Synthetic Song S7"}, // a takes-less draft Song
	}
}

// l2Builder writes the L2 rows through SQL in one transaction.
type l2Builder struct {
	tb  testing.TB
	st  *store.Store
	now time.Time
	tx  *sql.Tx
	out L2Info
	n   [l2Spaces]int
}

func newL2Builder(tb testing.TB, st *store.Store, now time.Time) *l2Builder {
	return &l2Builder{tb: tb, st: st, now: now, out: L2Info{Muses: map[string]string{}, Songs: map[string]string{}, Takes: map[string]string{},
		Records: map[string]string{}, Slots: map[string]string{}, Crates: map[string]string{}}}
}

func (b *l2Builder) id(space int) string       { b.n[space]++; return l2ID(space, b.n[space]) }
func (b *l2Builder) ts(d time.Duration) string { return store.FormatTime(b.now.Add(-d)) }

// put runs one statement of the builder's transaction.
func (b *l2Builder) put(q string, args ...any) {
	b.tb.Helper()
	if _, err := b.tx.ExecContext(context.Background(), q, args...); err != nil {
		b.tb.Fatalf("fixture L2: %v\n%s", err, q)
	}
}

// begin opens the builder's transaction; commit ends it; abort is the deferred safety net.
func (b *l2Builder) begin() { b.tx = begin(b.tb, b.st) }
func (b *l2Builder) commit() {
	b.tb.Helper()
	check(b.tb, b.tx.Commit())
	b.tx = nil
}
func (b *l2Builder) abort() {
	if b.tx != nil {
		_ = b.tx.Rollback()
	}
}

func (b *l2Builder) requireCuration() {
	b.tb.Helper()
	var n int
	check(b.tb, b.st.R.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='records'`).Scan(&n))
	if n != 1 {
		b.tb.Fatal("fixture L2: the schema has no records table (migration 0010 is missing)")
	}
}

// muse creates a reference Muse through muses.Service, the only package that writes Muse rows (the privacy gate allows no
// other file to name that table); the ordinal column, if the schema has it, is assigned by its trigger. It must run outside
// the builder's transaction (the writer pool holds one connection).
func (b *l2Builder) muse(label, title string, artist *string) {
	b.tb.Helper()
	svc := muses.New(b.st, func() time.Time { return b.now.Add(-10 * time.Hour) }, func() string { return b.id(l2SpMuse) })
	m, err := svc.Create(context.Background(), principalCLI(), muses.NewMuse{Kind: muses.KindReference, Title: title, Artist: artist})
	check(b.tb, err)
	b.out.Muses[label] = m.ID
}

// takes inserts the eight Takes through the library write path (recipes, style terms, loudness analyses).
func (b *l2Builder) takes() {
	ctx := context.Background()
	svc := library.New(b.st, nil, func() time.Time { return b.now })
	rn := 0
	svc.UseIDFunc(func() string { rn++; return l2ID(l2SpRecipe, rn) })
	for _, sp := range l2Specs {
		id := b.id(l2SpTake)
		b.out.Takes[sp.label] = id
		tx := begin(b.tb, b.st)
		slug := yue2.Slug(sp.title)
		h := sha(fmt.Appendf(nil, "l2-take/%s", sp.label))
		peaks := []library.NewRendition{{Kind: "peaks", SHA256: sha([]byte("l2-peaks/" + sp.label)), Bytes: 32, Path: "derived/l2/" + h[:16] + ".peaks.json", Params: map[string]any{"format": analysis.PeaksFormat}}}
		nt := library.NewTake{
			ID: id, Title: sp.title, Slug: slug, PrincipalID: "cli", RecordedAt: b.now.Add(-sp.age),
			DurationS: sp.dur, SampleRate: 48000, BitsPerSample: 24, Channels: 2, TotalSamples: int64(sp.dur * 48000),
			MasterPath: "audio/masters/" + slug + "__" + h[:8] + ".flac", MasterSHA256: h, MasterBytes: 64,
			SourceSHA256: sha([]byte("l2-src/" + sp.label)), FramesSHA256: sha([]byte("l2-frames/" + sp.label)), AudioMD5: h[:32],
			Loudness:    l2Loudness(sp),
			Spectrum:    analysis.Spectrum{BandwidthHz: bptrInt(16000), RefDB: -40, ThresholdDB: -110, Frames: 30},
			ToolVersion: "ffmpeg n9.0.1", ReplayGainDB: -18 - sp.lufs, Renditions: peaks,
		}
		if sp.lossy {
			// a lossy take is a reference take: a stub recipe, the nominal 16 bits and equal hashes
			audioKey := "mp3:l2-" + sp.label
			rid, _, err := svc.InsertReferenceRecipe(ctx, tx, library.NewReferenceRecipe{Title: sp.title, Slug: slug, AudioKey: audioKey})
			check(b.tb, err)
			lh := sha([]byte(audioKey))
			nt.RecipeID, nt.Reference, nt.ImportRule, nt.Lossy = rid, true, library.RuleReference, true
			nt.MasterPath, nt.MasterSHA256, nt.MasterBytes = "audio/masters/"+slug+"__"+lh[:8]+".mp3", lh, 64
			nt.SourceSHA256, nt.FramesSHA256, nt.AudioMD5 = lh, lh, "00000000000000000000000000000000"
			nt.SampleRate, nt.BitsPerSample, nt.TotalSamples = 44100, 16, int64(sp.dur*44100)
		} else {
			reqID := "l2_" + sp.label
			if sp.parent != "" {
				reqID = "l2_" + sp.parent // a child shares its root's request id (an inferred sibling)
			}
			p := mustParse(b.tb, reqJSON(reqID, sp.style, "[Verse 1]\nfixture line\n[Chorus]\nfixture hook", sp.seed))
			rid, _, err := svc.InsertRecipe(ctx, tx, p, "import")
			check(b.tb, err)
			nt.RecipeID, nt.ImportRule = rid, "slug"
			if sp.parent != "" {
				root := b.out.Takes[sp.parent]
				nt.ParentTrackID, nt.RootTrackID, nt.Generation = root, root, 1
				nt.Mutation = &library.NewMutation{Operator: "inferred_sibling", Inferred: true, Rule: "same_request_id"}
			}
		}
		check(b.tb, svc.InsertTake(ctx, tx, nt))
		check(b.tb, tx.Commit())
	}
}

// l2Loudness is the synthetic loudness of a take: its integrated value, a slowly varying 1 Hz short-term series around it
// (0.1 LU steps, one value per whole second) and the table's true peak.
func l2Loudness(sp l2Spec) analysis.Loudness {
	series := make([]float64, int(sp.dur))
	for i := range series {
		series[i] = math.Round((sp.lufs+float64((i*7)%11-5)/10)*10) / 10
	}
	return analysis.Loudness{IntegratedLUFS: sp.lufs, LRA: 5, LRALowLUFS: sp.lufs - 4, LRAHighLUFS: sp.lufs + 1.5, TruePeakDBTP: sp.tp,
		SamplePeakDBFS: sp.tp - 0.3, ShortTermMaxLUFS: sp.lufs + 1, ShortTerm1Hz: series}
}

// songs writes the Songs, their roots, the Pick history, the current Picks and the human facts the Picks rest on.
func (b *l2Builder) songs() {
	b.begin()
	defer b.abort()
	for _, s := range l2Songs(b.now) {
		sid := b.id(l2SpSong)
		b.out.Songs[s.label] = sid
		created := b.ts(7 * time.Hour)
		var muse, museSrc any
		if s.muse != "" {
			muse, museSrc = b.out.Muses[s.muse], "manual"
		}
		if len(s.takes) == 0 { // S7: the Song of an unfulfilled draft, declared as reggae
			did := b.id(l2SpDraft)
			b.out.S7Draft = did
			b.put(`INSERT INTO recipe_drafts(id,origin,group_key,label,request_id,request_json,status,created_at,updated_at)
				VALUES(?,'import_backlog','l2_s7','v1','l2_s7_v1',?,'backlog',?,?)`, did, reqJSON("l2_s7_v1", l2Reggae, "[Verse 1]\nfixture draft line", 7109), created, created)
			b.put(`INSERT INTO songs(id,root_track_id,draft_id,muse_id,muse_source,title,origin,created_by,created_at,updated_by,updated_at)
				VALUES(?,NULL,?,?,?,?,'draft','system:provenance',?,'system:provenance',?)`, sid, did, muse, museSrc, s.title, created, created)
			continue
		}
		root := b.out.Takes[s.takes[0]]
		b.put(`INSERT INTO songs(id,root_track_id,draft_id,muse_id,muse_source,title,origin,created_by,created_at,updated_by,updated_at)
			VALUES(?,?,NULL,?,?,?,'root','system:provenance',?,'system:provenance',?)`, sid, root, muse, museSrc, s.title, created, created)
		b.put(`INSERT INTO song_roots(root_track_id,song_id,via,attached_by,attached_at) VALUES(?,?,'own','system:provenance',?)`, root, sid, created)
		prev, prevSource := "", "none" // the previous event's take label and source
		for i, e := range s.events {
			eid := b.id(l2SpEvent)
			at := b.ts(e.age)
			ev := e.evidence
			var from, pinTake, pinBy, pinAt any
			if prev != "" {
				from = b.out.Takes[prev]
			}
			if e.source == "pinned" {
				ev = fmt.Sprintf(`{"def":"mm.pick/1","rule":"pinned","eligible":1,"pinned_by":%q,"pinned_at":%q}`, e.actor[0], at)
				pinTake, pinBy, pinAt = b.out.Takes[s.pinned], e.actor[0], at
			}
			b.put(`INSERT INTO pick_events(id,song_id,seq,at,from_take_id,to_take_id,from_source,to_source,reason,actor,actor_kind,actor_name,evidence_json)
				VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, eid, sid, i+1, at, from, b.out.Takes[e.to], prevSource, e.source, e.reason, e.actor[0], e.actor[1], e.actor[2], ev)
			if i == 0 {
				b.put(`INSERT INTO song_picks(song_id,take_id,source,pinned_take_id,pinned_by,pinned_at,evidence_json,last_event_id,changed_at,computed_at)
					VALUES(?,?,?,?,?,?,?,?,?,?)`, sid, b.out.Takes[e.to], e.source, pinTake, pinBy, pinAt, ev, eid, at, at)
			} else {
				b.put(`UPDATE song_picks SET take_id=?, source=?, pinned_take_id=?, pinned_by=?, pinned_at=?, evidence_json=?, last_event_id=?, changed_at=?, computed_at=? WHERE song_id=?`,
					b.out.Takes[e.to], e.source, pinTake, pinBy, pinAt, ev, eid, at, at, sid)
			}
			prev, prevSource = e.to, e.source
		}
	}
	// the human facts the Picks rest on: 4 stars on c1, 3 stars on s6a, and one device listen of a1
	for _, r := range []struct {
		label string
		stars int
		age   time.Duration
	}{{"c1", 4, 90 * time.Minute}, {"s6a", 3, 80 * time.Minute}} {
		at := b.ts(r.age)
		b.put(`INSERT INTO ratings(track_id,source,stars,principal_kind,principal_id,principal_name,created_at,updated_at)
			VALUES(?,'human',?,'human',?,'fixture',?,?)`, b.out.Takes[r.label], r.stars, FixtureHuman, at, at)
	}
	at := b.ts(30 * time.Minute)
	b.put(`INSERT INTO listen_events(id,track_id,session_id,kind,from_ms,to_ms,format,device_id,source,principal_kind,principal_id,client_at,received_at)
		VALUES(?,?,?,'start',0,0,'flac',?,'web','device',?,?,?)`, l2ID(l2SpEvent, 0x800), b.out.Takes["a1"], "l2-listen-session", DeviceID, "device:"+DeviceID, at, at)
	b.commit()
}

// l2Base writes the Songs, Takes, Muses, Picks, ratings and the listen of C132.2a over l1, with no L2 table row.
func l2Base(tb testing.TB, st *store.Store, now time.Time, l1 L1Info) *l2Builder {
	tb.Helper()
	b := newL2Builder(tb, st, now)
	b.out.L1Info = l1
	a1, a2 := L2Muse1Artist, L2Muse2Artist
	b.muse("M1", L2Muse1Title, &a1)
	b.muse("M2", L2Muse2Title, &a2)
	b.takes()
	b.songs()
	for _, sp := range l2Specs {
		b.out.Tracks = append(b.out.Tracks, b.out.Takes[sp.label])
	}
	b.out.Drafts = append(b.out.Drafts, b.out.S7Draft)
	return b
}

// FixtureL2 is FixtureL1 plus the C132.2a world (SQL-only): seven Songs S1-S7 holding eight Takes (one lossy) with synthetic
// loudness, the Pick each Song has after a Settle (S1-S3 newest, S4 and S6 stars, S5 pinned, S7 none), Record R1 (published,
// five slots: floating, floating, pinned to a non-Pick Take, an empty slot on the takes-less Song, floating) and R2 (an
// agent's draft of S1 and S6) with the changelogs the ops would have written, and four Crates (two manual with 3 and 4
// members, two smart). It needs schema 10 or later. The queue is not drained: a Settle changes no Pick and no Record row.
func FixtureL2(tb testing.TB, st *store.Store, now time.Time) L2Info {
	tb.Helper()
	b := newL2Builder(tb, st, now)
	b.requireCuration()
	b = l2Base(tb, st, now, FixtureL1(tb, st, now))
	b.curation()
	return b.out
}

// ApplyL2 adds the L2 world to a library that already holds FixtureM3's and FixtureL1's rows (servertest uses it).
func ApplyL2(tb testing.TB, st *store.Store, now time.Time) {
	tb.Helper()
	b := newL2Builder(tb, st, now)
	b.requireCuration()
	b = l2Base(tb, st, now, L1Info{})
	b.curation()
}

// FixtureL2Upgrade writes the Songs, Takes, Muses and Picks of FixtureL2, without any L2 table row, into a store opened
// with store.OpenRaw and store.MigrateTo(..., 9) (the schema before 0010), for TestMigration0010_UpgradeFromMain.
func FixtureL2Upgrade(tb testing.TB, st *store.Store, now time.Time) L2Info {
	tb.Helper()
	return l2Base(tb, st, now, FixtureL1(tb, st, now)).out
}

type l2Slot struct {
	key, song string
	pinned    string // the label of the pinned take; "" = floating
	synced    int    // the Song's Pick events the slot has consumed
	added     string // the label of the take the slot resolved to when it was added; "" = none yet
}

type l2Record struct {
	label, title, subtitle, liner string
	by, byName, byKind            string
	status                        string
	revision                      int
	age                           time.Duration
	slots                         []l2Slot
}

// curation writes Records, slots, changelogs and Crates.
func (b *l2Builder) curation() {
	b.tb.Helper()
	b.requireCuration()
	b.begin()
	defer b.abort()
	song := func(label string) string { return b.out.Songs[label] }
	take := func(label string) string { return b.out.Takes[label] }

	for _, r := range []l2Record{
		{label: "R1", title: "Synthetic Record One", subtitle: "Synthetic published sequence", liner: "Synthetic liner note.", by: FixtureHuman, byName: "fixture", byKind: "human",
			status: "published", revision: 3, age: 3 * time.Hour,
			slots: []l2Slot{{"A", song("S2"), "", 1, "a1"}, {"B", song("S3"), "", 2, "b1"}, {"C", song("S4"), "c2", 3, "c1"}, {"D", song("S7"), "", 0, ""}, {"E", song("S5"), "", 2, "e1"}}},
		{label: "R2", title: "Synthetic Record Two", by: FixtureAgent, byName: "fixture-agent", byKind: "agent",
			status: "draft", revision: 1, age: 45 * time.Minute,
			slots: []l2Slot{{"R2.1", song("S1"), "", 1, "s1a"}, {"R2.2", song("S6"), "", 2, "s6a"}}},
	} {
		rid := b.id(l2SpRecord)
		b.out.Records[r.label] = rid
		created := b.ts(r.age)
		step := func(i int) string { return store.FormatTime(b.now.Add(-r.age + time.Duration(i)*time.Minute)) }
		seq := 0
		change := func(kind string, slotID, pos, from, to, reason any) {
			seq++
			b.put(`INSERT INTO record_changes(id,record_id,seq,at,kind,slot_id,position,from_take_id,to_take_id,reason,actor,actor_kind,actor_name)
				VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, b.id(l2SpChange), rid, seq, step(seq), kind, slotID, pos, from, to, reason, r.by, r.byKind, r.byName)
		}
		b.put(`INSERT INTO records(id,title,subtitle,liner_note,cover_seed,cover_rerolls,status,revision,created_by,created_at,updated_by,updated_at)
			VALUES(?,?,?,?,?,0,'draft',?,?,?,?,?)`, rid, r.title, r.subtitle, r.liner, l2CoverSeed(rid, 0), r.revision, r.by, created, r.by, step(20))
		change("created", nil, nil, nil, nil, "initial")
		var order []string
		for i, s := range r.slots {
			sid := b.id(l2SpSlot)
			b.out.Slots[s.key] = sid
			order = append(order, sid)
			b.put(`INSERT INTO record_slots(id,record_id,song_id,synced_seq,added_by,added_at) VALUES(?,?,?,?,?,?)`, sid, rid, s.song, s.synced, r.by, created)
			var to any
			if s.added != "" {
				to = take(s.added)
			}
			change("slot_added", sid, i+1, nil, to, "initial")
		}
		for i, s := range r.slots {
			if s.pinned == "" {
				continue
			}
			b.put(`UPDATE record_slots SET pinned_take_id=?, pinned_by=?, pinned_at=? WHERE id=?`, take(s.pinned), r.by, step(10), b.out.Slots[s.key])
			change("slot_pinned", b.out.Slots[s.key], i+1, take(s.added), take(s.pinned), "pin")
		}
		ob, _ := json.Marshal(order)
		b.put(`UPDATE records SET order_json=? WHERE id=?`, string(ob), rid)
		if r.status == "published" {
			pub := step(15)
			change("published", nil, nil, nil, nil, nil)
			b.put(`UPDATE records SET status='published', published_by=?, published_at=?, first_published_at=? WHERE id=?`, r.by, pub, pub, rid)
		}
	}

	// Crates: two manual (3 Songs, 4 Takes) and two smart
	crate := func(label, name, desc, kind, subject string, filter any) string {
		cid := b.id(l2SpCrate)
		b.out.Crates[label] = cid
		at := b.ts(2 * time.Hour)
		b.put(`INSERT INTO crates(id,name,description,kind,subject,filter_json,created_by,created_at,updated_by,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			cid, name, desc, kind, subject, filter, FixtureHuman, at, FixtureHuman, at)
		return cid
	}
	item := func(crateID string, songID, trackID any) {
		b.put(`INSERT INTO crate_items(id,crate_id,song_id,track_id,added_by,added_at) VALUES(?,?,?,?,?,?)`, b.id(l2SpItem), crateID, songID, trackID, FixtureHuman, b.ts(90*time.Minute))
	}
	ms := crate("manual_song", "Synthetic Remake Candidates", "Synthetic songs to remake", "manual", "song", nil)
	for _, l := range []string{"S1", "S4", "S6"} {
		item(ms, song(l), nil)
	}
	mt := crate("manual_take", "Synthetic To Judge", "", "manual", "take", nil)
	for _, l := range []string{"s1a", "b1", "c2", "s6a"} {
		item(mt, nil, take(l))
	}
	crate("smart_song", "Synthetic Unheard Reggae", "Synthetic worked example", "smart", "song", L2SmartSongFilter)
	crate("smart_take", "Synthetic Unheard Drafts", "", "smart", "take", L2SmartTakeFilter)
	b.commit()
}

// FixtureL2Scale writes rows only (no audio) for the performance tests: muses Muses, songs Songs each with a root and one
// newer Take and a two-event Pick history, and records draft Records of up to 20 floating slots over the Songs in
// round-robin order with their created/slot_added changelog. It needs schema 10 or later. The queue is not drained.
func FixtureL2Scale(tb testing.TB, st *store.Store, now time.Time, songs, muses, records int) L2Info {
	tb.Helper()
	b := newL2Builder(tb, st, now)
	b.requireCuration()
	museIDs := make([]string, 0, muses)
	for m := 1; m <= muses; m++ {
		label := "M" + strconv.Itoa(m)
		b.muse(label, fmt.Sprintf("Synthetic Scale Muse %d", m), nil)
		museIDs = append(museIDs, b.out.Muses[label])
	}
	b.begin()
	defer b.abort()
	const trackQ = `INSERT INTO tracks(id,slug,title,recipe_id,parent_track_id,root_track_id,generation,status_set_by,origin,duration_s,sample_rate,bits_per_sample,channels,total_samples,
		master_path,master_sha256,master_bytes,source_sha256,frames_sha256,audio_md5,import_rule,recorded_at,created_at,status_set_at)
		VALUES(?,?,?,?,?,?,?,'import','import',200.0,48000,24,2,9600000,?,?,1,?,?,?,'slug',?,?,?)`
	const eventQ = `INSERT INTO pick_events(id,song_id,seq,at,from_take_id,to_take_id,from_source,to_source,reason,actor,actor_kind,actor_name,evidence_json)
		VALUES(?,?,?,?,?,?,?,'newest',?,'system:provenance','system','provenance',?)`
	const ev = `{"def":"mm.pick/1","rule":"newest","eligible":1}`
	out := L2Info{Muses: b.out.Muses, Songs: map[string]string{}, Records: map[string]string{}}
	songIDs := make([]string, 0, songs)
	for i := 1; i <= songs; i++ {
		rid := b.id(l2SpRecipe)
		req := fmt.Sprintf("l2_scale_%d", i)
		b.put(`INSERT INTO recipes(id,engine,request_id,request_json,style,lyrics,seed,cot,hash,core_hash,created_by,created_at)
			VALUES(?,'yue2',?,?,?,?,?,'full',?,?,'import',?)`, rid, req, reqJSON(req, l2Reggae, "[Verse 1]\nscale line", int64(100000+i)), l2Reggae, "[Verse 1]\nscale line",
			100000+i, Sha("h"+rid), Sha("c"+rid), b.ts(40*time.Hour))
		root, child := b.id(l2SpTake), b.id(l2SpTake)
		t0, t1 := b.ts(20*time.Hour), b.ts(19*time.Hour)
		for _, t := range []struct {
			id, at string
			parent any
			gen    int
		}{{root, t0, nil, 0}, {child, t1, root, 1}} {
			h := Sha(t.id)
			b.put(trackQ, t.id, "scale", fmt.Sprintf("Synthetic Scale Take %d", i), rid, t.parent, root, t.gen, "audio/masters/"+t.id+".flac", h, h, h, h[:32], t.at, t.at, t.at)
		}
		sid := b.id(l2SpSong)
		songIDs = append(songIDs, sid)
		var muse, src any
		if muses > 0 {
			muse, src = museIDs[(i-1)%muses], "manual"
		}
		b.put(`INSERT INTO songs(id,root_track_id,draft_id,muse_id,muse_source,title,origin,created_by,created_at,updated_by,updated_at)
			VALUES(?,?,NULL,?,?,?,'root','system:provenance',?,'system:provenance',?)`, sid, root, muse, src, fmt.Sprintf("Synthetic Scale Song %d", i), t0, t0)
		b.put(`INSERT INTO song_roots(root_track_id,song_id,via,attached_by,attached_at) VALUES(?,?,'own','system:provenance',?)`, root, sid, t0)
		e1, e2 := b.id(l2SpEvent), b.id(l2SpEvent)
		b.put(eventQ, e1, sid, 1, t0, nil, root, "none", "initial", ev)
		b.put(`INSERT INTO song_picks(song_id,take_id,source,evidence_json,last_event_id,changed_at,computed_at) VALUES(?,?,'newest',?,?,?,?)`, sid, root, ev, e1, t0, t0)
		b.put(eventQ, e2, sid, 2, t1, root, child, "newest", "newest", ev) // from newest to newest: the Pick moved to the newer take
		b.put(`UPDATE song_picks SET take_id=?, last_event_id=?, changed_at=?, computed_at=? WHERE song_id=?`, child, e2, t1, t1, sid)
		out.Songs["S"+strconv.Itoa(i)] = sid
	}
	for r := 1; r <= records && songs > 0; r++ {
		rid := b.id(l2SpRecord)
		out.Records["R"+strconv.Itoa(r)] = rid
		at := b.ts(10 * time.Hour)
		n := min(20, songs)
		b.put(`INSERT INTO records(id,title,cover_seed,status,created_by,created_at,updated_by,updated_at) VALUES(?,?,?,'draft',?,?,?,?)`,
			rid, fmt.Sprintf("Synthetic Scale Record %d", r), l2CoverSeed(rid, 0), FixtureHuman, at, FixtureHuman, at)
		b.put(`INSERT INTO record_changes(id,record_id,seq,at,kind,reason,actor,actor_kind,actor_name) VALUES(?,?,1,?,'created','initial',?,'human','fixture')`, b.id(l2SpChange), rid, at, FixtureHuman)
		var order []string
		for k := 0; k < n; k++ {
			sid := b.id(l2SpSlot)
			order = append(order, sid)
			sg := songIDs[((r-1)*n+k)%songs]
			b.put(`INSERT INTO record_slots(id,record_id,song_id,synced_seq,added_by,added_at) VALUES(?,?,?,2,?,?)`, sid, rid, sg, FixtureHuman, at)
			b.put(`INSERT INTO record_changes(id,record_id,seq,at,kind,slot_id,position,to_take_id,reason,actor,actor_kind,actor_name)
				VALUES(?,?,?,?,'slot_added',?,?,(SELECT take_id FROM song_picks WHERE song_id = ?),'initial',?,'human','fixture')`, b.id(l2SpChange), rid, k+2, at, sid, k+1, sg, FixtureHuman)
		}
		ob, _ := json.Marshal(order)
		b.put(`UPDATE records SET order_json=? WHERE id=?`, string(ob), rid)
	}
	b.commit()
	return out
}
