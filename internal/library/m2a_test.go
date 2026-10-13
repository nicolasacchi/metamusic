package library_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

func exec(t *testing.T, e *env, q string, a ...any) {
	t.Helper()
	if _, err := e.st.W.Exec(q, a...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func ip(i int) *int { return &i }

// TestSearchTracks_JudgmentFilters is the M2a filter matrix (rev 3 ids: track 1/2/3 =
// Luna/Citta/TwinA).
func TestSearchTracks_JudgmentFilters(t *testing.T) {
	t.Parallel()
	e := newEnvM2a(t)
	sameSet(t, "min_stars=4", search(t, e, library.SearchParams{MinStars: ip(4)}), lt.TrackLuna)
	sameSet(t, "min_stars=3", search(t, e, library.SearchParams{MinStars: ip(3)}), lt.TrackLuna, lt.TrackCitta)
	sameSet(t, "min_stars=5", search(t, e, library.SearchParams{MinStars: ip(5)}), lt.TrackLuna)
	// unrated: no human row (TwinA has only an agent row)
	sameSet(t, "unrated", search(t, e, library.SearchParams{Unrated: true}), lt.TrackTwinA, lt.TrackTwinB, lt.TrackShort)
	// ... and a cleared human rating counts as unrated
	exec(t, e, `UPDATE ratings SET stars = NULL WHERE track_id = ? AND source = 'human'`, lt.TrackCitta)
	sameSet(t, "unrated incl. cleared", search(t, e, library.SearchParams{Unrated: true}), lt.TrackCitta, lt.TrackTwinA, lt.TrackTwinB, lt.TrackShort)
	sameSet(t, "min_stars=3 after clear", search(t, e, library.SearchParams{MinStars: ip(3)}), lt.TrackLuna)
	// tags (AND), inherited child included
	sameSet(t, "tag=ballad", search(t, e, library.SearchParams{Tags: []string{"ballad"}}), lt.TrackTwinA, lt.TrackTwinB)
	sameSet(t, "tag=ballad&night", search(t, e, library.SearchParams{Tags: []string{"ballad", "night"}}), lt.TrackTwinA)
	sameSet(t, "tag normalised", search(t, e, library.SearchParams{Tags: []string{"  BALLAD "}}), lt.TrackTwinA, lt.TrackTwinB)
	sameSet(t, "tag=unknown", search(t, e, library.SearchParams{Tags: []string{"nope"}}))
	// open comments (the hidden comment of Short does not count, its visible one does)
	sameSet(t, "open comments", search(t, e, library.SearchParams{HasOpenComments: bp(true)}), lt.TrackLuna, lt.TrackShort)
	sameSet(t, "no open comments", search(t, e, library.SearchParams{HasOpenComments: bp(false)}), lt.TrackCitta, lt.TrackTwinA, lt.TrackTwinB)
	exec(t, e, `UPDATE comments SET state = 'resolved', state_changed_at = '2026-10-06T12:00:00Z', state_changed_by = 'human:fixture' WHERE track_id = ? AND hidden_at IS NULL`, lt.TrackShort)
	sameSet(t, "open comments after resolve", search(t, e, library.SearchParams{HasOpenComments: bp(true)}), lt.TrackLuna)

	// discarded tracks are hidden by default (D-M2a-18) and listed with status=discarded
	exec(t, e, `UPDATE tracks SET status = 'discarded', status_set_by = 'human:fixture' WHERE id = ?`, lt.TrackTwinB)
	if got := search(t, e, library.SearchParams{}); slices.Contains(got, lt.TrackTwinB) || len(got) != 4 {
		t.Errorf("default search shows discarded: %v", got)
	}
	sameSet(t, "status=discarded", search(t, e, library.SearchParams{Status: []string{"discarded"}}), lt.TrackTwinB)
	sameSet(t, "status=draft,discarded", search(t, e, library.SearchParams{Status: []string{"draft", "discarded"}}), e.fx.Tracks...)

	// sort stars_desc: human stars desc, unrated last, then recorded_desc
	exec(t, e, `UPDATE ratings SET stars = 3 WHERE track_id = ? AND source = 'human'`, lt.TrackCitta)
	exec(t, e, `UPDATE tracks SET status = 'draft' WHERE id = ?`, lt.TrackTwinB)
	got := search(t, e, library.SearchParams{Sort: "stars_desc"})
	want := []string{lt.TrackLuna, lt.TrackCitta, lt.TrackShort, lt.TrackTwinB, lt.TrackTwinA}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("stars_desc (-want +got):\n%s", diff)
	}

	// validation
	for name, p := range map[string]library.SearchParams{
		"min_stars+unrated": {MinStars: ip(3), Unrated: true},
		"min_stars 0":       {MinStars: ip(0)},
		"min_stars 6":       {MinStars: ip(6)},
		"six tags":          {Tags: []string{"a", "b", "c", "d", "e", "f"}},
	} {
		_, err := e.svc.SearchTracks(context.Background(), p)
		var ve *library.ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("%s: err = %v, want a ValidationError", name, err)
		}
	}
}

