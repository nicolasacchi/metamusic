package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/songwriting"
)

type songwritingOut struct{ Body songwriting.Targets }

type updateSongwritingIn struct {
	Body struct {
		MinDurationS       *int `json:"min_duration_s,omitempty" minimum:"30" maximum:"600" doc:"Songs shorter than this draw a duration_under_target warning"`
		TargetMinDurationS *int `json:"target_min_duration_s,omitempty" minimum:"30" maximum:"600" doc:"Lower end of the target band"`
		TargetMaxDurationS *int `json:"target_max_duration_s,omitempty" minimum:"30" maximum:"600" doc:"Upper end of the target band"`
	}
}

// registerSongwriting registers the songwriting duration-target operations. The read is the agent counterpart of the
// operator's write: agents see the targets (get_songwriting_settings, the track context, lint warnings) but never set them.
func registerSongwriting(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "get_songwriting_settings", Method: "GET", Path: "/api/settings/songwriting",
		Summary:     "Get the songwriting duration targets",
		Description: "Preferred song length in seconds: at least min_duration_s, aiming for target_min_duration_s to target_max_duration_s. Lint warns when a song misses them.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "/settings"},
		func(ctx context.Context, _ *empty) (*songwritingOut, error) {
			if d.Songwriting == nil {
				return nil, unavailable("songwriting")
			}
			t, err := d.Songwriting.Get(ctx)
			if err != nil {
				return nil, err
			}
			return &songwritingOut{Body: t}, nil
		})

	Register(a, withBody(huma.Operation{
		OperationID: "update_songwriting_settings", Method: "PUT", Path: "/human/api/settings/songwriting",
		Summary:     "Set the songwriting duration targets",
		Description: "The operator changes the minimum song length and/or the target band, in seconds (30 to 600; min <= target min <= target max). At least one field.",
	}, bodySmall), Meta{Scope: auth.ScopeAdmin, Cost: "free", MCP: "exclude:human-only", HumanOnly: true, UI: "/settings"},
		func(ctx context.Context, in *updateSongwritingIn) (*songwritingOut, error) {
			if d.Songwriting == nil {
				return nil, unavailable("songwriting")
			}
			b := in.Body
			t, err := d.Songwriting.Set(ctx, songwriting.Patch{MinDurationS: b.MinDurationS, TargetMinDurationS: b.TargetMinDurationS, TargetMaxDurationS: b.TargetMaxDurationS})
			if errors.Is(err, songwriting.ErrInvalid) {
				return nil, Err(http.StatusUnprocessableEntity, "VALIDATION_FAILED",
					"give at least one value, each 30 to 600 seconds, with min <= target min <= target max (after applying the patch)", nil)
			}
			if err != nil {
				return nil, err
			}
			return &songwritingOut{Body: t}, nil
		})
}
