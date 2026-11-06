package api

import (
	"context"
	"errors"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/judgments"
	"github.com/nicolasacchi/metamusic/internal/voice"
)

// registerVoiceM5 registers create_voice_note (C259, C260), the one M5 operation without an MCP face: a voice note is a human
// recording (parity exception, box B5). Frozen by M5-F2; the handler (M5-V1) refuses non-human
// principals with 403 HUMAN_ONLY).
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
			p := auth.FromContext(ctx)
			if !p.ActsAsHuman() {
				return nil, Err(403, "HUMAN_ONLY", "only the operator records voice notes", nil)
			}
			if d.Judgments == nil {
				return nil, unavailable("judgments")
			}
			id, err := d.Voice.Create(ctx, p, commentSource(ctx), in.ID, voice.Input{AudioBase64: in.Body.AudioBase64,
				AtSecond: in.Body.AtSecond, Kind: in.Body.Kind, Body: voiceBody(in.Body.Body)})
			if errors.Is(err, voice.ErrUnavailable) {
				return nil, unavailable("voice")
			}
			if err != nil {
				var je *judgments.Error
				if errors.As(err, &je) {
					return nil, judgErr(err)
				}
				return nil, m2bErr(err, taskV1)
			}
			c, err := d.Judgments.CommentByID(ctx, id)
			if err != nil {
				return nil, judgErr(err)
			}
			renderComment(p, d.Cfg.PublicURL, &c)
			return &commentOut{Body: c}, nil
		})
}

func voiceBody(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
