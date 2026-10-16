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

// L1Params shapes the mirror of the real library structure (C101.1): index-backed motherless drafts (Muses x Angles),
// jsonl_upload demo drafts, single-take reference FLACs ("originals") and lossy reference groups of GroupSize takes.
type L1Params struct {
	Muses, Angles, DemoDrafts, Originals, Groups, GroupSize int
}

// DefaultL1Params is what FixtureL1 builds.
var DefaultL1Params = L1Params{Muses: 6, Angles: 3, DemoDrafts: 4, Originals: 3, Groups: 2, GroupSize: 3}

// Names and ids of the L1 rows. Every Muse text is synthetic (C99.5).
const (
	L1IndexRunID      = "01900000-0000-7000-8000-000000000c01" // the index import
	L1ReferenceRunID  = "01900000-0000-7000-8000-000000000c02" // the reference import
	L1IndexCollection = "Synthetic Index Collection"
	L1DemoCollection  = "Synthetic Demo Collection"
	L1LineageRoot     = "01900000-0000-7000-8000-000000000c11" // the known-recipe lineage: root, child and a mothered draft
	L1LineageChild    = "01900000-0000-7000-8000-000000000c12"
)

// L1MuseTitle and L1MuseArtist are the reference Muse text of index Muse i (1-based).
func L1MuseTitle(i int) string  { return fmt.Sprintf("Synthetic Muse %03d", i) }
func L1MuseArtist(i int) string { return fmt.Sprintf("Synthetic Artist %03d", i) }

// L1Angle is the variant type of angle slot k (0-based): variant-a, variant-b, ...
func L1Angle(k int) string { return "variant-" + string(rune('a'+k)) }

// L1Info lists the L1 rows next to the base fixture's.
type L1Info struct {
	Fixtures
	IndexDrafts, DemoDrafts, MotheredDrafts []string
	Originals                               []string   // single-take reference FLACs
	Groups                                  [][]string // lossy groups, p1 (root) first
}

var l1Styles = []string{
	"reggae, male vocal, warm, 76bpm", "female vocals, soft pop, dreamy", "90s eurodance, energetic, female vocal",
	"jazz, smooth jazz, piano, warm", "roots reggae, laid-back groove, male singer", "synthpop, 80s, upbeat",
	"acoustic guitar, singer songwriter, intimate", "hip hop, boom bap, rap, 90bpm",
}

type l1Builder struct {
	tb  testing.TB
	st  *store.Store
	svc *library.Service
	now time.Time
	n   int
	seq int64
}

func newL1Builder(tb testing.TB, st *store.Store, now time.Time) *l1Builder {
	b := &l1Builder{tb: tb, st: st, now: now, n: 0xb000, seq: 5000}
	b.svc = library.New(st, nil, func() time.Time { return now })
	b.svc.UseIDFunc(func() string { b.n++; return fmt.Sprintf("01900000-0000-7000-8000-%012x", b.n) })
	return b
}

func (b *l1Builder) nextSeed() int64 { b.seq++; return b.seq }

func (b *l1Builder) run(id string, at time.Time, label string) {
	mustExec(b.tb, b.st, `INSERT INTO import_runs(id, started_at, heartbeat_at, finished_at, state, principal, inbox_label, plan_digest, summary_json, report_json)
		VALUES (?,?,?,?, 'completed', 'cli', 'inbox', ?, '{"files":1}', '{"format":"mm.inventory/1","report_id":"'||?||'","errors":[],"warnings":[]}')`,
		id, at.UTC().Format(time.RFC3339), at.UTC().Format(time.RFC3339), at.Add(10*time.Minute).UTC().Format(time.RFC3339), sha([]byte("plan/"+id)), id)
}

