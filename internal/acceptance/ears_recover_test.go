package acceptance_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/library"

	"github.com/nicolasacchi/metamusic/internal/archivetest"
	"github.com/nicolasacchi/metamusic/internal/ears/tierb"
	"github.com/nicolasacchi/metamusic/internal/ears/tierb/tierbtest"
	"github.com/nicolasacchi/metamusic/internal/provider/fake"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

// knownSungLyric is an invented Italian lyric (no real lyrics in this repository): the "known sung lyric" of a synthetic reference.
const knownSungLyric = "[verse]\nla luna cammina sul prato\nil vento racconta una storia\ne noi restiamo qui a guardare\n" +
	"[chorus]\nsole sole porta il giorno\ncanta con me fino a sera\nsole sole porta il giorno\n" +
	"[verse]\nla strada e lunga e stretta\nma il cuore conosce la via\ne non ha paura del buio"

// referenceRig is analyzeRig over the reference profile of the synthetic archive: imported references (stub recipes, no lyrics).
func referenceRig(t *testing.T) *servertest.Rig {
	t.Helper()
	if testing.Short() {
		t.Skip("heavy acceptance test")
	}
	testtools.Require(t, "ffmpeg")
	late, lf := &lateUploader{}, &lateFetcher{}
	tpl := archivetest.SyntheticArchive(t, "reference")
	o := servertest.Options{Archive: &tpl, Backend: "fake", M2b: servertest.M2bOptions{Media: true, Backfill: true}}
	o.Fake = &fake.Options{Worker: fake.NewSimWorker(fake.SimWorkerOptions{Uploader: late, Fetcher: lf, Tools: testtools.Tools(), AudioSeconds: 3})}
	r := servertest.New(t, o)
	hu := fake.HandlerUploader{UploadHandler: r.Services.Uploads.Handler(), StartHandler: r.Services.Uploads.StartHandler(), FetchHandler: r.Services.Server.Media}
	late.set(hu)
	lf.set(hu)
	return r
}

// seedRecoverDocument stores the analysis document of a recogniser that heard lyrics (as the ingest bridge would) and analyses it.
func seedRecoverDocument(t *testing.T, r *servertest.Rig, trackID, lyrics string) {
	t.Helper()
	doc := tierbtest.FromLyrics(lyrics, tierbtest.Opts{})
	dir := filepath.Join(r.DataDir, "takes", trackID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "analysis.json"), doc, 0o444); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(doc)
	if _, err := r.Store.W.Exec(`INSERT INTO artifacts(track_id, name, sha256, bytes, path, created_at) VALUES (?, 'analysis.json', ?, ?, ?, ?)`,
		trackID, hex.EncodeToString(sum[:]), len(doc), "takes/"+trackID+"/analysis.json", store.FormatTime(r.Clock.Now())); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Services.Ears.AnalyzeTierB(t.Context(), trackID); err != nil {
		t.Fatal(err)
	}
}

