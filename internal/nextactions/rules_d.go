package nextactions

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// rulesD are the four take-subject rules of M5-C1 (C257, D-M5-R3-4): what the human or an agent should do next about one take of
// the close-the-loop flow. Cutting the lane deletes this file and testdata/rules_v1_d.golden.json. Every Find is one batch
// statement restricted through TakeScope, so a call about one take costs that take's work, not the library's.
var rulesD = []Rule{
	{
		Code: "callback_waiting", Priority: 85, Audience: AudienceHuman, MinScope: auth.ScopeRead,
		Find: findCallbackWaiting, Text: textCallbackWaiting, Hint: hintCallbackWaiting,
	},
	{
		Code: "regressed_comment", Priority: 72, Audience: AudienceBoth, MinScope: auth.ScopeRead,
		Find: findRegressedComment, Text: textRegressedComment, Hint: hintGetTrackContext,
	},
	{
		Code: "open_fix_unaddressed", Priority: 58, Audience: AudienceAgent, MinScope: auth.ScopeRead,
		Find: findOpenFixUnaddressed, Text: textOpenFixUnaddressed, Hint: hintGetTrackContext,
	},
	{
		Code: "patch_ready_to_render", Priority: 52, Audience: AudienceAgent, MinScope: auth.ScopeGenerate,
		Find: findPatchReadyToRender, Text: textPatchReadyToRender, Hint: hintPatchReadyToRender,
	},
}

// takeScopeCond restricts a statement to the takes the call is about; pass TakeScope(q) twice.
const takeScopeCond = `(? = '' OR %s IN (SELECT value FROM json_each(?)))`

func takeScope(col string) string { return fmt.Sprintf(takeScopeCond, col) }

// liveJobSQL is true for a patch id column that has a render: a job that is not cancelled in a batch that was not rejected or
// expired (those never started). It is the "non-cancelled batch" of C253/C257 read from the jobs a patch owns.
func liveJobSQL(patchCol string) string {
	return `EXISTS (SELECT 1 FROM jobs j JOIN batches b ON b.id = j.batch_id WHERE j.patch_id = ` + patchCol + ` AND j.state <> 'cancelled' AND b.state NOT IN ('rejected', 'expired'))`
}

// scanTake scans (take id, count, title, total) into a candidate whose evidence has n and the private title.
func scanTake(key string) func(rows *sql.Rows) (Candidate, error) {
	return func(rows *sql.Rows) (Candidate, error) {
		var id, title string
		var n, total int
		if err := rows.Scan(&id, &n, &title, &total); err != nil {
			return Candidate{}, err
		}
		return Candidate{Subject: Subject{Kind: SubjectTake, ID: &id}, Evidence: map[string]any{key: n, "_title": title, evTotal: total}}, nil
	}
}

// findCallbackWaiting: a child take with at least one callback that no human verdict answers.
func findCallbackWaiting(ctx context.Context, q Reader, lim int) ([]Candidate, error) {
	scope := TakeScope(q)
	return queryCandidates(ctx, q, `SELECT c.child_track_id, COUNT(*), t.title, COUNT(*) OVER ()
		FROM callbacks c JOIN tracks t ON t.id = c.child_track_id
		WHERE t.status <> 'discarded' AND `+takeScope("c.child_track_id")+`
		  AND NOT EXISTS (SELECT 1 FROM callback_verdicts v WHERE v.callback_id = c.id AND v.source = 'human')
		GROUP BY c.child_track_id ORDER BY c.child_track_id LIMIT ?`, []any{scope, scope, lim}, scanTake("callbacks"))
}

// findRegressedComment: a take with a non-hidden comment in state regressed.
func findRegressedComment(ctx context.Context, q Reader, lim int) ([]Candidate, error) {
	scope := TakeScope(q)
	return queryCandidates(ctx, q, `SELECT cm.track_id, COUNT(*), t.title, COUNT(*) OVER ()
		FROM comments cm JOIN tracks t ON t.id = cm.track_id
		WHERE cm.state = 'regressed' AND cm.hidden_at IS NULL AND t.status <> 'discarded' AND `+takeScope("cm.track_id")+`
		GROUP BY cm.track_id ORDER BY cm.track_id LIMIT ?`, []any{scope, scope, lim}, scanTake("comments"))
}