// indexDraft inserts one index-backed motherless draft: a recipe, its source line in runID, the draft and its
// recipe_refs row (variant type, source title and artist).
func (b *l1Builder) indexDraft(runID string, museIdx, angleIdx, line int) string {
	ctx := context.Background()
	reqID := fmt.Sprintf("ex_%03d_v%d", museIdx, angleIdx+1)
	style := l1Styles[(museIdx+angleIdx)%len(l1Styles)]
	p := mustParse(b.tb, reqJSON(reqID, style, "[Verse 1]\nline one\n[Chorus]\nline two", b.nextSeed()))
	tx := begin(b.tb, b.st)
	rid, _, err := b.svc.InsertRecipe(ctx, tx, p, "import")
	check(b.tb, err)
	_, err = b.svc.InsertRecipeSource(ctx, tx, rid, library.SourceRef{Path: "music/index-variants.jsonl", Line: line, Raw: p.Canonical, RawKind: "jsonl_line"}, runID)
	check(b.tb, err)
	did, _, err := b.svc.InsertDraft(ctx, tx, library.NewDraft{Origin: "import_backlog", SourceRecipeID: rid, GroupKey: fmt.Sprintf("ex_%03d", museIdx), Label: L1Angle(angleIdx)})
	check(b.tb, err)
	check(b.tb, b.svc.InsertRefs(ctx, tx, rid, "music/index.csv", map[string]string{
		"variant_type": L1Angle(angleIdx), "source_title": L1MuseTitle(museIdx), "source_artist": L1MuseArtist(museIdx)}))
	check(b.tb, tx.Commit())
	return did
}

func (b *l1Builder) inCollection(name, source string, drafts []string) {
	tx := begin(b.tb, b.st)
	for _, d := range drafts {
		check(b.tb, b.svc.AddToCollection(context.Background(), tx, d, name, source))
	}
	check(b.tb, tx.Commit())
}

// demoDraft inserts a jsonl_upload draft (no recipe), as FixtureM2c's uploads do.
func (b *l1Builder) demoDraft(i int) string {
	b.n++
	id := fmt.Sprintf("01900000-0000-7000-8000-%012x", b.n)
	reqID := fmt.Sprintf("demo_%03d", i)
	at := b.now.Add(-20 * time.Minute).UTC().Format(time.RFC3339)
	mustExec(b.tb, b.st, `INSERT INTO recipe_drafts(id, origin, group_key, label, request_id, request_json, status, created_by, updated_by, created_at, updated_at)
		VALUES (?,'jsonl_upload',NULL,?,?,?,'backlog',?,?,?,?)`, id, fmt.Sprintf("d%d", i), reqID,
		reqJSON(reqID, l1Styles[i%len(l1Styles)], "[Verse 1]\nline one\n[Chorus]\nline two", b.nextSeed()), FixtureHuman, FixtureHuman, at, at)
	return id
}

