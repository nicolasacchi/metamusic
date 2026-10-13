package library_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

func newEnvM2c(t *testing.T) *env { return newEnvWith(t, lt.FixtureM2c) }

func TestReferenceRequestID(t *testing.T) {
	t.Parallel()
	got := library.ReferenceRequestID("syn_ref_a", "abc")
	if want := "ref_syn_ref_a_" + hex.EncodeToString(func() []byte { h := sha256.Sum256([]byte("abc")); return h[:] }())[:8]; got != want || got != "ref_syn_ref_a_ba7816bf" {
		t.Fatalf("got %q want %q", got, want)
	}
	if again := library.ReferenceRequestID("syn_ref_a", "abc"); again != got {
		t.Errorf("unstable: %q vs %q", again, got)
	}
	if other := library.ReferenceRequestID("syn_ref_a", "abd"); other == got {
		t.Errorf("different audio keys must differ: %q", other)
	}
}

func TestInsertReferenceRecipe(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	tx, err := e.st.W.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	in := library.NewReferenceRecipe{Title: "Syn ref a", Slug: "syn_ref_a", AudioKey: "abc"}
	id, created, err := e.svc.InsertReferenceRecipe(ctx, tx, in)
	if err != nil || !created || id == "" {
		t.Fatalf("first: %q %v %v", id, created, err)
	}
	var js, engine, hash, core, reqID string
	if err := tx.QueryRowContext(ctx, `SELECT request_json, engine, hash, core_hash, request_id FROM recipes WHERE id = ?`, id).Scan(&js, &engine, &hash, &core, &reqID); err != nil {
		t.Fatal(err)
	}
	if js != `{"id":"ref_syn_ref_a_ba7816bf","seed":0,"title":"Syn ref a"}` || engine != "unknown" || hash != core || hash != library.SHA256Hex([]byte(js)) || reqID != "ref_syn_ref_a_ba7816bf" {
		t.Errorf("row: %s %s %s %s %s", js, engine, hash, core, reqID)
	}
	id2, created, err := e.svc.InsertReferenceRecipe(ctx, tx, in)
	if err != nil || created || id2 != id {
		t.Errorf("second: %q %v %v", id2, created, err)
	}
	long := library.NewReferenceRecipe{Title: strings.Repeat("é", 300), Slug: "long", AudioKey: "k"}
	lid, _, err := e.svc.InsertReferenceRecipe(ctx, tx, long)
	if err != nil {
		t.Fatal(err)
	}
	var title string
	if err := tx.QueryRowContext(ctx, `SELECT json_extract(request_json, '$.title') FROM recipes WHERE id = ?`, lid).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(title)); n != 200 {
		t.Errorf("title runes=%d, want 200", n)
	}
}

// refTake builds a reference take (FLAC-shaped unless lossy) on a fresh stub recipe.
func refTake(t *testing.T, e *env, tx *sql.Tx, n string, lossy bool) library.NewTake {
	t.Helper()
	slug := "ref_" + n
	rid, _, err := e.svc.InsertReferenceRecipe(context.Background(), tx, library.NewReferenceRecipe{Title: "Syn " + n, Slug: slug, AudioKey: "key-" + n})
	if err != nil {
		t.Fatal(err)
	}
	bw := 15000
	tk := library.NewTake{
		ID: "ref-take-" + n, Title: "Syn " + n, Slug: slug, RecipeID: rid, Reference: true, ImportRule: library.RuleReference, PrincipalID: "cli",
		DurationS: 5, SampleRate: 48000, BitsPerSample: 24, Channels: 2, TotalSamples: 240000,
		MasterPath: "audio/masters/" + slug + ".flac", MasterSHA256: lt.Sha("m" + n), MasterBytes: 10,
		SourceSHA256: lt.Sha("s" + n), FramesSHA256: lt.Sha("f" + n), AudioMD5: strings.Repeat("1", 32),
		RecordedAt: lt.FixedNow, Loudness: analysis.Loudness{IntegratedLUFS: -14}, Spectrum: analysis.Spectrum{BandwidthHz: &bw}, ToolVersion: "ffmpeg n9.0.1",
		Renditions: []library.NewRendition{{Kind: "peaks", SHA256: lt.Sha("p" + n), Bytes: 1, Path: "derived/p/" + n + ".peaks.json"}},
	}
	if lossy {
		m := lt.Sha("m" + n)
		tk.Lossy = true
		tk.MasterPath = "audio/masters/" + slug + ".mp3"
		tk.BitsPerSample, tk.AudioMD5 = 16, strings.Repeat("0", 32)
		tk.SourceSHA256, tk.FramesSHA256 = m, m
		tk.SampleRate = 44100
	}
	return tk
}

