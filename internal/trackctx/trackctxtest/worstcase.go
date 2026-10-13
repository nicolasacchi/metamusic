// Package trackctxtest seeds the pathological track of the /context budget
// tests (C35). It is test-only: only _test.go files import it.
package trackctxtest

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Sizes of the worst case.
const (
	LyricsChars  = 12000
	Comments     = 300
	CommentChars = 4000
	Children     = 50
)

// Hostile is n characters that all expand under HTML escaping (six characters each on the wire).
func Hostile(n int) string {
	return strings.Repeat("<>&", n/3+1)[:n]
}

// ID returns the id of seeded track n.
func ID(n int) string { return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0x7a00+n) }

// WorstCase inserts one track (ID(0)) with 12 000 hostile lyric characters,
// 300 hostile 4 000-character comments and 50 children.
func WorstCase(tb testing.TB, st *store.Store, now time.Time) string {
	tb.Helper()
	ctx := context.Background()
	lib := library.New(st, nil, func() time.Time { return now })
	lyrics := "[Verse 1]\n" + Hostile(LyricsChars-10)
	take := func(n int, parent string) {
		lyrics := lyrics
		if n > 0 {
			lyrics = "[Verse 1]\nshort"
		}
		p, err := yue2.ParseRequest([]byte(fmt.Sprintf(`{"id":"wc-%d","style":"pop","lyrics":%q,"cot":"full","seed":%d}`, n, lyrics, 8000+n)), yue2.ParseOptions{})
		if err != nil {
			tb.Fatal(err)
		}
		tx, err := st.W.BeginTx(ctx, nil)
		if err != nil {
			tb.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		rid, _, err := lib.InsertRecipe(ctx, tx, p)
		if err != nil {
			tb.Fatal(err)
		}
		bw := 16000
		sha := func(s string) string { return library.SHA256Hex([]byte(s)) }
		nt := library.NewTake{
			ID: ID(n), Title: fmt.Sprintf("Worst %03d", n), Slug: fmt.Sprintf("worst_%03d", n), RecipeID: rid, ParentTrackID: parent, RootTrackID: parent,
			DurationS: 60, SampleRate: 48000, BitsPerSample: 24, Channels: 2, TotalSamples: 60 * 48000,
			MasterPath: fmt.Sprintf("audio/masters/worst_%03d.flac", n), MasterSHA256: sha(fmt.Sprintf("wm%d", n)), MasterBytes: 10,
			SourceSHA256: sha(fmt.Sprintf("ws%d", n)), FramesSHA256: sha(fmt.Sprintf("wf%d", n)), AudioMD5: strings.Repeat("0", 32),
			ImportRule: "slug", PrincipalID: "cli", RecordedAt: now.Add(-time.Duration(n) * time.Minute),
			Loudness: analysis.Loudness{IntegratedLUFS: -14}, Spectrum: analysis.Spectrum{BandwidthHz: &bw}, ToolVersion: "ffmpeg n9.0.1",
			Renditions: []library.NewRendition{{Kind: "mp3_320", SHA256: sha(fmt.Sprintf("wmp3%d", n)), Bytes: 10, Path: fmt.Sprintf("audio/mp3/worst_%03d.mp3", n),
				Params: map[string]any{"gain_db": -0.5}}},
		}
		if parent != "" {
			nt.Generation = 1
			nt.Mutation = &library.NewMutation{Operator: "inferred_sibling", Inferred: true, Rule: "same_request_id"}
		}
		if err := lib.InsertTake(ctx, tx, nt); err != nil {
			tb.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			tb.Fatal(err)
		}
	}
	take(0, "")
	for i := 1; i <= Children; i++ {
		take(i, ID(0))
	}
	at := now.Add(-time.Hour).UTC().Format(time.RFC3339)
	for i := range Comments {
		kind := "note"
		if i%7 == 0 {
			kind = "fix"
		}
		if _, err := st.W.ExecContext(ctx, `INSERT INTO comments(id, track_id, body, kind, state, source, principal_kind, principal_id, principal_name, created_at, updated_at)
			VALUES (?,?,?,?,'open','api','agent','agent:wc','wc',?,?)`, fmt.Sprintf("01900000-0000-7000-8000-%012x", 0x7b00+i), ID(0), Hostile(CommentChars), kind,
			now.Add(-time.Duration(Comments-i)*time.Minute).UTC().Format(time.RFC3339), at); err != nil {
			tb.Fatal(err)
		}
	}
	return ID(0)
}
