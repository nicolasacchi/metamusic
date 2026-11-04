package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// registerSongPRM5 registers the three Song PR operations (C253, C260). Frozen by M5-F2: each answers 501 naming lane M5-S1
// until that lane fills the handler (it then adds the id to implementedSongPRM5 in ops_songpr_m5_test.go).
func registerSongPRM5(a huma.API, d Deps) {
	Register(a, withBody(created(huma.Operation{
		OperationID: "propose_patch", Method: "POST", Path: "/api/patches",
		Summary:     "Propose a Song PR",
		Description: "Stores a bounded patch to a take (at most 8 lyric lines in at most 4 sections, or a style change of at most 3 terms and 20 BPM) that names the fix or note comments it addresses. Free; rendering goes through estimate_generation with item mode song_pr.",
	}), bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/tracks/:id", Idempotency: "create"},
		func(ctx context.Context, in *proposePatchIn) (*patchOut, error) {
			if d.SongPR == nil {
				return nil, unavailable("songpr")
			}
			return nil, notImplemented(taskS1)
		})

	Register(a, huma.Operation{
		OperationID: "get_patch", Method: "GET", Path: "/api/patches/{id}",
		Summary:     "Get a Song PR",
		Description: "A Song PR with its ops, diff, scope and bounds report, the comments it addresses, its renders and the tally of callback answers.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/patches/:id"},
		func(ctx context.Context, in *getPatchIn) (*patchOut, error) {
			if d.SongPR == nil {
				return nil, unavailable("songpr")
			}
			return nil, notImplemented(taskS1)
		})

	Register(a, huma.Operation{
		OperationID: "list_patches", Method: "GET", Path: "/api/patches",
		Summary:     "List Song PRs",
		Description: "Song PRs newest first, filtered by take, addressed comment or lineage root, without ops and diff.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/tracks/:id"},
		func(ctx context.Context, in *listPatchesIn) (*patchPageOut, error) {
			if d.SongPR == nil {
				return nil, unavailable("songpr")
			}
			return nil, notImplemented(taskS1)
		})
}

// songPRPending refuses the song_pr item mode and a patch_id until lane M5-S1 lands the generation side (C253.4); S1 deletes it
// together with its two call sites in ops_generation_m3.go.
func songPRPending(mode string, patchID *string) error {
	if mode == "song_pr" || patchID != nil {
		return notImplemented(taskS1)
	}
	return nil
}
