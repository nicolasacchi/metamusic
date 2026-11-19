package api

import (
	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/view"
)

// Response shaping for Editors and Listeners (C308.3, C308.4, D-U1-12). Every operation keeps one typed response for all
// roles; the last step of the response, a Huma transformer, removes the keys the viewer may not see, so the key is absent
// (never zeroed, never null) and the Owner side, whose view.Viewer needs nothing removed, gets the typed body untouched and
// byte-identical to before U1. The fields that can be absent are marked required:"false" in the schema.
//
// The rules are paths over the JSON body (see view.Rules). They are listed per operation, plus a common set for every
// operation of scope listen or rate (the ops a Listener reaches): money keys, the seed and request id, the agent's stars
// and the key of whoever set a status. A new Listener-reachable operation is therefore shaped by default and fails closed.

// listenerCommon is added to every operation of scope listen or rate.
var listenerCommon = view.Rules{
	MoneyKeys: true,
	Private:   []string{"**.request_id", "**.seed", "**.critic_stars", "**.status_set_by"},
}

// shapeRules is the per-operation part, keyed by operation id.
var shapeRules = map[string]view.Rules{
	"search_tracks": {Private: []string{"items.comments", "items.open_comments"}},
	"get_track": {
		Cost: []string{"generation.cost"},
		Muse: []string{"song.muse_id"},
		Private: []string{
			"generation", "import", "playlists", "listening", "comments", "open_comments", "rating.critic",
			"recipe.cot", "recipe.hash", "recipe.style", "audio.master_sha256", "audio.audio_md5", "audio.mp3.sha256",
			"lineage.operator", "lineage.rule", "lineage.inferred",
		},
	},
	"get_lineage": {
		Cost:    []string{"nodes.cost"},
		Private: []string{"nodes.job_id", "nodes.operator", "nodes.outcome", "nodes.rationale"},
	},
	"compare_tracks": {Private: []string{"duels", "ladder", "recipe_diff.seed", "recipe_diff.same_hash", "recipe_diff.changed"}},
	"list_songs":     {Muse: []string{"items.muse_id"}, Private: []string{"items.draft_id", "items.queued_jobs", "items.open_proposals"}},
	"search_sounds":  {Muse: []string{"songs.muse_id"}, Private: []string{"songs.draft_id", "songs.queued_jobs", "songs.open_proposals"}},
	"get_song": {
		Muse: []string{"muse", "song.muse_id"},
		Private: []string{
			"draft", "sessions", "proposals", "events", "song.draft_id", "song.queued_jobs",
			"takes.ladder", "pick.evidence", "pick.pinned_by", "pick.pinned_at", "pick.pinned_take_id", "pick.open_proposals",
		},
	},
	"get_song_atlas":     {Muse: []string{"muses", "points.mi"}},
	"get_sound_taxonomy": {Private: []string{"versions.created_by", "versions.note"}},
	"get_playlist":       {Private: []string{"items.track.comments", "items.track.open_comments"}},
	"list_crates":        {Private: []string{"items.filter"}},
	"get_crate":          {Private: []string{"crate.filter"}},
	"get_record":         {Private: []string{"liner", "record.liner_note", "record.proposal", "slots.why"}, Empty: []string{"changes"}},
	"get_record_changes": {Empty: []string{"items"}},
	"get_track_context":  {Editor: []string{"muse", "provenance.cost"}, MoneyKeys: true},
	"get_stats":          {Editor: []string{"generation", "backups", "storage", "imports", "library_model.muses"}},
}

// rulesFor returns the shaping rules of an operation: the per-operation rules, plus the common Listener set for an
// operation of scope listen or rate.
func rulesFor(opID string, m Meta) view.Rules {
	r := shapeRules[opID]
	if m.Scope == auth.ScopeListen || m.Scope == auth.ScopeRate {
		r.MoneyKeys = r.MoneyKeys || listenerCommon.MoneyKeys
		r.Private = append(append([]string(nil), r.Private...), listenerCommon.Private...)
	}
	return r
}

// shapeTransformer is the Huma transformer that applies rulesFor to a 2xx response body for the caller's view.
func shapeTransformer(ctx huma.Context, status string, v any) (any, error) {
	if len(status) == 0 || status[0] != '2' || v == nil {
		return v, nil
	}
	op := ctx.Operation()
	if op == nil {
		return v, nil
	}
	m, ok := OpMeta(op)
	if !ok {
		return v, nil
	}
	return view.FromContext(ctx.Context()).Shape(v, rulesFor(op.OperationID, m))
}