// seedReference imports a synthetic lossless reference (stub recipe, no lyrics, no audio on disk) and returns its track id; it gives the
// scripted agent session a reference to recover from.
func seedReference(t *testing.T, r *servertest.Rig, name string) string {
	t.Helper()
	ctx := t.Context()
	lib := r.Services.Deps.Library
	tx, err := r.Store.W.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	rid, _, err := lib.InsertReferenceRecipe(ctx, tx, library.NewReferenceRecipe{Title: name, Slug: name, AudioKey: "recover-seed/" + name})
	if err != nil {
		t.Fatal(err)
	}
	sum := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
	h := sum("recover-ref/" + name)
	bw := 16000
	id := "01900000-0000-7000-8000-00000000e301"
	if err := lib.InsertTake(ctx, tx, library.NewTake{
		ID: id, Title: name, Slug: name, RecipeID: rid, Reference: true, ImportRule: library.RuleReference, PrincipalID: "cli", RootTrackID: id,
		DurationS: 6, SampleRate: 48000, BitsPerSample: 24, Channels: 2, TotalSamples: 6 * 48000,
		MasterPath: "audio/masters/" + name + "__" + h[:8] + ".flac", MasterSHA256: h, MasterBytes: 64,
		SourceSHA256: sum("src/" + name), FramesSHA256: sum("frames/" + name), AudioMD5: h[:32], RecordedAt: r.Clock.Now(),
		Loudness: analysis.Loudness{IntegratedLUFS: -13.5}, Spectrum: analysis.Spectrum{BandwidthHz: &bw}, ToolVersion: "ffmpeg n9.0.1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return id
}

// referenceSnapshot is every stored row of the reference that Recover and the render of its draft must leave alone.
func referenceSnapshot(t *testing.T, r *servertest.Rig, id string) string {
	t.Helper()
	var parts []string
	for _, q := range []string{
		`SELECT COALESCE(group_concat(id || '|' || recipe_id || '|' || status || '|' || master_sha256 || '|' || COALESCE(parent_track_id, '') || '|' || root_track_id || '|' || generation, ';'), '') FROM tracks WHERE id = ?1`,
		`SELECT COALESCE(group_concat(r.id || '|' || r.engine || '|' || r.request_json, ';'), '') FROM recipes r JOIN tracks t ON t.recipe_id = r.id WHERE t.id = ?1`,
		`SELECT COALESCE(group_concat(source || '|' || idx || '|' || label || '|' || COALESCE(lines_json, ''), ';'), '') FROM sections WHERE track_id = ?1`,
		`SELECT COALESCE(group_concat(analyzer || '|' || version || '|' || metrics_json, ';'), '') FROM analyses WHERE track_id = ?1`,
		`SELECT COALESCE(group_concat(name || '|' || sha256, ';'), '') FROM artifacts WHERE track_id = ?1`,
		`SELECT COUNT(*) FROM tracks WHERE parent_track_id = ?1`,
	} {
		var s string
		if err := r.Store.R.QueryRow(q, id).Scan(&s); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "\n")
}

// lyricLines are the lyric lines of a text (headers and blanks left out), folded and joined.
func lyricLines(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "[") {
			out = append(out, tierb.Fold(l))
		}
	}
	return strings.Join(out, " ")
}

