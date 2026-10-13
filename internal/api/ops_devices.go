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

func registerPairedDevices(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "list_devices", Method: "GET", Path: "/human/api/devices",
		Summary:     "List paired devices",
		Description: "Paired browsers and phones with their last use. Operator only: user agents and names are personal data.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "exclude:human-only", UI: "/human/devices", HumanOnly: true},
		stub[empty, deviceListOut]("M2a-B9")) // STUB(M2a-B9)

	Register(a, withBody(created(huma.Operation{
		OperationID: "create_pairing_code", Method: "POST", Path: "/human/api/devices/pairing-codes",
		Summary:     "Create a pairing code",
		Description: "Mints a single-use code (5 minutes) for pairing a new device; shown once with its pairing URL. Operator only.",
	}), bodySmall), Meta{Scope: auth.ScopeAdmin, Cost: "free", MCP: "exclude:human-only", UI: "/human/devices", HumanOnly: true,
		Idempotency: "create", SecretFields: []string{"code", "pair_url"}},
		stub[createPairingIn, pairingOut]("M2a-B9")) // STUB(M2a-B9)

	Register(a, huma.Operation{
		OperationID: "revoke_device", Method: "POST", Path: "/human/api/devices/{id}/revoke",
		Summary:     "Revoke a paired device",
		Description: "Unpairs a device by id. Repeating the call returns the same result. Operator only.",
	}, Meta{Scope: auth.ScopeAdmin, Cost: "free", MCP: "exclude:human-only", UI: "/human/devices", HumanOnly: true},
		stub[deviceIDIn, deviceOut]("M2a-B9")) // STUB(M2a-B9)

	Register(a, huma.Operation{
		OperationID: "revoke_own_device", Method: "POST", Path: "/api/devices/self/revoke",
		Summary:     "Unpair this device",
		Description: "Revokes the paired device that made this call and clears its cookie. Other callers get 403 NOT_A_DEVICE.",
	}, Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "exclude:device-self-service", UI: "/settings"},
		stub[empty, deviceSelfOut]("M2a-B9")) // STUB(M2a-B9)
}