// TestSearchTracks_SummaryJudgmentFields: stars, critic stars, effective tags and comment counts.
func TestSearchTracks_SummaryJudgmentFields(t *testing.T) {
	t.Parallel()
	e := newEnvM2a(t)
	page, err := e.svc.SearchTracks(context.Background(), library.SearchParams{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]library.TrackSummary{}
	for _, s := range page.Items {
		by[s.ID] = s
	}
	l := by[lt.TrackLuna]
	if l.Stars == nil || *l.Stars != 5 || l.CriticStars == nil || *l.CriticStars != 2 || l.Comments != 3 || l.OpenComments != 3 || len(l.Tags) != 0 {
		t.Errorf("luna %+v", l)
	}
	a := by[lt.TrackTwinA]
	if a.Stars != nil || a.CriticStars == nil || *a.CriticStars != 4 || !slices.Equal(a.Tags, []string{"ballad", "night", "upbeat"}) {
		t.Errorf("twin a %+v", a)
	}
	if b := by[lt.TrackTwinB]; !slices.Equal(b.Tags, []string{"ballad"}) {
		t.Errorf("twin b tags %v", b.Tags)
	}
	// hidden comments are not counted
	if s := by[lt.TrackShort]; s.Comments != 1 || s.OpenComments != 1 {
		t.Errorf("short comments %d/%d", s.Comments, s.OpenComments)
	}
	// a removed tag is not effective
	exec(t, e, `UPDATE track_tags SET removed_at = '2026-10-06T12:00:00Z', removed_by = 'human:fixture' WHERE track_id = ? AND source = 'agent'`, lt.TrackTwinA)
	if got := search(t, e, library.SearchParams{Tags: []string{"upbeat"}}); len(got) != 0 {
		t.Errorf("removed tag still filters: %v", got)
	}
}

// TestMatchTrackIDs_AgreesWithSearchTracks: every filter combination matches the paged search.
func TestMatchTrackIDs_AgreesWithSearchTracks(t *testing.T) {
	t.Parallel()
	e := newEnvM2a(t)
	exec(t, e, `UPDATE tracks SET status = 'discarded' WHERE id = ?`, lt.TrackCitta)
	base := []library.SearchParams{
		{}, {MinStars: ip(4)}, {MinStars: ip(3)}, {Unrated: true}, {Tags: []string{"ballad"}}, {Tags: []string{"ballad", "night"}},
		{HasOpenComments: bp(true)}, {HasOpenComments: bp(false)}, {Status: []string{"discarded"}}, {Status: []string{"draft"}, Unrated: true},
		{Tags: []string{"ballad"}, HasOpenComments: bp(false)}, {Q: "twin"}, {Style: []string{"rock"}, MinStars: ip(1)},
		{Sort: "stars_desc"},
	}
	for i, p := range base {
		p := p
		p.Limit = 2 // small pages: MatchTrackIDs must ignore paging
		var paged []string
		for {
			pg, err := e.svc.SearchTracks(context.Background(), p)
			if err != nil {
				t.Fatalf("case %d: %v", i, err)
			}
			paged = append(paged, ids(pg)...)
			if pg.NextCursor == nil {
				break
			}
			p.Cursor = *pg.NextCursor
		}
		p.Limit, p.Cursor = 0, ""
		got, err := e.svc.MatchTrackIDs(context.Background(), p)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if diff := cmp.Diff(paged, got, cmpopts.EquateEmpty()); diff != "" {
			t.Errorf("case %d %+v (-search +match):\n%s", i, base[i], diff)
		}
	}
	if _, err := e.svc.MatchTrackIDs(context.Background(), library.SearchParams{MinStars: ip(2), Unrated: true}); err == nil {
		t.Error("exclusive filters accepted")
	}
}

func TestTrackDetail_M2aGolden(t *testing.T) {
	t.Parallel()
	e := newEnvM2a(t)
	d, err := e.svc.Track(context.Background(), lt.TrackLuna)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.MarshalIndent(map[string]any{
		"rating": d.Rating, "tags": d.Tags, "comments": d.Comments, "open_comments": d.OpenComments, "playlists": d.Playlists, "listening": d.Listening,
	}, "", "  ")
	got = append(got, '\n')
	path := filepath.Join("testdata", "track_detail_m2a.golden.json")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if diff := cmp.Diff(string(want), string(got)); diff != "" {
		t.Errorf("M2a track detail drift (-want +got):\n%s", diff)
	}
	// TwinA carries three tags with their sources; TwinB's is inherited
	a, _ := e.svc.Track(context.Background(), lt.TrackTwinA)
	if diff := cmp.Diff([]library.TagRef{{Name: "ballad", Sources: []string{"human"}}, {Name: "night", Sources: []string{"human"}}, {Name: "upbeat", Sources: []string{"agent"}}}, a.Tags); diff != "" {
		t.Errorf("twin a tags (-want +got):\n%s", diff)
	}
	b, _ := e.svc.Track(context.Background(), lt.TrackTwinB)
	if diff := cmp.Diff([]library.TagRef{{Name: "ballad", Sources: []string{"inherited"}}}, b.Tags); diff != "" {
		t.Errorf("twin b tags (-want +got):\n%s", diff)
	}
	if a.Rating.Human != nil || a.Rating.Critic == nil || *a.Rating.Critic.Stars != 4 {
		t.Errorf("twin a rating %+v", a.Rating)
	}
}

func TestFacets_TagsAndStars(t *testing.T) {
	t.Parallel()
	e := newEnvM2a(t)
	f, err := e.svc.Facets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]library.TagFacet{{Tag: "ballad", Count: 2}, {Tag: "night", Count: 1}, {Tag: "upbeat", Count: 1}}, f.Tags); diff != "" {
		t.Errorf("tags (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]library.StarFacet{{Stars: 3, Count: 1}, {Stars: 5, Count: 1}}, f.Stars); diff != "" {
		t.Errorf("stars (-want +got):\n%s", diff)
	}
	if f.Unrated != 3 {
		t.Errorf("unrated = %d", f.Unrated)
	}
}

