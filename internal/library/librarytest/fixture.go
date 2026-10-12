// Package librarytest builds a small deterministic library for tests (C17).
// It is test-only: only _test.go files and `make fixture-db` import it.
package librarytest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Fixed ids (UUIDv7-shaped), identical on every call.
const (
	TrackLuna  = "01900000-0000-7000-8000-000000000101"
	TrackCitta = "01900000-0000-7000-8000-000000000102"
	TrackTwinA = "01900000-0000-7000-8000-000000000103"
	TrackTwinB = "01900000-0000-7000-8000-000000000104"
	TrackShort = "01900000-0000-7000-8000-000000000105"
	// Draft ids come from the fixture's id sequence; TestFixture_FixedIds pins them.
	Draft1      = "01900000-0000-7000-8000-000000000208" // fxb_001_v1, in pilot-x, has a reference
	Draft2      = "01900000-0000-7000-8000-00000000020a"
	Draft3      = "01900000-0000-7000-8000-00000000020c"
	Draft4      = "01900000-0000-7000-8000-00000000020e"
	Draft5      = "01900000-0000-7000-8000-000000000210"
	Draft6      = "01900000-0000-7000-8000-000000000212"
	RunID       = "01900000-0000-7000-8000-000000000301"
	DeviceID    = "fixture-device-0001"
	idBase      = 0x200 // recipes, drafts and mutations count up from here
	collection  = "pilot-x"
	FixtureLUFS = -14.0
)

// Fixtures lists the ids Fixture created, in deterministic order.
type Fixtures struct {
	Tracks, Drafts, Runs, Devices, Recipes []string
}

type fxTrack struct {
	id, title, requestID, style, lyrics string
	seed                                int64
	lufs                                float64
	bandwidth                           int
	dur                                 float64
	ageDays                             int
	abc                                 string
	truncABC, truncSem                  *bool
	parent                              string
}

func bptr(b bool) *bool { return &b }

const alignedABC = "X:1\nT:t\nM:4/4\nL:1/8\nQ:1/4=120\nK:C\n% verse\nCDEF|CDEF|\n% chorus\nCDEF|CDEF|\n% outro\nCDEF|CDEF|]\n"
const untimedABC = "X:1\nT:t\nM:4/4\nL:1/8\nK:C\nCDEF|CDEF|]\n"

var fxTracks = []fxTrack{
	{id: TrackLuna, title: "Luna Park", requestID: "fx-luna", style: "Dream pop, 120 bpm, warm synth, female vocal",
		lyrics: "[Verse 1]\nla la luna\n[Chorus]\nsing it loud\n[Outro]\nfade away", seed: 101, lufs: -14.0, bandwidth: 16000, dur: 12, ageDays: 5, abc: alignedABC},
	{id: TrackCitta, title: "Città lenta", requestID: "fx-citta", style: "ambient, slow, piano",
		lyrics: "[Verse]\nè sera\n<b>&", seed: 102, lufs: -15.5, bandwidth: 12000, dur: 6, ageDays: 4},
	{id: TrackTwinA, title: "Twin A", requestID: "fx-twin", style: "rock, 140 bpm, guitar",
		lyrics: "[Verse]\nsame words\n[Chorus]\nsame again", seed: 103, lufs: -12.0, bandwidth: 20000, dur: 8, ageDays: 3},
	{id: TrackTwinB, title: "Twin B", requestID: "fx-twin", style: "rock, 140 bpm, guitar",
		lyrics: "[Verse]\nsame words\n[Chorus]\nsame again", seed: 104, lufs: -13.0, bandwidth: 18000, dur: 8, ageDays: 2, parent: TrackTwinA},
	{id: TrackShort, title: "Cut Short", requestID: "fx-short", style: "ambient, drone",
		lyrics: "[Intro]\nhum", seed: 105, lufs: -16.0, bandwidth: 8000, dur: 6, ageDays: 1, abc: untimedABC, truncABC: bptr(false), truncSem: bptr(true)},
}

type fxDraft struct {
	reqID, label, group string
	seed                int64
}

var fxDrafts = []fxDraft{
	{"fxb_001_v1", "v1", "fxb_001", 201}, {"fxb_001_v2", "v2", "fxb_001", 202}, {"fxb_001_v3", "v3", "fxb_001", 203},
	{"fxb_002_v1", "v1", "fxb_002", 204}, {"fxb_002_v2", "v2", "fxb_002", 205}, {"fxb_002_v3", "v3", "fxb_002", 206},
}

type fxFile struct {
	path string
	data []byte
}

func sha(b []byte) string { return library.SHA256Hex(b) }

