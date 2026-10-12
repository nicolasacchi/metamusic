package importer

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestPlace_DraftsForAllTracklessRecipes(t *testing.T) {
	e := newEnv(t)
	e.put("r.jsonl", lines(req("has-take", 1), req("no-take", 2), req("also-take", 3)))
	e.flac("has_take.flac", "h")
	e.flac("also_take.flac", "a")
	inv, res := e.applyAll()
	d := e.drafts()
	if len(d) != 3 {
		t.Fatalf("drafts = %d", len(d))
	}
	tr := e.tracks()
	if d["has-take#1"].Status != "fulfilled" || d["has-take#1"].FulfilledBy != tr["has-take#1"].ID ||
		d["also-take#3"].Status != "fulfilled" || d["no-take#2"].Status != "backlog" {
		t.Errorf("drafts = %+v", d)
	}
	s := inv.Report.Summary
	if s.DraftsNew != 3 || s.DraftsFulfilled != 2 || s.TracksNew != 2 || res.TracksImported != 2 {
		t.Errorf("summary = %+v", s)
	}
	for _, r := range inv.Report.Recipes {
		want := "track"
		if r.RequestID == "no-take" {
			want = "draft_backlog"
		}
		if r.Placement != want {
			t.Errorf("%s placement = %s", r.RequestID, r.Placement)
		}
	}
	// the draft of a recipe whose track exists is not touched by a later run
	before := e.drafts()
	e.applyAll()
	for k, v := range e.drafts() {
		if before[k] != v {
			t.Errorf("draft %s changed: %+v -> %+v", k, before[k], v)
		}
	}
}

func TestPlace_BacklogAndVarianti(t *testing.T) {
	e := newEnv(t)
	e.put("backlog.jsonl", lines(req("bk_001_v1", 1), req("bk_001_v2", 2), req("plain", 3), req("seedy-s5", 4)))
	e.put("varianti/vm.json", req("bk_001_v9", 9))
	e.put("idx.csv", "variant_id,seed,variant_type\nbk_001_v1,1,remix\n")
	inv, _ := e.applyAll()
	d := e.drafts()
	check := func(k, origin, group, label string) {
		t.Helper()
		if x := d[k]; x.Origin != origin || x.Group != group || x.Label != label || x.Status != "backlog" {
			t.Errorf("%s = %+v, want %s/%s/%s", k, x, origin, group, label)
		}
	}
	check("bk_001_v1#1", "import_backlog", "bk_001", "remix") // reference variant_type wins
	check("bk_001_v2#2", "import_backlog", "bk_001", "v2")
	check("plain#3", "import_backlog", "plain", "")
	check("seedy-s5#4", "import_backlog", "seedy", "s5")
	check("bk_001_v9#9", "varianti_import", "bk_001", "v9")
	var vr *RecipeReport
	for i := range inv.Report.Recipes {
		if inv.Report.Recipes[i].RequestID == "bk_001_v9" {
			vr = &inv.Report.Recipes[i]
		}
	}
	if vr == nil || vr.Placement != "draft_varianti" || vr.GroupKey != "bk_001" {
		t.Errorf("recipe report = %+v", vr)
	}
	if !hasCode(inv.Report.Warnings, "mother_not_found") {
		t.Errorf("warnings = %v", issueCodes(inv.Report.Warnings))
	}
	if !inv.NeedsMapping() {
		t.Error("a varianti draft without a mother belongs in mapping.todo.csv")
	}
}

func TestPlace_VariantiMotherWithSiblings(t *testing.T) {
	// all at once: the mother has a sibling take, the draft still resolves to the shared root
	e := newEnv(t)
	e.put("r.jsonl", lines(req("mom", 1), req("mom", 2)))
	e.put("varianti/mom-v2.json", req("mom-v2", 3))
	e.flac("mom_a.flac", "ma")
	e.age("mom_a.flac", e.now.Add(-5*time.Hour))
	e.flac("mom_b.flac", "mb")
	e.mapping("audio,request_id,seed\nmom_a.flac,mom,1\nmom_b.flac,mom,2\n")
	inv, _ := e.applyAll()
	if hasCode(inv.Report.Warnings, "mother_not_found") || hasCode(inv.Report.Warnings, "mother_ambiguous") {
		t.Errorf("warnings = %v", issueCodes(inv.Report.Warnings))
	}
	tr := e.tracks()
	if got := e.drafts()["mom-v2#3"].Parent; got != tr["mom#1"].ID {
		t.Errorf("mother = %s, want root %s", got, tr["mom#1"].ID)
	}
	if len(inv.Report.Drafts) == 0 || inv.Report.Drafts[len(inv.Report.Drafts)-1].Mother == nil {
		t.Errorf("drafts report = %+v", inv.Report.Drafts)
	}

	// incrementally: the draft arrives first, the mother later (RelinkVariantiMothers)
	e2 := newEnv(t)
	e2.put("r.jsonl", lines(req("kid", 1), req("kid-v2", 2)))
	e2.put("varianti/kid-v2.json", req("kid-v2", 2))
	e2.applyAll()
	if got := e2.drafts()["kid-v2#2"].Parent; got != "" {
		t.Fatalf("mother = %q before the take exists", got)
	}
	e2.flac("kid.flac", "k")
	e2.applyAll()
	if got := e2.drafts()["kid-v2#2"].Parent; got != e2.tracks()["kid#1"].ID {
		t.Errorf("mother after relink = %q", got)
	}
}

