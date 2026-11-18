package acceptance_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/archivetest"
	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// E2b attach ingest on the fake ACE-Step backend (C157.1, C166): stems are artifacts of their source take and lyrics.lrc rides
// along with a normal take.

// fetchMedia GETs a signed relative media URL and returns the body and content type.
func fetchMedia(t *testing.T, r *servertest.Rig, url string) ([]byte, string) {
	t.Helper()
	resp, err := http.Get(r.URL + url) //nolint:gosec,noctx // loopback test server
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("GET media: %d", resp.StatusCode)
	}
	return body, resp.Header.Get("Content-Type")
}

func sha256Hex(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// stemNames reads the names of the stem entries of get_track.
func stemNames(t *testing.T, tr map[string]any) []string {
	t.Helper()
	var out []string
	for _, s := range arr(tr["stems"]) {
		out = append(out, obj(s)["artifact"].(string))
	}
	return out
}

// TestAcceptance_E2_ExtractStems: an extract job attaches its stem to the source take (no new take, no Song or Pick effect), a
// repeated stem is numbered (and the estimate warned stem_exists), the job and track views name them, and the media links are
// signed and serve the very bytes of the artifacts.
func TestAcceptance_E2_ExtractStems(t *testing.T) {
	t.Parallel()
	r := aceRig(t, archivetest.SyntheticArchive(t, "acceptance"), aceOpts{adopt: true, allOn: true, audioMax: 100})
	h := r.Human(t)
	base := aceBase(t, r, "ex-base", 40, map[string]any{"lrc": true})
	library := func() string {
		return rowsDump(t, r, `SELECT (SELECT COUNT(*) FROM tracks), (SELECT COUNT(*) FROM mutations), (SELECT COUNT(*) FROM songs), (SELECT COUNT(*) FROM song_picks),
			(SELECT COUNT(*) FROM pick_events), (SELECT COUNT(*) FROM renditions), (SELECT COUNT(*) FROM track_tags)`)
	}
	before := library()

	est, _, jobs := aceRun(t, r, editBody("stems", base, map[string]any{"tracks": []string{"drums", "bass"}}, nil), "ex-stems")
	if strings.Contains(string(mustJSON(t, est)), "stem_exists") {
		t.Error("a first stem was warned stem_exists")
	}
	stems := map[string]string{} // stem name -> job
	for _, j := range jobs {
		if s, code := jobState(t, r, j); s != "completed" {
			t.Fatalf("stem job %s: %s (%s)", j, s, code)
		}
		v := obj(h.OK(t, "GET", "/api/jobs/"+j, nil))
		if v["task"] != "extract" || v["track_id"] != base || v["outcome"] != "clean" || v["state"] != "completed" {
			t.Errorf("job view = task %v track %v outcome %v", v["task"], v["track_id"], v["outcome"])
		}
		stems[v["stem"].(string)] = j
		if env := jobRow(t, r, j, "env_id")[0]; env == nil {
			t.Errorf("job %s has no env", j)
		}
	}
	if len(stems) != 2 || stems["drums"] == "" || stems["bass"] == "" {
		t.Fatalf("stems by job = %v", stems)
	}

	// the stem is a frozen artifact of the source take, byte-identical to what the worker uploaded
	for _, name := range []string{"stem-drums.flac", "stem-bass.flac"} {
		var path, sha string
		var n int64
		if err := r.Store.R.QueryRow(`SELECT path, sha256, bytes FROM artifacts WHERE track_id = ? AND name = ?`, base, name).Scan(&path, &sha, &n); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		raw, err := os.ReadFile(filepath.Join(r.DataDir, path))
		if err != nil || sha256Hex(raw) != sha || int64(len(raw)) != n {
			t.Errorf("%s: file mismatch (%v)", name, err)
		}
		if st, _ := os.Stat(filepath.Join(r.DataDir, path)); st == nil || st.Mode().Perm() != 0o444 {
			t.Errorf("%s: not frozen at 0444", name)
		}
	}
	// the two stems are distinct audio
	var s1, s2 string
	_ = r.Store.R.QueryRow(`SELECT sha256 FROM artifacts WHERE track_id = ? AND name = 'stem-drums.flac'`, base).Scan(&s1)
	_ = r.Store.R.QueryRow(`SELECT sha256 FROM artifacts WHERE track_id = ? AND name = 'stem-bass.flac'`, base).Scan(&s2)
	if s1 == s2 {
		t.Error("drums and bass are the same audio")
	}
	// nothing else in the library moved
	if after := library(); after != before {
		t.Errorf("library rows changed:\nbefore %safter  %s", before, after)
	}

	// the same stem again: warned in the estimate, numbered in the take
	est2, _, jobs2 := aceRun(t, r, editBody("stems", base, map[string]any{"tracks": []string{"drums"}}, nil), "ex-stems-2")
	if !strings.Contains(string(mustJSON(t, est2)), "stem_exists") {
		t.Errorf("the repeated stem was not warned: %s", mustJSON(t, est2))
	}
	if s, code := jobState(t, r, jobs2[0]); s != "completed" {
		t.Fatalf("repeated stem: %s (%s)", s, code)
	}
	if n := count(t, r, `SELECT COUNT(*) FROM artifacts WHERE track_id = ? AND name = 'stem-drums-2.flac'`, base); n != 1 {
		t.Error("the repeated stem is not stem-drums-2.flac")
	}

	// the track view lists them and the lrc
	tr := obj(h.OK(t, "GET", "/api/tracks/"+base, nil))
	got := stemNames(t, tr)
	slices.Sort(got)
	if want := []string{"stem-bass.flac", "stem-drums-2.flac", "stem-drums.flac"}; !slices.Equal(got, want) {
		t.Errorf("get_track stems = %v, want %v", got, want)
	}
	if obj(tr["lrc"])["sha256"] == nil {
		t.Errorf("get_track lrc = %v", tr["lrc"])
	}

	// the media links are signed and serve the artifacts
	ml := obj(h.OK(t, "GET", "/api/tracks/"+base+"/media", nil))
	links := arr(ml["stems"])
	if len(links) != 3 {
		t.Fatalf("media links stems = %d", len(links))
	}
	for _, l := range links {
		lk := obj(l)
		url := lk["url"].(string)
		if !strings.Contains(url, "sig=") || !strings.HasPrefix(url, "/media/"+lk["sha256"].(string)+".flac") {
			t.Errorf("stem link %q is not a signed flac link", url)
		}
		body, ct := fetchMedia(t, r, url)
		if ct != "audio/flac" || sha256Hex(body) != lk["sha256"] || int64(len(body)) != int64(lk["bytes"].(float64)) {
			t.Errorf("stem %v served %s %d bytes", lk["name"], ct, len(body))
		}
		if n := lk["name"].(string); n != "drums" && n != "bass" {
			t.Errorf("stem link name %q", n)
		}
	}
	lrc := obj(ml["lrc"])
	if lrc == nil || !strings.Contains(lrc["url"].(string), "sig=") || !strings.HasSuffix(strings.SplitN(lrc["url"].(string), "?", 2)[0], ".lrc") {
		t.Fatalf("media links lrc = %v", ml["lrc"])
	}
	body, ct := fetchMedia(t, r, lrc["url"].(string))
	if !strings.HasPrefix(ct, "text/plain") || sha256Hex(body) != lrc["sha256"] || !bytes.HasPrefix(body, []byte("[00:00.00]")) {
		t.Errorf("lrc served %q %q", ct, body)
	}
	// a link of a take without stems or lrc lists none
	other := aceBase(t, r, "ex-plain", 12, nil)
	om := obj(h.OK(t, "GET", "/api/tracks/"+other+"/media", nil))
	if len(arr(om["stems"])) != 0 || om["lrc"] != nil {
		t.Errorf("plain take media links: stems %v lrc %v", om["stems"], om["lrc"])
	}
	// an unsigned or foreign signature is refused like any media link
	url := obj(links[0])["url"].(string)
	resp, err := http.Get(r.URL + strings.SplitN(url, "sig=", 2)[0] + "sig=AAAA") //nolint:gosec,noctx // loopback test server
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("a forged stem link answered %d", resp.StatusCode)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestAcceptance_E2_LRCInvalidDropped: a take that asked for LRC and received a lyrics.lrc without time stamps completes
// without it and records an lrc_dropped event; a valid one is stored (the control).
func TestAcceptance_E2_LRCInvalidDropped(t *testing.T) {
	t.Parallel()
	r := aceRig(t, archivetest.SyntheticArchive(t, "acceptance"), aceOpts{adopt: true, allOn: true, audioMax: 100})
	h := r.Human(t)

	_, _, bad := aceRun(t, r, composeBody("lrc-bad", 20, map[string]any{"lrc": true, "caption": "synth pop, warm fake:bad_lrc"}), "lrc-bad")
	if s, code := jobState(t, r, bad[0]); s != "completed" {
		t.Fatalf("take with an invalid lrc: %s (%s)", s, code)
	}
	tk := jobTrack(t, r, bad[0])
	if n := count(t, r, `SELECT COUNT(*) FROM artifacts WHERE track_id = ? AND name = 'lyrics.lrc'`, tk); n != 0 {
		t.Error("an invalid lyrics.lrc was kept")
	}
	if n := count(t, r, `SELECT COUNT(*) FROM artifacts WHERE track_id = ?`, tk); n < 3 {
		t.Errorf("the take lost its other artifacts (%d)", n)
	}
	if jobEventCount(t, r, bad[0], "lrc_dropped") != 1 {
		t.Error("no lrc_dropped event")
	}
	v := obj(h.OK(t, "GET", "/api/tracks/"+tk, nil))
	if v["lrc"] != nil || len(arr(v["stems"])) != 0 {
		t.Errorf("track view: lrc %v stems %v", v["lrc"], v["stems"])
	}
	events := arr(obj(h.OK(t, "GET", "/api/jobs/"+bad[0], nil))["events"])
	seen := false
	for _, e := range events {
		seen = seen || obj(e)["event"] == "lrc_dropped"
	}
	if !seen {
		t.Error("the job view does not show lrc_dropped")
	}

	// control: a valid LRC is stored and linked
	_, _, good := aceRun(t, r, composeBody("lrc-good", 20, map[string]any{"lrc": true}), "lrc-good")
	gt := jobTrack(t, r, good[0])
	if n := count(t, r, `SELECT COUNT(*) FROM artifacts WHERE track_id = ? AND name = 'lyrics.lrc'`, gt); n != 1 {
		t.Error("a valid lyrics.lrc was not stored")
	}
	if jobEventCount(t, r, good[0], "lrc_dropped") != 0 {
		t.Error("a valid lrc was reported dropped")
	}
	if obj(obj(h.OK(t, "GET", "/api/tracks/"+gt, nil))["lrc"])["sha256"] == nil {
		t.Error("get_track shows no lrc")
	}
}

// TestAcceptance_E2_InputMismatchTerminalCode: the worker checks the source it fetched against the digest the app pinned; when the
// file on disk no longer matches, the worker answers INPUT_MISMATCH and the job ends with that code, not WORKER_LOST (B-INT saw
// the latter). The stem attaches nothing.
func TestAcceptance_E2_InputMismatchTerminalCode(t *testing.T) {
	t.Parallel()
	r := aceRig(t, archivetest.SyntheticArchive(t, "acceptance"), aceOpts{adopt: true, allOn: true, audioMax: 100})
	base := aceBase(t, r, "im-base", 40, nil)
	est, batch := aceCreate(t, r, editBody("stems", base, map[string]any{"tracks": []string{"vocals"}}, nil), "im-stems")
	_ = est
	var path string
	if err := r.Store.R.QueryRow(`SELECT master_path FROM tracks WHERE id = ?`, base).Scan(&path); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(r.DataDir, path)
	raw, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(abs, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, append(raw, 0), 0o644); err != nil { // a byte the pinned digest does not know
		t.Fatal(err)
	}
	job := obj(arr(batch["jobs"])[0])["id"].(string)
	pump(t, r, func() bool { s, _ := jobState(t, r, job); return terminalJob(s) })
	if s, code := jobState(t, r, job); s != "failed" || code != "INPUT_MISMATCH" {
		t.Errorf("job ended %s %s, want failed INPUT_MISMATCH", s, code)
	}
	if n := count(t, r, `SELECT COUNT(*) FROM artifacts WHERE track_id = ? AND name LIKE 'stem-%'`, base); n != 0 {
		t.Errorf("%d stems attached by a failed job", n)
	}
	if err := os.WriteFile(abs, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(abs, 0o444)
}

// TestAcceptance_E2_RemixFromStem: the remix pipeline end to end on the fake. Without the stem the estimate is refused (nothing is
// chained or spent); after a stems item attached the vocals stem, a complete item with edit.source_stem renders a new take whose
// parent is the source take, whose request pins the stem digest (the signed input is the stem file: the fake verifies it) and whose
// caption is the new style.
func TestAcceptance_E2_RemixFromStem(t *testing.T) {
	t.Parallel()
	r := aceRig(t, archivetest.SyntheticArchive(t, "acceptance"), aceOpts{adopt: true, allOn: true, audioMax: 100})
	h := r.Human(t)
	base := aceBase(t, r, "rmx-base", 40, nil)
	remix := func() map[string]any {
		b := editBody("complete", base, map[string]any{"tracks": []string{"drums", "bass"}, "source_stem": "vocals"}, nil)
		obj(arr(b["items"])[0])["patch"] = map[string]any{"style": "slow ambient, warm pads"}
		return b
	}

	before := count(t, r, `SELECT COUNT(*) FROM estimates`)
	resp := h.Do(t, "POST", "/api/generations/estimate", remix())
	code, d := problemCode(t, resp)
	if resp.Status != 422 || code != "INVALID_ITEM" || d["rule"] != "source_stem_missing" {
		t.Fatalf("missing stem: %d %s", resp.Status, resp.Body)
	}
	if n := count(t, r, `SELECT COUNT(*) FROM estimates`); n != before {
		t.Error("a refused remix wrote an estimate")
	}

	_, _, sj := aceRun(t, r, editBody("stems", base, map[string]any{"tracks": []string{"vocals"}}, nil), "rmx-stems")
	if s, code := jobState(t, r, sj[0]); s != "completed" {
		t.Fatalf("stems job %s (%s)", s, code)
	}
	var stemSHA string
	if err := r.Store.R.QueryRow(`SELECT sha256 FROM artifacts WHERE track_id = ? AND name = 'stem-vocals.flac'`, base).Scan(&stemSHA); err != nil {
		t.Fatal(err)
	}

	est, _, jobs := aceRun(t, r, remix(), "rmx-complete")
	if !strings.Contains(string(mustJSON(t, est)), "source_is_stem") {
		t.Error("the estimate did not warn source_is_stem")
	}
	if ed := obj(obj(arr(obj(arr(est["items"])[0])["takes"])[0])["edit"]); ed["source_stem"] != "vocals" {
		t.Errorf("the estimate echo = %v, want source_stem vocals", ed)
	}
	if s, code := jobState(t, r, jobs[0]); s != "completed" {
		t.Fatalf("remix job %s (%s)", s, code)
	}
	child := jobTrack(t, r, jobs[0])
	var parent, req, op, patch string
	if err := r.Store.R.QueryRow(`SELECT t.parent_track_id, rc.request_json, m.operator, m.recipe_patch_json FROM tracks t JOIN recipes rc ON rc.id = t.recipe_id
		JOIN mutations m ON m.child_track_id = t.id WHERE t.id = ?`, child).Scan(&parent, &req, &op, &patch); err != nil {
		t.Fatal(err)
	}
	var rq acestep.Request
	if err := json.Unmarshal([]byte(req), &rq); err != nil {
		t.Fatal(err)
	}
	if parent != base || op != "complete_tracks" || rq.Task != "complete" || rq.Src == nil || rq.Src.TrackID != base || rq.Src.MasterSHA256 != stemSHA ||
		rq.Caption != "slow ambient, warm pads" || !strings.Contains(patch, `"source_stem":"vocals"`) {
		t.Errorf("remix: parent=%s op=%s src=%+v caption=%q patch=%s", parent, op, rq.Src, rq.Caption, patch)
	}
	if n := count(t, r, `SELECT COUNT(*) FROM artifacts WHERE track_id = ? AND name LIKE 'stem-%'`, child); n != 0 {
		t.Errorf("the remix take has %d stem artifacts", n)
	}
}