func (b *l1Builder) refTake(title, audioKey, parent, root string, gen int, lossy bool, runID string, age time.Duration) string {
	ctx := context.Background()
	slug := yue2.Slug(title)
	tx := begin(b.tb, b.st)
	rid, _, err := b.svc.InsertReferenceRecipe(ctx, tx, library.NewReferenceRecipe{Title: title, Slug: slug, AudioKey: audioKey})
	check(b.tb, err)
	b.n++
	id := fmt.Sprintf("01900000-0000-7000-8000-%012x", b.n)
	h := sha([]byte("l1-ref/" + audioKey))
	ext := "flac"
	if lossy {
		ext = "mp3"
	}
	nt := library.NewTake{
		ID: id, Title: title, Slug: slug, RecipeID: rid, Reference: true, ImportRule: library.RuleReference, PrincipalID: "cli",
		ParentTrackID: parent, RootTrackID: root, Generation: gen,
		DurationS: 6, SampleRate: 48000, BitsPerSample: 24, Channels: 2, TotalSamples: 6 * 48000,
		MasterPath: "audio/masters/" + slug + "__" + h[:8] + "." + ext, MasterSHA256: h, MasterBytes: 64,
		SourceSHA256: sha([]byte("l1-ref-src/" + audioKey)), FramesSHA256: sha([]byte("l1-ref-frames/" + audioKey)), AudioMD5: h[:32],
		ImportRunID: runID, RecordedAt: b.now.Add(-age),
		Loudness:    analysis.Loudness{IntegratedLUFS: -13.5, LRA: 4, LRALowLUFS: -18, LRAHighLUFS: -14, TruePeakDBTP: -1.0, SamplePeakDBFS: -1.2, ShortTermMaxLUFS: -12, ShortTerm1Hz: []float64{-13.5, -13}},
		Spectrum:    analysis.Spectrum{BandwidthHz: bptrInt(16000), RefDB: -40, ThresholdDB: -110, Frames: 30},
		ToolVersion: "ffmpeg n9.0.1", ReplayGainDB: -4.5,
		Renditions: []library.NewRendition{{Kind: "peaks", SHA256: sha([]byte("l1-peaks/" + audioKey)), Bytes: 32, Path: "derived/l1/" + h[:16] + ".peaks.json", Params: map[string]any{"format": analysis.PeaksFormat}}},
	}
	if lossy {
		nt.Lossy = true
		nt.DurationS, nt.SampleRate, nt.BitsPerSample, nt.Channels = LossyDuration, 44100, 16, 2
		nt.TotalSamples = int64(LossyDuration * 44100)
		nt.SourceSHA256, nt.FramesSHA256, nt.AudioMD5 = h, h, "00000000000000000000000000000000"
	} else {
		mp := sha([]byte("l1-mp3/" + audioKey))
		nt.Renditions = append(nt.Renditions, library.NewRendition{Kind: "mp3_320", SHA256: mp, Bytes: 48, Path: "derived/l1/" + mp[:16] + ".mp3",
			Params: map[string]any{"encoder": "libmp3lame", "bitrate": "320k", "sample_rate": 48000, "gain_db": -0.6, "ffmpeg": "n9.0.1"}})
	}
	if parent != "" {
		nt.Mutation = &library.NewMutation{Operator: "inferred_sibling", Inferred: true, Rule: "reference_group"}
	}
	check(b.tb, b.svc.InsertTake(ctx, tx, nt))
	check(b.tb, tx.Commit())
	return id
}