func TestPlace_DraftMappingRow(t *testing.T) {
	e := newEnv(t)
	e.put("r.jsonl", lines(req("mama", 1), req("papa", 1), req("pre-v2", 2), req("noparent-v2", 3), req("other-v2", 4), req("bad-v2", 5)))
	e.put("varianti/a.json", req("pre-v2", 2))
	e.put("varianti/b.json", req("noparent-v2", 3))
	e.put("varianti/c.json", req("other-v2", 4))
	e.put("varianti/d.json", req("bad-v2", 5))
	e.flac("mama.flac", "m")
	e.flac("papa.flac", "p")
	e.mapping("audio,request_id,seed,title,parent\n" +
		"mama.flac,mama,1,,\npapa.flac,papa,1,,\n" +
		"draft:pre-v2,,,,mama\n" + // request id
		"draft:noparent-v2,,,,-\n" + // explicitly none
		"draft:other-v2,,,,papa.flac\n" + // an audio path
		"draft:bad-v2,,,,nobody\n")
	inv, _ := e.applyAll()
	tr, d := e.tracks(), e.drafts()
	if d["pre-v2#2"].Parent != tr["mama#1"].ID || d["other-v2#4"].Parent != tr["papa#1"].ID {
		t.Errorf("drafts = %+v", d)
	}
	if d["noparent-v2#3"].Parent != "" || d["bad-v2#5"].Parent != "" {
		t.Errorf("drafts = %+v", d)
	}
	for _, w := range inv.Report.Warnings {
		if w.Code == "mother_not_found" && strings.Contains(w.Path, "b.json") {
			t.Error("'-' must not warn mother_not_found")
		}
	}
	if !hasCode(inv.Report.Warnings, "parent_unresolved") {
		t.Errorf("warnings = %v", issueCodes(inv.Report.Warnings))
	}
}

func TestPlace_ScoreMatchingRules(t *testing.T) {
	e := newEnv(t)
	e.put("r.jsonl", lines(req3("stemmed", 1), req3("seedy", 11), req3("seedy", 12), req3("idonly", 5), req3("loner", 6)))
	for _, n := range []string{"stemmed", "seedy_a", "seedy_b", "idonly", "loner"} {
		e.flac(n+".flac", n)
	}
	e.mapping("audio,request_id,seed\nseedy_a.flac,seedy,11\nseedy_b.flac,seedy,12\n")
	e.put("scores/stemmed.abc", abc3)
	e.put("scores/seedy-s11.abc", abc3)
	e.put("scores/seedy_seed12.abc", strings.Replace(abc3, "T:t", "T:u", 1))
	e.put("scores/idonly.abc", abc3) // equals the audio stem of exactly one take => attaches
	e.put("scores/seedy.abc", abc3)  // request id only: never attaches
	e.put("scores/nothing.abc", abc3)
	e.put("scores/broken.abc", "this is not abc at all\n")
	inv, _ := e.applyAll()
	un := 0
	for _, w := range inv.Report.Warnings {
		if w.Code == "score_unattached" {
			un++
		}
	}
	if un != 3 {
		t.Errorf("score_unattached = %d (%v)", un, inv.Report.Warnings)
	}
	rows := e.query(`SELECT r.request_id, r.seed, s.aligned, s.seconds_per_bar, s.bars, s.source FROM scores s JOIN tracks t ON t.id=s.track_id JOIN recipes r ON r.id=t.recipe_id ORDER BY 1,2`)
	defer rows.Close()
	var got []string
	for rows.Next() {
		var id, src string
		var seed int64
		var aligned, bars int
		var spb float64
		if err := rows.Scan(&id, &seed, &aligned, &spb, &bars, &src); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s#%d/%d/%.1f/%d/%s", id, seed, aligned, spb, bars, src))
	}
	want := "idonly#5/1/2.0/6/import,seedy#11/1/2.0/6/import,seedy#12/1/2.0/6/import,stemmed#1/1/2.0/6/import"
	if strings.Join(got, ",") != want {
		t.Errorf("scores = %v\nwant %s", got, want)
	}
	if inv.Report.Summary.ScoresAttached != 4 {
		t.Errorf("scores_attached = %d", inv.Report.Summary.ScoresAttached)
	}
	var plan int
	if err := e.st.R.QueryRow(`SELECT count(*) FROM sections WHERE source='plan' AND start_s IS NOT NULL`).Scan(&plan); err != nil || plan != 12 {
		t.Errorf("plan sections = %d %v", plan, err)
	}
	// the attached score is kept as takes/<id>/score.abc
	if names := e.ing.artifacts[e.tracks()["stemmed#1"].ID]; len(names) != 1 || names[0] != "score.abc" {
		t.Errorf("artifacts = %v", names)
	}
}

