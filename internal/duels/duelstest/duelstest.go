// Package duelstest is TEST-ONLY: a deterministic duels fixture with
// distinctive values for blindness and redaction tests. Import it only from
// _test.go files and servertest (see TestNoTestHelpersInBinary).
package duelstest

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
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/clip/cliptest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Fixed ids (UUIDv7-shaped; the 0xa.. range is free of librarytest's and the rig's sequences).
const (
	TrackAlpha = "01900000-0000-7000-8000-000000000a01" // "Blindtest Alpha", seed 7340021, 61.3 s
	TrackBeta  = "01900000-0000-7000-8000-000000000a02" // "Blindtest Beta", seed 7340022, 66.6 s; inferred sibling of Alpha

	DuelOpen    = "01900000-0000-7000-8000-000000000b01" // blind, open; slot A = Alpha, slot B = Beta
	DuelDecided = "01900000-0000-7000-8000-000000000b02" // blind, swapped (A = Beta); human verdict "a", critic verdict "b"
	DuelSighted = "01900000-0000-7000-8000-000000000b03" // non-blind, decided by a device verdict "tie"
	DuelExpired = "01900000-0000-7000-8000-000000000b04" // blind, no verdict, expired

	VerdictHuman  = "01900000-0000-7000-8000-000000000c01" // human:fixture on DuelDecided
	VerdictCritic = "01900000-0000-7000-8000-000000000c02" // agent:fixture-agent on DuelDecided
	VerdictDevice = "01900000-0000-7000-8000-000000000c03" // the paired device on DuelSighted

	// Distinctive values redaction tests look for.
	TitleAlpha, TitleBeta = "Blindtest Alpha", "Blindtest Beta"
	RequestAlpha          = "syn-blind"
	RequestBeta           = "syn-blind-s7340022"
	SeedAlpha, SeedBeta   = int64(7340021), int64(7340022)
	DurAlpha, DurBeta     = 61.3, 66.6
	GainAcdB, GainBcdB    = -737, 0
	LUFSA, LUFSB          = -12.63, -20.00
)

// Duels lists what Fixture created.
type Duels struct {
	Alpha, Beta                         string
	Open, Decided, Sighted, Expired     string
	HumanVerdict, CriticVerdict, Device string
}

