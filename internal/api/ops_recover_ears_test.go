package api_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/ears/tierb/tierbtest"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// recoverSong is an invented Italian lyric: a verse, a chorus and a closing verse (no real lyrics anywhere in the tests).
const recoverSong = "[verse]\nla luna cammina sul prato\nil vento racconta una storia\n[chorus]\nsole sole porta il giorno\ncanta con me fino a sera\n[verse]\nla strada e lunga e stretta\nma il cuore conosce la via"

func recoverRig(t *testing.T) *servertest.Rig {
	t.Helper()
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, Files: true, M2b: servertest.M2bOptions{Fixture: true}})
	r.Settle(t)
	return r
}

func recoverPath(id string) string { return "/api/tracks/" + id + "/recover" }

// tableSums reads the rows of the reference that Recover must never touch.
func refRows(t *testing.T, r *servertest.Rig, id string) string {
	t.Helper()
	var out []string
	for _, q := range []string{
		`SELECT COALESCE(group_concat(t.id || '|' || t.recipe_id || '|' || t.status || '|' || t.master_sha256 || '|' || COALESCE(t.parent_track_id,''), ';'), '') FROM tracks t WHERE t.id = ?1`,
		`SELECT COALESCE(group_concat(r.id || '|' || r.engine || '|' || r.request_json, ';'), '') FROM recipes r JOIN tracks t ON t.recipe_id = r.id WHERE t.id = ?1`,
		`SELECT COALESCE(group_concat(source || '|' || idx || '|' || label, ';'), '') FROM sections WHERE track_id = ?1`,
		`SELECT COALESCE(group_concat(analyzer || '|' || version || '|' || metrics_json, ';'), '') FROM analyses WHERE track_id = ?1`,
		`SELECT COALESCE(group_concat(key || '|' || value, ';'), '') FROM (SELECT tag_id AS key, source AS value FROM track_tags WHERE track_id = ?1)`,
		`SELECT COUNT(*) FROM mutations WHERE parent_track_id = ?1 OR child_track_id = ?1`,
	} {
		var s string
		if err := r.Store.R.QueryRow(q, id).Scan(&s); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		out = append(out, s)
	}
	return strings.Join(out, "\n")
}

