package nextactions_test

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/nextactions"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// The sample candidates of the four M5-C1 rules, for the golden texts of testdata/rules_v1_d.golden.json.
func init() {
	take := nextactions.Subject{Kind: "take", ID: ptr("01900000-0000-7000-8000-0000000000a4")}
	samples["callback_waiting"] = sample{take, map[string]any{"callbacks": 2, "_title": "Synthetic Take"}}
	samples["regressed_comment"] = sample{take, map[string]any{"comments": 1, "_title": "Synthetic Take"}}
	samples["open_fix_unaddressed"] = sample{take, map[string]any{"fixes": 3, "_title": "Synthetic Take"}}
	samples["patch_ready_to_render"] = sample{take, map[string]any{"patches": 1, "_patch_id": "01900000-0000-7000-8000-0000000000a5", "_title": "Synthetic Take"}}
}

// ---- behaviour of the four take rules (C257) against the L3 fixture world ----

type takeWorld struct {
	r                  *servertest.Rig
	take, child, other string // a take with a Song, a take that renders the patch, and a take with no Song
	n                  int
}

func newTakeWorld(t *testing.T) *takeWorld {
	t.Helper()
	r := l3Rig(t)
	w := &takeWorld{r: r}
	rows, err := r.Store.R.Query(`SELECT id, EXISTS (SELECT 1 FROM take_songs ts WHERE ts.track_id = tracks.id) FROM tracks WHERE status <> 'discarded' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var withSong, free []string
	for rows.Next() {
		var id string
		var has bool
		if err := rows.Scan(&id, &has); err != nil {
			t.Fatal(err)
		}
		if has {
			withSong = append(withSong, id)
		} else {
			free = append(free, id)
		}
	}
	if len(withSong) < 2 || len(free) < 1 {
		t.Fatalf("fixture shape: %d takes with a Song, %d without", len(withSong), len(free))
	}
	w.take, w.child, w.other = withSong[0], withSong[1], free[0]
	// the fixture's own open fixes would answer for the take; start from a world with none
	exec(t, r, `UPDATE comments SET state = 'resolved', state_changed_at = '2026-10-06T10:00:00Z', state_changed_by = 'human:fixture' WHERE kind = 'fix' AND state = 'open'`)
	return w
}

func (w *takeWorld) next() string {
	w.n++
	return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xf40000+w.n)
}

func (w *takeWorld) comment(t *testing.T, track, kind, state string, hidden bool) string {
	t.Helper()
	id := w.next()
	var hat, hby any
	var sc, sb any
	if hidden {
		hat, hby = "2026-10-06T10:00:00Z", "human:fixture"
	}
	if state != "open" {
		sc, sb = "2026-10-06T10:00:00Z", "human:fixture"
	}
	exec(t, w.r, `INSERT INTO comments(id, track_id, body, kind, state, source, principal_kind, principal_id, principal_name, created_at, updated_at, state_changed_at, state_changed_by, hidden_at, hidden_by)
		VALUES (?,?,?,?,?,'web','human','human:fixture','fixture','2026-10-06T10:00:00Z','2026-10-06T10:00:00Z',?,?,?,?)`, id, track, "synthetic remark", kind, state, sc, sb, hat, hby)
	return id
}

func (w *takeWorld) patch(t *testing.T, base string, comments ...string) string {
	t.Helper()
	id := w.next()
	exec(t, w.r, `INSERT INTO patches(id, base_track_id, base_recipe_hash, operator, ops_json, compiled_json, scope_json, diff_json, rationale, principal_kind, principal_id, principal_name, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, base, "h", "lyric_edit", "[]", "{}", "{}", "{}", "r", "agent", "agent:x", "x", fmt.Sprintf("2026-10-06T11:%02d:00Z", w.n%60))
	for _, c := range comments {
		exec(t, w.r, `INSERT INTO patch_comments(patch_id, comment_id) VALUES (?,?)`, id, c)
	}
	return id
}

// job renders patch: a job of an existing batch with patch_id set from birth (the column is frozen), in state.
func (w *takeWorld) job(t *testing.T, patch, state string) string {
	t.Helper()
	id := w.next()
	finished := "NULL"
	if state == "completed" || state == "failed" {
		finished = "'2026-10-06T12:00:00Z'"
	}
	// jobs are born queued; the synthetic terminal rows skip the transition trigger (the rules only read the state)
	exec(t, w.r, `INSERT INTO jobs(id,batch_id,take_index,kind,engine,backend,recipe_id,source_track_id,operator,mutation_json,title,expected_audio_s,expected_s,timeout_s,
			expected_micro_usd,reserved_micro_usd,state,next_attempt_at,queued_at,created_at,updated_at,patch_id)
		SELECT ?, batch_id, 90 + ?, kind, engine, backend, recipe_id, source_track_id, 'lyric_edit', mutation_json, 'T', expected_audio_s, expected_s, timeout_s, expected_micro_usd,
		       reserved_micro_usd, 'queued', next_attempt_at, queued_at, created_at, updated_at, ? FROM jobs ORDER BY id LIMIT 1`, id, w.n, patch)
	switch state {
	case "queued":
	case "completed", "failed":
		exec(t, w.r, `DROP TRIGGER IF EXISTS jobs_transition`)
		exec(t, w.r, `UPDATE jobs SET state = ?, finished_at = `+finished+` WHERE id = ?`, state, id)
	default:
		t.Fatalf("job state %s", state)
	}
	return id
}

func (w *takeWorld) callback(t *testing.T, child, comment, patch, job string) string {
	t.Helper()
	id := w.next()
	exec(t, w.r, `INSERT INTO callbacks(id, child_track_id, comment_id, source_track_id, kind, patch_id, job_id, created_at) VALUES (?,?,?,?,?,?,?,?)`,
		id, child, comment, w.take, "fix", patch, job, "2026-10-06T12:00:00Z")
	return id
}

func takeActions(t *testing.T, w *takeWorld, p auth.Principal, code string) []nextactions.Action {
	t.Helper()
	return byCode(list(t, w.r, p, nextactions.Filter{Limit: 50}), code)
}

var (
	tHumanRead = auth.Principal{Kind: auth.KindHuman, ID: "h", Name: "h", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}}
	tAgentRead = auth.Principal{Kind: auth.KindAgent, ID: "a", Name: "a", Scopes: []auth.Scope{auth.ScopeRead}}
	tAgentGen  = auth.Principal{Kind: auth.KindAgent, ID: "g", Name: "g", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeGenerate}}
)

