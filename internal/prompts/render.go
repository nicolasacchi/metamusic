package prompts

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"text/template"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/listening"
)

//go:embed text/*.tmpl
var textFS embed.FS

var templates = template.Must(template.New("prompts").Option("missingkey=error").ParseFS(textFS, "text/*.tmpl"))

const (
	// SessionDefault is how far back session_review looks when since is absent.
	SessionDefault = 12 * time.Hour
	// SessionMaxAge is the oldest since session_review accepts.
	SessionMaxAge = 30 * 24 * time.Hour
	// MaxGoal is the longest write_variant goal, in bytes.
	MaxGoal = 500
	// sessionTracks is the track cap of a session_review summary.
	sessionTracks = 20
)

// Error is a client-visible failure with a stable code; the api layer maps it to a problem.
type Error struct {
	Status  int
	Code    string
	Msg     string
	Details map[string]any
}

func (e *Error) Error() string { return e.Code + ": " + e.Msg }

func invalid(field, msg string) error {
	return &Error{Status: http.StatusUnprocessableEntity, Code: "VALIDATION_FAILED", Msg: msg, Details: map[string]any{"field": field}}
}

func spec(name string) (Spec, bool) {
	i := slices.IndexFunc(Catalog, func(s Spec) bool { return s.Name == name })
	if i < 0 {
		return Spec{}, false
	}
	return Catalog[i], true
}

func execute(name string, data any) (string, error) {
	var b bytes.Buffer
	if err := templates.ExecuteTemplate(&b, name+".tmpl", data); err != nil {
		return "", fmt.Errorf("prompts: render %s: %w", name, err)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func textMsg(s string) Message { return Message{Role: "user", Content: Content{Type: "text", Text: s}} }

func contextMsg(trackID string, body []byte) Message {
	return Message{Role: "user", Content: Content{Type: "resource", Resource: &Embedded{
		URI: "metamusic://tracks/" + trackID + "/context", MIMEType: "application/json", Text: string(body)}}}
}

// Render builds the messages of prompt name for p (C112.2). Errors are *Error (422), or whatever Sources returns
// (an unknown track is library.ErrNotFound, wrapped, so the api layer answers 404).
func Render(ctx context.Context, p auth.Principal, name string, args map[string]string, src Sources, now time.Time) (Spec, []Message, error) {
	sp, ok := spec(name)
	if !ok {
		return Spec{}, nil, invalid("name", "unknown prompt")
	}
	switch name {
	case "session_review":
		return renderSession(ctx, sp, args, src, now)
	default:
		return renderTrack(ctx, p, sp, args, src)
	}
}

func renderTrack(ctx context.Context, p auth.Principal, sp Spec, args map[string]string, src Sources) (Spec, []Message, error) {
	id := strings.TrimSpace(args["track_id"])
	if id == "" {
		return Spec{}, nil, invalid("track_id", sp.Name+" needs track_id")
	}
	goal := strings.TrimSpace(args["goal"])
	if sp.Name == "write_variant" && len(goal) > MaxGoal {
		return Spec{}, nil, invalid("goal", fmt.Sprintf("at most %d characters", MaxGoal))
	}
	title, err := src.Title(ctx, id)
	if err != nil {
		return Spec{}, nil, fmt.Errorf("prompts: track: %w", err)
	}
	if sp.Name == "write_variant" {
		known, err := src.RecipeKnown(ctx, id)
		if err != nil {
			return Spec{}, nil, fmt.Errorf("prompts: recipe: %w", err)
		}
		if !known {
			return Spec{}, nil, &Error{Status: http.StatusUnprocessableEntity, Code: "RECIPE_UNKNOWN",
				Msg: "a reference track has no recipe to vary", Details: map[string]any{"track_id": id}}
		}
	}
	body, err := src.ContextJSON(ctx, p, id)
	if err != nil {
		return Spec{}, nil, fmt.Errorf("prompts: context: %w", err)
	}
	txt, err := execute(sp.Name, map[string]any{"Title": title, "TrackID": id, "Goal": goal, "CanGenerate": p.Has(auth.ScopeGenerate)})
	if err != nil {
		return Spec{}, nil, err
	}
	return sp, []Message{textMsg(txt), contextMsg(id, body)}, nil
}

func renderSession(ctx context.Context, sp Spec, args map[string]string, src Sources, now time.Time) (Spec, []Message, error) {
	since := now.Add(-SessionDefault)
	if raw := strings.TrimSpace(args["since"]); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return Spec{}, nil, invalid("since", "an RFC 3339 timestamp")
		}
		if t.Before(now.Add(-SessionMaxAge)) {
			return Spec{}, nil, invalid("since", "at most 30 days back")
		}
		since = t
	}
	sess, err := src.Session(ctx, since, sessionTracks)
	if err != nil {
		return Spec{}, nil, fmt.Errorf("prompts: session: %w", err)
	}
	js, err := json.MarshalIndent(sessionView(sess), "", "  ")
	if err != nil {
		return Spec{}, nil, fmt.Errorf("prompts: session: %w", err)
	}
	txt, err := execute(sp.Name, map[string]any{
		"Since": sess.Since.UTC().Format(time.RFC3339), "Until": sess.Until.UTC().Format(time.RFC3339), "SessionJSON": string(js)})
	if err != nil {
		return Spec{}, nil, err
	}
	return sp, []Message{textMsg(txt)}, nil
}

// sessionView keeps Tracks a JSON array (never null) whatever the source returned.
func sessionView(s listening.Session) listening.Session {
	if s.Tracks == nil {
		s.Tracks = []listening.SessionTrack{}
	}
	return s
}