func rfc(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func must(tb testing.TB, err error) {
	tb.Helper()
	if err != nil {
		tb.Fatal(err)
	}
}

// Master bytes are fixed so their sha256 is known to the clip fakes.
func masterBytes(name string) []byte { return []byte("duelstest-master-" + name) }

// Fixture inserts the two Blindtest tracks and four duels into a database that
// already holds librarytest.Fixture (it is not part of the rig's cached
// template). When dataDir is not empty the two master files are written under it,
// so a real clip.Service with cliptest fakes can link them. now is the rig clock.
func Fixture(tb testing.TB, st *store.Store, dataDir string, fx lt.Fixtures, now time.Time, newID func() string) Duels {
	tb.Helper()
	ctx := context.Background()
	if newID == nil {
		n := 0xa00
		newID = func() string { n++; return fmt.Sprintf("01900000-0000-7000-8000-%012x", n+0x1000) }
	}
	svc := library.New(st, nil, func() time.Time { return now })
	svc.UseIDFunc(newID)

	type tk struct {
		id, title, reqID, name, parent string
		seed                           int64
		dur                            float64
	}
	for i, t := range []tk{
		{TrackAlpha, TitleAlpha, RequestAlpha, "alpha", "", SeedAlpha, DurAlpha},
		{TrackBeta, TitleBeta, RequestBeta, "beta", TrackAlpha, SeedBeta, DurBeta},
	} {
		tx, err := st.W.BeginTx(ctx, nil)
		must(tb, err)
		raw, _ := json.Marshal(map[string]any{"id": t.reqID, "style": "synthetic, duel fixture", "lyrics": "[Verse]\nla la\n[Chorus]\nsing it", "cot": "full", "seed": t.seed})
		p, err := yue2.ParseRequest(raw, yue2.ParseOptions{})
		must(tb, err)
		rid, _, err := svc.InsertRecipe(ctx, tx, p)
		must(tb, err)
		_, err = svc.InsertRecipeSource(ctx, tx, rid, library.SourceRef{Path: "music/songs.jsonl", Line: 100 + i, Raw: p.Canonical, RawKind: "jsonl_line"}, lt.RunID)
		must(tb, err)
		master := masterBytes(t.name)
		path := fmt.Sprintf("audio/masters/%s__%d__20261001-1200.flac", t.name, t.seed)
		if dataDir != "" {
			full := filepath.Join(dataDir, path)
			must(tb, os.MkdirAll(filepath.Dir(full), 0o750))
			must(tb, os.WriteFile(full, master, 0o600))
		}
		bw := 16000
		root, gen := "", 0
		if t.parent != "" {
			root, gen = t.parent, 1
		}
		nt := library.NewTake{
			ID: t.id, Title: t.title, Slug: yue2.Slug(t.title), RecipeID: rid, ParentTrackID: t.parent, RootTrackID: root, Generation: gen,
			DurationS: t.dur, SampleRate: 48000, BitsPerSample: 24, Channels: 2, TotalSamples: int64(t.dur * 48000),
			MasterPath: path, MasterSHA256: library.SHA256Hex(master), MasterBytes: int64(len(master)),
			SourceSHA256: lt.Sha("duelstest-source-" + t.name), FramesSHA256: lt.Sha("duelstest-frames-" + t.name), AudioMD5: fmt.Sprintf("%032x", 0xd0+i),
			ImportRule: "slug", ImportRunID: lt.RunID, PrincipalID: "cli", RecordedAt: now.Add(-time.Duration(60-i) * time.Hour),
			Loudness:    analysis.Loudness{IntegratedLUFS: -14, LRA: 5, LRALowLUFS: -19, LRAHighLUFS: -13, TruePeakDBTP: -1.2, SamplePeakDBFS: -1.5, ShortTermMaxLUFS: -12, ShortTerm1Hz: []float64{-14}},
			Spectrum:    analysis.Spectrum{BandwidthHz: &bw, RefDB: -40, ThresholdDB: -110, Frames: 30},
			ToolVersion: "ffmpeg n9.0.1", ReplayGainDB: -4,
		}
		if t.parent != "" {
			nt.Mutation = &library.NewMutation{Operator: "inferred_sibling", Inferred: true, Rule: "same_request_id"}
		}
		must(tb, svc.InsertTake(ctx, tx, nt))
		must(tb, tx.Commit())
	}

	exec := func(q string, a ...any) {
		tb.Helper()
		if _, err := st.W.ExecContext(ctx, q, a...); err != nil {
			tb.Fatal(err)
		}
	}
	const duelSQL = `INSERT INTO duels(id, slot_a_track_id, slot_b_track_id, swapped, blind, from_ms, to_ms, clip_def, tool_version, match, target_lufs,
		gain_a_cdb, gain_b_cdb, window_lufs_a, window_lufs_b, window_tp_a, window_tp_b, created_by, created_by_kind, created_at, expires_at)
		VALUES (?,?,?,?,?,?,?, 'mm-clip/1', ?, 'matched', ?, ?,?,?,?,?,?, ?,?,?,?)`
	day := 24 * time.Hour
	duel := func(id, a, b string, swapped, blind int, createdAt time.Time, by, kind string, ga, gb int, la, lb float64) {
		exec(duelSQL, id, a, b, swapped, blind, 16300, 61300, cliptest.Tool, min(la, lb), ga, gb, la, lb, -1.0, -2.0, by, kind, rfc(createdAt), rfc(createdAt.Add(30*day)))
	}
	duel(DuelExpired, TrackAlpha, TrackBeta, 0, 1, now.Add(-40*day), lt.FixtureHuman, "human", GainAcdB, GainBcdB, LUFSA, LUFSB)
	duel(DuelDecided, TrackBeta, TrackAlpha, 1, 1, now.Add(-3*day), lt.FixtureHuman, "human", GainBcdB, GainAcdB, LUFSB, LUFSA)
	duel(DuelSighted, TrackAlpha, TrackBeta, 0, 0, now.Add(-2*day), lt.FixtureAgent, "agent", GainAcdB, GainBcdB, LUFSA, LUFSB)
	duel(DuelOpen, TrackAlpha, TrackBeta, 0, 1, now.Add(-time.Hour), lt.FixtureHuman, "human", GainAcdB, GainBcdB, LUFSA, LUFSB)

	const verdictSQL = `INSERT INTO duel_verdicts(id, duel_id, source, principal_kind, principal_id, verdict, winner_track_id, note, latency_ms, switches, saw_reveal, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`
	// DuelDecided: slot A holds Beta (swapped), so "a" crowns Beta and the critic's "b" crowns Alpha.
	exec(verdictSQL, VerdictHuman, DuelDecided, "human", "human", lt.FixtureHuman, "a", TrackBeta, "synthetic note", 8200, 6, 0, rfc(now.Add(-3*day+time.Minute)))
	exec(verdictSQL, VerdictCritic, DuelDecided, "critic", "agent", lt.FixtureAgent, "b", TrackAlpha, nil, nil, nil, 1, rfc(now.Add(-3*day+2*time.Minute)))
	exec(verdictSQL, VerdictDevice, DuelSighted, "human", "device", lt.FixtureDev, "tie", nil, nil, 4100, 2, 1, rfc(now.Add(-2*day+time.Minute)))

	return Duels{Alpha: TrackAlpha, Beta: TrackBeta, Open: DuelOpen, Decided: DuelDecided, Sighted: DuelSighted, Expired: DuelExpired,
		HumanVerdict: VerdictHuman, CriticVerdict: VerdictCritic, Device: VerdictDevice}
}