// TestAcceptance_Ears_RecoverRecipe (C343.6, EARS-R1): an imported reference with a known sung lyric is analysed on the operator's
// choice (analyze estimate -> create -> the listening check is stored), then recover_recipe makes a NEW draft whose lyric lines match
// the known lyric (CER <= 0.15), free of charge; the draft renders through the ordinary estimate and approval as a `recover` take: a
// new root whose mutation names the reference as its parent. The reference, its recipe stub, its sections and its analyses are
// byte-identical throughout.
func TestAcceptance_Ears_RecoverRecipe(t *testing.T) {
	r := referenceRig(t)
	h := r.Human(t)

	var ref, masterSHA string
	if err := r.Store.R.QueryRow(`SELECT t.id, t.master_sha256 FROM tracks t WHERE t.import_rule = 'reference' AND t.lossy = 0 ORDER BY t.id LIMIT 1`).Scan(&ref, &masterSHA); err != nil {
		t.Fatalf("no lossless reference in the archive: %v", err)
	}
	fake.ScriptAnalyzeLyrics(masterSHA, knownSungLyric) // what the synthetic reference "sings"

	// before the listening check there is nothing to recover from, and the refusal says what to do
	resp := h.Do(t, "POST", "/api/tracks/"+ref+"/recover", map[string]any{}, "Idempotency-Key", "recover-early")
	if resp.Status != 409 || obj(obj(resp.JSON(t))["details"])["hint"] != "estimate an analyze item" {
		t.Fatalf("recover before analyze: %d %s", resp.Status, resp.Body)
	}

	// analyze the chosen reference: an ordinary estimate, approval and create
	est := obj(h.OK(t, "POST", "/api/generations/estimate", analyzeItems(ref)))
	cr := h.Do(t, "POST", "/human/api/generations", map[string]any{"confirmation_token": est["confirmation_token"], "rationale": "listen to the reference"}, "Idempotency-Key", "recover-analyze")
	if cr.Status != 201 {
		t.Fatalf("create analyze: %d %s", cr.Status, cr.Body)
	}
	job := obj(arr(obj(cr.JSON(t))["jobs"])[0])["id"].(string)
	pump(t, r, func() bool { s, _ := jobState(t, r, job); return terminalJob(s) })
	if s, code := jobState(t, r, job); s != "completed" {
		t.Fatalf("analyze job %s (%s): %s", s, code, jobEvents(t, r, job))
	}
	snapshot := referenceSnapshot(t, r, ref)
	spendRows := func() string {
		return fmt.Sprintf("jobs=%d estimates=%d batches=%d ledger=%d", count(t, r, `SELECT COUNT(*) FROM jobs`), count(t, r, `SELECT COUNT(*) FROM estimates`),
			count(t, r, `SELECT COUNT(*) FROM batches`), count(t, r, `SELECT COUNT(*) FROM ledger`))
	}
	spent := spendRows()

	// Recover: free, a new draft, the lyric of the recording
	rr := h.Do(t, "POST", "/api/tracks/"+ref+"/recover", map[string]any{"title": "Recovered reference"}, "Idempotency-Key", "recover-1")
	if rr.Status != 201 {
		t.Fatalf("recover: %d %s", rr.Status, rr.Body)
	}
	body := obj(rr.JSON(t))
	draft := obj(body["draft"])
	if got := spendRows(); got != spent {
		t.Errorf("recover spent or reserved something: %s -> %s", spent, got)
	}
	got, want := lyricLines(obj(draft["request"])["lyrics"].(string)), lyricLines(knownSungLyric)
	cer := float64(tierb.Levenshtein([]rune(got), []rune(want))) / float64(len([]rune(want)))
	if cer > 0.15 {
		t.Errorf("CER of the recovered lyric = %.3f, want <= 0.15\n%s", cer, obj(draft["request"])["lyrics"])
	}
	if draft["origin"] != "recovered" || draft["parent_track_id"] != nil || body["style_source"] != "placeholder" {
		t.Errorf("draft = origin %v parent %v style_source %v", draft["origin"], draft["parent_track_id"], body["style_source"])
	}
	if after := referenceSnapshot(t, r, ref); after != snapshot {
		t.Errorf("recover changed the reference:\n%s\n---\n%s", snapshot, after)
	}

	// the draft is edited like any other, then rendered through the ordinary path
	id := draft["id"].(string)
	est2 := obj(h.OK(t, "POST", "/api/generations/estimate", map[string]any{"options": map[string]any{"tier_b": false},
		"items": []any{map[string]any{"mode": "from_draft", "draft_ids": []string{id}}}}))
	take := obj(arr(obj(arr(est2["items"])[0])["takes"])[0])
	if take["operator"] != "recover" || take["parent_track_id"] != nil {
		t.Fatalf("take plan = %v, want operator recover and no mother", take)
	}
	if mother := h.Do(t, "POST", "/api/generations/estimate", map[string]any{"options": map[string]any{"tier_b": false},
		"items": []any{map[string]any{"mode": "from_draft", "draft_ids": []string{id}, "parent_track_id": ref}}}); mother.Status != 422 {
		t.Errorf("a mother on a recovered draft: %d %s, want 422", mother.Status, mother.Body)
	}
	c2 := h.Do(t, "POST", "/human/api/generations", map[string]any{"confirmation_token": est2["confirmation_token"], "rationale": "render the recovered draft"}, "Idempotency-Key", "recover-render")
	if c2.Status != 201 {
		t.Fatalf("create render: %d %s", c2.Status, c2.Body)
	}
	rj := obj(arr(obj(c2.JSON(t))["jobs"])[0])["id"].(string)
	pump(t, r, func() bool { s, _ := jobState(t, r, rj); return terminalJob(s) })
	if s, code := jobState(t, r, rj); s != "completed" {
		t.Fatalf("render job %s (%s): %s", s, code, jobEvents(t, r, rj))
	}

	var child, op, mparent, params string
	var tparent *string
	var root string
	var gen int
	if err := r.Store.R.QueryRow(`SELECT track_id FROM jobs WHERE id = ?`, rj).Scan(&child); err != nil {
		t.Fatal(err)
	}
	if err := r.Store.R.QueryRow(`SELECT operator, COALESCE(parent_track_id, ''), params_json FROM mutations WHERE child_track_id = ?`, child).Scan(&op, &mparent, &params); err != nil {
		t.Fatal(err)
	}
	if err := r.Store.R.QueryRow(`SELECT parent_track_id, root_track_id, generation FROM tracks WHERE id = ?`, child).Scan(&tparent, &root, &gen); err != nil {
		t.Fatal(err)
	}
	if op != "recover" || mparent != ref || !strings.Contains(params, ref) {
		t.Errorf("mutation = operator %s parent %s params %s, want recover from %s", op, mparent, params, ref)
	}
	if tparent != nil || root != child || gen != 0 {
		t.Errorf("child = parent %v root %s generation %d, want a new root", tparent, root, gen)
	}
	if n := count(t, r, `SELECT COUNT(*) FROM draft_recoveries WHERE draft_id = ? AND track_id = ?`, id, ref); n != 1 {
		t.Errorf("draft_recoveries rows = %d", n)
	}
	// the reference is still untouched after the render
	if after := referenceSnapshot(t, r, ref); after != snapshot {
		t.Errorf("the render changed the reference:\n%s\n---\n%s", snapshot, after)
	}
}