func TestNextActions_CallbackWaiting(t *testing.T) {
	t.Parallel()
	w := newTakeWorld(t)
	fix := w.comment(t, w.take, "fix", "addressed", false)
	patch := w.patch(t, w.take, fix)
	job := w.job(t, patch, "queued")
	cb := w.callback(t, w.child, fix, patch, job)
	title := str(t, w.r, `SELECT title FROM tracks WHERE id = ?`, w.child)

	got := takeActions(t, w, tHumanRead, "callback_waiting")
	want := fmt.Sprintf(`1 callback(s) wait on "%s": listen and answer`, title)
	if len(got) != 1 || got[0].Subject.Kind != "take" || *got[0].Subject.ID != w.child || got[0].Text != want || got[0].Priority != 85 || got[0].Evidence["callbacks"] != 1 ||
		got[0].Hint == nil || got[0].Hint.Op != "list_callbacks" || got[0].Hint.Args["child_track_id"] != w.child {
		t.Fatalf("callback_waiting: %+v want text %q", got, want)
	}
	if _, leaked := got[0].Evidence["_title"]; leaked {
		t.Error("the private title left the package")
	}
	if as := takeActions(t, w, tAgentRead, "callback_waiting"); len(as) != 0 {
		t.Error("an agent got a human-only rule")
	}
	// a critic verdict does not answer it; a human one does
	exec(t, w.r, `INSERT INTO callback_verdicts(id, callback_id, source, principal_kind, principal_id, verdict, created_at) VALUES (?,?,'critic','agent','agent:a','fixed','2026-10-06T12:01:00Z')`, w.next(), cb)
	if len(takeActions(t, w, tHumanRead, "callback_waiting")) != 1 {
		t.Error("a critic verdict answered the callback")
	}
	exec(t, w.r, `INSERT INTO callback_verdicts(id, callback_id, source, principal_kind, principal_id, verdict, state_before, state_after, created_at) VALUES (?,?,'human','human','human:h','fixed','addressed','resolved','2026-10-06T12:02:00Z')`, w.next(), cb)
	if as := takeActions(t, w, tHumanRead, "callback_waiting"); len(as) != 0 {
		t.Errorf("an answered callback still waits: %+v", as)
	}
}

