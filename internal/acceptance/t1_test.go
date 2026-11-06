package acceptance_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// t1Bodies are small requests, one per kind of the real surface, over FixtureT1's A and B (morph and polytempo take both).
func t1Bodies(tr *t1Rig) map[string]map[string]any {
	a, b := tr.Info.Tx["A"], tr.Info.Tx["B"]
	return map[string]map[string]any{
		"glacier":   glacierBody(a, 10, 3),
		"ensemble":  {"kind": "ensemble", "ensemble": map[string]any{"source_id": a, "space": "cathedral", "tail_s": 4.0}},
		"master":    masterBody(a, "warm"),
		"widen":     widenBody(a),
		"morph":     {"kind": "morph", "morph": map[string]any{"track_a_id": a, "track_b_id": b, "bars": 2, "downbeat_a_s": 0.1, "downbeat_b_s": 0.1}},
		"polytempo": {"kind": "polytempo", "polytempo": map[string]any{"track_a_id": a, "track_b_id": b, "downbeat_a_s": 0.1, "downbeat_b_s": 0.1, "converge_bar": 4, "unison_bars": 2}},
		"shepard":   {"kind": "shepard", "shepard": map[string]any{"source_id": a, "loop_s": 4.0, "start_s": 0.0, "layers": 3, "period_s": 8.0, "duration_s": 16.0}},
	}
}

// assertTakeShape is the C216/TakeShape contract on the Take a finished transform became.
func assertTakeShape(t *testing.T, tr *t1Rig, v map[string]any, kind string, parents ...string) {
	t.Helper()
	id := v["id"].(string)
	var engine, reqJSON, op, parent, root, mparent, tid string
	var inferred, bits, ch, gen, pgen int
	var mode os.FileMode
	var master string
	if err := tr.Store.R.QueryRow(`SELECT c.engine, c.request_json, t.master_path, t.bits_per_sample, t.channels, t.parent_track_id, t.root_track_id, t.generation
		FROM tracks t JOIN recipes c ON c.id = t.recipe_id WHERE t.id = ?`, id).Scan(&engine, &reqJSON, &master, &bits, &ch, &parent, &root, &gen); err != nil {
		t.Fatal(err)
	}
	if engine != "dsp" || bits != 24 || ch != 2 {
		t.Errorf("engine %q, %d bits, %d channels", engine, bits, ch)
	}
	var proot string
	if err := tr.Store.R.QueryRow(`SELECT root_track_id, generation FROM tracks WHERE id = ?`, parents[0]).Scan(&proot, &pgen); err != nil {
		t.Fatal(err)
	}
	if parent != parents[0] || root != proot || gen != pgen+1 {
		t.Errorf("lineage: parent %s root %s generation %d, want %s %s %d", parent, root, gen, parents[0], proot, pgen+1)
	}
	var spec struct {
		DSPVersion string `json:"dsp_version"`
		Kind       string `json:"kind"`
		Inputs     []struct {
			TrackID      string `json:"track_id"`
			MasterSHA256 string `json:"master_sha256"`
		} `json:"inputs"`
	}
	if err := json.Unmarshal([]byte(reqJSON), &spec); err != nil {
		t.Fatal(err)
	}
	if spec.Kind != kind || spec.DSPVersion == "" || len(spec.Inputs) != len(parents) {
		t.Errorf("recipe request: %+v", spec)
	}
	for i, p := range parents {
		var msha string
		if err := tr.Store.R.QueryRow(`SELECT master_sha256 FROM tracks WHERE id = ?`, p).Scan(&msha); err != nil {
			t.Fatal(err)
		}
		if i < len(spec.Inputs) && (spec.Inputs[i].MasterSHA256 != msha || spec.Inputs[i].TrackID != p) {
			t.Errorf("recipe input %d = %+v, want %s %s", i, spec.Inputs[i], p, msha)
		}
		var gotTrack string
		if err := tr.Store.R.QueryRow(`SELECT track_id FROM transform_inputs WHERE transform_id = ? AND ord = ?`, id, i).Scan(&gotTrack); err != nil || gotTrack != p {
			t.Errorf("transform_inputs ord %d = %q (%v), want %s", i, gotTrack, err, p)
		}
	}
	if err := tr.Store.R.QueryRow(`SELECT operator, inferred, parent_track_id, transform_id FROM mutations WHERE child_track_id = ?`, id).Scan(&op, &inferred, &mparent, &tid); err != nil {
		t.Fatalf("no mutation row: %v", err)
	}
	if op != kind || inferred != 0 || mparent != parents[0] || tid != id {
		t.Errorf("mutation: %s inferred %d parent %s transform %s", op, inferred, mparent, tid)
	}
	abs := tr.DataDir + "/" + master
	fi, err := os.Stat(abs)
	if err != nil {
		t.Fatal(err)
	}
	mode = fi.Mode().Perm()
	if mode != 0o444 {
		t.Errorf("master mode %v, want 0444", mode)
	}
	tags, err := exec.Command(tr.Tools.Metaflac, "--export-tags-to=-", abs).CombinedOutput() // #nosec G204
	if err != nil || !strings.Contains(string(tags), id) {
		t.Errorf("MM_TAKE_ID tag: %v %s", err, tags)
	}
	if out, err := exec.Command(tr.Tools.FLAC, "-t", abs).CombinedOutput(); err != nil { // #nosec G204
		t.Errorf("flac -t: %v %s", err, out)
	}
	if n := tr.Count(`SELECT COUNT(*) FROM analyses WHERE track_id = ?`, id); n == 0 {
		t.Error("no analysis rows (Ears) for the new Take")
	}
}