func TestPlace_ScoresAndTruncation(t *testing.T) {
	e := newEnv(t)
	put := func(dir, id string, seed int, result string) {
		e.put(dir+"/request.json", yue2Request3(id, seed))
		e.flac(dir+"/audio.flac", id)
		e.put(dir+"/result.json", result)
		e.put(dir+"/score.abc", abc3)
		e.put(dir+"/config.json", "{}")
		e.put(dir+"/semantic.npy", "npy")
		e.put(dir+"/latent.npy", "latent")
	}
	put("res/dict", "tr-dict", 1, `{"truncated": {"abc": false, "semantic": true}, "other": 1}`)
	put("res/bool", "tr-bool", 2, `{"truncated": true}`)
	put("res/none", "tr-none", 3, `{"duration": 12}`)
	put("res/off", "tr-off", 4, `{"truncated": {"abc": false}}`)
	inv, _ := e.applyAll()
	rows := e.query(`SELECT r.request_id, t.truncated_abc, t.truncated_semantic FROM tracks t JOIN recipes r ON r.id=t.recipe_id ORDER BY 1`)
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var id string
		var a, s *int
		if err := rows.Scan(&id, &a, &s); err != nil {
			t.Fatal(err)
		}
		f := func(p *int) string {
			if p == nil {
				return "NULL"
			}
			return fmt.Sprint(*p)
		}
		got[id] = f(a) + "/" + f(s)
	}
	want := map[string]string{"tr-dict": "0/1", "tr-bool": "NULL/NULL", "tr-none": "NULL/NULL", "tr-off": "NULL/NULL"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s truncated = %s, want %s", k, got[k], v)
		}
	}
	n := 0
	for _, w := range inv.Report.Warnings {
		if w.Code == "truncated_not_dict" {
			n++
		}
	}
	if n != 2 {
		t.Errorf("truncated_not_dict = %d", n)
	}
	// artifacts: request/result/config/semantic/score; never latent
	for _, name := range e.ing.artifacts[e.tracks()["tr-dict#1"].ID] {
		if name == "latent.npy" {
			t.Error("latent.npy was kept")
		}
	}
	if got := strings.Join(e.ing.artifacts[e.tracks()["tr-dict#1"].ID], ","); got != "config.json,request.json,result.json,score.abc,semantic.npy" {
		t.Errorf("artifacts = %s", got)
	}
	if e.count("artifacts") != 20 || e.count("scores") != 4 {
		t.Errorf("artifacts=%d scores=%d", e.count("artifacts"), e.count("scores"))
	}
}

func yue2Request3(id string, seed int) string {
	return fmt.Sprintf("{\n  \"style\": \"pop, 120 bpm\",\n  \"lyrics\": \"[Verse 1]\\nla\\n[Chorus]\\nsing\\n[Outro]\\nfade\",\n  \"cot\": \"full\",\n  \"seed\": %d,\n  \"abc\": null,\n  \"cfg_scale\": null,\n  \"id\": %q\n}\n", seed, id)
}

