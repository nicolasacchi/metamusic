package librarytest

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Fixed ids of the M2a rows (UUIDv7-shaped, identical on every call).
const (
	CommentLunaNote   = "01900000-0000-7000-8000-000000000401" // general note, human
	CommentLunaFix    = "01900000-0000-7000-8000-000000000402" // fix at 5.0 s, section chorus (plan), human
	CommentLunaKeep   = "01900000-0000-7000-8000-000000000403" // keep at 1.0 s, authored by the device
	CommentShortTimed = "01900000-0000-7000-8000-000000000404" // timestamped, no section (untimed track)
	CommentShortHid   = "01900000-0000-7000-8000-000000000405" // hidden, authored by the agent
	PlaylistMix       = "01900000-0000-7000-8000-000000000501" // "fixture-mix", 3 tracks
	DevicePaired      = "01900000-0000-7000-8000-000000000601" // a paired device (devices table)
	BackupRunID       = "01900000-0000-7000-8000-000000000701"
	DraftVarianti     = "01900000-0000-7000-8000-000000000801" // varianti_import, mother TrackLuna
	DraftUpload1      = "01900000-0000-7000-8000-000000000802" // jsonl_upload
	DraftUpload2      = "01900000-0000-7000-8000-000000000803" // jsonl_upload

	// FixtureHuman and FixtureAgent are the principal keys of the human and
	// agent rows the fixture writes.
	FixtureHuman = "human:fixture"
	FixtureAgent = "agent:fixture-agent"
	FixtureDev   = "device:" + DevicePaired
)

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// addM2a inserts the M2a rows next to FixtureV2's.
func addM2a(tb testing.TB, st *store.Store, now time.Time, fx Fixtures) Fixtures {
	tb.Helper()
	ctx := context.Background()
	at := func(d time.Duration) string { return ts(now.Add(d)) }

	// One paired device (the playlist, comment and listen rows refer to it).
	mustExec(tb, st, `INSERT INTO devices(id, name, prefix, hmac_hash, scopes, user_agent, paired_by, created_at, last_seen_at, expires_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`, DevicePaired, "Fixture phone", "mmd_fixt", sha([]byte("fixture-device-token")), "read,write", "FixtureAgent/1.0",
		FixtureHuman, at(-48*time.Hour), at(-time.Hour), at(30*24*time.Hour))
	fx.Devices = append(fx.Devices, DevicePaired)

	// Ratings: Luna human 5 + agent 2; Citta human 3; TwinA agent 4 only.
	rate := func(track, source string, stars int, verdict any, kind, id, name string) {
		mustExec(tb, st, `INSERT INTO ratings(track_id, source, stars, verdict, principal_kind, principal_id, principal_name, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?)`, track, source, stars, verdict, kind, id, name, at(-3*time.Hour), at(-2*time.Hour))
	}
	rate(TrackLuna, "human", 5, "Synthetic keeper", "human", FixtureHuman, "fixture")
	rate(TrackLuna, "agent", 2, "Synthetic critic note", "agent", FixtureAgent, "fixture-agent")
	rate(TrackCitta, "human", 3, nil, "human", FixtureHuman, "fixture")
	rate(TrackTwinA, "agent", 4, nil, "agent", FixtureAgent, "fixture-agent")

	// Tags: TwinA human ballad+night, agent upbeat; TwinB inherits ballad (a direct row).
	tagIDs := map[string]int64{}
	for _, name := range []string{"ballad", "night", "upbeat"} {
		res, err := st.W.ExecContext(ctx, `INSERT INTO tags(name, created_at) VALUES (?,?)`, name, at(-5*time.Hour))
		check(tb, err)
		tagIDs[name], _ = res.LastInsertId()
	}
	tag := func(track, name, source, from, principal string) {
		var inh any
		if from != "" {
			inh = from
		}
		mustExec(tb, st, `INSERT INTO track_tags(track_id, tag_id, source, inherited_from, principal_id, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?)`, track, tagIDs[name], source, inh, principal, at(-4*time.Hour), at(-4*time.Hour))
	}
	tag(TrackTwinA, "ballad", "human", "", FixtureHuman)
	tag(TrackTwinA, "night", "human", "", FixtureHuman)
	tag(TrackTwinA, "upbeat", "agent", "", FixtureAgent)
	tag(TrackTwinB, "ballad", "inherited", TrackTwinA, "system:inherit")

	// Comments.
	type cm struct {
		id, track, body, kind, state string
		atSec                        any
		label, labelSrc, conf        any
		pkind, pid, pname            string
		hidden                       bool
		offset                       time.Duration
	}
	comments := []cm{
		{CommentLunaNote, TrackLuna, "Synthetic general note", "note", "open", nil, nil, nil, nil, "human", FixtureHuman, "fixture", false, -90 * time.Minute},
		{CommentLunaFix, TrackLuna, "Synthetic fix at the chorus", "fix", "open", 5.0, "chorus", "plan", 0.7, "human", FixtureHuman, "fixture", false, -80 * time.Minute},
		{CommentLunaKeep, TrackLuna, "Synthetic keep at the start", "keep", "open", 1.0, "verse", "plan", 0.7, "device", FixtureDev, "Fixture phone", false, -70 * time.Minute},
		{CommentShortTimed, TrackShort, "Synthetic timestamped note", "note", "open", 2.0, nil, nil, nil, "human", FixtureHuman, "fixture", false, -60 * time.Minute},
		{CommentShortHid, TrackShort, "Synthetic hidden note", "note", "open", nil, nil, nil, nil, "agent", FixtureAgent, "fixture-agent", true, -50 * time.Minute},
	}
	for _, c := range comments {
		var hat, hby any
		if c.hidden {
			hat, hby = at(c.offset+time.Minute), FixtureHuman
		}
		mustExec(tb, st, `INSERT INTO comments(id, track_id, body, at_second, section_label, section_source, section_confidence, kind, state, source,
			principal_kind, principal_id, principal_name, created_at, updated_at, hidden_at, hidden_by)
			VALUES (?,?,?,?,?,?,?,?,?,'web',?,?,?,?,?,?,?)`, c.id, c.track, c.body, c.atSec, c.label, c.labelSrc, c.conf, c.kind, c.state,
			c.pkind, c.pid, c.pname, at(c.offset), at(c.offset), hat, hby)
	}

	// Playlist "fixture-mix": three tracks.
	items, _ := json.Marshal([]string{TrackLuna, TrackCitta, TrackTwinA})
	mustExec(tb, st, `INSERT INTO playlists(id, name, description, items_json, items_count, revision, created_by, updated_by, created_at, updated_at)
		VALUES (?,?,?,?,3,1,?,?,?,?)`, PlaylistMix, "fixture-mix", "Synthetic playlist", string(items), FixtureHuman, FixtureHuman, at(-6*time.Hour), at(-6*time.Hour))

	// Listen events: Luna has 2 starts and 1 complete.
	ev := func(n int, kind string, from, to any, offset time.Duration) {
		mustExec(tb, st, `INSERT INTO listen_events(id, track_id, session_id, kind, from_ms, to_ms, format, device_id, source, principal_kind, principal_id, client_at, received_at)
			VALUES (?,?,?,?,?,?,'flac',?,'web','human',?,?,?)`, fmt.Sprintf("01900000-0000-7000-8000-%012x", 0x900+n), TrackLuna, "fixture-session-1", kind, from, to,
			DeviceID, FixtureHuman, at(offset), at(offset+time.Second))
	}
	ev(1, "start", 0, nil, -30*time.Minute)
	ev(2, "start", 0, nil, -20*time.Minute)
	ev(3, "complete", 0, 12000, -19*time.Minute)

	// One completed backup run.
	mustExec(tb, st, `INSERT INTO backup_runs(id, cause, principal_id, state, snapshot, started_at, heartbeat_at, finished_at, stats_json)
		VALUES (?,?,?,'completed',?,?,?,?,?)`, BackupRunID, "api", FixtureHuman, "20261006T100000Z", at(-2*time.Hour), at(-2*time.Hour), at(-110*time.Minute), `{"files":12}`)

	// Drafts of the new origins: one varianti_import with a source recipe and a
	// mother, two jsonl_upload drafts without a recipe.
	svc := library.New(st, nil, func() time.Time { return now })
	n := 0x7ff // the recipe takes 0x800, the draft 0x801 (DraftVarianti)
	svc.UseIDFunc(func() string { n++; return fmt.Sprintf("01900000-0000-7000-8000-%012x", n) })
	tx := begin(tb, st)
	p := mustParse(tb, reqJSON("fxv_001_v1", "synth pop, 100 bpm", "[Verse 1]\nline one\n[Chorus]\nline two", 207))
	rid, _, err := svc.InsertRecipe(ctx, tx, p, "import")
	check(tb, err)
	fx.Recipes = append(fx.Recipes, rid)
	_, err = svc.InsertRecipeSource(ctx, tx, rid, library.SourceRef{Path: "varianti/fxv.jsonl", Line: 1, Raw: p.Canonical, RawKind: "jsonl_line"}, RunID)
	check(tb, err)
	did, _, err := svc.InsertDraft(ctx, tx, library.NewDraft{Origin: "varianti_import", SourceRecipeID: rid, ParentTrackID: TrackLuna, GroupKey: "fxv_001", Label: "v1"})
	check(tb, err)
	if did != DraftVarianti {
		tb.Fatalf("librarytest: varianti draft id %s, want %s", did, DraftVarianti)
	}
	for i, id := range []string{DraftUpload1, DraftUpload2} {
		reqID := fmt.Sprintf("fxu_%03d", i+1)
		_, err := tx.ExecContext(ctx, `INSERT INTO recipe_drafts(id, origin, group_key, label, request_id, request_json, status, created_by, updated_by, created_at, updated_at)
			VALUES (?,'jsonl_upload',NULL,?,?,?,'backlog',?,?,?,?)`, id, fmt.Sprintf("u%d", i+1), reqID,
			reqJSON(reqID, "indie rock, 110 bpm", "[Verse 1]\nline one\n[Chorus]\nline two", int64(300+i)), FixtureHuman, FixtureHuman, at(-30*time.Minute), at(-30*time.Minute))
		check(tb, err)
	}
	check(tb, tx.Commit())
	fx.Drafts = append(fx.Drafts, DraftVarianti, DraftUpload1, DraftUpload2)
	return fx
}