func TestNextActions_RegressedComment(t *testing.T) {
	t.Parallel()
	w := newTakeWorld(t)
	w.comment(t, w.take, "fix", "regressed", true) // hidden: not counted
	if as := takeActions(t, w, tHumanRead, "regressed_comment"); len(as) != 0 {
		t.Fatalf("a hidden regressed comment fires: %+v", as)
	}
	w.comment(t, w.take, "fix", "regressed", false)
	w.comment(t, w.take, "note", "regressed", false)
	for _, p := range []auth.Principal{tHumanRead, tAgentRead} {
		as := takeActions(t, w, p, "regressed_comment")
		if len(as) != 1 || *as[0].Subject.ID != w.take || as[0].Evidence["comments"] != 2 || as[0].Priority != 72 || as[0].Hint == nil || as[0].Hint.Op != "get_track_context" || as[0].Hint.Args["id"] != w.take {
			t.Errorf("%s: %+v", p.Kind, as)
		}
	}
}

func TestNextActions_OpenFixUnaddressed_And_PatchReady(t *testing.T) {
	t.Parallel()
	w := newTakeWorld(t)
	fix := w.comment(t, w.take, "fix", "open", false)
	w.comment(t, w.take, "fix", "open", true)   // hidden
	w.comment(t, w.take, "note", "open", false) // not a fix
	w.comment(t, w.child, "fix", "resolved", false)

	as := takeActions(t, w, tAgentRead, "open_fix_unaddressed")
	if len(as) != 1 || *as[0].Subject.ID != w.take || as[0].Evidence["fixes"] != 1 || as[0].Priority != 58 || as[0].Hint.Op != "get_track_context" {
		t.Fatalf("open_fix_unaddressed: %+v", as)
	}
	if len(takeActions(t, w, tHumanRead, "open_fix_unaddressed")) != 0 {
		t.Error("a human got an agent rule")
	}
	// an unrendered patch: the fix is in progress, patch_ready_to_render owns the take
	p1 := w.patch(t, w.take, fix)
	if len(takeActions(t, w, tAgentRead, "open_fix_unaddressed")) != 0 {
		t.Error("open_fix_unaddressed fires while a patch waits for its render")
	}
	if len(takeActions(t, w, tAgentRead, "patch_ready_to_render")) != 0 {
		t.Error("patch_ready_to_render needs the generate scope")
	}
	pr := takeActions(t, w, tAgentGen, "patch_ready_to_render")
	if len(pr) != 1 || *pr[0].Subject.ID != w.take || pr[0].Evidence["patches"] != 1 || pr[0].Priority != 52 || pr[0].Hint.Op != "estimate_generation" {
		t.Fatalf("patch_ready_to_render: %+v", pr)
	}
	items := pr[0].Hint.Args["items"].([]any)[0].(map[string]any)
	if items["mode"] != "song_pr" || items["patch_id"] != p1 {
		t.Errorf("hint %v", pr[0].Hint)
	}
	// a newer patch is the one the hint names; both count
	p2 := w.patch(t, w.take, fix)
	pr = takeActions(t, w, tAgentGen, "patch_ready_to_render")
	if len(pr) != 1 || pr[0].Evidence["patches"] != 2 || pr[0].Hint.Args["items"].([]any)[0].(map[string]any)["patch_id"] != p2 {
		t.Errorf("two patches: %+v", pr)
	}
	// rendering (non-terminal job): neither rule fires for p1/p2's comment
	w.job(t, p1, "queued")
	w.job(t, p2, "queued")
	if len(takeActions(t, w, tAgentGen, "patch_ready_to_render")) != 0 || len(takeActions(t, w, tAgentRead, "open_fix_unaddressed")) != 0 {
		t.Error("a rendering patch still prompts")
	}
	// the renders ended and the comment is still open (the human said not yet): the fix is unaddressed again
	exec(t, w.r, `DROP TRIGGER IF EXISTS jobs_transition`)
	exec(t, w.r, `UPDATE jobs SET state = 'failed', finished_at = '2026-10-06T12:00:00Z' WHERE patch_id IN (?, ?)`, p1, p2)
	if len(takeActions(t, w, tAgentRead, "open_fix_unaddressed")) != 1 {
		t.Error("terminal renders leave the open fix unaddressed")
	}
	if len(takeActions(t, w, tAgentGen, "patch_ready_to_render")) != 0 {
		t.Error("a rendered patch is not ready to render")
	}
}