// lineage inserts the known-recipe lineage: a root, an inferred-sibling child and a mothered varianti draft.
func (b *l1Builder) lineage() (mothered string) {
	ctx := context.Background()
	runID := L1IndexRunID
	for i, id := range []string{L1LineageRoot, L1LineageChild} {
		reqID := "fxl_001"
		seed := b.nextSeed()
		p := mustParse(b.tb, reqJSON(reqID, "funk, groovy bass, male vocal, 104bpm", "[Verse 1]\nlineage line\n[Chorus]\nlineage hook", seed))
		tx := begin(b.tb, b.st)
		rid, _, err := b.svc.InsertRecipe(ctx, tx, p, "import")
		check(b.tb, err)
		_, err = b.svc.InsertRecipeSource(ctx, tx, rid, library.SourceRef{Path: "music/songs.jsonl", Line: 900 + i, Raw: p.Canonical, RawKind: "jsonl_line"}, runID)
		check(b.tb, err)
		h := sha([]byte(fmt.Sprintf("l1-lineage-%d", i)))
		nt := library.NewTake{
			ID: id, Title: fmt.Sprintf("Synthetic Lineage %d", i+1), Slug: fmt.Sprintf("synthetic_lineage_%d", i+1), RecipeID: rid,
			DurationS: 8, SampleRate: 48000, BitsPerSample: 24, Channels: 2, TotalSamples: 8 * 48000,
			MasterPath: fmt.Sprintf("audio/masters/synthetic_lineage_%d__%d__20261001-1200.flac", i+1, seed), MasterSHA256: h, MasterBytes: 64,
			SourceSHA256: sha([]byte("l1-lineage-src" + h)), FramesSHA256: sha([]byte("l1-lineage-frames" + h)), AudioMD5: h[:32],
			ImportRule: "slug", ImportRunID: runID, PrincipalID: "cli", RecordedAt: b.now.Add(-time.Duration(3-i) * time.Hour),
			Renditions:  []library.NewRendition{{Kind: "peaks", SHA256: sha([]byte("l1-lp" + h)), Bytes: 32, Path: "derived/l1/" + h[:16] + ".peaks.json", Params: map[string]any{"format": analysis.PeaksFormat}}},
			Loudness:    analysis.Loudness{IntegratedLUFS: -14, LRA: 5, LRALowLUFS: -19, LRAHighLUFS: -13, TruePeakDBTP: -1.2, SamplePeakDBFS: -1.5, ShortTermMaxLUFS: -12, ShortTerm1Hz: []float64{-14, -13.5}},
			Spectrum:    analysis.Spectrum{BandwidthHz: bptrInt(16000), RefDB: -40, ThresholdDB: -110, Frames: 30},
			ToolVersion: "ffmpeg n9.0.1", ReplayGainDB: -4,
		}
		if i == 1 {
			nt.ParentTrackID, nt.RootTrackID, nt.Generation = L1LineageRoot, L1LineageRoot, 1
			nt.Mutation = &library.NewMutation{Operator: "inferred_sibling", Inferred: true, Rule: "same_request_id"}
		}
		check(b.tb, b.svc.InsertTake(ctx, tx, nt))
		check(b.tb, tx.Commit())
	}
	// a mothered draft (varianti_import) of the lineage root
	p := mustParse(b.tb, reqJSON("fxl_002_v1", "funk, slap bass, 104bpm", "[Verse 1]\nlineage two\n[Chorus]\nlineage hook two", b.nextSeed()))
	tx := begin(b.tb, b.st)
	rid, _, err := b.svc.InsertRecipe(ctx, tx, p, "import")
	check(b.tb, err)
	_, err = b.svc.InsertRecipeSource(ctx, tx, rid, library.SourceRef{Path: "varianti/fxl.jsonl", Line: 1, Raw: p.Canonical, RawKind: "jsonl_line"}, runID)
	check(b.tb, err)
	did, _, err := b.svc.InsertDraft(ctx, tx, library.NewDraft{Origin: "varianti_import", SourceRecipeID: rid, ParentTrackID: L1LineageRoot, GroupKey: "fxl_002", Label: "v1"})
	check(b.tb, err)
	check(b.tb, tx.Commit())
	return did
}

// buildL1 writes the mirror on top of whatever the store already holds (FixtureM3). Rows only; it never runs Settle.
func buildL1(tb testing.TB, st *store.Store, now time.Time, p L1Params, base Fixtures) L1Info {
	tb.Helper()
	b := newL1Builder(tb, st, now)
	info := L1Info{Fixtures: base}
	b.run(L1IndexRunID, now.Add(-3*time.Hour), "index")
	b.run(L1ReferenceRunID, now.Add(-2*time.Hour), "reference")
	line := 0
	for m := 1; m <= p.Muses; m++ {
		for a := 0; a < p.Angles; a++ {
			line++
			info.IndexDrafts = append(info.IndexDrafts, b.indexDraft(L1IndexRunID, m, a, line))
		}
	}
	if len(info.IndexDrafts) > 0 {
		b.inCollection(L1IndexCollection, "music/index-collection.json", info.IndexDrafts)
	}
	for i := 1; i <= p.DemoDrafts; i++ {
		info.DemoDrafts = append(info.DemoDrafts, b.demoDraft(i))
	}
	if len(info.DemoDrafts) > 0 {
		b.inCollection(L1DemoCollection, "music/demo-collection.json", info.DemoDrafts)
	}
	for i := 1; i <= p.Originals; i++ {
		id := b.refTake(fmt.Sprintf("Synthetic Original %03d", i), fmt.Sprintf("l1-original-%d", i), "", "", 0, false, L1ReferenceRunID, time.Duration(i)*time.Hour)
		info.Originals = append(info.Originals, id)
	}
	for g := 1; g <= p.Groups; g++ {
		var grp []string
		root := ""
		for k := 1; k <= max(p.GroupSize, 1); k++ {
			title := fmt.Sprintf("Synthetic Pilot %03d p%d", g, k)
			if k == 1 {
				root = b.refTake(title, fmt.Sprintf("mp3:l1-pilot-%d-%d", g, k), "", "", 0, true, L1ReferenceRunID, time.Duration(30+g)*time.Minute)
				grp = append(grp, root)
				continue
			}
			grp = append(grp, b.refTake(title, fmt.Sprintf("mp3:l1-pilot-%d-%d", g, k), root, root, 1, true, L1ReferenceRunID, time.Duration(30+g)*time.Minute))
		}
		info.Groups = append(info.Groups, grp)
	}
	info.MotheredDrafts = append(info.MotheredDrafts, b.lineage())
	info.Tracks = append(info.Tracks, L1LineageRoot, L1LineageChild)
	info.Tracks = append(info.Tracks, info.Originals...)
	for _, g := range info.Groups {
		info.Tracks = append(info.Tracks, g...)
	}
	info.Drafts = append(info.Drafts, info.IndexDrafts...)
	info.Drafts = append(info.Drafts, info.DemoDrafts...)
	info.Drafts = append(info.Drafts, info.MotheredDrafts...)
	return info
}