// findOpenFixUnaddressed: a take with open non-hidden fix comments that no patch in progress addresses. A patch is in progress
// while it has no render yet (patch_ready_to_render owns that case) or a render that has not finished.
func findOpenFixUnaddressed(ctx context.Context, q Reader, lim int) ([]Candidate, error) {
	scope := TakeScope(q)
	return queryCandidates(ctx, q, `SELECT cm.track_id, COUNT(*), t.title, COUNT(*) OVER ()
		FROM comments cm JOIN tracks t ON t.id = cm.track_id
		WHERE cm.kind = 'fix' AND cm.state = 'open' AND cm.hidden_at IS NULL AND t.status <> 'discarded' AND `+takeScope("cm.track_id")+`
		  AND NOT EXISTS (SELECT 1 FROM patch_comments pc WHERE pc.comment_id = cm.id AND (NOT `+liveJobSQL("pc.patch_id")+`
		       OR EXISTS (SELECT 1 FROM jobs j WHERE j.patch_id = pc.patch_id AND j.state NOT IN ('completed', 'failed', 'cancelled', 'expired'))))
		GROUP BY cm.track_id ORDER BY cm.track_id LIMIT ?`, []any{scope, scope, lim}, scanTake("fixes"))
}

// findPatchReadyToRender: a take that is the base of at least one patch with no render. The candidate names the newest such patch
// (the hint estimates it) and counts them.
func findPatchReadyToRender(ctx context.Context, q Reader, lim int) ([]Candidate, error) {
	scope := TakeScope(q)
	return queryCandidates(ctx, q, `SELECT base, id, n, title, COUNT(*) OVER () FROM (
		SELECT p.base_track_id AS base, p.id AS id, t.title AS title,
		       ROW_NUMBER() OVER (PARTITION BY p.base_track_id ORDER BY p.created_at DESC, p.id DESC) AS rn,
		       COUNT(*) OVER (PARTITION BY p.base_track_id) AS n
		FROM patches p JOIN tracks t ON t.id = p.base_track_id
		WHERE t.status <> 'discarded' AND `+takeScope("p.base_track_id")+` AND NOT `+liveJobSQL("p.id")+`)
		WHERE rn = 1 ORDER BY base LIMIT ?`, []any{scope, scope, lim},
		func(rows *sql.Rows) (Candidate, error) {
			var id, patch, title string
			var n, total int
			if err := rows.Scan(&id, &patch, &n, &title, &total); err != nil {
				return Candidate{}, err
			}
			return Candidate{Subject: Subject{Kind: SubjectTake, ID: &id}, Evidence: map[string]any{"patches": n, "_patch_id": patch, "_title": title, evTotal: total}}, nil
		})
}

// takeTitle is the private title the Find put in the evidence; a take whose title is gone reads "a take".
func takeTitle(c Candidate) string {
	if t := evidenceString(c, "_title"); t != "" {
		return t
	}
	return "a take"
}

func textCallbackWaiting(c Candidate, _ Names) string {
	return fmt.Sprintf("%d callback(s) wait on \"%s\": listen and answer", evidenceInt(c, "callbacks"), takeTitle(c))
}

func textRegressedComment(c Candidate, _ Names) string {
	return fmt.Sprintf("\"%s\": %d comment(s) regressed — see what got worse before rendering again", takeTitle(c), evidenceInt(c, "comments"))
}

func textOpenFixUnaddressed(c Candidate, _ Names) string {
	return fmt.Sprintf("\"%s\" has %d open fix comment(s) no patch addresses — read the context and propose a Song PR (propose_patch)", takeTitle(c), evidenceInt(c, "fixes"))
}

func textPatchReadyToRender(c Candidate, _ Names) string {
	return fmt.Sprintf("\"%s\" has %d patch(es) never rendered — estimate the newest (the estimate is free)", takeTitle(c), evidenceInt(c, "patches"))
}

func hintCallbackWaiting(c Candidate, _ auth.Principal) *Hint {
	return &Hint{Op: "list_callbacks", Args: map[string]any{"state": []any{"pending"}, "child_track_id": subjectID(c.Subject)}}
}

func hintGetTrackContext(c Candidate, _ auth.Principal) *Hint {
	return &Hint{Op: "get_track_context", Args: map[string]any{"id": subjectID(c.Subject)}}
}

func hintPatchReadyToRender(c Candidate, _ auth.Principal) *Hint {
	return &Hint{Op: "estimate_generation", Args: map[string]any{"items": []any{map[string]any{"mode": "song_pr", "patch_id": evidenceString(c, "_patch_id")}}}}
}
