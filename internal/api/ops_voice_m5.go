package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// registerVoiceM5 registers create_voice_note (C259, C260), the one M5 operation without an MCP face: a voice note is a human
// recording (parity exception, box B5). Frozen by M5-F2; the optional lane M5-V1 fills the handler (it refuses non-human
// principals with 403 HUMAN_ONLY) and adds the id to implementedVoiceM5 in ops_voice_m5_test.go.
func registerVoiceM5(a huma.API, d Deps) {
	Register(a, withBody(created(huma.Operation{
		OperationID: "create_voice_note", Method: "POST", Path: "/api/tracks/{id}/voice-notes",
		Summary:     "Leave a voice note on a track",
		Description: "Records a spoken comment (at most 120 s and 4 MiB, base64) at a second of the track as a note, fix or keep comment. Only the operator and their devices; the MP3 rendition is what gets played back.",
	}), bodyVoice), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "exclude:human-recording", UI: "/tracks/:id", Idempotency: "create"},
		func(ctx context.Context, in *createVoiceNoteIn) (*commentOut, error) {
			if d.Voice == nil {
				return nil, unavailable("voice")
			}
			return nil, notImplemented(taskV1)
		})
}