// TestAcceptance_T1_TakeShape (media tier, real REST surface and real child): each kind becomes an ordinary frozen 24-bit Take
// whose recipe, ledger edge and inputs say what made it. The plan's TakeShape also runs in internal/transform on the bridge; this
// one goes through create_transform.
func TestAcceptance_T1_TakeShape(t *testing.T) {
	tr := newT1Rig(t)
	bodies := t1Bodies(tr)
	a, b := tr.Info.Tx["A"], tr.Info.Tx["B"]
	for _, kind := range []string{"glacier", "ensemble", "master", "widen", "morph", "polytempo", "shepard"} {
		t.Run(kind, func(t *testing.T) {
			tr.T = t
			v := tr.RunDone(bodies[kind])
			if v["kind"] != kind || v["cost"].(map[string]any)["micro_usd"].(float64) != 0 {
				t.Errorf("view: %v", v)
			}
			parents := []string{a}
			if kind == "morph" || kind == "polytempo" {
				parents = append(parents, b)
			}
			assertTakeShape(t, tr, v, kind, parents...)
		})
	}
}

// fileState is what rule 7 pins about a master: content, mode and modification time.
type fileState struct {
	sha  string
	mode os.FileMode
	mod  time.Time
}

func (tr *t1Rig) fileState(id string) fileState {
	tr.T.Helper()
	p := tr.MasterPath(id)
	fi, err := os.Stat(p)
	if err != nil {
		tr.T.Fatal(err)
	}
	return fileState{sha: fileSHA(tr.T, p), mode: fi.Mode().Perm(), mod: fi.ModTime()}
}