func counts(t *testing.T, r *servertest.Rig, tables ...string) string {
	t.Helper()
	var out []string
	for _, tb := range tables {
		var n int
		if err := r.Store.R.QueryRow(`SELECT COUNT(*) FROM ` + tb).Scan(&n); err != nil { //nolint:gosec // G202: fixed table names of the test
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%s=%d", tb, n))
	}
	return strings.Join(out, " ")
}

// TestRecoverRecipe_CreatesDraftAndRow: a reference with a stored listening check becomes a new draft (origin recovered, no parent,
// the placeholder style), the draft_recoveries row is written with it, and the answer carries the transcript, the evidence and the
// lint of the draft. Each call makes another draft. Nothing is spent: no job, estimate, batch or ledger row appears.
func TestRecoverRecipe_CreatesDraftAndRow(t *testing.T) {
	t.Parallel()
	r := recoverRig(t)
	owner := r.Human(t)
	storeEarsDoc(t, r, lt.TrackRefFlac, tierbtest.FromLyrics(recoverSong, tierbtest.Opts{}))
	spendTables := []string{"jobs", "estimates", "batches", "mutations", "tracks", "recipes", "sections"}
	before := counts(t, r, spendTables...)

	resp := owner.Do(t, "POST", recoverPath(lt.TrackRefFlac), map[string]any{"rationale": "rebuild it from the recording"}, "Idempotency-Key", "rec-1")
	if resp.Status != 201 {
		t.Fatalf("status %d: %s", resp.Status, resp.Body)
	}
	b := obj(t, resp.JSON(t))
	d := obj(t, b["draft"])
	req := obj(t, d["request"])
	if d["origin"] != "recovered" || d["status"] != "backlog" || d["parent_track_id"] != nil || d["editable"] != true {
		t.Errorf("draft = origin %v status %v parent %v", d["origin"], d["status"], d["parent_track_id"])
	}
	if id, _ := d["request_id"].(string); !strings.HasPrefix(id, "rec-") || len(id) < 11 || req["cot"] != "melody" {
		t.Errorf("request id %q cot %v", d["request_id"], req["cot"])
	}
	style, _ := req["style"].(string)
	if b["style_source"] != "placeholder" || !strings.HasPrefix(style, "italian vocals") {
		t.Errorf("style %q source %v", style, b["style_source"])
	}
	lyrics, _ := req["lyrics"].(string)
	if !strings.Contains(lyrics, "la luna cammina sul prato") || !strings.Contains(lyrics, "[intro]") {
		t.Errorf("lyrics = %q", lyrics)
	}
	tr := obj(t, b["transcript"])
	if tr["language"] != "it" || tr["words"] != float64(33) || tr["lines"] != float64(6) || tr["confidence"] != 0.9 {
		t.Errorf("transcript = %v", tr)
	}
	if secs, _ := tr["sections"].([]any); len(secs) < 2 {
		t.Errorf("sections = %v", tr["sections"])
	}
	if _, ok := obj(t, b["evidence"])["duration_s"]; !ok {
		t.Errorf("evidence = %v", b["evidence"])
	}
	if _, ok := obj(t, d["lint"])["warnings"]; !ok {
		t.Errorf("lint missing: %v", d["lint"])
	}

	var track, sha, style2, rationale, by string
	var words int
	var conf float64
	if err := r.Store.R.QueryRow(`SELECT track_id, analysis_sha256, words, confidence, style_source, rationale, created_by FROM draft_recoveries WHERE draft_id = ?`, d["id"]).
		Scan(&track, &sha, &words, &conf, &style2, &rationale, &by); err != nil {
		t.Fatal(err)
	}
	if track != lt.TrackRefFlac || len(sha) != 64 || words != 33 || conf != 0.9 || style2 != "placeholder" || rationale != "rebuild it from the recording" || by == "" {
		t.Errorf("row = %s %s %d %v %s %q %s", track, sha, words, conf, style2, rationale, by)
	}
	after := counts(t, r, spendTables...)
	if after != before {
		t.Errorf("Recover changed spend or library tables: %s -> %s", before, after)
	}

	// a replay with the same key answers the same draft; a new key makes a new draft with another id and seed
	if again := owner.Do(t, "POST", recoverPath(lt.TrackRefFlac), map[string]any{"rationale": "rebuild it from the recording"}, "Idempotency-Key", "rec-1"); again.Status != 201 || obj(t, obj(t, again.JSON(t))["draft"])["id"] != d["id"] {
		t.Errorf("replay: %d %s", again.Status, again.Body)
	}
	second := obj(t, obj(t, owner.OK(t, "POST", recoverPath(lt.TrackRefFlac), map[string]any{"style": "italian vocals, 90 BPM, A minor", "cot": "full", "title": "Second try"}, "Idempotency-Key", "rec-2"))["draft"])
	if second["id"] == d["id"] || second["request_id"] == d["request_id"] || second["label"] != "Second try" || obj(t, second["request"])["cot"] != "full" {
		t.Errorf("second draft = %v", second)
	}
	var n int
	if err := r.Store.R.QueryRow(`SELECT COUNT(*) FROM draft_recoveries WHERE track_id = ?`, lt.TrackRefFlac).Scan(&n); err != nil || n != 2 {
		t.Errorf("recoveries = %d (%v), want 2", n, err)
	}
	var src string
	if err := r.Store.R.QueryRow(`SELECT style_source FROM draft_recoveries WHERE draft_id = ?`, second["id"]).Scan(&src); err != nil || src != "caller" {
		t.Errorf("second style_source = %q (%v)", src, err)
	}
}

// TestRecoverRecipe_UnknownLanguage: a document whose recogniser reported no language code (auto) recovers with an empty language,
// a NULL column and a style without a language word.
func TestRecoverRecipe_UnknownLanguage(t *testing.T) {
	t.Parallel()
	r := recoverRig(t)
	owner := r.Human(t)
	storeEarsDoc(t, r, lt.TrackRefFlac, tierbtest.FromLyrics(recoverSong, tierbtest.Opts{Lang: "auto"}))
	b := obj(t, owner.OK(t, "POST", recoverPath(lt.TrackRefFlac), map[string]any{}, "Idempotency-Key", "k"))
	if lang := obj(t, b["transcript"])["language"]; lang != "" {
		t.Errorf("language = %v, want empty", lang)
	}
	style := obj(t, obj(t, b["draft"])["request"])["style"].(string)
	if !strings.HasPrefix(style, "vocals") {
		t.Errorf("style = %q", style)
	}
	var null bool
	if err := r.Store.R.QueryRow(`SELECT language IS NULL FROM draft_recoveries WHERE draft_id = ?`, obj(t, b["draft"])["id"]).Scan(&null); err != nil || !null {
		t.Errorf("language NULL = %v (%v)", null, err)
	}
}

// TestRecoverRecipe_ReferenceUnchanged: the reference's track row, recipe stub, sections, analyses, tags and ledger edges are
// byte-identical after a Recover.
func TestRecoverRecipe_ReferenceUnchanged(t *testing.T) {
	t.Parallel()
	r := recoverRig(t)
	owner := r.Human(t)
	storeEarsDoc(t, r, lt.TrackRefFlac, tierbtest.FromLyrics(recoverSong, tierbtest.Opts{}))
	before := refRows(t, r, lt.TrackRefFlac)
	if !strings.Contains(before, "tier_b_raw") {
		t.Fatalf("the reference has no Tier B row to protect:\n%s", before)
	}
	owner.OK(t, "POST", recoverPath(lt.TrackRefFlac), map[string]any{}, "Idempotency-Key", "k")
	if after := refRows(t, r, lt.TrackRefFlac); after != before {
		t.Errorf("the reference changed:\n%s\n---\n%s", before, after)
	}
}

// TestRecoverRecipe_NotAReference422: a take with a known recipe is RECIPE_KNOWN, and nothing is written.
func TestRecoverRecipe_NotAReference422(t *testing.T) {
	t.Parallel()
	r := recoverRig(t)
	owner := r.Human(t)
	storeEarsDoc(t, r, lt.TrackLuna, tierbtest.FromLyrics(earsBLyrics, tierbtest.Opts{}))
	resp := owner.Do(t, "POST", recoverPath(lt.TrackLuna), map[string]any{}, "Idempotency-Key", "k")
	if resp.Status != 422 || code(t, resp) != "RECIPE_KNOWN" {
		t.Fatalf("%d %s", resp.Status, resp.Body)
	}
	if n := counts(t, r, "draft_recoveries"); n != "draft_recoveries=0" {
		t.Error(n)
	}
}

// TestRecoverRecipe_NoAnalysis409: a reference without a usable listening check is 409 ANALYSIS_UNAVAILABLE with the state and the
// hint to estimate an analyze item; an unknown track is 404; a document with no sung word is 422 TRANSCRIPT_EMPTY.
func TestRecoverRecipe_NoAnalysis409(t *testing.T) {
	t.Parallel()
	r := recoverRig(t)
	owner := r.Human(t)
	resp := owner.Do(t, "POST", recoverPath(lt.TrackRefFlac), map[string]any{}, "Idempotency-Key", "k1")
	if resp.Status != 409 || code(t, resp) != "ANALYSIS_UNAVAILABLE" {
		t.Fatalf("%d %s", resp.Status, resp.Body)
	}
	d := obj(t, obj(t, resp.JSON(t))["details"])
	if d["analyzer"] != "tier_b" || d["state"] != "none" || d["hint"] != "estimate an analyze item" {
		t.Errorf("details = %v", d)
	}
	if got := owner.Do(t, "POST", recoverPath("no-such-track"), map[string]any{}, "Idempotency-Key", "k2"); got.Status != 404 {
		t.Errorf("unknown track: %d %s", got.Status, got.Body)
	}
	// a stored error document holds no transcript
	storeEarsDoc(t, r, lt.TrackRefFlac, tierbtest.FromLyrics(recoverSong, tierbtest.Opts{Status: "error"}))
	resp = owner.Do(t, "POST", recoverPath(lt.TrackRefFlac), map[string]any{}, "Idempotency-Key", "k3")
	if resp.Status != 409 || obj(t, obj(t, resp.JSON(t))["details"])["state"] != "error" {
		t.Errorf("error document: %d %s", resp.Status, resp.Body)
	}
	if n := counts(t, r, "draft_recoveries", "recipe_drafts"); !strings.HasPrefix(n, "draft_recoveries=0") {
		t.Error(n)
	}
}

func TestRecoverRecipe_TranscriptEmpty(t *testing.T) {
	t.Parallel()
	r := recoverRig(t)
	owner := r.Human(t)
	drops := map[int]bool{0: true, 1: true, 2: true}
	storeEarsDoc(t, r, lt.TrackRefFlac, tierbtest.FromLyrics(recoverSong, tierbtest.Opts{DropSections: drops}))
	resp := owner.Do(t, "POST", recoverPath(lt.TrackRefFlac), map[string]any{}, "Idempotency-Key", "k")
	if resp.Status != 422 || code(t, resp) != "TRANSCRIPT_EMPTY" {
		t.Fatalf("%d %s", resp.Status, resp.Body)
	}
}

// TestRecoverRecipe_Authorization: write scope. An Editor and a write agent recover; a Listener and a read-scoped agent are refused
// (403) and nothing is written.
func TestRecoverRecipe_Authorization(t *testing.T) {
	t.Parallel()
	r := recoverRig(t)
	storeEarsDoc(t, r, lt.TrackRefFlac, tierbtest.FromLyrics(recoverSong, tierbtest.Opts{}))
	editor := r.User(t, "bob", config.RoleEditor)
	listener := r.User(t, "carol", config.RoleListener)
	readAgent, _ := r.Agent(t, auth.ScopeRead)
	writeAgent, _ := r.Agent(t, auth.ScopeWrite)
	for name, c := range map[string]*servertest.Client{"listener": listener, "read agent": readAgent} {
		if got := c.Do(t, "POST", recoverPath(lt.TrackRefFlac), map[string]any{}, "Idempotency-Key", "k-"+strings.ReplaceAll(name, " ", "-")); got.Status != 403 {
			t.Errorf("%s: %d %s, want 403", name, got.Status, got.Body)
		}
	}
	if n := counts(t, r, "draft_recoveries"); n != "draft_recoveries=0" {
		t.Errorf("a refused call wrote a row: %s", n)
	}
	for name, c := range map[string]*servertest.Client{"editor": editor, "write agent": writeAgent} {
		got := c.Do(t, "POST", recoverPath(lt.TrackRefFlac), map[string]any{}, "Idempotency-Key", "k-"+strings.ReplaceAll(name, " ", "-"))
		if got.Status != 201 {
			t.Errorf("%s: %d %s", name, got.Status, got.Body)
		}
	}
	// an agent never sees the principal key of the draft's author
	got := writeAgent.Do(t, "POST", recoverPath(lt.TrackRefFlac), map[string]any{}, "Idempotency-Key", "k-again")
	if cb, _ := obj(t, obj(t, got.JSON(t))["draft"])["created_by"].(string); !strings.HasPrefix(cb, "agent:") {
		t.Errorf("created_by = %q", cb)
	}
}

// TestRecoverRecipe_BodyLimits: the documented bounds of the body are enforced.
func TestRecoverRecipe_BodyLimits(t *testing.T) {
	t.Parallel()
	r := recoverRig(t)
	owner := r.Human(t)
	storeEarsDoc(t, r, lt.TrackRefFlac, tierbtest.FromLyrics(recoverSong, tierbtest.Opts{}))
	for name, body := range map[string]map[string]any{
		"empty style":    {"style": ""},
		"long style":     {"style": strings.Repeat("s", 4001)},
		"bad cot":        {"cot": "loud"},
		"long title":     {"title": strings.Repeat("t", 201)},
		"long rationale": {"rationale": strings.Repeat("r", 1001)},
	} {
		if got := owner.Do(t, "POST", recoverPath(lt.TrackRefFlac), body, "Idempotency-Key", "k-"+strings.ReplaceAll(name, " ", "-")); got.Status != 422 {
			t.Errorf("%s: %d %s, want 422", name, got.Status, got.Body)
		}
	}
}

// TestPlaceholderStyle: "<language> vocals, <tempo> BPM, <key>", every unknown part left out.
func TestPlaceholderStyle(t *testing.T) {
	t.Parallel()
	bpm, key := 103.6, "A minor"
	for _, c := range []struct {
		lang string
		ev   ears.RecoverEvidence
		want string
	}{
		{"it", ears.RecoverEvidence{TempoBPM: &bpm, Key: &key}, "italian vocals, 104 BPM, A minor"},
		{"it", ears.RecoverEvidence{TempoBPM: &bpm}, "italian vocals, 104 BPM"},
		{"it", ears.RecoverEvidence{Key: &key}, "italian vocals, A minor"},
		{"es", ears.RecoverEvidence{}, "spanish vocals"},
		{"nl", ears.RecoverEvidence{TempoBPM: &bpm}, "nl vocals, 104 BPM"},
		{"", ears.RecoverEvidence{Key: &key}, "vocals, A minor"},
	} {
		if got := ears.PlaceholderStyle(c.lang, c.ev); got != c.want {
			t.Errorf("PlaceholderStyle(%q) = %q, want %q", c.lang, got, c.want)
		}
	}
}
