package main

import (
	"context"
	"encoding/json"
	"math"
	"path/filepath"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

type offlineRow struct {
	items     []string
	durations []float64
	revision  int
}

func offlineSet(t *testing.T, env map[string]string) offlineRow {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(env["MM_DATA_DIR"], "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var raw string
	var o offlineRow
	if err := st.R.QueryRow(`SELECT items_json, revision FROM playlists WHERE role = 'offline'`).Scan(&raw, &o.revision); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &o.items); err != nil {
		t.Fatal(err)
	}
	for _, id := range o.items {
		var d float64
		if err := st.R.QueryRow(`SELECT duration_s FROM tracks WHERE id = ?`, id).Scan(&d); err != nil {
			t.Fatal(err)
		}
		o.durations = append(o.durations, d)
	}
	return o
}

// TestCLI_SeedSynthetic_E2EM4a: after `seed-synthetic --profile e2e` the offline set holds 2 tracks (the 12 s timed
// one first, then the 6 s one) and one connector grant `E2E Connector` with read,write exists; a second run (the
// "already seeded" early return) changes nothing, and an offline set the operator has edited is left alone.
func TestCLI_SeedSynthetic_E2EM4a(t *testing.T) {
	t.Parallel()
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	env := seedEnv(t)
	if code, out, errs := do(t, env, "", "seed-synthetic", "--profile", "e2e"); code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errs)
	}
	o := offlineSet(t, env)
	if len(o.items) != 2 || math.Abs(o.durations[0]-12) > 1 || math.Abs(o.durations[1]-6) > 1 || o.durations[0] <= o.durations[1] {
		t.Fatalf("offline set = %v durations %v, want the 12 s track then the 6 s one", o.items, o.durations)
	}
	if o.revision < 2 {
		t.Errorf("revision %d: the set was written through the playlists service", o.revision)
	}
	if n := dbRow(t, env, `SELECT count(*) FROM connector_grants WHERE client_id='e2e-connector' AND subject='e2e-subject' AND label='E2E Connector' AND username='e2e' AND scopes='read,write' AND revoked_at IS NULL`); n != 1 {
		t.Errorf("connector grant rows = %d, want 1", n)
	}
	if n := dbRow(t, env, `SELECT count(*) FROM connector_grants`); n != 1 {
		t.Errorf("connector_grants holds %d rows, want 1", n)
	}

	// second run: nothing changes
	if code, _, errs := do(t, env, "", "seed-synthetic", "--profile", "e2e"); code != 0 {
		t.Fatalf("second run exit %d %s", code, errs)
	}
	again := offlineSet(t, env)
	if again.revision != o.revision || len(again.items) != 2 || again.items[0] != o.items[0] {
		t.Errorf("second run changed the offline set: %+v -> %+v", o, again)
	}
	if n := dbRow(t, env, `SELECT count(*) FROM connector_grants`); n != 1 {
		t.Errorf("second run: %d grants", n)
	}

	// an edited set (one track) is not overwritten by a later seed
	st, err := store.Open(context.Background(), filepath.Join(env["MM_DATA_DIR"], "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.Exec(`UPDATE playlists SET items_json = ?, items_count = 1, revision = revision + 1 WHERE role = 'offline'`, `["`+o.items[1]+`"]`); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	if code, _, errs := do(t, env, "", "seed-synthetic", "--profile", "e2e"); code != 0 {
		t.Fatalf("third run exit %d %s", code, errs)
	}
	if got := offlineSet(t, env); len(got.items) != 1 || got.items[0] != o.items[1] {
		t.Errorf("an edited offline set was overwritten: %v", got.items)
	}
}

// TestCLI_SeedSynthetic_E2EReferencesM4a: the e2e server is seeded with `--profile e2e --references` (the reference
// profile overrides the profile name); the M4a rows must be there all the same.
func TestCLI_SeedSynthetic_E2EReferencesM4a(t *testing.T) {
	t.Parallel()
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	env := seedEnv(t)
	if code, out, errs := do(t, env, "", "seed-synthetic", "--profile", "e2e", "--references"); code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errs)
	}
	if o := offlineSet(t, env); len(o.items) != 2 {
		t.Errorf("offline set = %v, want 2 tracks", o.items)
	}
	if n := dbRow(t, env, `SELECT count(*) FROM connector_grants WHERE label='E2E Connector'`); n != 1 {
		t.Errorf("connector grant rows = %d, want 1", n)
	}
	// the dev profile with references gets neither
	devEnv := seedEnv(t)
	if code, out, errs := do(t, devEnv, "", "seed-synthetic", "--profile", "dev", "--references"); code != 0 {
		t.Fatalf("dev exit %d\n%s\n%s", code, out, errs)
	}
	if n := dbRow(t, devEnv, `SELECT count(*) FROM connector_grants`); n != 0 {
		t.Errorf("dev profile seeded %d connector grants", n)
	}
}