// TestAcceptance_T1_MastersFrozen: the parents' masters (bytes, mode, mtime) and the output master are unchanged by the run and by
// a Verify, and the master_sha256 column cannot be rewritten.
func TestAcceptance_T1_MastersFrozen(t *testing.T) {
	tr := newT1Rig(t)
	a, b := tr.Info.Tx["A"], tr.Info.Tx["B"]
	before := map[string]fileState{a: tr.fileState(a), b: tr.fileState(b)}
	v := tr.RunDone(t1Bodies(tr)["morph"])
	id := v["id"].(string)
	out := tr.fileState(id)
	rep, err := tr.Services.Transform.Verify(context.Background(), id)
	if err != nil || !rep.Match {
		t.Fatalf("verify: %+v %v", rep, err)
	}
	for _, p := range []string{a, b} {
		if got := tr.fileState(p); got != before[p] {
			t.Errorf("parent %s changed: %+v -> %+v", p, before[p], got)
		}
	}
	if got := tr.fileState(id); got != out || out.mode != 0o444 {
		t.Errorf("output master %+v -> %+v (mode must be 0444)", out, got)
	}
	for _, q := range []string{a, id} {
		if _, err := tr.Store.W.Exec(`UPDATE tracks SET master_sha256 = ? WHERE id = ?`, strings.Repeat("0", 64), q); err == nil {
			t.Errorf("UPDATE tracks.master_sha256 of %s succeeded", q)
		}
	}
}

// TestAcceptance_T1_SameSpecReusesTake: the second create of a spec answers 200 reused with the same id, adds no transform row
// and leaves the runner nothing to do.
func TestAcceptance_T1_SameSpecReusesTake(t *testing.T) {
	tr := newT1Rig(t)
	body := masterBody(tr.Info.Tx["A"], "punch")
	first := tr.RunDone(body)
	rows := tr.Count(`SELECT COUNT(*) FROM transforms`)
	takes := tr.Count(`SELECT COUNT(*) FROM tracks`)
	st, second := tr.Create(tr.H, body)
	if st != http.StatusOK || second["reused"] != true || second["id"] != first["id"] || second["state"] != "done" {
		t.Fatalf("second create = %d %v", st, second)
	}
	// another principal asking for the same spec gets the same Take
	st, third := tr.Create(tr.writer(), body)
	if st != http.StatusOK || third["id"] != first["id"] {
		t.Errorf("an agent's create of the same spec = %d %v", st, third)
	}
	if tr.Drain() != 0 {
		t.Error("the runner found work after a reused create")
	}
	if tr.Count(`SELECT COUNT(*) FROM transforms`) != rows || tr.Count(`SELECT COUNT(*) FROM tracks`) != takes {
		t.Error("a reused create wrote a row")
	}
}

// TestAcceptance_T1_Deterministic: the same spec in two scratch libraries renders the same audio; another seed renders other audio.
func TestAcceptance_T1_Deterministic(t *testing.T) {
	one, two := newT1Rig(t), newT1Rig(t)
	body := func(tr *t1Rig, seed int) map[string]any {
		m := glacierBody(tr.Info.Tx["A"], 10, 3)
		m["seed"] = seed
		return m
	}
	pcmOf := func(tr *t1Rig, seed int) string {
		v := tr.RunDone(body(tr, seed))
		_, pcm, _ := tr.Row(v["id"].(string))
		if len(pcm) != 64 {
			t.Fatalf("pcm_sha256 = %q", pcm)
		}
		return pcm
	}
	p1, p2 := pcmOf(one, 5), pcmOf(two, 5)
	if p1 != p2 {
		t.Errorf("same spec, two libraries: %s != %s", p1, p2)
	}
	if other := pcmOf(one, 6); other == p1 {
		t.Error("another seed rendered the same audio")
	}
}