func TestPlace_RefSeedMismatch(t *testing.T) {
	e := newEnv(t)
	e.put("r.jsonl", lines(req("rf-1", 1), req("rf-2", 2), req("rf-3", 3)))
	e.put("idx.csv", "\xef\xbb\xbfvariant_id,track_n,source_title,source_artist,variant,variant_type,seed\r\n"+
		"rf-1,1,SecretTitleOne,SecretArtist,v1,alpha,1\r\n"+
		"rf-2,2,SecretTitleTwo,SecretArtist,v1,beta,999\r\n"+ // seed mismatch
		"rf-3,3,SecretTitleThree,SecretArtist,v1,gamma,\r\n"+ // empty seed => attach
		"unknown-id,4,SecretTitleFour,SecretArtist,v1,delta,1\r\n")
	inv, _ := e.applyAll()
	if !hasCode(inv.Report.Warnings, "ref_seed_mismatch") {
		t.Errorf("warnings = %v", issueCodes(inv.Report.Warnings))
	}
	if len(inv.Report.Metadata) != 1 || inv.Report.Metadata[0].Rows != 4 || inv.Report.Metadata[0].Matched != 2 || inv.Report.Metadata[0].Unmatched != 2 {
		t.Errorf("metadata = %+v", inv.Report.Metadata)
	}
	for _, r := range inv.Report.Recipes {
		if r.HasReference != (r.RequestID != "rf-2") {
			t.Errorf("%s has_reference = %v", r.RequestID, r.HasReference)
		}
	}
	if e.count("recipe_refs") != 2 {
		t.Fatalf("refs = %d", e.count("recipe_refs"))
	}
	if got := e.scalar(`SELECT ref_json FROM recipe_refs WHERE recipe_id=(SELECT id FROM recipes WHERE request_id='rf-1')`); got !=
		`{"source_artist":"SecretArtist","source_title":"SecretTitleOne","track_n":"1","variant":"v1","variant_type":"alpha"}` {
		t.Errorf("ref_json = %s", got)
	}
	// private values appear nowhere else
	rep, _ := jsonMarshal(inv.Report)
	if strings.Contains(string(rep), "Secret") {
		t.Error("reference values leaked into the report")
	}
	var sb strings.Builder
	WriteMappingTemplate(&sb, inv)
	inv.Report.FormatText(&sb)
	if strings.Contains(sb.String(), "Secret") {
		t.Error("reference values leaked into the template or stdout")
	}
	// a changed CSV row warns and is not rewritten
	e.put("idx.csv", "variant_id,track_n,source_title,source_artist,variant,variant_type,seed\nrf-1,1,Changed,SecretArtist,v1,alpha,1\n")
	inv2, _ := e.applyAll()
	if !hasCode(inv2.Report.Warnings, "ref_changed") {
		t.Errorf("warnings = %v", issueCodes(inv2.Report.Warnings))
	}
	if strings.Contains(e.scalar(`SELECT ref_json FROM recipe_refs WHERE recipe_id=(SELECT id FROM recipes WHERE request_id='rf-1')`), "Changed") {
		t.Error("stored reference was rewritten")
	}
}

func TestPlace_OneCollectionForPilot(t *testing.T) {
	e := newEnv(t)
	a, b, c, d := req("pl_001_v1", 1), req("pl_001_v2", 2), req("pl_001_v3", 3), req("pl_002_v1", 4)
	e.put("all.jsonl", lines(a, b, c, d))
	e.put("pilot.jsonl", lines(a, b, c))
	e.put("pilot.json", `{"name":"pilot-set","rule":"r","songs":[{"n":1,"title":"T","artist":"A","why":"W","variant_ids":["pl_001_v1","pl_001_v2","pl_001_v3"]}]}`)
	inv, _ := e.applyAll()
	if inv.Report.Summary.Collections != 1 || e.count("draft_collections") != 3 {
		t.Fatalf("collections=%d members=%d", inv.Report.Summary.Collections, e.count("draft_collections"))
	}
	if n := e.scalar(`SELECT DISTINCT name FROM draft_collections`); n != "pilot-set" {
		t.Errorf("name = %s", n)
	}
	if src := e.scalar(`SELECT DISTINCT source FROM draft_collections`); src != "pilot.json" {
		t.Errorf("source = %s", src)
	}

	// no collection JSON: the subset JSONL becomes a collection named after its stem
	e2 := newEnv(t)
	e2.put("all.jsonl", lines(a, b, c, d))
	e2.put("pilot.jsonl", lines(a, b, c))
	inv2, _ := e2.applyAll()
	if inv2.Report.Summary.Collections != 1 || e2.scalar(`SELECT DISTINCT name FROM draft_collections`) != "pilot" || e2.count("draft_collections") != 3 {
		t.Errorf("stem collection: %+v / %d", inv2.Report.Summary, e2.count("draft_collections"))
	}

	// a collection JSON over a different set does not suppress the subset collection
	e3 := newEnv(t)
	e3.put("all.jsonl", lines(a, b, c, d))
	e3.put("pilot.jsonl", lines(a, b, c))
	e3.put("other.json", `{"name":"other","songs":[{"variant_ids":["pl_002_v1","missing-id"]}]}`)
	inv3, _ := e3.applyAll()
	if inv3.Report.Summary.Collections != 2 || e3.count("draft_collections") != 4 {
		t.Errorf("two collections expected: %+v / %d", inv3.Report.Summary, e3.count("draft_collections"))
	}
	var meta *MetaReport
	for i := range inv3.Report.Metadata {
		if inv3.Report.Metadata[i].Path == "other.json" {
			meta = &inv3.Report.Metadata[i]
		}
	}
	if meta == nil || meta.Rows != 2 || meta.Matched != 1 || meta.Unmatched != 1 {
		t.Errorf("metadata = %+v", meta)
	}
}

