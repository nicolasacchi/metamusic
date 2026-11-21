package servertest_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// TestProviderStatus_WorkerCaps: get_provider_status carries the gate of each backend (C322.6): every capability for a fake with no
// row, then exactly what OpenCaps wrote; an Editor and a Listener get nothing of it (the whole operation is Owner-only).
func TestProviderStatus_WorkerCaps(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, Backend: "fake"})
	owner := r.Human(t)
	read := func(c *servertest.Client) (status int, w struct {
		ImageDigest *string  `json:"image_digest"`
		Caps        []string `json:"caps"`
		ChangedAt   *string  `json:"changed_at"`
		Via         string   `json:"via"`
	}) {
		resp := c.Do(t, "GET", "/api/provider", nil)
		if resp.Status != 200 {
			return resp.Status, w
		}
		var body struct {
			Worker   *json.RawMessage `json:"worker"`
			Backends []struct {
				Worker json.RawMessage `json:"worker"`
			} `json:"backends"`
		}
		if err := json.Unmarshal(resp.Body, &body); err != nil || body.Worker == nil {
			t.Fatalf("no worker block: %v %s", err, resp.Body)
		}
		if len(body.Backends) == 0 || string(body.Backends[0].Worker) != string(*body.Worker) {
			t.Fatalf("backends[0].worker differs from the top-level block: %s", resp.Body)
		}
		if err := json.Unmarshal(*body.Worker, &w); err != nil {
			t.Fatal(err)
		}
		return resp.Status, w
	}
	_, w := read(owner)
	if !slices.Equal(w.Caps, provider.Caps) || w.Via != "none" || w.ImageDigest != nil || w.ChangedAt != nil {
		t.Fatalf("fake without a row: %+v", w)
	}
	r.OpenCaps(t, provider.BackendFake, "v2")
	_, w = read(owner)
	if !slices.Equal(w.Caps, []string{"v2"}) || w.Via != "cli" || w.ImageDigest == nil || w.ChangedAt == nil {
		t.Fatalf("after OpenCaps: %+v", w)
	}
	r.OpenCaps(t, provider.BackendFake)
	if _, w = read(owner); w.Caps == nil || len(w.Caps) != 0 {
		t.Fatalf("closed gate: %+v", w)
	}
	for name, role := range map[string]config.Role{servertest.UserEditor: config.RoleEditor, servertest.UserListener: config.RoleListener} {
		if status, _ := read(r.User(t, name, role)); status != 403 {
			t.Errorf("%s read provider status: %d", role, status)
		}
	}
}