func TestGetStats_Judgments(t *testing.T) {
	t.Parallel()
	e := newEnvM2a(t)
	s, err := e.svc.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantJ := library.JudgmentStats{RatedHuman: 2, RatedCritic: 2, StarsHist: map[string]int{"1": 0, "2": 0, "3": 1, "4": 0, "5": 1}, Comments: 4, OpenFix: 1, Tags: 3}
	if diff := cmp.Diff(wantJ, s.Judgments); diff != "" {
		t.Errorf("judgments (-want +got):\n%s", diff)
	}
	if s.Playlists != 1 {
		t.Errorf("playlists = %d", s.Playlists)
	}
	if diff := cmp.Diff(map[string]int{"import_backlog": 6, "varianti_import": 1, "jsonl_upload": 2}, s.DraftsByOrigin); diff != "" {
		t.Errorf("drafts_by_origin (-want +got):\n%s", diff)
	}
	if s.Backups.LastState == nil || *s.Backups.LastState != "completed" || s.Backups.LastCompletedAt == nil {
		t.Errorf("backups %+v", s.Backups)
	}
	// an empty library still reports the zero shape
	es, err := library.New(e.st, nil, nil).Stats(context.Background())
	if err != nil || es.Judgments.StarsHist == nil {
		t.Errorf("shape %v %+v", err, es.Judgments)
	}
	// a cleared rating and a resolved fix change the counters
	exec(t, e, `UPDATE ratings SET stars = NULL WHERE track_id = ? AND source = 'human'`, lt.TrackLuna)
	exec(t, e, `UPDATE comments SET state = 'resolved', state_changed_at = '2026-10-06T12:00:00Z', state_changed_by = 'human:fixture' WHERE id = ?`, lt.CommentLunaFix)
	s2, _ := e.svc.Stats(context.Background())
	if s2.Judgments.RatedHuman != 1 || s2.Judgments.OpenFix != 0 || s2.Judgments.StarsHist["5"] != 0 {
		t.Errorf("after changes %+v", s2.Judgments)
	}
}