func TestPlace_CoreMatchFulfilment(t *testing.T) {
	// a result-dir request with an explicit non-default cfg_scale hashes differently from the
	// backlog line, but fulfils its draft through the core hash, with a warning
	e := newEnv(t)
	e.put("r.jsonl", lines(req("core-1", 1)))
	rd := strings.Replace(yue2Request("core-1", 1), `"cfg_scale": null`, `"cfg_scale": 2.5`, 1)
	e.put("res/core-1/request.json", rd)
	e.flac("res/core-1/audio.flac", "core")
	inv, _ := e.applyAll()
	if e.count("recipes") != 2 {
		t.Fatalf("recipes = %d", e.count("recipes"))
	}
	for _, d := range e.drafts() {
		if d.Status != "fulfilled" {
			t.Errorf("draft %+v not fulfilled", d)
		}
	}
	found := false
	for _, w := range inv.Report.Warnings {
		if w.Code == "fulfilled_by_core_match" && strings.Contains(w.Message, "cfg_scale") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %+v", inv.Report.Warnings)
	}
}

func TestApply_MotherRelinkFollowsPlan(t *testing.T) {
	// a "-" row beats an existing mother lineage; a draft's own take is never its mother
	e := newEnv(t)
	e.put("r.jsonl", lines(req("grp", 1), req("solo-v2", 2)))
	e.put("varianti/a.json", req("grp-v2", 3))
	e.put("varianti/b.json", req("solo-v2", 2))
	e.flac("grp.flac", "g")
	e.flac("solo.flac", "s")
	e.mapping("audio,request_id,seed,title,parent\ngrp.flac,grp,1,,\nsolo.flac,solo-v2,2,,\ndraft:grp-v2,,,,-\n")
	inv, _ := e.applyAll()
	if hasCode(inv.Report.Warnings, "mother_ambiguous") {
		t.Errorf("warnings = %v", issueCodes(inv.Report.Warnings))
	}
	d := e.drafts()
	if d["grp-v2#3"].Parent != "" {
		t.Errorf("'-' draft got mother %q", d["grp-v2#3"].Parent)
	}
	if d["solo-v2#2"].Parent != "" {
		t.Errorf("draft became its own mother: %q", d["solo-v2#2"].Parent)
	}
	// a second run changes nothing
	e.applyAll()
	if d2 := e.drafts(); d2["grp-v2#3"].Parent != "" || d2["solo-v2#2"].Parent != "" {
		t.Errorf("second run relinked: %+v", d2)
	}
}

func TestInventory_DigestCoversMotherOfExistingDraft(t *testing.T) {
	e := newEnv(t)
	e.put("r.jsonl", lines(req("kid", 1), req("other", 1), req("kid-v2", 2)))
	e.put("varianti/kid-v2.json", req("kid-v2", 2))
	e.applyAll()
	e.flac("kid.flac", "k")
	e.flac("other.flac", "o")
	e.mapping("audio,request_id,seed,title,parent\nkid.flac,kid,1,,\nother.flac,other,1,,\n")
	_, inv0 := e.inventory()
	e.mapping("audio,request_id,seed,title,parent\nkid.flac,kid,1,,\nother.flac,other,1,,\ndraft:kid-v2,,,,other.flac\n")
	_, inv1 := e.inventory()
	if inv0.PlanDigest() == inv1.PlanDigest() {
		t.Error("plan digest ignores the mother written onto an existing draft")
	}
}
