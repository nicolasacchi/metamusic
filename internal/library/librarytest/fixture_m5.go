package librarytest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// The M5 fixture (C264) is FixtureL3 plus the rows the close-the-loop lanes read, SQL-only and synthetic. It writes no M5
// table (the migration exists, the lanes write them). What it holds, besides everything L1-L3 already model:
//
//   - the real-world shapes L3's second pass taught: draft-only Songs (d1-d4, u2), reference and lossy imports (the L1/M2c
//     originals and groups, the multi-part lossy groups p1/p2/p3, TrackLossy, the L2 lossy take) which must refuse propose_patch,
//     Songs with and without Picks (T1-T8 versus the drafts);
//   - section shapes: T5 and T6 have lyrics-only sections (an import), T1 a plan-timed take, T2 plan plus manual rows, T3 measured
//     plus plan, and a three-generation known-recipe lineage G0 -> G1 -> G2 whose comments sit on the grandparent;
//   - comments on G0: timestamped open fixes (one in a chorus, one in a verse), a note, a keep, a hidden fix and a resolved fix;
//   - listening: a few human segments, a replayed span (no rewind seek, see below) and a skip on G0, and one Subsonic scrobble.
//
// G0 is plan-timed (verse 1, chorus 1, verse 2, chorus 2, outro over 120 s); G1 has manual sections that differ from the plan;
// G2 is lyrics-only, so a spot on G2 maps proportionally.

// M5 fixed ids (the range 0xd5xxxx is free of every other fixture).
func m5ID(space, n int) string {
	return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xd50000+space*0x1000+n)
}

const (
	m5SpTake = iota + 1
	m5SpRecipe
	m5SpComment
	m5SpEvent
)

// M5Lyrics are the lyrics of the lineage recipes: verse 1, chorus, verse 2, chorus (second occurrence), outro.
const M5Lyrics = "[Verse 1]\nfixture line a1\nfixture line a2\nfixture line a3\nfixture line a4\n[Chorus]\nfixture hook b1\nfixture hook b2\nfixture hook b3\nfixture hook b4\n" +
	"[Verse 2]\nfixture line c1\nfixture line c2\nfixture line c3\nfixture line c4\n[Chorus]\nfixture hook b1\nfixture hook b2\nfixture hook b3\nfixture hook b4\n[Outro]\nfixture tail d1"

// M5Info lists the M5 rows on top of L3Info.
type M5Info struct {
	L3Info
	// Lineage is G0, G1, G2 (root, child, grandchild; all known recipes, all with Songs-less standalone takes).
	Lineage [3]string
	// Comments maps "fix_chorus", "fix_verse", "note", "keep", "hidden_fix", "resolved_fix" to comment ids on G0.
	Comments map[string]string
	// SubsonicEvent is the id of the Subsonic scrobble event on G0.
	SubsonicEvent string
}

// M5PlanSections are the plan timings of G0 and T1 (seconds): verse 1, chorus, verse 2, chorus, outro of a 120 s take.
var M5PlanSections = []struct {
	Kind, Label string
	From, To    float64
}{
	{"verse", "verse 1", 0, 30}, {"chorus", "chorus 1", 30, 50}, {"verse", "verse 2", 50, 80}, {"chorus", "chorus 2", 80, 105}, {"outro", "outro", 105, 120},
}

// FixtureM5 is FixtureL3 plus the C264 world.
func FixtureM5(tb testing.TB, st *store.Store, now time.Time) M5Info {
	tb.Helper()
	return addM5(tb, st, now, M5Info{L3Info: FixtureL3(tb, st, now)})
}

// ApplyM5 adds the L3 and M5 worlds to a library that already holds FixtureM3's and FixtureL1's rows (servertest uses it).
func ApplyM5(tb testing.TB, st *store.Store, now time.Time) {
	tb.Helper()
	ApplyL3(tb, st, now)
	// ApplyL3 does not return its labels; the M5 rows need only the take ids of its own and of T1-T3, derived by label order
	var info M5Info
	info.Takes = map[string]string{}
	for i, l := range []string{"T1", "T2", "T3", "T4", "T5", "T6", "T7", "T8", "u1"} {
		info.Takes[l] = l3ID(l3SpTake, i+1)
	}
	addM5(tb, st, now, info)
}

