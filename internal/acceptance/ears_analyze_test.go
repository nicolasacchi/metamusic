package acceptance_test

import (
	"path/filepath"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/archivetest"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/fake"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

// analyzeRig is a generation rig over the fake backend whose simulated worker also FETCHES its inputs through the rig's media route
// (the digest of every pinned master is checked, as the real worker does before any stage).
func analyzeRig(t *testing.T) *servertest.Rig {
	t.Helper()
	if testing.Short() {
		t.Skip("heavy acceptance test")
	}
	testtools.Require(t, "ffmpeg")
	late, lf := &lateUploader{}, &lateFetcher{}
	tpl := archivetest.SyntheticArchive(t, "acceptance")
	o := servertest.Options{Archive: &tpl, Backend: "fake", M2b: servertest.M2bOptions{Media: true, Backfill: true}}
	o.Fake = &fake.Options{Worker: fake.NewSimWorker(fake.SimWorkerOptions{Uploader: late, Fetcher: lf, Tools: testtools.Tools(), AudioSeconds: 3})}
	r := servertest.New(t, o)
	hu := fake.HandlerUploader{UploadHandler: r.Services.Uploads.Handler(), StartHandler: r.Services.Uploads.StartHandler(), FetchHandler: r.Services.Server.Media}
	late.set(hu)
	lf.set(hu)
	return r
}

// analyzeItems is the body of an analyze estimate.
func analyzeItems(ids ...string) map[string]any {
	return map[string]any{"items": []any{map[string]any{"mode": "analyze", "track_ids": ids}}}
}

// TestAcceptance_Ears_AnalyzeChosenTake (C340.7, EARS-G2): an imported take with synthetic lyrics is analysed on the operator's choice:
// estimate (integer micro-USD, the fetch term and the listening check) -> create -> the job completes ON the take, with no new track and
// no mutation -> the artifact, the Tier B rows and the measured sections -> get_track_tier_b answers 200. A second estimate is
// refused (already_analyzed), an Editor's estimate awaits the Owner, nothing but this one chosen take is ever analysed, and the take's
// master, recipe and tags are untouched.
func TestAcceptance_Ears_AnalyzeChosenTake(t *testing.T) {
	r := analyzeRig(t)
	h := r.Human(t)
	editor := r.User(t, servertest.UserEditor, config.RoleEditor)

	var track, lyrics, masterSHA, masterPath string
	if err := r.Store.R.QueryRow(`SELECT t.id, rc.lyrics, t.master_sha256, t.master_path FROM tracks t JOIN recipes rc ON rc.id = t.recipe_id
		WHERE rc.engine <> 'unknown' AND t.lossy = 0 AND rc.lyrics LIKE '%' || char(10) || '%' ORDER BY t.id LIMIT 1`).Scan(&track, &lyrics, &masterSHA, &masterPath); err != nil {
		t.Fatal(err)
	}
	fake.ScriptAnalyzeLyrics(masterSHA, lyrics) // the known sung lyric of this synthetic master
	otherTracks := count(t, r, `SELECT COUNT(*) FROM tracks`)
	mutations, recipes := count(t, r, `SELECT COUNT(*) FROM mutations`), count(t, r, `SELECT COUNT(*) FROM recipes`)
	masterBefore := fileSHA(t, filepath.Join(r.DataDir, masterPath))
	if masterBefore != masterSHA {
		t.Fatalf("fixture master digest %s != row %s", masterBefore, masterSHA)
	}

	// estimate: an ordinary spend, priced in integer micro-USD
	est := obj(h.OK(t, "POST", "/api/generations/estimate", analyzeItems(track)))
	take := obj(arr(obj(arr(est["items"])[0])["takes"])[0])
	if take["operator"] != "analyze" || take["parent_track_id"] != track || take["tier_b"] == true {
		t.Fatalf("take plan = %v", take)
	}
	cost := obj(est["cost"])
	if _, ok := cost["ceiling_micro_usd"].(float64); !ok || cost["ceiling_micro_usd"].(float64) <= 0 {
		t.Errorf("cost = %v, want an integer ceiling in micro-USD", cost)
	}
	var needs, spec, kind string
	// the editor sees an approval wait, no money field
	ed := obj(editor.OK(t, "POST", "/api/generations/estimate", analyzeItems(track)))
	if ed["requires_human_approval"] != true {
		t.Errorf("an Editor's analyze estimate does not await the Owner: %v", ed["approval_reasons"])
	}
	if b := obj(ed["budget"]); ed["price"] != nil || b["month"] != nil || b["day"] != nil || b["batch_cap_micro_usd"] != nil {
		t.Errorf("an Editor sees owner-side money: price %v budget %v", ed["price"], ed["budget"])
	}

	resp := h.Do(t, "POST", "/human/api/generations", map[string]any{"confirmation_token": est["confirmation_token"], "rationale": "listen to it"}, "Idempotency-Key", "ears-analyze-1")
	if resp.Status != 201 {
		t.Fatalf("create: %d %s", resp.Status, resp.Body)
	}
	job := obj(arr(obj(resp.JSON(t))["jobs"])[0])["id"].(string)
	if err := r.Store.R.QueryRow(`SELECT kind, needs_json, spec_json FROM jobs WHERE id = ?`, job).Scan(&kind, &needs, &spec); err != nil {
		t.Fatal(err)
	}
	if kind != "analyze" || needs != `["analyze","ears","v2"]` {
		t.Errorf("job kind %s needs %s", kind, needs)
	}
	pump(t, r, func() bool { s, _ := jobState(t, r, job); return terminalJob(s) })
	if s, code := jobState(t, r, job); s != "completed" {
		t.Fatalf("job %s (%s): %s", s, code, jobEvents(t, r, job))
	}

	// the job completed ON the take: no new track, no mutation, no new recipe
	var jobTrack, outcome string
	if err := r.Store.R.QueryRow(`SELECT track_id, outcome FROM jobs WHERE id = ?`, job).Scan(&jobTrack, &outcome); err != nil {
		t.Fatal(err)
	}
	if jobTrack != track || outcome != "clean" {
		t.Errorf("job track %s outcome %s, want %s clean", jobTrack, outcome, track)
	}
	if count(t, r, `SELECT COUNT(*) FROM tracks`) != otherTracks || count(t, r, `SELECT COUNT(*) FROM mutations`) != mutations || count(t, r, `SELECT COUNT(*) FROM recipes`) != recipes {
		t.Error("the analyze job wrote a track, a mutation or a recipe")
	}
	if fileSHA(t, filepath.Join(r.DataDir, masterPath)) != masterBefore {
		t.Error("the master changed")
	}

	// the artifact, the rows, the measured sections
	if n := count(t, r, `SELECT COUNT(*) FROM artifacts WHERE track_id = ? AND name = 'analysis.json'`, track); n != 1 {
		t.Fatalf("analysis.json artifacts = %d", n)
	}
	for _, a := range []string{"tier_b_raw", "tier_b_diction"} {
		if n := count(t, r, `SELECT COUNT(*) FROM analyses WHERE track_id = ? AND analyzer = ?`, track, a); n != 1 {
			t.Errorf("%s rows = %d", a, n)
		}
	}
	if n := count(t, r, `SELECT COUNT(*) FROM sections WHERE track_id = ? AND source = 'measured'`, track); n == 0 {
		t.Error("no measured sections")
	}
	tb := obj(h.OK(t, "GET", "/api/tracks/"+track+"/tier-b", nil))
	if tb["state"] != "ok" || tb["status"] != "ok" {
		t.Errorf("tier-b = %v", tb)
	}

	// a second estimate is refused; nothing else was analysed and no loop created analyze work
	again := h.Do(t, "POST", "/api/generations/estimate", analyzeItems(track))
	if again.Status != 422 {
		t.Errorf("second estimate = %d %s, want 422 already_analyzed", again.Status, again.Body)
	}
	if n := count(t, r, `SELECT COUNT(*) FROM jobs WHERE kind = 'analyze'`); n != 1 {
		t.Errorf("analyze jobs = %d, want only the chosen one", n)
	}
	if n := count(t, r, `SELECT COUNT(DISTINCT track_id) FROM artifacts WHERE name LIKE 'analysis%.json'`); n != 1 {
		t.Errorf("%d takes carry a document, want only the chosen one", n)
	}
}

// TestAcceptance_Ears_AnalyzeClosedCapabilities: with the capabilities closed (production today) an analyze estimate is 503
// GENERATION_DISABLED worker_outdated, nothing is reserved, stored or created; opened, the same request prices. No cap, balance or
// approval is bypassed: the batch is created awaiting nothing the Owner did not give.
func TestAcceptance_Ears_AnalyzeClosedCapabilities(t *testing.T) {
	r := analyzeRig(t)
	h := r.Human(t)
	var track string
	if err := r.Store.R.QueryRow(`SELECT t.id FROM tracks t JOIN recipes rc ON rc.id = t.recipe_id WHERE rc.engine <> 'unknown' AND t.lossy = 0 ORDER BY t.id LIMIT 1`).Scan(&track); err != nil {
		t.Fatal(err)
	}
	row := func(caps ...string) {
		t.Helper()
		if err := provider.AppendContract(t.Context(), r.Store.W, provider.WorkerContractRow{Backend: provider.BackendFake, ImageDigest: "sha256:" + hexOf("3"),
			Caps: caps, Via: provider.ContractViaCLI, ChangedBy: "system:cli", ChangedAt: r.Clock.Now(), Reason: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	row() // closed
	estimates, ledger, jobs := count(t, r, `SELECT COUNT(*) FROM estimates`), count(t, r, `SELECT COUNT(*) FROM ledger`), count(t, r, `SELECT COUNT(*) FROM jobs`)
	resp := h.Do(t, "POST", "/api/generations/estimate", analyzeItems(track))
	if resp.Status != 503 {
		t.Fatalf("closed estimate = %d %s, want 503", resp.Status, resp.Body)
	}
	body := obj(resp.JSON(t))
	if body["code"] != "GENERATION_DISABLED" || obj(body["details"])["reason"] != "worker_outdated" {
		t.Errorf("body = %v", body)
	}
	if count(t, r, `SELECT COUNT(*) FROM estimates`) != estimates || count(t, r, `SELECT COUNT(*) FROM ledger`) != ledger || count(t, r, `SELECT COUNT(*) FROM jobs`) != jobs {
		t.Error("a refused analyze estimate stored or reserved something")
	}
	row("analyze", "ears", "v2") // the operator opened them
	est := obj(h.OK(t, "POST", "/api/generations/estimate", analyzeItems(track)))
	if _, ok := obj(est["cost"])["ceiling_micro_usd"].(float64); !ok {
		t.Errorf("opened estimate has no integer ceiling: %v", est["cost"])
	}
}

func hexOf(c string) string {
	out := ""
	for range 64 {
		out += c
	}
	return out
}