// TestNextActions_TakeSubject: the List filter accepts subject_kind take (it answered 422 before M5-C1), keeps only that kind and
// that id, and still refuses an unknown kind.
func TestNextActions_TakeSubject(t *testing.T) {
	t.Parallel()
	w := newTakeWorld(t)
	w.comment(t, w.take, "fix", "regressed", false)
	w.comment(t, w.child, "fix", "regressed", false)
	pg := list(t, w.r, tHumanRead, nextactions.Filter{SubjectKind: "take"})
	if len(pg.Items) != 2 || pg.Total != 2 {
		t.Fatalf("take filter: %+v", pg.Items)
	}
	one := list(t, w.r, tHumanRead, nextactions.Filter{SubjectKind: "take", SubjectID: w.child})
	if len(one.Items) != 1 || *one.Items[0].Subject.ID != w.child {
		t.Errorf("take id filter: %+v", one.Items)
	}
	if _, err := w.r.Services.NextActions.List(context.Background(), tHumanRead, nextactions.Filter{SubjectKind: "takes"}); err == nil {
		t.Error("an unknown subject kind is accepted")
	}
}

// TestNextActions_ForTakeCarriesTakeRules: /context's ForTake returns the take rules for that exact take, with or without a Song,
// and none for another take.
func TestNextActions_ForTakeCarriesTakeRules(t *testing.T) {
	t.Parallel()
	w := newTakeWorld(t)
	ctx := context.Background()
	for _, track := range []string{w.take, w.other} {
		w.comment(t, track, "fix", "regressed", false)
	}
	for _, c := range []struct {
		track string
		song  bool
	}{{w.take, true}, {w.other, false}} {
		acts, err := w.r.Services.NextActions.ForTake(ctx, tAgentRead, c.track, 10)
		if err != nil {
			t.Fatal(err)
		}
		var codes []string
		for _, a := range acts {
			if a.Subject.Kind == "take" && *a.Subject.ID != c.track {
				t.Errorf("%s: an action about another take %s", c.track, *a.Subject.ID)
			}
			codes = append(codes, a.Code)
		}
		if !slices.Contains(codes, "regressed_comment") {
			t.Errorf("take %s (song %v): codes %v lack regressed_comment", c.track, c.song, codes)
		}
		if !c.song {
			for _, a := range acts {
				if a.Subject.Kind != "take" {
					t.Errorf("a take without a Song got a %s action", a.Subject.Kind)
				}
			}
		}
	}
	// a take nothing is wrong with has none of the four
	acts, err := w.r.Services.NextActions.ForTake(ctx, tAgentGen, w.child, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range acts {
		if a.Subject.Kind == "take" {
			t.Errorf("clean take %s got %s", w.child, a.Code)
		}
	}
	// every take code is evaluated by ForTake
	for _, code := range []string{"callback_waiting", "regressed_comment", "open_fix_unaddressed", "patch_ready_to_render"} {
		if !nextactions.ForTakeCodes()[code] {
			t.Errorf("ForTake does not evaluate %s", code)
		}
	}
}

// TestNextActions_TakeRulesHintsValidate: the hints of the four rules satisfy the generated input schemas and name free operations.
func TestNextActions_TakeRulesHintsValidate(t *testing.T) {
	t.Parallel()
	doc, ops := loadSpec(t)
	w := newTakeWorld(t)
	fix := w.comment(t, w.take, "fix", "addressed", false)
	patch := w.patch(t, w.take, fix)
	w.callback(t, w.child, fix, patch, w.job(t, patch, "queued"))
	w.comment(t, w.take, "fix", "regressed", false)
	w.comment(t, w.take, "fix", "open", false)
	w.patch(t, w.other)
	seen := map[string]bool{}
	for _, v := range []auth.Principal{tHumanRead, tAgentGen} {
		for _, a := range list(t, w.r, v, nextactions.Filter{Limit: 50, SubjectKind: "take"}).Items {
			seen[a.Code] = true
			if a.Hint == nil {
				t.Errorf("%s has no hint", a.Code)
				continue
			}
			validateHint(t, doc, ops, v, *a.Hint)
		}
	}
	for _, code := range []string{"callback_waiting", "regressed_comment", "open_fix_unaddressed", "patch_ready_to_render"} {
		if !seen[code] {
			t.Errorf("%s was not exercised", code)
		}
	}
}
