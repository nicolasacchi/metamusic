package ears_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/ears/earstest"
	"github.com/nicolasacchi/metamusic/internal/ears/tierb"
	"github.com/nicolasacchi/metamusic/internal/ears/tierb/tierbtest"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// lunaLyrics are the lyrics of the fixture's Luna Park (three sung sections, plan-timed through its score).
const lunaLyrics = "[Verse 1]\nla la luna\n[Chorus]\nsing it loud\n[Outro]\nfade away"

// putDoc stores an analysis document of a track as an artifact (file under the data root, immutable row), as the ingest bridge
// does for a render with the ears capability, and returns its sha256.
func (r *rig) putDoc(trackID, name string, doc []byte, age time.Duration) string {
	r.t.Helper()
	dir := "takes/" + trackID
	if err := r.root.MkdirAll(dir, 0o750); err != nil {
		r.t.Fatal(err)
	}
	if err := r.root.WriteFile(dir+"/"+name, doc, 0o444); err != nil {
		r.t.Fatal(err)
	}
	sum := sha256.Sum256(doc)
	sha := hex.EncodeToString(sum[:])
	r.exec(`INSERT INTO artifacts(track_id, name, sha256, bytes, path, created_at) VALUES (?,?,?,?,?,?)`,
		trackID, name, sha, len(doc), dir+"/"+name, store.FormatTime(r.now.Add(-age)))
	return sha
}

func (r *rig) analyze(trackID string) ears.TierBOutcome {
	r.t.Helper()
	out, err := r.svc.AnalyzeTierB(context.Background(), trackID)
	if err != nil {
		r.t.Fatalf("AnalyzeTierB: %v", err)
	}
	return out
}

