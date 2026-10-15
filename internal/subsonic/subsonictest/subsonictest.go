// Package subsonictest is the test-only Subsonic client kit (C117.4): it signs requests like the three reference
// clients, parses both envelopes and replays the synthetic client fixtures. It is listed in
// TestNoTestHelpersInBinary and must never be linked into the binary.
//
// The exported API is frozen by M4a-B2 (servertest.Rig.Subsonic returns *Client). Client.Call lives in client.go
// (M4a-S1); NewPlaceholders and Replay are STUB(M4a-S4) and skip the calling test by name.
package subsonictest

import (
	"net/http"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/store"
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

// PlaceholderSet resolves the {{song:N}}, {{album:N}}, {{mid}}, {{bytes}}, {{user}}, {{text}} and {{query}}
// placeholders of a client fixture deterministically.
type PlaceholderSet map[string]string

// NewPlaceholders resolves the placeholders against the library in st (songs and albums in created_at, id order).
// (The plan names this function Placeholders, which collides with the type of the same name.) STUB(M4a-S4)
func NewPlaceholders(tb testing.TB, _ *store.Store) PlaceholderSet {
	tb.Helper()
	tb.Skip("M4a-S4: subsonictest.NewPlaceholders not implemented yet")
	return nil
}

// Replay runs one client fixture file (internal/subsonic/testdata/clients/*.jsonl). STUB(M4a-S4)
func Replay(tb testing.TB, _ *Client, _ PlaceholderSet, _ string) {
	tb.Helper()
	tb.Skip("M4a-S4: subsonictest.Replay not implemented yet")
}
