// Package subsonictest is the test-only Subsonic client kit (C117.4): it signs requests like the three reference
// clients, parses both envelopes and replays the synthetic client fixtures. It is listed in
// TestNoTestHelpersInBinary and must never be linked into the binary.
//
// The exported API is frozen by M4a-B2 (servertest.Rig.Subsonic returns *Client). Client.Call lives in client.go
// (M4a-S1); NewPlaceholders and Replay live in placeholders.go and replay.go (M4a-S4).
package subsonictest

import (
	"net/http"
	"time"
)

// ClientOpts describe a client created through create_subsonic_client as the human.
type ClientOpts struct {
	Name, Username string
	Write          bool
	Format         string // default_format: "raw" | "mp3"
}

// Client signs and sends requests for one Subsonic client.
type Client struct {
	BaseURL  string
	Username string
	Password string // app password; "" when the client has none
	APIKey   string // mms_...; "" when the client has none
	HTTP     *http.Client
	// Clock advances the rig clock for fixture steps with an "advance" field (C117.5); servertest.Rig.Clock satisfies it.
	// Replay fails the test when a fixture needs it and it is nil.
	Clock interface{ Advance(time.Duration) }
}

// CallOpts select the request shape.
type CallOpts struct {
	JSON, View, POST, APIKey bool
	Salt                     string // "" => a fresh random salt
	Range                    string
	UserAgent                string
}

// Response is one parsed answer.
type Response struct {
	HTTP   int
	Header http.Header
	Body   []byte
	Status string // "ok" | "failed" ("" for a binary answer)
	Code   int    // the protocol error code (0 when ok)
	Doc    map[string]any
}