func (r *rig) tierBRows(trackID string) []string {
	r.t.Helper()
	rows, err := r.st.R.Query(`SELECT analyzer || ' ' || version FROM analyses WHERE track_id = ? AND tier = 'B' ORDER BY analyzer, version`, trackID)
	if err != nil {
		r.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			r.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// TestAnalyzeTierB_Stores: a faithful recognition of Luna's lyrics writes the three kinds of result, the take reads Tier B
// afterwards, its sections are measured, and get_track_tier_b's payload carries the numbers.
func TestAnalyzeTierB_Stores(t *testing.T) {
	t.Parallel()
	r := newRig(t, withTierARows())
	r.putDoc(lt.TrackLuna, "analysis.json", tierbtest.FromLyrics(lunaLyrics, tierbtest.Opts{}), time.Hour)
	out := r.analyze(lt.TrackLuna)
	if out.Status != "ok" || out.Measured != "written" || out.Sections != 3 {
		t.Fatalf("outcome = %+v, want ok, written, 3 sections", out)
	}
	rows := r.tierBRows(lt.TrackLuna)
	if len(rows) != 2 || !strings.HasPrefix(rows[0], "tier_b_diction mm-diction/1 go1") ||
		!strings.HasPrefix(rows[1], "tier_b_raw mm-tierb-raw/1 faster-whisper-large-v3@bbbbbbbbbbbb ") {
		t.Fatalf("rows = %q", rows)
	}
	ctx := context.Background()
	d, err := r.svc.Dossier(ctx, lt.TrackLuna)
	if err != nil {
		t.Fatal(err)
	}
	if d.Tier != "B" || !d.Complete {
		t.Errorf("dossier tier %q complete %v, want B true (complete keeps its Tier A meaning)", d.Tier, d.Complete)
	}
	if c, err := r.svc.Dossier(ctx, lt.TrackCitta); err != nil || c.Tier != "A" {
		t.Errorf("a take without a listening check stays tier A: %q, %v", c.Tier, err)
	}
	secs, err := r.lib.Sections(ctx, lt.TrackLuna)
	if err != nil || secs.Source != "measured" || len(secs.Timeline) != 3 {
		t.Fatalf("sections after the analysis: %+v, %v", secs, err)
	}
	tb, err := r.svc.TierB(ctx, lt.TrackLuna)
	if err != nil {
		t.Fatal(err)
	}
	if tb.State != "ok" || tb.Status != "ok" || tb.Language != "it" || !tb.Forced || tb.Diction == nil || tb.Diction.Overall.CER != 0 ||
		len(tb.Diction.Sections) != 3 || tb.Diction.WorstSection == nil || len(tb.MeasuredSections) != 3 || tb.Adherence != nil ||
		tb.Artifact.Bytes == 0 || len(tb.Artifact.SHA256) != 64 || tb.Models.ASR == nil {
		t.Errorf("TierB = %+v", tb)
	}
	if got := tb.Diction.Sections[0]; got.Label != "verse" || got.RefWords != 3 || got.LineCount != 1 {
		t.Errorf("first diction section = %+v", got)
	}
	// the Ears flags carry no ear check for a clean recognition
	for _, f := range tb.Flags {
		if f.Code == "diction_poor" || f.Code == "alignment_failed" || f.Code == "listening_check_failed" {
			t.Errorf("unexpected flag %s", f.Code)
		}
	}
	// Tier B facts reword the two blind spots they replace
	if !slicesContainsPrefix(tb.BlindSpots, "diction measured by speech recognition") {
		t.Errorf("blind spots = %q", tb.BlindSpots)
	}
	// perception shows the diction block and no adherence
	p, err := r.svc.Perception(ctx, lt.TrackLuna)
	if err != nil || p.Tier != "B" || p.Diction == nil || p.Adherence != nil || p.Diction.CER != 0 {
		t.Errorf("perception = %+v, %v", p, err)
	}
	if p, _ := r.svc.Perception(ctx, lt.TrackCitta); p.Diction != nil || p.Adherence != nil {
		t.Errorf("perception without a listening check = %+v", p)
	}
}

func slicesContainsPrefix(ss []string, prefix string) bool {
	for _, s := range ss {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

// TestAnalyzeTierB_Idempotent: a second call writes nothing new, and measured sections are written once.
func TestAnalyzeTierB_Idempotent(t *testing.T) {
	t.Parallel()
	r := newRig(t, withTierARows())
	r.putDoc(lt.TrackLuna, "analysis.json", tierbtest.FromLyrics(lunaLyrics, tierbtest.Opts{}), time.Hour)
	first := r.analyze(lt.TrackLuna)
	before := r.tierBRows(lt.TrackLuna)
	second := r.analyze(lt.TrackLuna)
	if len(first.Wrote) != 2 || len(second.Wrote) != 0 {
		t.Errorf("wrote %v then %v, want 2 rows then none", first.Wrote, second.Wrote)
	}
	if diff := cmp.Diff(before, r.tierBRows(lt.TrackLuna)); diff != "" {
		t.Errorf("rows changed (-before +after):\n%s", diff)
	}
	if n := r.count(`SELECT count(*) FROM sections WHERE track_id = ? AND source = 'measured'`, lt.TrackLuna); n != 3 {
		t.Errorf("measured rows = %d, want 3", n)
	}
}

// TestAnalyzeTierB_ErrorDocumentRawOnly: a document with status error stores the raw row only; the state is error, the flag
// carries the code, the tier stays A and no section is measured.
func TestAnalyzeTierB_ErrorDocumentRawOnly(t *testing.T) {
	t.Parallel()
	r := newRig(t, withTierARows())
	r.putDoc(lt.TrackLuna, "analysis.json", tierbtest.FromLyrics(lunaLyrics, tierbtest.Opts{Status: "error", Code: "OOM"}), time.Hour)
	out := r.analyze(lt.TrackLuna)
	if out.Status != "error" || len(out.Wrote) != 1 || out.Wrote[0] != "tier_b_raw" || out.Measured != "none" {
		t.Fatalf("outcome = %+v", out)
	}
	ctx := context.Background()
	st, err := r.svc.TierBState(ctx, lt.TrackLuna)
	if err != nil || st != "skipped" && st != "error" {
		t.Fatalf("state = %q, %v", st, err)
	}
	d, err := r.svc.Dossier(ctx, lt.TrackLuna)
	if err != nil || d.Tier != "A" {
		t.Fatalf("tier = %q, %v; a failed listening check does not raise it", d.Tier, err)
	}
	var found bool
	for _, f := range d.Flags {
		if f.Code == "listening_check_failed" {
			found = true
			if f.Severity != "info" || f.Evidence["code"] != "OOM" {
				t.Errorf("flag = %+v", f)
			}
		}
	}
	if !found {
		t.Errorf("flags = %v, want listening_check_failed", d.Flags)
	}
	if n := r.count(`SELECT count(*) FROM sections WHERE track_id = ? AND source = 'measured'`, lt.TrackLuna); n != 0 {
		t.Errorf("measured rows = %d", n)
	}
	tb, err := r.svc.TierB(ctx, lt.TrackLuna)
	if err != nil || tb.Status != "error" || tb.Code == nil || *tb.Code != "OOM" || tb.Diction != nil || tb.MeasuredSections != nil {
		t.Errorf("TierB = %+v, %v", tb, err)
	}
}

// TestAnalyzeTierB_NoLyricsNoDiction: a recipe without lyric lines gets the raw row only: nothing to compare, nothing to place.
func TestAnalyzeTierB_NoLyricsNoDiction(t *testing.T) {
	t.Parallel()
	r := newRig(t, withTierARows())
	r.dropTriggers("recipes_immutable")
	r.exec(`UPDATE recipes SET lyrics = '[Intro]
[Outro]' WHERE id = (SELECT recipe_id FROM tracks WHERE id = ?)`, lt.TrackLuna)
	r.putDoc(lt.TrackLuna, "analysis.json", tierbtest.FromLyrics("some words that were heard", tierbtest.Opts{}), time.Hour)
	out := r.analyze(lt.TrackLuna)
	if len(out.Wrote) != 1 || out.Wrote[0] != "tier_b_raw" || out.Measured != "none" {
		t.Fatalf("outcome = %+v", out)
	}
	tb, err := r.svc.TierB(context.Background(), lt.TrackLuna)
	if err != nil || tb.Diction != nil || tb.State != "ok" {
		t.Errorf("TierB = %+v, %v", tb, err)
	}
}

// TestAnalyzeTierB_ExistingMeasuredKept: a take that already has measured sections keeps them; the diction row says so.
func TestAnalyzeTierB_ExistingMeasuredKept(t *testing.T) {
	t.Parallel()
	r := newRig(t, withTierARows())
	r.exec(`INSERT INTO sections(track_id, source, idx, kind, label, raw_label, start_s, end_s, confidence) VALUES (?, 'measured', 0, 'verse', 'verse', '[Verse]', 0, 5, 0.9)`, lt.TrackLuna)
	r.putDoc(lt.TrackLuna, "analysis.json", tierbtest.FromLyrics(lunaLyrics, tierbtest.Opts{}), time.Hour)
	if out := r.analyze(lt.TrackLuna); out.Measured != "kept" || out.Sections != 0 {
		t.Fatalf("outcome = %+v", out)
	}
	if n := r.count(`SELECT count(*) FROM sections WHERE track_id = ? AND source = 'measured'`, lt.TrackLuna); n != 1 {
		t.Errorf("measured rows = %d, want the single existing one", n)
	}
}

// TestAnalyzeTierB_AlignmentFailed: words that match nothing leave the sections alone and raise alignment_failed.
func TestAnalyzeTierB_AlignmentFailed(t *testing.T) {
	t.Parallel()
	r := newRig(t, withTierARows())
	subs := map[string]string{"la": "zzzz", "luna": "qqqq", "sing": "xxxx", "it": "yyyy", "loud": "wwww", "fade": "vvvv", "away": "uuuu"}
	r.putDoc(lt.TrackLuna, "analysis.json", tierbtest.FromLyrics(lunaLyrics, tierbtest.Opts{Subs: subs}), time.Hour)
	out := r.analyze(lt.TrackLuna)
	if out.Measured != "failed" {
		t.Fatalf("outcome = %+v", out)
	}
	d, err := r.svc.Dossier(context.Background(), lt.TrackLuna)
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	for _, f := range d.Flags {
		codes = append(codes, f.Code)
	}
	if !contains(codes, "alignment_failed") {
		t.Errorf("flags = %v, want alignment_failed", codes)
	}
	if secs, _ := r.lib.Sections(context.Background(), lt.TrackLuna); secs.Source != "plan" {
		t.Errorf("sections source = %q, want the plan timing kept", secs.Source)
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// TestAnalyzeTierB_Errors: no document, a corrupt one, a changed one and an unknown track each fail without writing.
func TestAnalyzeTierB_Errors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := newRig(t, withTierARows())
	if _, err := r.svc.AnalyzeTierB(ctx, lt.TrackLuna); !errors.Is(err, ears.ErrNoTierBDocument) {
		t.Errorf("no document: %v", err)
	}
	if _, err := r.svc.AnalyzeTierB(ctx, "no-such-track"); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("unknown track: %v", err)
	}
	r.putDoc(lt.TrackLuna, "analysis.json", []byte(`{"contract":"nope"}`), time.Hour)
	if _, err := r.svc.AnalyzeTierB(ctx, lt.TrackLuna); !errors.Is(err, ears.ErrBadTierBDocument) {
		t.Errorf("corrupt document: %v", err)
	}
	r.putDoc(lt.TrackCitta, "analysis.json", tierbtest.FromLyrics("è sera", tierbtest.Opts{}), time.Hour)
	if err := r.root.Remove("takes/" + lt.TrackCitta + "/analysis.json"); err != nil {
		t.Fatal(err)
	}
	if err := r.root.WriteFile("takes/"+lt.TrackCitta+"/analysis.json", []byte("changed"), 0o444); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.AnalyzeTierB(ctx, lt.TrackCitta); !errors.Is(err, ears.ErrBadTierBDocument) {
		t.Errorf("changed document: %v", err)
	}
	r.putDoc(lt.TrackTwinA, "analysis.json", []byte(strings.Repeat(" ", tierb.MaxDocBytes+1)), time.Hour)
	if _, err := r.svc.AnalyzeTierB(ctx, lt.TrackTwinA); !errors.Is(err, ears.ErrBadTierBDocument) {
		t.Errorf("oversized document: %v", err)
	}
	for _, id := range []string{lt.TrackLuna, lt.TrackCitta, lt.TrackTwinA} {
		if rows := r.tierBRows(id); len(rows) != 0 {
			t.Errorf("%s rows after failures = %q", id, rows)
		}
	}
	if _, err := ears.New(ears.Options{}).AnalyzeTierB(ctx, lt.TrackLuna); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("unwired service: %v", err)
	}
}

// TestAnalyzeTierB_NewestDocumentWins: of two stored documents the newest by created_at is analysed.
func TestAnalyzeTierB_NewestDocumentWins(t *testing.T) {
	t.Parallel()
	r := newRig(t, withTierARows())
	r.putDoc(lt.TrackLuna, "analysis.json", tierbtest.FromLyrics(lunaLyrics, tierbtest.Opts{Lang: "auto"}), 2*time.Hour)
	r.putDoc(lt.TrackLuna, "analysis-2.json", tierbtest.FromLyrics(lunaLyrics, tierbtest.Opts{Lang: "it"}), time.Hour)
	r.analyze(lt.TrackLuna)
	tb, err := r.svc.TierB(context.Background(), lt.TrackLuna)
	if err != nil || tb.Language != "it" || !tb.Forced {
		t.Errorf("TierB language %q forced %v (%v), want the newer document", tb.Language, tb.Forced, err)
	}
}

// TestTierB_AnalysisDoesNotDependOnTierA: a take with no Tier A rows can still be analysed and read.
func TestTierB_AnalysisDoesNotDependOnTierA(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.putDoc(lt.TrackLuna, "analysis.json", tierbtest.FromLyrics(lunaLyrics, tierbtest.Opts{}), time.Hour)
	r.analyze(lt.TrackLuna)
	d, err := r.svc.Dossier(context.Background(), lt.TrackLuna)
	if err != nil || d.Tier != "0" {
		t.Errorf("tier = %q, %v; Tier A is not complete, so B is not reached", d.Tier, err)
	}
}

var _ = earstest.ClipTrack

// TestGETNeverWritesDB_TierB: every read of the listening check, on a take with a stored document that nobody analysed yet, and
// on one analysed, leaves analyses, sections, artifacts and jobs exactly as they were.
func TestGETNeverWritesDB_TierB(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := newRig(t, withTierARows())
	r.putDoc(lt.TrackLuna, "analysis.json", tierbtest.FromLyrics(lunaLyrics, tierbtest.Opts{}), time.Hour)
	r.putDoc(lt.TrackCitta, "analysis.json", tierbtest.FromLyrics("è sera", tierbtest.Opts{}), time.Hour)
	r.analyze(lt.TrackCitta)
	snap := func() [4]int {
		return [4]int{r.count(`SELECT count(*) FROM analyses`), r.count(`SELECT count(*) FROM sections`),
			r.count(`SELECT count(*) FROM artifacts`), r.count(`SELECT count(*) FROM jobs`)}
	}
	before := snap()
	for _, id := range []string{lt.TrackLuna, lt.TrackCitta} {
		_, _ = r.svc.TierB(ctx, id)
		_, _ = r.svc.TierBState(ctx, id)
		if _, err := r.svc.Dossier(ctx, id); err != nil {
			t.Fatal(err)
		}
		if _, err := r.svc.Perception(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.svc.FlagCodes(ctx, []string{lt.TrackLuna, lt.TrackCitta}); err != nil {
		t.Fatal(err)
	}
	if after := snap(); after != before {
		t.Errorf("a read wrote: %v -> %v", before, after)
	}
}

// TestAnalyzeTierB_LaterDocumentNotShadowed: a second document (another model or other content) gets its own raw and diction
// rows and the read side shows it whole, not the new counts over the old diction.
func TestAnalyzeTierB_LaterDocumentNotShadowed(t *testing.T) {
	t.Parallel()
	r := newRig(t, withTierARows())
	r.putDoc(lt.TrackLuna, "analysis.json", tierbtest.FromLyrics(lunaLyrics, tierbtest.Opts{Lang: "auto"}), 2*time.Hour)
	r.analyze(lt.TrackLuna)
	r.dropTriggers("analyses_immutable")
	r.exec(`UPDATE analyses SET created_at = ? WHERE track_id = ? AND tier = 'B'`, store.FormatTime(r.now.Add(-time.Hour)), lt.TrackLuna)
	r.putDoc(lt.TrackLuna, "analysis-2.json", tierbtest.FromLyrics(lunaLyrics, tierbtest.Opts{Lang: "it"}), time.Hour)
	r.analyze(lt.TrackLuna)
	if n := len(r.tierBRows(lt.TrackLuna)); n != 4 {
		t.Fatalf("rows = %d (%v), want raw and diction of each document", n, r.tierBRows(lt.TrackLuna))
	}
	tb, err := r.svc.TierB(context.Background(), lt.TrackLuna)
	if err != nil || tb.Language != "it" {
		t.Errorf("TierB language %q (%v), want the later document", tb.Language, err)
	}
}
