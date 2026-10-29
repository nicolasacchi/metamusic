package acceptance_test

import (
	"context"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// l2Rig is the settled L2 library (FixtureL2 on the L1 and M3 base: Records R1 and R2, four Crates, Picks, loudness) behind the
// real server. The Reconciler, the Syncer and the tape runner are idle: tests call Rig.Settle and Rig.Tapes.RunOne.
func l2Rig(t *testing.T, o servertest.Options) *servertest.Rig {
	t.Helper()
	if testing.Short() {
		t.Skip("heavy acceptance test")
	}
	o.Fixture, o.M2c, o.M3, o.L1, o.L2 = true, true, true, true, true
	r := servertest.New(t, o)
	r.Settle(t)
	return r
}

// l2ID returns the id of the row of table with the given title (name for crates).
func l2ID(t *testing.T, r *servertest.Rig, table, title string) string {
	t.Helper()
	col := "title"
	if table == "crates" {
		col = "name"
	}
	var id string
	if err := r.Store.R.QueryRow(`SELECT id FROM `+table+` WHERE `+col+` = ?`, title).Scan(&id); err != nil {
		t.Fatalf("%s %q: %v", table, title, err)
	}
	return id
}

const (
	l2R1 = "Synthetic Record One"
	l2R2 = "Synthetic Record Two"
)

// l2Take is the id of a fixture Take by its synthetic label ("B1" for "Synthetic Take B1").
func l2Take(t *testing.T, r *servertest.Rig, label string) string {
	t.Helper()
	return l2ID(t, r, "tracks", "Synthetic Take "+label)
}

// l2Song is the id of a fixture Song by its label ("S3").
func l2Song(t *testing.T, r *servertest.Rig, label string) string {
	t.Helper()
	return l2ID(t, r, "songs", "Synthetic Song "+label)
}

// l2Changes returns the changelog rows of a Record oldest first.
func l2Changes(t *testing.T, c *servertest.Client, rid string) []map[string]any {
	t.Helper()
	page := obj(c.OK(t, "GET", "/api/records/"+rid+"/changes?order=asc&limit=200", nil))
	var out []map[string]any
	for _, it := range page["items"].([]any) {
		out = append(out, obj(it))
	}
	return out
}

// l2Slots returns the slots of a Record view in order.
func l2Slots(t *testing.T, c *servertest.Client, rid string) []map[string]any {
	t.Helper()
	v := obj(c.OK(t, "GET", "/api/records/"+rid+"?include=changes", nil))
	var out []map[string]any
	for _, it := range v["slots"].([]any) {
		out = append(out, obj(it))
	}
	return out
}

func takeTitleOf(slot map[string]any) string {
	tk := obj(slot["take"])
	s, _ := tk["title"].(string)
	return s
}

// l2Verify fails the test when records.Verify finds anything.
func l2Verify(t *testing.T, r *servertest.Rig) {
	t.Helper()
	an, err := r.Services.Records.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range an {
		if a.Severity == "error" || !strings.HasPrefix(a.Code, "published_") && !strings.HasPrefix(a.Code, "undecided") {
			t.Errorf("records.Verify: %+v", a)
		}
	}
}