// FixtureL1 is FixtureM3 plus a mirror of the real library's structure with synthetic text (C101.1, DefaultL1Params):
// 18 index-backed motherless drafts (3 per Muse, variant types variant-a/b/c), a collection of those and one of 4
// jsonl_upload drafts, 3 single-take reference FLACs, 2 lossy reference groups of 3 takes and a known-recipe lineage
// with a mothered draft. Rows only: the Reconciler has not run, so songs is empty and provenance_dirty is not.
func FixtureL1(tb testing.TB, st *store.Store, now time.Time) L1Info {
	tb.Helper()
	return buildL1(tb, st, now, DefaultL1Params, FixtureM3(tb, st, now))
}

// ApplyL1 adds the FixtureL1 mirror to a library that already holds FixtureM3's rows (servertest uses it).
func ApplyL1(tb testing.TB, st *store.Store, now time.Time) {
	tb.Helper()
	buildL1(tb, st, now, DefaultL1Params, Fixtures{})
}

// FixtureL1Mirror builds the same shape at any parameters (the acceptance mirror uses Muses 120, DemoDrafts 5,
// Originals 4, Groups 5). It adds two import runs and two collections on top of FixtureM3.
func FixtureL1Mirror(tb testing.TB, st *store.Store, now time.Time, p L1Params) L1Info {
	tb.Helper()
	return buildL1(tb, st, now, p, FixtureM3(tb, st, now))
}

// FixtureL1Upgrade writes the FixtureL1 data into a store opened with store.OpenRaw + store.MigrateTo(..., 7) (schema 7,
// before 0008), the upgrade_m3_test pattern, for TestMigration0008_UpgradeFromM3.
func FixtureL1Upgrade(tb testing.TB, st *store.Store, now time.Time) L1Info {
	tb.Helper()
	return FixtureL1(tb, st, now)
}

// FixtureL1Scale writes only drafts, their refs and one collection (no audio, no takes) for the performance tests:
// muses x angles index-backed motherless drafts.
func FixtureL1Scale(tb testing.TB, st *store.Store, now time.Time, muses, angles int) L1Info {
	tb.Helper()
	b := newL1Builder(tb, st, now)
	b.run(L1IndexRunID, now.Add(-3*time.Hour), "index")
	var info L1Info
	line := 0
	for m := 1; m <= muses; m++ {
		for a := 0; a < angles; a++ {
			line++
			info.IndexDrafts = append(info.IndexDrafts, b.indexDraft(L1IndexRunID, m, a, line))
		}
	}
	info.Drafts = info.IndexDrafts
	return info
}
