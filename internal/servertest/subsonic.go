package servertest

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/subsonic/subsonictest"
)

// Subsonic creates a Subsonic client through create_subsonic_client as the human and returns a kit client for it
// (C117.1). The client carries both an API key and an app password; ClientOpts.Write false makes it read-only.
func (r *Rig) Subsonic(tb testing.TB, o subsonictest.ClientOpts) *subsonictest.Client {
	tb.Helper()
	name := o.Name
	if name == "" {
		name = "Test app"
	}
	body := map[string]any{"name": name, "write": o.Write}
	if o.Username != "" {
		body["username"] = o.Username
	}
	if o.Format != "" {
		body["default_format"] = o.Format
	}
	k := make([]byte, 8)
	if _, err := rand.Read(k); err != nil {
		tb.Fatal(err)
	}
	var out struct {
		APIKey   *string `json:"api_key"`
		Password *string `json:"password"`
		Username string  `json:"username"`
	}
	resp := r.Human(tb).Do(tb, "POST", "/api/subsonic-clients", body, "Idempotency-Key", "rig-subsonic-"+hex.EncodeToString(k))
	if resp.Status != http.StatusCreated {
		tb.Fatalf("servertest: create_subsonic_client: status %d: %s", resp.Status, resp.Body)
	}
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		tb.Fatalf("servertest: create_subsonic_client: %v", err)
	}
	c := &subsonictest.Client{BaseURL: r.URL, Username: out.Username, HTTP: &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if out.APIKey != nil {
		c.APIKey = *out.APIKey
	}
	if out.Password != nil {
		c.Password = *out.Password
	}
	return c
}