func addM5(tb testing.TB, st *store.Store, now time.Time, info M5Info) M5Info {
	tb.Helper()
	ctx := context.Background()
	info.Comments = map[string]string{}
	svc := library.New(st, nil, func() time.Time { return now })
	n := 0
	svc.UseIDFunc(func() string { n++; return m5ID(m5SpRecipe, n) })
	styles := []string{"folk, warm, male vocal, italian, 90bpm", "folk, warm, female vocal, italian, 92bpm", "folk, bright, female vocal, italian, 95bpm"}
	// 1. the three-generation lineage (known recipes)
	for g := 0; g < 3; g++ {
		id := m5ID(m5SpTake, g+1)
		info.Lineage[g] = id
		title := fmt.Sprintf("Synthetic Lineage G%d", g)
		slug := yue2.Slug(title)
		h := sha(fmt.Appendf(nil, "m5-lineage/%d", g))
		p := mustParse(tb, reqJSON(fmt.Sprintf("m5_g%d", g), styles[g], M5Lyrics, int64(8500+g)))
		tx := begin(tb, st)
		rid, _, err := svc.InsertRecipe(ctx, tx, p, "import")
		check(tb, err)
		nt := library.NewTake{
			ID: id, Title: title, Slug: slug, RecipeID: rid, PrincipalID: "cli", RecordedAt: now.Add(-time.Duration(6-g) * time.Hour),
			DurationS: 120, SampleRate: 48000, BitsPerSample: 24, Channels: 2, TotalSamples: 120 * 48000,
			MasterPath: "audio/masters/" + slug + "__" + h[:8] + ".flac", MasterSHA256: h, MasterBytes: 64,
			SourceSHA256: sha([]byte("m5-src/" + h)), FramesSHA256: sha([]byte("m5-frames/" + h)), AudioMD5: h[:32], ImportRule: "slug",
			Loudness:    analysis.Loudness{IntegratedLUFS: -14, LRA: 5, LRALowLUFS: -18, LRAHighLUFS: -12.5, TruePeakDBTP: -3, SamplePeakDBFS: -3.3, ShortTermMaxLUFS: -13, ShortTerm1Hz: []float64{-14, -14}},
			Spectrum:    analysis.Spectrum{BandwidthHz: bptrInt(16000), RefDB: -40, ThresholdDB: -110, Frames: 30},
			ToolVersion: "ffmpeg n9.0.1", ReplayGainDB: -4,
			Renditions: []library.NewRendition{{Kind: "peaks", SHA256: sha([]byte("m5-peaks/" + h)), Bytes: 32, Path: "derived/m5/" + h[:16] + ".peaks.json", Params: map[string]any{"format": analysis.PeaksFormat}}},
		}
		if g > 0 {
			nt.ParentTrackID, nt.RootTrackID, nt.Generation = info.Lineage[g-1], info.Lineage[0], g
			nt.Mutation = &library.NewMutation{Operator: "inferred_sibling", Inferred: true, Rule: "same_request_id"}
		}
		check(tb, svc.InsertTake(ctx, tx, nt))
		check(tb, tx.Commit())
	}
	// 2. section timings: G0 plan; G1 manual rows that differ from the plan (and a plan too); T1 plan; T2 plan + manual; T3 measured + plan
	sec := func(track, source string, idx int, kind, label string, from, to float64) {
		mustExec(tb, st, `INSERT INTO sections(track_id, source, idx, kind, label, raw_label, start_s, end_s, confidence) VALUES (?,?,?,?,?,?,?,?,0.9)`,
			track, source, idx, kind, label, "["+label+"]", from, to)
	}
	plan := func(track string) {
		for i, s := range M5PlanSections {
			sec(track, "plan", i, s.Kind, s.Label, s.From, s.To)
		}
	}
	plan(info.Lineage[0])
	plan(info.Lineage[1])
	for i, s := range []struct {
		kind, label string
		from, to    float64
	}{{"verse", "verse 1", 4, 36}, {"chorus", "chorus 1", 36, 58}, {"verse", "verse 2", 58, 86}, {"chorus", "chorus 2", 86, 112}, {"outro", "outro", 112, 120}} {
		sec(info.Lineage[1], "manual", i, s.kind, s.label, s.from, s.to)
	}
	if t := info.Takes["T1"]; t != "" {
		plan(t)
	}
	if t := info.Takes["T2"]; t != "" {
		plan(t)
		sec(t, "manual", 0, "verse", "verse 1", 0, 40)
		sec(t, "manual", 1, "chorus", "chorus 1", 40, 100)
	}
	if t := info.Takes["T3"]; t != "" {
		plan(t)
		sec(t, "measured", 0, "verse", "verse 1", 2, 44)
		sec(t, "measured", 1, "chorus", "chorus 1", 44, 90)
	}
	// 3. comments on the grandparent G0
	g0 := info.Lineage[0]
	at := store.FormatTime(now.Add(-5 * time.Hour))
	comment := func(key, kind, state, body string, atSec *float64, label *string, hidden bool) {
		id := m5ID(m5SpComment, len(info.Comments)+1)
		info.Comments[key] = id
		var src any
		if label != nil {
			src = "plan"
		}
		var hat, hby any
		if hidden {
			hat, hby = at, FixtureHuman
		}
		var sc, sb any
		if state != "open" {
			sc, sb = at, FixtureHuman
		}
		mustExec(tb, st, `INSERT INTO comments(id, track_id, body, at_second, section_label, section_source, section_confidence, kind, state, source, principal_kind, principal_id, principal_name,
			created_at, updated_at, state_changed_at, state_changed_by, hidden_at, hidden_by) VALUES (?,?,?,?,?,?,?,?,?,'web','human',?,'fixture',?,?,?,?,?,?)`,
			id, g0, body, atSec, label, src, map[bool]any{true: 0.8, false: nil}[label != nil], kind, state, FixtureHuman, at, at, sc, sb, hat, hby)
	}
	f := func(v float64) *float64 { return &v }
	s := func(v string) *string { return &v }
	comment("fix_chorus", "fix", "open", "the chorus lands late", f(40), s("chorus 1"), false)
	comment("fix_verse", "fix", "open", "second line of the verse is rushed", f(12), s("verse 1"), false)
	comment("note", "note", "open", "nice outro", f(110), s("outro"), false)
	comment("keep", "keep", "open", "keep the bass in the second chorus", f(90), s("chorus 2"), false)
	comment("hidden_fix", "fix", "open", "a hidden remark", f(20), s("verse 1"), true)
	comment("resolved_fix", "fix", "resolved", "an old remark", f(60), s("verse 2"), false)
	// 4. listening on G0: human sessions (a replayed 10-30 s span, a skip at 5 s) and one Subsonic scrobble
	listen := func(n int, sess, kind string, from, to *int64, source, pk, pid string) string {
		id := m5ID(m5SpEvent, n)
		mustExec(tb, st, `INSERT INTO listen_events(id, track_id, session_id, kind, from_ms, to_ms, format, device_id, source, principal_kind, principal_id, client_at, received_at)
			VALUES (?,?,?,?,?,?,'flac',NULL,?,?,?,?,?)`, id, g0, sess, kind, from, to, source, pk, pid, at, at)
		return id
	}
	ms := func(v int64) *int64 { return &v }
	listen(1, "m5session-a1", "start", nil, nil, "web", "human", FixtureHuman)
	listen(2, "m5session-a1", "segment", ms(0), ms(60000), "web", "human", FixtureHuman)
	listen(3, "m5session-b1", "start", nil, nil, "web", "human", FixtureHuman)
	listen(4, "m5session-b1", "segment", ms(0), ms(30000), "web", "human", FixtureHuman)
	// no backward seek row: the listen-event validator and the table CHECK both require to_ms >= from_ms, so a rewind is
	// never stored (the client emits one, the server rejects it as bad_range); only the replayed segment shows it
	listen(5, "m5session-b1", "seek", ms(30000), ms(33000), "web", "human", FixtureHuman)
	listen(6, "m5session-b1", "segment", ms(10000), ms(40000), "web", "human", FixtureHuman)
	listen(7, "m5session-c1", "start", nil, nil, "web", "human", FixtureHuman)
	listen(8, "m5session-c1", "skip", ms(5000), nil, "web", "human", FixtureHuman)
	info.SubsonicEvent = listen(9, "m5session-s1", "start", nil, nil, "subsonic", "device", "fixture-client")
	listen(10, "m5session-s1", "segment", ms(0), ms(45000), "subsonic", "device", "fixture-client")
	return info
}