// trackFiles derives the deterministic media files of track index i.
func trackFiles(i int, t fxTrack) (master, mp3, peaks fxFile) {
	m := []byte(fmt.Sprintf("fixture-master-%d", i))
	p := []byte(fmt.Sprintf("fixture-mp3-%d", i))
	pk := analysis.Peaks{DurationS: t.dur,
		Overview: analysis.Waveform{Version: 2, Channels: 1, SampleRate: 48000, SamplesPerPixel: 141, Bits: 8, Length: 4, Data: []int8{-64, 64, -32, 32, -16, 16, -8, 8}},
		Detail:   analysis.Waveform{Version: 2, Channels: 1, SampleRate: 48000, SamplesPerPixel: 2400, Bits: 8, Length: 2, Data: []int8{-100, 100, -50, 50}}}
	pb, _ := json.Marshal(pk)
	pb = append(pb, '\n')
	ps, ks := sha(p), sha(pb)
	return fxFile{fmt.Sprintf("audio/masters/%s__%d__%s.flac", yue2.Slug(t.title), t.seed, "20261001-1200"), m},
		fxFile{fmt.Sprintf("derived/%s/%s.mp3", ps[:2], ps), p},
		fxFile{fmt.Sprintf("derived/%s/%s.peaks.json", ks[:2], ks), pb}
}

// WriteFiles writes the deterministic media and peaks files the fixture rows
// point at under dataDir.
func WriteFiles(tb testing.TB, dataDir string) {
	tb.Helper()
	for i, t := range fxTracks {
		m, p, k := trackFiles(i, t)
		for _, f := range []fxFile{m, p, k} {
			full := filepath.Join(dataDir, f.path)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				tb.Fatal(err)
			}
			if err := os.WriteFile(full, f.data, 0o644); err != nil {
				tb.Fatal(err)
			}
		}
	}
}

func mustParse(tb testing.TB, raw string) yue2.Parsed {
	tb.Helper()
	p, err := yue2.ParseRequest([]byte(raw), yue2.ParseOptions{})
	if err != nil {
		tb.Fatal(err)
	}
	return p
}

func reqJSON(id, style, lyrics string, seed int64) string {
	b, _ := json.Marshal(map[string]any{"id": id, "style": style, "lyrics": lyrics, "cot": "full", "seed": seed})
	return string(b)
}

