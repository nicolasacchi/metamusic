// Package prompts is the prompt catalog and renderer behind the get_prompt operation (C112.2). Prompts are
// presentations of a registry operation; nothing here is reachable except through the gate.
//
// The exported API is frozen by M4a-B2; the rendering lives in render.go and text/*.tmpl.
package prompts

import (
	"context"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/listening"
)

// Arg is one prompt argument.
type Arg struct {
	Name, Description string
	Required          bool
}

// Spec describes one prompt.
type Spec struct {
	Name, Title, Description, Version string
	Args                              []Arg
}

// Catalog lists every prompt, in presentation order.
var Catalog = []Spec{
	{Name: "critique_track", Title: "Critique a take", Description: "Review a take from its context bundle and record fixes as comments", Version: "1", Args: []Arg{{Name: "track_id", Description: "the take to review", Required: true}}},
	{Name: "write_variant", Title: "Write a variant", Description: "Propose one bounded change to a take and save it as a draft", Version: "1", Args: []Arg{{Name: "track_id", Description: "the take to vary", Required: true}, {Name: "goal", Description: "what the variant should achieve"}}},
	{Name: "session_review", Title: "Review a listening session", Description: "Summarise what the human listened to, finished and skipped", Version: "1", Args: []Arg{{Name: "since", Description: "RFC 3339 start; default 12 hours ago, at most 30 days"}}},
}

// Embedded is a resource carried inside a message.
type Embedded struct{ URI, MIMEType, Text string }

// Content is one message part: Type "text" (Text) or "resource" (Resource).
type Content struct {
	Type     string
	Text     string
	Resource *Embedded
}

// Message is one rendered prompt message.
type Message struct {
	Role    string // "user"
	Content Content
}

// Sources are the data a prompt needs; the api layer implements it over the services.
type Sources interface {
	ContextJSON(ctx context.Context, p auth.Principal, trackID string) ([]byte, error) // trackctx bundle, REST bytes
	Session(ctx context.Context, since time.Time, limit int) (listening.Session, error)
	Title(ctx context.Context, trackID string) (string, error)
	RecipeKnown(ctx context.Context, trackID string) (bool, error) // library.Service.RecipeKnown (D-M4a-25)
}
