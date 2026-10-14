package api

import (
	"context"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/listening"
)

func registerListen(a huma.API, d Deps) {
	Register(a, withBody(huma.Operation{
		OperationID: "log_listen_events", Method: "POST", Path: "/api/listen-events",
		Summary:     "Log listen events",
		Description: "Records player events (start, segment, seek, complete, skip) and client playback diagnostics (autoplay_blocked, stall_end, advance, error) with client-generated ids; re-sending an id is harmless.",
	}, bodyListen), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/tracks/:id"},
		func(ctx context.Context, in *logListenIn) (*logListenOut, error) {
			if d.Listening == nil {
				return nil, unavailable("listening")
			}
			evs := make([]listening.Event, len(in.Body.Events))
			for i, e := range in.Body.Events {
				// An unparsable timestamp stays zero; the service rejects it as bad_client_at.
				at, _ := time.Parse(time.RFC3339, e.ClientAt)
				evs[i] = listening.Event{ID: e.ID, TrackID: e.TrackID, SessionID: e.SessionID, Kind: e.Kind,
					FromMS: e.FromMS, ToMS: e.ToMS, Format: e.Format, DeviceID: e.DeviceID, Detail: e.Detail, ClientAt: at}
			}
			acc, dup, rej, err := d.Listening.Log(ctx, auth.FromContext(ctx), commentSource(ctx), evs)
			if err != nil {
				return nil, err
			}
			out := &logListenOut{}
			out.Body.Accepted, out.Body.Duplicates = acc, dup
			out.Body.Rejected = make([]listenRejected, len(rej))
			for i, r := range rej {
				out.Body.Rejected[i] = listenRejected{ID: r.ID, Code: r.Code}
			}
			return out, nil
		})
}
