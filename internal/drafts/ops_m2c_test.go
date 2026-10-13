package drafts_test

import (
	"testing"

	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// TestDraftOps_RecipeUnknownREST: the REST face answers the 422 RECIPE_UNKNOWN problem for a reference
// parent and for a reference track's request id.
func TestDraftOps_RecipeUnknownREST(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, Files: true, M2c: true})
	human := r.Human(t)

	resp := human.Do(t, "POST", "/api/drafts", map[string]any{"request": goodRequest("ru-a"), "parent_track_id": lt.TrackRefFlac}, "Idempotency-Key", "ru1")
	p := m(t, resp.JSON(t))
	if resp.Status != 422 || p["code"] != "RECIPE_UNKNOWN" || m(t, p["details"])["field"] != "parent_track_id" {
		t.Errorf("parent: %d %s", resp.Status, resp.Body)
	}
	resp = human.Do(t, "POST", "/api/drafts", map[string]any{"request": goodRequest("ru-b"), "parent_track_id": lt.TrackLuna}, "Idempotency-Key", "ru2")
	if resp.Status != 201 {
		t.Errorf("known parent: %d %s", resp.Status, resp.Body)
	}
}
