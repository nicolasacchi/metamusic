package librarytest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// Scene builds small ad-hoc libraries row by row, straight through SQL, for the L1 reconcile tests: the exact
// situations of the acceptance table (a draft that gains a mother, two drafts fulfilled by one root, a ref that
// arrives late ...) cannot be reached through the fixtures. It never runs the Reconciler; the triggers fill the queue.
type Scene struct {
	TB  testing.TB
	St  *store.Store
	now time.Time
	n   int
}

// NewScene starts a scene over st (any schema >= 8 store).
func NewScene(tb testing.TB, st *store.Store, now time.Time) *Scene {
	return &Scene{TB: tb, St: st, now: now}
}

func (s *Scene) id(kind string) string {
	s.n++
	_ = kind
	return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xd00000+s.n)
}

// At renders an instant: even i give the 20-character pre-M3 form, odd i the 24-character form (D-L1-34).
func (s *Scene) At(i int) string {
	t := s.now.Add(time.Duration(i) * time.Minute)
	if i%2 == 0 {
		return t.UTC().Format(time.RFC3339)
	}
	return store.FormatTime(t)
}

func (s *Scene) exec(q string, args ...any) {
	s.TB.Helper()
	if _, err := s.St.W.ExecContext(context.Background(), q, args...); err != nil {
		s.TB.Fatalf("scene: %v\n%s", err, q)
	}
}

// Recipe inserts a yue2 recipe with the given request id and style; the seed is unique per call.
func (s *Scene) Recipe(requestID, style string) string {
	s.TB.Helper()
	id := s.id("recipe")
	seed := 7000 + s.n
	rj := reqJSON(requestID, style, "[Verse 1]\nscene line", int64(seed))
	s.exec(`INSERT INTO recipes(id,engine,request_id,request_json,style,lyrics,seed,cot,hash,core_hash,created_by,created_at)
		VALUES(?,'yue2',?,?,?,?,?,'full',?,?,'import',?)`, id, requestID, rj, style, "[Verse 1]\nscene line", seed, Sha("h"+id), Sha("c"+id), s.At(0))
	return id
}

// Ref inserts a recipe_refs row (Muse text, synthetic only).
func (s *Scene) Ref(recipeID, source, title, artist, variantType string) {
	s.TB.Helper()
	m := map[string]string{"source_title": title, "variant_type": variantType}
	if artist != "" {
		m["source_artist"] = artist
	}
	s.exec(`INSERT INTO recipe_refs(recipe_id,source,ref_json,created_at) VALUES(?,?,?,?)`, recipeID, source, jsonObj(m), s.At(0))
}

func jsonObj(m map[string]string) string {
	out := "{"
	i := 0
	for _, k := range []string{"source_title", "source_artist", "variant_type"} {
		v, ok := m[k]
		if !ok {
			continue
		}
		if i > 0 {
			out += ","
		}
		out += fmt.Sprintf("%q:%q", k, v)
		i++
	}
	return out + "}"
}

// Track inserts a take; parent "" makes a root. at is the recorded_at (see At).
func (s *Scene) Track(recipeID, title, parent, at string) string {
	s.TB.Helper()
	id := s.id("track")
	h := Sha("t" + id)
	if parent == "" {
		s.exec(`INSERT INTO tracks(id,slug,title,recipe_id,root_track_id,generation,status_set_by,origin,duration_s,sample_rate,bits_per_sample,channels,total_samples,
			master_path,master_sha256,master_bytes,source_sha256,frames_sha256,audio_md5,import_rule,recorded_at,created_at)
			VALUES(?,?,?,?,?,0,'import','import',10.0,44100,16,2,?,?,?,1,'s',?,'m','slug',?,?)`,
			id, "slug", title, recipeID, id, 1000+s.n, "masters/"+id, h, h, at, at)
		return id
	}
	s.exec(`INSERT INTO tracks(id,slug,title,recipe_id,parent_track_id,root_track_id,generation,status_set_by,origin,duration_s,sample_rate,bits_per_sample,channels,total_samples,
		master_path,master_sha256,master_bytes,source_sha256,frames_sha256,audio_md5,import_rule,recorded_at,created_at)
		VALUES(?,?,?,?,?,(SELECT root_track_id FROM tracks WHERE id = ?),1,'import','import',10.0,44100,16,2,?,?,?,1,'s',?,'m','slug',?,?)`,
		id, "slug", title, recipeID, parent, parent, 1000+s.n, "masters/"+id, h, h, at, at)
	return id
}