// FixtureV2 is the M1 fixture body: it inserts 5 tracks (one aligned score, one untimed score, one without
// score, two siblings, one truncated), 6 drafts in 2 groups, the collection
// "pilot-x" (3 drafts), 1 completed import run and 1 device profile. No files
// are created; see WriteFiles.
func FixtureV2(tb testing.TB, st *store.Store, now time.Time) Fixtures {
	tb.Helper()
	ctx := context.Background()
	n := idBase
	svc := library.New(st, nil, func() time.Time { return now })
	svc.UseIDFunc(func() string { n++; return fmt.Sprintf("01900000-0000-7000-8000-%012x", n) })
	var fx Fixtures

	// import run first (recipe_sources reference it)
	report := `{"format":"mm.inventory/1","report_id":"` + RunID + `","errors":[{"path":"music/songs.jsonl","line":3,"code":"not_object","message":"line is not a JSON object"}],` +
		`"warnings":[{"path":"music/index.csv","line":0,"code":"bom"},{"path":"music/songs.jsonl","line":0,"code":"crlf"}],` +
		`"recipes":[{"request_id":"fx-luna","hash":"h1","placement":"track"}],"audio":[{"path":"music/flac/mystery.flac","status":"unmatched"},{"path":"music/flac/luna.flac","status":"new"}],` +
		`"drafts":[],"duplicates":[],"ignored":[]}`
	summary := `{"files":5,"errors":1,"warnings":2,"tracks_new":5}`
	mustExec(tb, st, `INSERT INTO import_runs(id, started_at, heartbeat_at, finished_at, state, principal, inbox_label, plan_digest, summary_json, report_json)
		VALUES (?,?,?,?, 'completed', 'cli', 'inbox', ?, ?, ?)`, RunID, now.Add(-time.Hour).UTC().Format(time.RFC3339), now.Add(-time.Hour).UTC().Format(time.RFC3339),
		now.Add(-50*time.Minute).UTC().Format(time.RFC3339), sha([]byte("fixture-plan")), summary, report)
	fx.Runs = []string{RunID}

	for i, t := range fxTracks {
		tx := begin(tb, st)
		p := mustParse(tb, reqJSON(t.requestID, t.style, t.lyrics, t.seed))
		rid, _, err := svc.InsertRecipe(ctx, tx, p)
		check(tb, err)
		fx.Recipes = append(fx.Recipes, rid)
		_, err = svc.InsertRecipeSource(ctx, tx, rid, library.SourceRef{Path: "music/songs.jsonl", Line: i + 1, Raw: p.Canonical, RawKind: "jsonl_line"}, RunID)
		check(tb, err)
		master, mp3, peaks := trackFiles(i, t)
		root := ""
		gen := 0
		if t.parent != "" {
			root, gen = t.parent, 1
		}
		nt := library.NewTake{
			ID: t.id, Title: t.title, Slug: yue2.Slug(t.title), RecipeID: rid, ParentTrackID: t.parent, RootTrackID: root, Generation: gen,
			DurationS: t.dur, SampleRate: 48000, BitsPerSample: 24, Channels: 2, TotalSamples: int64(t.dur * 48000),
			MasterPath: master.path, MasterSHA256: sha(master.data), MasterBytes: int64(len(master.data)),
			SourceSHA256: sha([]byte(fmt.Sprintf("fixture-source-%d", i))), FramesSHA256: sha([]byte(fmt.Sprintf("fixture-frames-%d", i))),
			AudioMD5: fmt.Sprintf("%032x", i+1), TruncatedABC: t.truncABC, TruncatedSemantic: t.truncSem,
			ImportRule: "slug", ImportRunID: RunID, PrincipalID: "cli", RecordedAt: now.Add(-time.Duration(t.ageDays) * 24 * time.Hour),
			Renditions: []library.NewRendition{
				{Kind: "mp3_320", SHA256: sha(mp3.data), Bytes: int64(len(mp3.data)), Path: mp3.path, Params: map[string]any{"encoder": "libmp3lame", "bitrate": "320k", "sample_rate": 48000, "gain_db": -0.6, "ffmpeg": "n9.0.1"}},
				{Kind: "peaks", SHA256: sha(peaks.data), Bytes: int64(len(peaks.data)), Path: peaks.path, Params: map[string]any{"format": analysis.PeaksFormat}},
			},
			Loudness:    analysis.Loudness{IntegratedLUFS: t.lufs, LRA: 5.5, LRALowLUFS: t.lufs - 5, LRAHighLUFS: t.lufs + 1, TruePeakDBTP: -1.2, SamplePeakDBFS: -1.5, ShortTermMaxLUFS: t.lufs + 2, ShortTerm1Hz: []float64{t.lufs, t.lufs + 0.5}},
			Spectrum:    analysis.Spectrum{BandwidthHz: &t.bandwidth, RefDB: -40, ThresholdDB: -110, Frames: 30},
			ToolVersion: "ffmpeg n9.0.1", ReplayGainDB: -4.0,
			Artifacts: []library.NewArtifact{{Name: "request.json", SHA256: sha(p.Canonical), Bytes: int64(len(p.Canonical)), Path: "takes/" + t.id + "/request.json"}},
		}
		if t.abc != "" {
			nt.ScoreABC = []byte(t.abc)
		}
		if t.parent != "" {
			nt.Mutation = &library.NewMutation{Operator: "inferred_sibling", Inferred: true, Rule: "same_request_id"}
		}
		check(tb, svc.InsertTake(ctx, tx, nt))
		check(tb, tx.Commit())
		fx.Tracks = append(fx.Tracks, t.id)
	}

	tx := begin(tb, st)
	var pilot []string
	for i, d := range fxDrafts {
		style, lyrics := "synth pop, 100 bpm", "[Verse 1]\nline one\n[Chorus]\nline two\n[Bridge]\nline three"
		p := mustParse(tb, reqJSON(d.reqID, style, lyrics, d.seed))
		rid, _, err := svc.InsertRecipe(ctx, tx, p)
		check(tb, err)
		fx.Recipes = append(fx.Recipes, rid)
		_, err = svc.InsertRecipeSource(ctx, tx, rid, library.SourceRef{Path: "music/backlog-variants.jsonl", Line: i + 1, Raw: p.Canonical, RawKind: "jsonl_line"}, RunID)
		check(tb, err)
		did, _, err := svc.InsertDraft(ctx, tx, library.NewDraft{Origin: "import_backlog", SourceRecipeID: rid, GroupKey: d.group, Label: d.label})
		check(tb, err)
		fx.Drafts = append(fx.Drafts, did)
		if i < 3 {
			pilot = append(pilot, did)
		}
		if i == 0 {
			check(tb, svc.InsertRefs(ctx, tx, rid, "music/index.csv", map[string]string{"variant_type": "slower", "source_title": "Synthetic Reference Title"}))
		}
	}
	for _, did := range pilot {
		check(tb, svc.AddToCollection(ctx, tx, did, collection, "music/pilot.json"))
	}
	check(tb, tx.Commit())

	label := "Test phone"
	_, err := svc.PutDeviceProfile(ctx, principalCLI(), library.DeviceProfileIn{DeviceID: DeviceID, Label: label, UserAgent: "FixtureAgent/1.0",
		Quality: "auto", QualitySource: "default", ProbeResult: "none", ProbeVersion: 1})
	check(tb, err)
	fx.Devices = []string{DeviceID}
	return fx
}

// WriteFixtureDB creates a database at path holding the fixture library plus
// the peaks and media files next to it; `make fixture-db` runs it.
func WriteFixtureDB(tb testing.TB, path string) {
	tb.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		tb.Fatal(err)
	}
	st, err := store.Open(context.Background(), path)
	if err != nil {
		tb.Fatal(err)
	}
	defer st.Close()
	Fixture(tb, st, FixedNow)
	WriteFiles(tb, filepath.Dir(path))
}

// FixedNow is the clock every fixture user shares.
var FixedNow = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// Sha is the hex sha256 of s, for tests that need unique addresses.
func Sha(s string) string { return sha([]byte(s)) }

// TrackIDForTest returns a fixed UUIDv7-shaped track id for tests that add
// tracks next to the fixture (n >= 1).
func TrackIDForTest(n int) string { return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0x7000+n) }
