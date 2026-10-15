// Package prompts is the prompt catalog and renderer behind the get_prompt operation (C112.2). Prompts are
// presentations of a registry operation; nothing here is reachable except through the gate.
//
// The exported API is frozen by M4a-B2; Render is STUB(M4a-A1) until the agent lane implements it.
package prompts

import (
	"context"
	"errors"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/listening"
)

// ErrNotImplemented marks a STUB(M4a-A1) body.
var ErrNotImplemented = errors.New("prompts: not implemented")

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
	{Name: "critique_track", Title: "Critique a take", Version: "1", Args: []Arg{{Name: "track_id", Required: true}}},
	{Name: "write_variant", Title: "Write a variant", Version: "1", Args: []Arg{{Name: "track_id", Required: true}, {Name: "goal"}}},
	{Name: "session_review", Title: "Review a listening session", Version: "1", Args: []Arg{{Name: "since"}}},
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

// Render builds the messages of prompt name for p. STUB(M4a-A1)
func Render(context.Context, auth.Principal, string, map[string]string, Sources, time.Time) (Spec, []Message, error) {
	return Spec{}, nil, ErrNotImplemented
}