func count(t *testing.T, tx *sql.Tx, q string, a ...any) int {
	t.Helper()
	var n int
	if err := tx.QueryRowContext(context.Background(), q, a...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestInsertTake_Reference(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	tx, err := e.st.W.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()

	// a draft with the same request id as the stub must stay in the backlog
	tk := refTake(t, e, tx, "a", false)
	var reqID string
	if err := tx.QueryRowContext(ctx, `SELECT request_id FROM recipes WHERE id = ?`, tk.RecipeID).Scan(&reqID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO recipe_drafts(id, origin, request_id, request_json, status, created_at, updated_at)
		VALUES ('d-same', 'agent', ?, '{}', 'backlog', ?, ?)`, reqID, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	var out library.Fulfilment
	tk.Out = &out
	if err := e.svc.InsertTake(ctx, tx, tk); err != nil {
		t.Fatal(err)
	}
	if out.OwnDraftID != "" || len(out.CoreDrafts) != 0 {
		t.Errorf("a reference take must not fulfil drafts: %+v", out)
	}
	if n := count(t, tx, `SELECT count(*) FROM recipe_drafts WHERE id='d-same' AND status='backlog'`); n != 1 {
		t.Errorf("draft fulfilled")
	}
	for _, tbl := range []string{"sections", "scores", "artifacts", "track_style_terms"} {
		if n := count(t, tx, `SELECT count(*) FROM `+tbl+` WHERE track_id = ?`, tk.ID); n != 0 {
			t.Errorf("%s rows=%d, want 0", tbl, n)
		}
	}
	var style, lyrics string
	if err := tx.QueryRowContext(ctx, `SELECT style, lyrics FROM tracks_fts WHERE track_id = ?`, tk.ID).Scan(&style, &lyrics); err != nil || style != "" || lyrics != "" {
		t.Errorf("fts row style=%q lyrics=%q err=%v", style, lyrics, err)
	}
	if n := count(t, tx, `SELECT count(*) FROM tracks_fts WHERE tracks_fts MATCH 'title:syn'`); n != 1 {
		t.Errorf("fts title match=%d", n)
	}
	if n := count(t, tx, `SELECT lossy FROM tracks WHERE id = ?`, tk.ID); n != 0 {
		t.Errorf("lossy=%d for a FLAC reference", n)
	}

	// rejected shapes
	bad := refTake(t, e, tx, "b", false)
	bad.ScoreABC = []byte("X:1\n")
	var ve *library.ValidationError
	if err := e.svc.InsertTake(ctx, tx, bad); !errors.As(err, &ve) {
		t.Errorf("score: %v", err)
	}
	bad = refTake(t, e, tx, "c", false)
	bad.Artifacts = []library.NewArtifact{{Name: "x", SHA256: "s", Path: "p", Bytes: 1}}
	if err := e.svc.InsertTake(ctx, tx, bad); !errors.As(err, &ve) {
		t.Errorf("artifacts: %v", err)
	}
	bad = refTake(t, e, tx, "d", true)
	bad.Renditions = append(bad.Renditions, library.NewRendition{Kind: "mp3_320", SHA256: "x", Bytes: 1, Path: "x.mp3"})
	if err := e.svc.InsertTake(ctx, tx, bad); !errors.As(err, &ve) || ve.Field != "rendition" {
		t.Errorf("lossy mp3 rendition: %v", err)
	}
	bad = refTake(t, e, tx, "d2", true)
	bad.BitsPerSample = 24
	if err := e.svc.InsertTake(ctx, tx, bad); !errors.As(err, &ve) {
		t.Errorf("lossy bits: %v", err)
	}
	bad = refTake(t, e, tx, "d3", false)
	bad.ImportRule = "slug"
	if err := e.svc.InsertTake(ctx, tx, bad); !errors.As(err, &ve) {
		t.Errorf("rule mismatch: %v", err)
	}

	// lossy insert
	ok := refTake(t, e, tx, "e", true)
	if err := e.svc.InsertTake(ctx, tx, ok); err != nil {
		t.Fatal(err)
	}
	if n := count(t, tx, `SELECT lossy FROM tracks WHERE id = ?`, ok.ID); n != 1 {
		t.Errorf("lossy=%d", n)
	}
}

func TestRecipeKnown_Predicate(t *testing.T) {
	t.Parallel()
	e := newEnvM2c(t)
	ctx := context.Background()
	for id, want := range map[string]bool{lt.TrackLuna: true, lt.TrackRefFlac: false, lt.TrackLossy: false} {
		got, err := e.svc.RecipeKnown(ctx, id)
		if err != nil || got != want {
			t.Errorf("%s: %v %v, want %v", id, got, err, want)
		}
	}
	if _, err := e.svc.RecipeKnown(ctx, "nope"); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("unknown id: %v", err)
	}
	if !library.RecipeKnownEngine("yue2") || library.RecipeKnownEngine(library.EngineUnknown) {
		t.Error("RecipeKnownEngine")
	}
}

func TestRequireRecipeKnown(t *testing.T) {
	t.Parallel()
	e := newEnvM2c(t)
	ctx := context.Background()
	if err := e.svc.RequireRecipeKnown(ctx, lt.TrackLuna); err != nil {
		t.Errorf("known: %v", err)
	}
	err := e.svc.RequireRecipeKnown(ctx, lt.TrackRefFlac)
	var ru *library.RecipeUnknownError
	if !errors.Is(err, library.ErrRecipeUnknown) || !errors.As(err, &ru) || ru.TrackID != lt.TrackRefFlac {
		t.Errorf("reference: %v", err)
	}
	if err := e.svc.RequireRecipeKnown(ctx, "nope"); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("unknown id: %v", err)
	}
}

func TestLosslessMaster_Predicate(t *testing.T) {
	t.Parallel()
	e := newEnvM2c(t)
	ctx := context.Background()
	for id, want := range map[string]bool{lt.TrackLuna: true, lt.TrackRefFlac: true, lt.TrackLossy: false} {
		got, err := e.svc.LosslessMaster(ctx, id)
		if err != nil || got != want {
			t.Errorf("%s: %v %v, want %v", id, got, err, want)
		}
	}
	if _, err := e.svc.LosslessMaster(ctx, "nope"); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("unknown id: %v", err)
	}
}

func TestRequireLosslessMaster(t *testing.T) {
	t.Parallel()
	e := newEnvM2c(t)
	ctx := context.Background()
	if err := e.svc.RequireLosslessMaster(ctx, lt.TrackRefFlac); err != nil {
		t.Errorf("lossless reference: %v", err)
	}
	err := e.svc.RequireLosslessMaster(ctx, lt.TrackLossy)
	var lm *library.LossyMasterError
	if !errors.Is(err, library.ErrLossyMaster) || !errors.As(err, &lm) || lm.TrackID != lt.TrackLossy {
		t.Errorf("lossy: %v", err)
	}
}

func TestSnapshot_ReferenceAndLossy(t *testing.T) {
	t.Parallel()
	e := newEnvM2c(t)
	snap, err := e.svc.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var lossy, ref int
	for _, tr := range snap.Tracks {
		if tr.Lossy {
			lossy++
			if snap.AudioIDs["mp3:"+tr.MasterSHA256] != tr.ID {
				t.Errorf("lossy audio id missing for %s", tr.ID)
			}
		}
		if tr.Reference {
			ref++
		}
	}
	unknown := 0
	for _, r := range snap.Recipes {
		if r.Engine == "unknown" {
			unknown++
		}
	}
	if lossy != 1 || ref != 2 || unknown != 2 {
		t.Errorf("lossy=%d reference=%d unknown recipes=%d", lossy, ref, unknown)
	}
}
