package attention_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/attention"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestMain(m *testing.M) {
	store.UseMigratedTemplateForTests()
	os.Exit(m.Run())
}

func newEnv(t *testing.T) (*attention.Service, *store.Store, lt.M5Info) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	info := lt.FixtureM5(t, st, lt.FixedNow)
	return attention.New(attention.Options{Store: st}), st, info
}

func exec(t *testing.T, st *store.Store, q string, args ...any) {
	t.Helper()
	if _, err := st.W.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func count(t *testing.T, st *store.Store, q string) (n int) {
	t.Helper()
	if err := st.R.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

var evN int

// addEvent inserts one listen event row directly (the write path is the listening service's, not this lane's).
func addEvent(t *testing.T, st *store.Store, track, session, kind string, from, to *int64, pk string) {
	t.Helper()
	evN++
	exec(t, st, `INSERT INTO listen_events(id, track_id, session_id, kind, from_ms, to_ms, source, principal_kind, principal_id, client_at, received_at)
		VALUES (?,?,?,?,?,?,'web',?,'p','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xa5000+evN), track, session, kind, from, to, pk)
}

func ms(n int64) *int64 { return &n }

// TestService_Heat_Fixture: the fixture's G0 has three human sessions (one replaying 10-30 s, one skipping at 5 s) and a
// Subsonic scrobble of a device principal; the heat counts all four sessions, and agents and diagnostics are left out.
func TestService_Heat_Fixture(t *testing.T) {
	t.Parallel()
	svc, st, info := newEnv(t)
	g0 := info.Lineage[0]
	addEvent(t, st, g0, "agent-session", "segment", ms(0), ms(120_000), "agent") // never counts
	addEvent(t, st, g0, "agent-session", "start", nil, nil, "agent")
	addEvent(t, st, g0, "human-diag-1", "error", nil, nil, "human") // diagnostics never count
	h, err := svc.Heat(context.Background(), g0)
	if err != nil {
		t.Fatal(err)
	}
	if h.TrackID != g0 || h.Bins != attention.Bins || h.DurationS != 120 || h.BinS != 1 {
		t.Fatalf("shape: %+v", h)
	}
	if h.Sessions != 4 || h.Plays != 4 {
		t.Errorf("sessions %d plays %d, want 4 and 4 (three human sessions and one device)", h.Sessions, h.Plays)
	}
	if h.Heard[20] != 3 || h.Replays[20] != 1 || h.Heard[42] != 2 || h.Heard[100] != 0 {
		t.Errorf("heard[20]=%d replays[20]=%d heard[42]=%d heard[100]=%d", h.Heard[20], h.Replays[20], h.Heard[42], h.Heard[100])
	}
	if !reflect.DeepEqual(h.Cold, []int{5}) || h.Skips[5] != 1 {
		t.Errorf("cold %v skips[5] %d", h.Cold, h.Skips[5])
	}
	if len(h.Hotspots) != 0 {
		t.Errorf("hotspots = %+v, want none (one replay is below the span rule)", h.Hotspots)
	}
}

// TestService_Heat_HotspotLabel: more replays make a hotspot, labelled with the timed section at its start.
func TestService_Heat_HotspotLabel(t *testing.T) {
	t.Parallel()
	svc, st, info := newEnv(t)
	g0 := info.Lineage[0]
	for range 3 { // one session hears 0-20 s three times: replays 2 in every bin of the span
		addEvent(t, st, g0, "replay-session", "segment", ms(0), ms(20_000), "human")
	}
	h, err := svc.Heat(context.Background(), g0)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Hotspots) != 1 {
		t.Fatalf("hotspots = %+v", h.Hotspots)
	}
	hs := h.Hotspots[0]
	if hs.FromS != 0 || hs.ToS != 20 || hs.Score != 3 || hs.Label == nil || *hs.Label != "verse 1" {
		t.Errorf("hotspot = %+v (label %v), want 0-20 s score 3 labelled verse 1", hs, hs.Label)
	}
	// a take without timed sections leaves the label null
	g2 := info.Lineage[2]
	for range 3 {
		addEvent(t, st, g2, "replay-session", "segment", ms(0), ms(20_000), "human")
	}
	h2, err := svc.Heat(context.Background(), g2)
	if err != nil {
		t.Fatal(err)
	}
	if len(h2.Hotspots) != 1 || h2.Hotspots[0].Label != nil {
		t.Errorf("untimed hotspot = %+v", h2.Hotspots)
	}
	hot, err := svc.Hotspots(context.Background(), g0)
	if err != nil || !reflect.DeepEqual(hot, h.Hotspots) {
		t.Errorf("Hotspots = %+v, %v", hot, err)
	}
}

// TestService_Heat_NoEventsAndUnknownTrack: a take nobody listened to has an empty strip; an unknown track is not found.
func TestService_Heat_NoEventsAndUnknownTrack(t *testing.T) {
	t.Parallel()
	svc, _, info := newEnv(t)
	h, err := svc.Heat(context.Background(), info.Lineage[1])
	if err != nil {
		t.Fatal(err)
	}
	if h.Sessions != 0 || h.Plays != 0 || h.CompletionRate != nil || h.Bins != attention.Bins || len(h.Hotspots) != 0 || len(h.Cold) != 0 {
		t.Errorf("empty heat = %+v", h)
	}
	if _, err := svc.Heat(context.Background(), "01900000-0000-7000-8000-00000000dead"); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("unknown track: %v", err)
	}
}

// TestHeat_GETNeverWrites: reading the heat (and the other read helpers) changes no row of any table.
func TestHeat_GETNeverWrites(t *testing.T) {
	t.Parallel()
	svc, st, info := newEnv(t)
	tables := []string{"listen_events", "audit_log", "comments", "patches", "jobs", "tracks"}
	before := map[string]int{}
	for _, tb := range tables {
		before[tb] = count(t, st, "SELECT count(*) FROM "+tb)
	}
	var ver int
	if err := st.R.QueryRow(`PRAGMA data_version`).Scan(&ver); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for range 3 {
		if _, err := svc.Heat(ctx, info.Lineage[0]); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Hotspots(ctx, info.Lineage[0]); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Timing(ctx, info.Lineage[0]); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.InFlightPatches(ctx, []string{info.Comments["fix_chorus"]}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tb := range tables {
		if n := count(t, st, "SELECT count(*) FROM "+tb); n != before[tb] {
			t.Errorf("%s: %d rows before, %d after", tb, before[tb], n)
		}
	}
}

// insertPatchWithJob stores a patch addressing comments and one job of it in the given state (the job rides in an existing
// batch of the M3 fixture).
func insertPatchWithJob(t *testing.T, st *store.Store, n int, base string, comments []string, state string) string {
	t.Helper()
	pid := fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xa6000+n)
	exec(t, st, `INSERT INTO patches(id, base_track_id, base_recipe_hash, operator, ops_json, compiled_json, scope_json, diff_json, rationale, principal_kind, principal_id, principal_name, created_at)
		VALUES (?,?,'h','style_edit','[]','{}','{}','{}','r','agent','tok','claude',?)`, pid, base, fmt.Sprintf("2026-01-01T00:00:%02dZ", n))
	for _, c := range comments {
		exec(t, st, `INSERT INTO patch_comments(patch_id, comment_id) VALUES (?,?)`, pid, c)
	}
	var batch, recipe string
	if err := st.R.QueryRow(`SELECT b.id, j.recipe_id FROM jobs j JOIN batches b ON b.id = j.batch_id ORDER BY j.id LIMIT 1`).Scan(&batch, &recipe); err != nil {
		t.Fatal(err)
	}
	fin := any(nil)
	if state == "failed" || state == "completed" || state == "cancelled" || state == "expired" {
		fin = "2026-01-01T00:00:00Z"
	}
	exec(t, st, `PRAGMA foreign_keys = ON`)
	exec(t, st, `INSERT INTO jobs(id, batch_id, take_index, kind, engine, backend, recipe_id, operator, mutation_json, title, expected_audio_s, expected_s, timeout_s,
		expected_micro_usd, reserved_micro_usd, state, next_attempt_at, created_at, updated_at, finished_at, patch_id)
		VALUES (?,?,?,'generate','yue2','fake',?,'style_edit','{}','T',100,50,120,10,20,'queued','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z',NULL,?)`,
		fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xa7000+n), batch, 50+n, recipe, pid)
	if state != "queued" {
		exec(t, st, `UPDATE jobs SET state = ?, finished_at = ? WHERE id = ?`, state, fin, fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xa7000+n))
	}
	return pid
}

// TestService_InFlightPatches: a patch is in flight while one of its jobs has not reached a terminal state; the map is keyed
// by comment, oldest patch first.
func TestService_InFlightPatches(t *testing.T) {
	t.Parallel()
	svc, st, info := newEnv(t)
	c := info.Comments
	g0 := info.Lineage[0]
	p1 := insertPatchWithJob(t, st, 1, g0, []string{c["fix_chorus"], c["fix_verse"]}, "queued")
	p2 := insertPatchWithJob(t, st, 2, g0, []string{c["fix_chorus"]}, "queued")
	insertPatchWithJob(t, st, 3, g0, []string{c["fix_chorus"], c["note"]}, "failed") // ended: not in flight
	got, err := svc.InFlightPatches(context.Background(), []string{c["fix_chorus"], c["fix_verse"], c["note"], c["keep"]})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{c["fix_chorus"]: {p1, p2}, c["fix_verse"]: {p1}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("in flight = %v, want %v", got, want)
	}
	if got, err := svc.InFlightPatches(context.Background(), nil); err != nil || len(got) != 0 {
		t.Errorf("no ids: %v %v", got, err)
	}
}