// RootInRun inserts a root take that belongs to an import run.
func (s *Scene) RootInRun(recipeID, title, runID, at string) string {
	s.TB.Helper()
	id := s.id("track")
	h := Sha("t" + id)
	s.exec(`INSERT INTO tracks(id,slug,title,recipe_id,root_track_id,generation,status_set_by,origin,duration_s,sample_rate,bits_per_sample,channels,total_samples,
		master_path,master_sha256,master_bytes,source_sha256,frames_sha256,audio_md5,import_rule,import_run_id,recorded_at,created_at)
		VALUES(?,?,?,?,?,0,'import','import',10.0,44100,16,2,?,?,?,1,'s',?,'m','slug',?,?,?)`,
		id, "slug", title, recipeID, id, 1000+s.n, "masters/"+id, h, h, runID, at, at)
	return id
}

// DraftOpts shapes a draft.
type DraftOpts struct {
	Origin       string // default import_backlog
	Recipe       string // source recipe id ("" => none)
	Parent       string // mother track ("" => motherless)
	Label, Group string
	Fulfilled    string // fulfilling track ("" => backlog)
	CreatedAt    string // created_at ("" => At(1))
}

// Draft inserts a draft and returns its id. A draft with a recipe takes the recipe's request id.
func (s *Scene) Draft(requestID string, o DraftOpts) string {
	s.TB.Helper()
	id := s.id("draft")
	if o.Origin == "" {
		o.Origin = "import_backlog"
	}
	status := "backlog"
	if o.Fulfilled != "" {
		status = "fulfilled"
	}
	if o.CreatedAt == "" {
		o.CreatedAt = s.At(1)
	}
	var recipe, parent, group, label, ful any
	if o.Recipe != "" {
		recipe = o.Recipe
	}
	if o.Parent != "" {
		parent = o.Parent
	}
	if o.Group != "" {
		group = o.Group
	}
	if o.Label != "" {
		label = o.Label
	}
	if o.Fulfilled != "" {
		ful = o.Fulfilled
	}
	s.exec(`INSERT INTO recipe_drafts(id,origin,source_recipe_id,parent_track_id,group_key,label,request_id,request_json,status,fulfilled_by_track_id,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, id, o.Origin, recipe, parent, group, label, requestID, reqJSON(requestID, "pop", "[Verse 1]\nl", int64(9000+s.n)), status, ful, o.CreatedAt, o.CreatedAt)
	return id
}

// SetMother sets (or with "" clears) a draft's mother, as the importer and update_draft do.
func (s *Scene) SetMother(draft, track string) {
	s.TB.Helper()
	var v any
	if track != "" {
		v = track
	}
	s.exec(`UPDATE recipe_drafts SET parent_track_id = ? WHERE id = ?`, v, draft)
}

// Fulfil marks a backlog draft fulfilled by a take, as the M3 ingest does.
func (s *Scene) Fulfil(draft, track string) {
	s.TB.Helper()
	s.exec(`UPDATE recipe_drafts SET status='fulfilled', fulfilled_by_track_id=? WHERE id = ?`, track, draft)
}

// Collection puts a draft into a named collection.
func (s *Scene) Collection(draft, name string) {
	s.TB.Helper()
	s.exec(`INSERT INTO draft_collections(draft_id,name,source) VALUES(?,?,'c.json')`, draft, name)
}

// Run inserts an import run (state completed or running).
func (s *Scene) Run(state string, startIdx int) string {
	s.TB.Helper()
	id := s.id("run")
	fin := any(nil)
	if state != "running" {
		fin = s.At(startIdx + 2)
	}
	s.exec(`INSERT INTO import_runs(id,started_at,heartbeat_at,finished_at,state,principal,inbox_label,plan_digest) VALUES(?,?,?,?,?,'cli','inbox','d')`,
		id, s.At(startIdx), s.At(startIdx), fin, state)
	return id
}

// Source records a draft's recipe occurrence in a run (its first occurrence decides the run's draft membership).
func (s *Scene) Source(recipeID, runID string, line int) {
	s.TB.Helper()
	s.exec(`INSERT INTO recipe_sources(recipe_id,source_path,line_no,raw,raw_sha256,raw_kind,import_run_id,created_at) VALUES(?,?,?,x'00',?,'jsonl_line',?,?)`,
		recipeID, "music/songs.jsonl", line, Sha(fmt.Sprintf("%s/%d", recipeID, line)), runID, s.At(0))
}

// Rate inserts or updates the human rating of a take.
func (s *Scene) Rate(track string, stars int) {
	s.TB.Helper()
	s.exec(`INSERT INTO ratings(track_id,source,stars,principal_kind,principal_id,principal_name,created_at,updated_at) VALUES(?,'human',?,'human','human:h','h',?,?)
		ON CONFLICT(track_id, source) DO UPDATE SET stars = excluded.stars`, track, stars, s.At(3), s.At(3))
}

// Status sets a take's status (draft | to_relisten | final | discarded).
func (s *Scene) Status(track, status string) {
	s.TB.Helper()
	s.exec(`UPDATE tracks SET status = ? WHERE id = ?`, status, track)
}

// Batch inserts an estimate whose expanded plan is items (a JSON array of {"item_index": n}, one element per take
// position) and the active batch that consumed it; nTakes is the number of takes (1..100).
func (s *Scene) Batch(items string, nTakes int, state string) string {
	s.TB.Helper()
	eid, bid := s.id("estimate"), s.id("batch")
	s.exec(`INSERT INTO estimates(id,principal_id,principal_kind,engine,backend,items_json,recipe_hashes,n_takes,quote_json,
		expected_micro_usd,max_micro_usd,retry_allowance_micro_usd,ceiling_micro_usd,price_pph_micro_usd,workers_max,idle_s,
		requires_approval,approval_reasons,token_hash,created_at,expires_at,consumed_at,consumed_batch_id)
		VALUES(?,'human:h','human','yue2','fake',?,'[]',?,'{}',10,20,5,25,690000,2,20,0,'[]',?,?,?,?,?)`,
		eid, items, nTakes, Sha("tok"+eid), store.FormatTime(s.now), store.FormatTime(s.now.Add(time.Hour)), store.FormatTime(s.now), bid)
	fin := any(nil)
	if state == "finished" || state == "rejected" || state == "expired" {
		fin = store.FormatTime(s.now.Add(time.Minute))
	}
	s.exec(`INSERT INTO batches(id,estimate_id,principal_id,principal_kind,principal_name,via,engine,backend,n_takes,rationale,
		expected_micro_usd,max_micro_usd,retry_allowance_micro_usd,ceiling_micro_usd,price_pph_micro_usd,attempt_overhead_s,kill_slack_s,
		workers_max,idle_s,state,created_at,finished_at) VALUES(?,?,'human:h','human','h','human_create','yue2','fake',?,NULL,10,20,5,25,690000,200,15,2,20,?,?,?)`,
		bid, eid, nTakes, state, store.FormatTime(s.now), fin)
	return bid
}

// Job inserts a queued job of a batch: draft and source (a parent take) may be empty (a compose_new job has neither),
// recipe is the job's recipe.
func (s *Scene) Job(batch string, takeIndex int, draft, source, recipe string) string {
	s.TB.Helper()
	id := s.id("job")
	var d, src any
	if draft != "" {
		d = draft
	}
	if source != "" {
		src = source
	}
	ts := store.FormatTime(s.now)
	s.exec(`INSERT INTO jobs(id,batch_id,take_index,kind,engine,backend,recipe_id,source_track_id,draft_id,operator,mutation_json,title,expected_audio_s,expected_s,
		timeout_s,expected_micro_usd,reserved_micro_usd,state,next_attempt_at,queued_at,created_at,updated_at)
		VALUES(?,?,?,'generate','yue2','fake',?,?,?,'reroll_seed','{}','Job',216,96,186,10,20,'queued',?,?,?,?)`,
		id, batch, takeIndex, recipe, src, d, ts, ts, ts, ts)
	return id
}

// JobProduced records the take a job produced (its track_id), the link the Reconciler follows.
func (s *Scene) JobProduced(job, track string) {
	s.TB.Helper()
	s.exec(`UPDATE jobs SET track_id = ? WHERE id = ?`, track, job)
}

// Duel inserts a blind duel between two takes and returns its id.
func (s *Scene) Duel(a, b string) string {
	s.TB.Helper()
	id := s.id("duel")
	s.exec(`INSERT INTO duels(id,slot_a_track_id,slot_b_track_id,swapped,blind,from_ms,to_ms,clip_def,tool_version,match,gain_a_cdb,gain_b_cdb,created_by,created_by_kind,created_at,expires_at)
		VALUES(?,?,?,0,1,0,1000,'mm-clip/1','ffmpeg n9','off',0,0,'human:h','human',?,?)`, id, a, b, s.At(1), s.At(2))
	return id
}

// Verdict records the human verdict ('a' | 'b' | 'tie') of a duel.
func (s *Scene) Verdict(duel, verdict string) {
	s.TB.Helper()
	win := any(nil)
	switch verdict {
	case "a":
		win = s.str(`SELECT slot_a_track_id FROM duels WHERE id = ?`, duel)
	case "b":
		win = s.str(`SELECT slot_b_track_id FROM duels WHERE id = ?`, duel)
	}
	s.exec(`INSERT INTO duel_verdicts(id,duel_id,source,principal_kind,principal_id,verdict,winner_track_id,saw_reveal,created_at) VALUES(?,?,'human','human','human:h',?,?,0,?)`,
		s.id("verdict"), duel, verdict, win, s.At(3))
}

func (s *Scene) str(q string, args ...any) string {
	s.TB.Helper()
	var v string
	if err := s.St.R.QueryRow(q, args...).Scan(&v); err != nil {
		s.TB.Fatalf("scene: %v\n%s", err, q)
	}
	return v
}