func TestDrafts_ListAndGetM2aFields(t *testing.T) {
	t.Parallel()
	e := newEnvM2a(t)
	ctx := context.Background()
	p := listDrafts(t, e, library.DraftParams{Limit: 100})
	if p.Total != 9 {
		t.Fatalf("total %d", p.Total)
	}
	for _, d := range p.Items {
		if d.Revision != 1 || !d.Editable || d.CreatedBy == "" || d.UpdatedBy == "" {
			t.Errorf("%s: %+v", d.ID, d)
		}
	}
	for _, id := range []string{lt.DraftUpload1, lt.DraftVarianti, e.fx.Drafts[0]} {
		d, err := e.svc.Draft(ctx, id, true)
		if err != nil {
			t.Fatal(err)
		}
		if d.Revision != 1 || !d.Editable || d.Lint.Errors == nil || d.Lint.Warnings == nil {
			t.Errorf("%s detail %+v", id, d)
		}
	}
	up, _ := e.svc.Draft(ctx, lt.DraftUpload1, false)
	if up.CreatedBy != lt.FixtureHuman || up.Origin != "jsonl_upload" || up.Request.Cot != "full" {
		t.Errorf("upload draft %+v", up)
	}
	imp, _ := e.svc.Draft(ctx, e.fx.Drafts[0], false)
	if imp.CreatedBy != "import" || imp.UpdatedBy != "import" {
		t.Errorf("imported draft by: %q %q", imp.CreatedBy, imp.UpdatedBy)
	}
	// request.abc and cfg_scale come through when set; fulfilled drafts are not editable
	exec(t, e, `UPDATE recipe_drafts SET request_json = json_set(request_json, '$.abc', 'X:1', '$.cfg_scale', 1.5), revision = 2 WHERE id = ?`, lt.DraftUpload2)
	d2, _ := e.svc.Draft(ctx, lt.DraftUpload2, false)
	if d2.Request.ABC == nil || *d2.Request.ABC != "X:1" || d2.Request.CfgScale == nil || *d2.Request.CfgScale != 1.5 || d2.Revision != 2 {
		t.Errorf("abc/cfg_scale: %+v", d2.Request)
	}
	exec(t, e, `UPDATE recipe_drafts SET status = 'fulfilled', fulfilled_by_track_id = ? WHERE id = ?`, lt.TrackLuna, lt.DraftUpload1)
	if f, _ := e.svc.Draft(ctx, lt.DraftUpload1, false); f.Editable {
		t.Error("fulfilled draft reported editable")
	}
	filtered := listDrafts(t, e, library.DraftParams{Origin: "jsonl_upload", Status: "fulfilled"})
	if filtered.Total != 1 || filtered.Items[0].Editable {
		t.Errorf("origin filter %+v", filtered)
	}
	for _, o := range []string{"editor", "agent", "jsonl_upload"} {
		if _, err := e.svc.ListDrafts(ctx, library.DraftParams{Origin: o}); err != nil {
			t.Errorf("origin %s: %v", o, err)
		}
	}
}

// edit marks a draft as edited by a human (revision 2).
func editDraft(t *testing.T, e *env, id string) {
	t.Helper()
	exec(t, e, `UPDATE recipe_drafts SET request_json = json_set(request_json, '$.lyrics', '[Verse 1]\nedited line'), revision = 2, updated_by = 'human:fixture' WHERE id = ?`, id)
}