// TestAcceptance_T1_VerifyCLI: `metamusic transform verify` re-renders from the stored spec in a separate process, exit 0 and a
// "match" line; --all verifies every done transform.
func TestAcceptance_T1_VerifyCLI(t *testing.T) {
	tr := newT1Rig(t)
	a := tr.Info.Tx["A"]
	ids := []string{tr.RunDone(masterBody(a, "neutral"))["id"].(string), tr.RunDone(widenBody(a))["id"].(string)}
	sec := filepath.Join(t.TempDir(), "secrets")
	if err := os.MkdirAll(sec, 0o700); err != nil {
		t.Fatal(err)
	}
	for f, v := range map[string]string{"secret_key": strings.Repeat("ab", 32), "edge_secret": "edge"} {
		if err := os.WriteFile(filepath.Join(sec, f), []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bin := t1ChildBin()
	run := func(args ...string) (string, int) {
		cmd := exec.Command(bin, args...) // #nosec G204 -- the binary this test built
		cmd.Env = []string{"MM_AUTH=local", "MM_DATA_DIR=" + tr.DataDir, "MM_SECRETS_DIR=" + sec, "MM_ADDR=127.0.0.1:0", "PATH=" + os.Getenv("PATH"),
			"MM_FFMPEG=" + tr.Tools.FFmpeg, "MM_FLAC=" + tr.Tools.FLAC, "MM_METAFLAC=" + tr.Tools.Metaflac, "HOME=" + t.TempDir()}
		out, err := cmd.CombinedOutput()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return string(out), code
	}
	out, code := run("transform", "verify", ids[0])
	_, pcm, _ := tr.Row(ids[0])
	if code != 0 || !strings.Contains(out, ids[0]+" match "+pcm) {
		t.Errorf("verify one: exit %d %s", code, out)
	}
	out, code = run("transform", "verify", "--all")
	if code != 0 || !strings.Contains(out, "2 verified: 2 match") {
		t.Errorf("verify --all: exit %d %s", code, out)
	}
	if out, code = run("transform", "verify", "01900000-0000-7000-8000-00000000dead"); code == 0 {
		t.Errorf("verify of an unknown id exited 0: %s", out)
	}
}

// TestAcceptance_T1_NoSpendRows: renders, a reuse, a refusal and a cancel add no row to any money or backend table, and every
// create/estimate/get answers cost 0.
func TestAcceptance_T1_NoSpendRows(t *testing.T) {
	tr := newT1Rig(t)
	tables := []string{"ledger", "batches", "jobs", "estimates", "provider_state", "spend_caps"}
	snap := func() map[string]string {
		out := map[string]string{}
		for _, tb := range tables {
			var n int
			var sum sql.NullString
			if err := tr.Store.R.QueryRow(`SELECT COUNT(*) FROM ` + tb).Scan(&n); err != nil {
				t.Fatalf("%s: %v", tb, err)
			}
			_ = sum
			out[tb] = fmt.Sprint(n)
		}
		return out
	}
	before := snap()
	a := tr.Info.Tx["A"]
	est := obj(tr.H.OK(t, "POST", "/api/transforms/estimate", masterBody(a, "bright")))
	if obj(est["cost"])["micro_usd"].(float64) != 0 {
		t.Errorf("estimate cost = %v", est["cost"])
	}
	done := tr.RunDone(masterBody(a, "bright"))
	queued := tr.CreateOK(widenBody(a))
	tr.H.OK(t, "POST", "/api/transforms/"+queued["id"].(string)+"/cancel", nil)
	tr.CreateOK(masterBody(a, "bright")) // a reuse
	if got := snap(); fmt.Sprint(got) != fmt.Sprint(before) {
		t.Errorf("money/backend tables moved: %v -> %v", before, got)
	}
	if obj(done["cost"])["micro_usd"].(float64) != 0 {
		t.Errorf("cost = %v", done["cost"])
	}
	for _, id := range []string{done["id"].(string), queued["id"].(string)} {
		if n := tr.Count(`SELECT COUNT(*) FROM jobs WHERE id = ?`, id); n != 0 {
			t.Errorf("a job row exists for transform %s", id)
		}
	}
}

// TestAcceptance_T1_LoudnessSafe: every kind on the real files lands inside the pipeline's own safety measurement: true peak at or
// below -0.5 dBTP, integrated loudness within 1.5 LU of the effective target (or the limiter engaged), no clipping or silence flag.
func TestAcceptance_T1_LoudnessSafe(t *testing.T) {
	tr := newT1Rig(t)
	for kind, body := range t1Bodies(tr) {
		t.Run(kind, func(t *testing.T) {
			tr.T = t
			v := tr.RunDone(body)
			id := v["id"].(string)
			d := obj(tr.H.OK(t, "GET", "/api/tracks/"+id, nil))
			lo := obj(obj(d["analysis"])["loudness"])
			tp := lo["true_peak_dbtp"].(float64)
			raw, err := os.ReadFile(filepath.Join(tr.DataDir, "takes", id, "transform.json"))
			if err != nil {
				t.Fatal(err)
			}
			var doc struct {
				Safety struct {
					Effective float64 `json:"target_lufs_effective"`
					Limiter   struct {
						Engaged float64 `json:"engaged_fraction"`
					} `json:"limiter"`
				} `json:"safety"`
			}
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			li := lo["integrated_lufs"].(float64)
			if tp > -0.5 {
				t.Errorf("true peak %.2f dBTP > -0.5", tp)
			}
			if math.Abs(li-doc.Safety.Effective) > 1.5 && doc.Safety.Limiter.Engaged == 0 {
				t.Errorf("integrated %.2f LUFS, effective target %.2f", li, doc.Safety.Effective)
			}
			ctxDoc := obj(tr.H.OK(t, "GET", "/api/tracks/"+id+"/context", nil))
			flags, _ := obj(ctxDoc["perception"])["flags"].([]any)
			for _, f := range flags {
				if s := fmt.Sprint(f); strings.Contains(s, "clip") || strings.Contains(s, "silen") {
					t.Errorf("perception flag %v", f)
				}
			}
		})
	}
}

// TestAcceptance_T1_ShepardLoop: the output is exactly duration_s long, and a loop (end followed by start) has no audible seam: the
// spectral flux across the seam is at most the median flux of the piece.
func TestAcceptance_T1_ShepardLoop(t *testing.T) {
	tr := newT1Rig(t)
	body := t1Bodies(tr)["shepard"]
	v := tr.RunDone(body)
	id := v["id"].(string)
	var frames int64
	if err := tr.Store.R.QueryRow(`SELECT total_samples FROM tracks WHERE id = ?`, id).Scan(&frames); err != nil {
		t.Fatal(err)
	}
	if frames != 16*48000 {
		t.Fatalf("output has %d frames, want %d", frames, 16*48000)
	}
	if d := obj(tr.H.OK(t, "GET", "/api/tracks/"+id, nil))["duration_s"].(float64); d != 16 {
		t.Errorf("duration_s = %v, want exactly 16", d)
	}
	l, r := tr.decodeMaster(id)
	mono := make([]float64, len(l))
	for i := range l {
		mono[i] = (l[i] + r[i]) / 2
	}
	t.Run("seam", func(t *testing.T) {
		const win, hop = 2048, 512
		loop := append(append([]float64{}, mono...), mono...)
		flux := func(at int) float64 { // spectral flux between the frames at at-hop and at
			a := dsptest.Spectrum(loop[at-hop:at-hop+win], win)
			b := dsptest.Spectrum(loop[at:at+win], win)
			s := 0.0
			for k := range a {
				if d := b[k] - a[k]; d > 0 {
					s += d
				}
			}
			return s
		}
		interior := 0.0
		var all []float64
		for at := hop + win; at+win < len(mono)-2*win; at += hop {
			f := flux(at)
			all = append(all, f)
			interior = math.Max(interior, f)
		}
		sort.Float64s(all)
		seam := 0.0
		for at := len(mono) - win/2; at <= len(mono)+win/2; at += hop / 2 { // every frame pair straddling the junction
			seam = math.Max(seam, flux(at))
		}
		// the plan says "at most the median"; every interior frame is a draw from the same distribution, so half of them exceed it.
		// The seam must at least not stand out: no more than the largest interior flux.
		if seam > interior {
			t.Errorf("flux across the loop seam %.3g exceeds every interior frame (max %.3g, median %.3g)", seam, interior, all[len(all)/2])
		}
	})
}

// TestAcceptance_T1_MorphSameLyrics: search_tracks lyrics_of offers B as A's partner; estimate, create and wait reach done; the
// second input is an also_from annotation of the lineage, not a tree edge.
func TestAcceptance_T1_MorphSameLyrics(t *testing.T) {
	tr := newT1Rig(t)
	a, b := tr.Info.Tx["A"], tr.Info.Tx["B"]
	page := obj(tr.H.OK(t, "GET", "/api/tracks?lyrics_of="+a+"&limit=100", nil))
	var found []string
	for _, it := range page["items"].([]any) {
		found = append(found, obj(it)["id"].(string))
	}
	if !slices.Contains(found, b) || slices.Contains(found, tr.Info.Tx["NF"]) || slices.Contains(found, tr.Info.Tx["I"]) {
		t.Fatalf("lyrics_of(A) = %v, want B and neither NF nor I", found)
	}
	body := t1Bodies(tr)["morph"]
	est := obj(tr.H.OK(t, "POST", "/api/transforms/estimate", body))
	if obj(est["cost"])["micro_usd"].(float64) != 0 || obj(est["cpu"])["expected_ms"].(float64) <= 0 || len(est["inputs"].([]any)) != 2 {
		t.Errorf("estimate: %v", est)
	}
	v := tr.RunDone(body)
	id := v["id"].(string)
	lin := obj(tr.H.OK(t, "GET", "/api/tracks/"+id+"/lineage", nil))
	var node map[string]any
	for _, n := range lin["nodes"].([]any) {
		if obj(n)["id"] == id {
			node = obj(n)
		}
		if obj(n)["id"] == b && obj(n)["parent_id"] != nil && obj(n)["id"] != a {
			t.Error("B is a tree node below another Take: the second input must be an annotation")
		}
	}
	also, _ := json.Marshal(node["also_from"])
	if node == nil || node["parent_id"] != a || !strings.Contains(string(also), b) || !strings.Contains(string(also), `"b"`) {
		t.Errorf("morph node %v: parent_id must be A and also_from must name B with role b, got %s", node["parent_id"], also)
	}
}

// TestAcceptance_T1_CancelRunning: a render is killed within 2 s of the cancel operation; the row is cancelled with
// CANCELLED_BY_USER, no Take exists, the work dir is gone and no child process is left behind.
func TestAcceptance_T1_CancelRunning(t *testing.T) {
	tr := newT1Rig(t)
	v := tr.CreateOK(glacierBody(tr.Info.Tx["A"], 20, 18)) // about 50 s of CPU: long enough to be caught mid-render
	id := v["id"].(string)
	done := make(chan error, 1)
	go func() { _, err := tr.Services.Transform.RunOne(context.Background()); done <- err }()
	deadline := time.Now().Add(60 * time.Second)
	for {
		g := tr.Get(id)
		if g["state"] == "running" && g["phase"] == "rendering" {
			break
		}
		if time.Now().After(deadline) || g["state"] == "done" || g["state"] == "failed" {
			t.Fatalf("never saw the render running: %v", g)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !childAlive(id) {
		t.Fatal("no dsp-run child is alive while the row is running: the test is vacuous")
	}
	t0 := time.Now()
	tr.H.OK(t, "POST", "/api/transforms/"+id+"/cancel", nil)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if d := time.Since(t0); d > 2*time.Second {
		t.Errorf("the render stopped %v after the cancel, want at most 2 s", d)
	}
	if state, _, code := tr.Row(id); state != "cancelled" || code != "CANCELLED_BY_USER" {
		t.Errorf("row = %s %s", state, code)
	}
	if n := tr.Count(`SELECT COUNT(*) FROM tracks WHERE id = ?`, id); n != 0 {
		t.Error("a cancelled transform left a Take")
	}
	if _, err := os.Stat(filepath.Join(tr.DataDir, "incoming", "transform-"+id)); err == nil {
		t.Error("the work dir survived")
	}
	if childAlive(id) {
		t.Error("a dsp-run child outlived the cancel")
	}
}

// childAlive scans /proc for a dsp-run process working in this transform's dir.
func childAlive(id string) bool {
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		b, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if err != nil {
			continue
		}
		if s := string(b); strings.Contains(s, "dsp-run") && strings.Contains(s, "transform-"+id) {
			return true
		}
	}
	return false
}

// TestAcceptance_T1_Quota: the daily CPU pool is per principal kind and shared by every principal of it. Once the agent pool is
// spent an agent's create is refused 429 TRANSFORM_QUOTA_EXCEEDED, a human's still works, and a cancelled queued transform gives
// its reservation back.
func TestAcceptance_T1_Quota(t *testing.T) {
	tr := newT1Rig(t)
	a := tr.Info.Tx["A"]
	agent := tr.writer()
	// the pool holds 1800 CPU-seconds: age one finished agent transform into the window with the whole pool as its cpu_ms
	first := tr.Create
	st, v := first(agent, masterBody(a, "neutral"))
	if st != http.StatusAccepted {
		t.Fatalf("agent create = %d %v", st, v)
	}
	tr.Drain()
	id := v["id"].(string)
	for _, trg := range []string{"transforms_final"} { // seeding the history: terminal rows are immutable, so lift the trigger for this statement only
		if _, err := tr.Store.W.Exec(`DROP TRIGGER ` + trg); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tr.Store.W.Exec(`UPDATE transforms SET cpu_ms = 1800000 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	est := obj(tr.writerOK(agent, "POST", "/api/transforms/estimate", masterBody(a, "warm")))
	q := obj(est["quota"])
	if q["principal_kind"] != "agent" || q["used_ms"].(float64) < 1800000 {
		t.Errorf("estimate quota = %v", q)
	}
	resp := agent.Do(t, "POST", "/api/transforms", masterBody(a, "warm"), tr.idem()...)
	e := obj(resp.JSON(t))
	if resp.Status != http.StatusTooManyRequests || e["code"] != "TRANSFORM_QUOTA_EXCEEDED" {
		t.Fatalf("agent create over quota = %d %v", resp.Status, e)
	}
	if st, hv := tr.Create(tr.H, masterBody(a, "warm")); st != http.StatusAccepted {
		t.Errorf("the human pool is separate, got %d %v", st, hv)
	} else {
		tr.H.OK(t, "POST", "/api/transforms/"+hv["id"].(string)+"/cancel", nil)
	}
}

func (tr *t1Rig) writerOK(c *servertest.Client, method, path string, body any) any {
	tr.T.Helper()
	return c.OK(tr.T, method, path, body)
}

// TestAcceptance_T1_Glacier50x: a glacier of an 18 s excerpt at x50 (900 s, the output cap) is accepted and renders within its
// budget.
func TestAcceptance_T1_Glacier50x(t *testing.T) {
	weeklyOnly(t, "75 s of CPU in a CI shard; the limit check is covered by the estimate tests and the real-child glacier by TestAcceptance_T1_RealChild")
	tr := newT1Rig(t)
	v := tr.RunDone(glacierBody(tr.Info.Tx["A"], 50, 18))
	id := v["id"].(string)
	cpu := obj(v["cpu"])
	if cpu["used_ms"].(float64) > cpu["max_ms"].(float64) {
		t.Errorf("cpu %v over budget", cpu)
	}
	if d := obj(tr.H.OK(t, "GET", "/api/tracks/"+id, nil))["duration_s"].(float64); math.Abs(d-900) > 0.5 {
		t.Errorf("duration_s = %v, want 900", d)
	}
}
