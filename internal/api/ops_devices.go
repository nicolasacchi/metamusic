package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library"
)

func registerDevices(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "put_device_profile", Method: "PUT", Path: "/api/device-profiles/{device_id}",
		Summary:     "Create or update a device profile",
		Description: "Upserts the playback profile of one client device (preferred quality and the FLAC probe result). Idempotent.",
	}, Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/settings"},
		func(ctx context.Context, in *putDeviceProfileIn) (*deviceProfileOut, error) {
			if d.Library == nil {
				return nil, unavailable("library")
			}
			b := in.Body
			prof, err := d.Library.PutDeviceProfile(ctx, auth.FromContext(ctx), library.DeviceProfileIn{
				DeviceID: in.DeviceID, Label: b.Label, UserAgent: b.UserAgent, Quality: b.Quality, QualitySource: b.QualitySource,
				ProbeResult: b.ProbeResult, FlacCanPlay: b.FlacCanPlay, FlacDecodeOK: b.FlacDecodeOK, FlacSeekOK: b.FlacSeekOK,
				ProbeVersion: b.ProbeVersion, ProbedAt: b.ProbedAt,
			})
			if err != nil {
				return nil, libErr(err)
			}
			return &deviceProfileOut{Body: prof}, nil
		})

	Register(a, huma.Operation{
		OperationID: "list_device_profiles", Method: "GET", Path: "/api/device-profiles",
		Summary:     "List device profiles",
		Description: "Known device playback profiles. Pages are capped in size; follow next_cursor.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/settings"},
		func(ctx context.Context, in *listDeviceProfilesIn) (*deviceProfilesOut, error) {
			if d.Library == nil {
				return nil, unavailable("library")
			}
			l, err := d.Library.DeviceProfiles(ctx, in.Limit, in.Cursor)
			if err != nil {
				return nil, libErr(err)
			}
			return &deviceProfilesOut{Body: l}, nil
		})
}