func TestInsertTake_EditedDraftNotFulfilled(t *testing.T) {
	t.Parallel()
	e := newEnvM2a(t)
	ctx := context.Background()
	own, sibling, coreTarget := e.fx.Drafts[0], e.fx.Drafts[1], e.fx.Drafts[3]
	editDraft(t, e, own)
	editDraft(t, e, coreTarget)
	var before string
	if err := e.st.R.QueryRow(`SELECT request_json FROM recipe_drafts WHERE id = ?`, own).Scan(&before); err != nil {
		t.Fatal(err)
	}

	// a core-hash twin of the (edited) fxb_002_v1 recipe
	var req string
	if err := e.st.R.QueryRow(`SELECT request_json FROM recipes WHERE id = ?`, draftRecipe(t, e, coreTarget)).Scan(&req); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(req), &m)
	m["cfg_scale"] = 5.0
	raw, _ := json.Marshal(m)
	pc, err := yue2.ParseRequest(raw, yue2.ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}

	tx, _ := e.st.W.BeginTx(ctx, nil)
	defer func() { _ = tx.Rollback() }()
	var f1, f2, f3 library.Fulfilment
	t1 := newTake(lt.TrackIDForTest(1), draftRecipe(t, e, own), 1)
	t1.Out = &f1
	t2 := newTake(lt.TrackIDForTest(2), draftRecipe(t, e, sibling), 2)
	t2.Out = &f2
	rid, _, err := e.svc.InsertRecipe(ctx, tx, pc, "import")
	if err != nil {
		t.Fatal(err)
	}
	t3 := newTake(lt.TrackIDForTest(3), rid, 3)
	t3.Out = &f3
	for _, tk := range []library.NewTake{t1, t2, t3} {
		if err := e.svc.InsertTake(ctx, tx, tk); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if f1.OwnDraftID != "" || !slices.Equal(f1.EditedSkipped, []string{own}) {
		t.Errorf("edited own draft: %+v", f1)
	}
	if f2.OwnDraftID != sibling || len(f2.EditedSkipped) != 0 {
		t.Errorf("unedited sibling: %+v", f2)
	}
	if len(f3.CoreDrafts) != 0 || !slices.Equal(f3.EditedSkipped, []string{coreTarget}) {
		t.Errorf("core fallback: %+v", f3)
	}
	state := func(id string) (status, req, by string) {
		var b sql.NullString
		if err := e.st.R.QueryRow(`SELECT status, request_json, fulfilled_by_track_id FROM recipe_drafts WHERE id = ?`, id).Scan(&status, &req, &b); err != nil {
			t.Fatal(err)
		}
		return status, req, b.String
	}
	if st, rq, by := state(own); st != "backlog" || rq != before || by != "" {
		t.Errorf("edited draft changed: %s %q %q", st, rq, by)
	}
	if st, _, by := state(coreTarget); st != "backlog" || by != "" {
		t.Errorf("edited core draft changed: %s %q", st, by)
	}
	if st, _, by := state(sibling); st != "fulfilled" || by != t2.ID {
		t.Errorf("sibling: %s %q", st, by)
	}
}

func TestRelink_SkipsEditedDrafts(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	mk := func(tx *sql.Tx, reqID string, seed int) string {
		p, err := yue2.ParseRequest([]byte(fmt.Sprintf(`{"id":%q,"style":"s","lyrics":"[Verse]\nx","cot":"full","seed":%d}`, reqID, seed)), yue2.ParseOptions{})
		if err != nil {
			t.Fatal(err)
		}
		rid, _, err := e.svc.InsertRecipe(ctx, tx, p, "import")
		if err != nil {
			t.Fatal(err)
		}
		return rid
	}
	tx, _ := e.st.W.BeginTx(ctx, nil)
	defer func() { _ = tx.Rollback() }()
	draft := func(req string) string {
		id, _, err := e.svc.InsertDraft(ctx, tx, library.NewDraft{Origin: "varianti_import", SourceRecipeID: mk(tx, req, 100+len(req)), GroupKey: "fxe_001"})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	edited, plain := draft("fxe_001_v2"), draft("fxe_001_v3")
	// the human edits one draft (revision 2) and clears nothing: its parent stays NULL
	if _, err := tx.Exec(`UPDATE recipe_drafts SET revision = 2, updated_by = 'human:fixture' WHERE id = ?`, edited); err != nil {
		t.Fatal(err)
	}
	mother := newTake(lt.TrackIDForTest(20), mk(tx, "fxe_001", 20), 20)
	if err := e.svc.InsertTake(ctx, tx, mother); err != nil {
		t.Fatal(err)
	}
	n, err := e.svc.RelinkVariantiMothers(ctx, tx)
	if err != nil || n != 1 {
		t.Fatalf("relinked %d (%v), want 1", n, err)
	}
	parent := func(id string) string {
		var s sql.NullString
		if err := tx.QueryRow(`SELECT parent_track_id FROM recipe_drafts WHERE id = ?`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s.String
	}
	if parent(edited) != "" || parent(plain) != mother.ID {
		t.Errorf("parents: edited=%q plain=%q", parent(edited), parent(plain))
	}
}

func TestLibrary_DraftOriginsExtended(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	tx, _ := e.st.W.BeginTx(ctx, nil)
	defer func() { _ = tx.Rollback() }()
	for _, o := range []string{"editor", "agent", "jsonl_upload"} {
		id, created, err := e.svc.InsertDraft(ctx, tx, library.NewDraft{Origin: o, RequestID: "x-" + o, RequestJSON: `{"id":"x"}`})
		if err != nil || !created || id == "" {
			t.Errorf("origin %s: %v %v", o, created, err)
		}
	}
	if _, _, err := e.svc.InsertDraft(ctx, tx, library.NewDraft{Origin: "nope", RequestID: "x", RequestJSON: `{}`}); err == nil {
		t.Error("unknown origin accepted")
	}
}
