package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
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

func deviceView(di auth.DeviceInfo) Device {
	out := Device{ID: di.ID, Name: di.Name, Prefix: di.Prefix, UserAgent: di.UserAgent, PairedBy: di.PairedBy,
		CreatedAt: di.CreatedAt, ExpiresAt: di.ExpiresAt, Scopes: make([]string, len(di.Scopes))}
	for i, sc := range di.Scopes {
		out.Scopes[i] = string(sc)
	}
	if di.LastSeenAt != "" {
		out.LastSeenAt = &di.LastSeenAt
	}
	if di.RevokedAt != "" {
		out.RevokedAt = &di.RevokedAt
	}
	return out
}

func deviceErr(err error) error {
	switch {
	case errors.Is(err, auth.ErrNotFound):
		return Err(404, "NOT_FOUND", "no such device", nil)
	case errors.Is(err, auth.ErrInvalidName):
		return Err(422, "VALIDATION_FAILED", "name must be 1 to 80 characters", nil)
	}
	return err
}

// clearDeviceCookie expires the device cookie on the response.
func clearDeviceCookie(cfg config.Config) http.Cookie {
	return http.Cookie{Name: auth.DeviceCookie, //nolint:gosec // G124: Secure follows PublicURL (plain-http dev); HttpOnly and SameSite are always set
		Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		Secure: strings.HasPrefix(cfg.PublicURL, "https://"), SameSite: http.SameSiteLaxMode}
}

func registerPairedDevices(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "list_devices", Method: "GET", Path: "/human/api/devices",
		Summary:     "List paired devices",
		Description: "Paired browsers and phones with their last use. Operator only: user agents and names are personal data.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "exclude:human-only", UI: "/human/devices", HumanOnly: true},
		func(ctx context.Context, _ *empty) (*deviceListOut, error) {
			if d.Devices == nil {
				return nil, unavailable("devices")
			}
			list, err := d.Devices.List(ctx)
			if err != nil {
				return nil, err
			}
			out := &deviceListOut{}
			out.Body.Items = make([]Device, len(list))
			for i, di := range list {
				out.Body.Items[i] = deviceView(di)
			}
			return out, nil
		})

	Register(a, withBody(created(huma.Operation{
		OperationID: "create_pairing_code", Method: "POST", Path: "/human/api/devices/pairing-codes",
		Summary:     "Create a pairing code",
		Description: "Mints a single-use code (5 minutes) for pairing a new device; shown once with its pairing URL. Operator only.",
	}), bodySmall), Meta{Scope: auth.ScopeAdmin, Cost: "free", MCP: "exclude:human-only", UI: "/human/devices", HumanOnly: true,
		Idempotency: "create", SecretFields: []string{"code", "pair_url"}},
		func(ctx context.Context, in *createPairingIn) (*pairingOut, error) {
			if d.Devices == nil {
				return nil, unavailable("devices")
			}
			code, pc, err := d.Devices.CreatePairingCode(ctx, auth.FromContext(ctx), in.Body.Name)
			if err != nil {
				return nil, deviceErr(err)
			}
			pairURL := strings.TrimRight(d.Cfg.PublicURL, "/") + "/pair#" + code
			return &pairingOut{Body: PairingCodeOut{ID: pc.ID, Name: pc.Name, Code: &code, PairURL: &pairURL, ExpiresAt: pc.ExpiresAt}}, nil
		})

	Register(a, huma.Operation{
		OperationID: "revoke_device", Method: "POST", Path: "/human/api/devices/{id}/revoke",
		Summary:     "Revoke a paired device",
		Description: "Unpairs a device by id. Repeating the call returns the same result. Operator only.",
	}, Meta{Scope: auth.ScopeAdmin, Cost: "free", MCP: "exclude:human-only", UI: "/human/devices", HumanOnly: true},
		func(ctx context.Context, in *deviceIDIn) (*deviceOut, error) {
			if d.Devices == nil {
				return nil, unavailable("devices")
			}
			di, err := d.Devices.Revoke(ctx, in.ID)
			if err != nil {
				return nil, deviceErr(err)
			}
			return &deviceOut{Body: deviceView(di)}, nil
		})

	Register(a, huma.Operation{
		OperationID: "revoke_own_device", Method: "POST", Path: "/api/devices/self/revoke",
		Summary:     "Unpair this device",
		Description: "Revokes the paired device that made this call and clears its cookie. Other callers get 403 NOT_A_DEVICE.",
	}, Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "exclude:device-self-service", UI: "/settings"},
		func(ctx context.Context, _ *empty) (*deviceSelfOut, error) {
			p := auth.FromContext(ctx)
			if p.Kind != auth.KindDevice {
				return nil, Err(403, "NOT_A_DEVICE", "this call was not made by a paired device", nil)
			}
			if d.Devices == nil {
				return nil, unavailable("devices")
			}
			di, err := d.Devices.Revoke(ctx, p.ID)
			if err != nil {
				return nil, deviceErr(err)
			}
			self := DeviceSelf{ID: di.ID, Name: di.Name}
			if di.RevokedAt != "" {
				self.RevokedAt = &di.RevokedAt
			}
			return &deviceSelfOut{SetCookie: clearDeviceCookie(d.Cfg), Body: self}, nil
		})
}
