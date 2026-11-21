package store_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// recoverMigration is the position of 0024_recover.sql (M5b EARS-R1, C343.1); renumber it with the file when main moves.
const recoverMigration = 24

// TestMigration_Recover: a database migrated from 0023 upgrades additively (every existing table keeps its rows, the new table is
// empty) and user_version advances to the newest migration.
func TestMigration_Recover(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	prev, p := openAt(t, recoverMigration-1)
	librarytest.FixtureM5(t, prev, librarytest.FixedNow)
	if n := scalar(t, prev.R, `SELECT COUNT(*) FROM sqlite_master WHERE name LIKE 'draft_recoveries%'`); n != "0" {
		t.Fatalf("schema %d already has draft_recoveries", recoverMigration-1)
	}
	before := dbSnapshot(t, prev.R)
	if err := store.MigrateTo(ctx, prev.W, recoverMigration); err != nil {
		t.Fatal(err)
	}
	if v, _ := prev.UserVersion(ctx); v != recoverMigration {
		t.Fatalf("user_version = %d, want %d", v, recoverMigration)
	}
	after := dbSnapshot(t, prev.R)
	if n := len(after["draft_recoveries"]); n != 0 {
		t.Fatalf("draft_recoveries has %d rows after the upgrade", n)
	}
	delete(after, "draft_recoveries")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("an existing table changed")
	}
	prev.W.Close()
	s, err := store.Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, _ := s.UserVersion(ctx); v != store.MigrationCount() || v < recoverMigration {
		t.Fatalf("version = %d, migrations = %d", v, store.MigrationCount())
	}
}

// TestDraftRecoveries_Triggers: the table carries its checks, only a reference track can be recovered, a row is immutable and is
// never physically deleted.
func TestDraftRecoveries_Triggers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := openAt(t, recoverMigration)
	librarytest.FixtureM5(t, s, librarytest.FixedNow)
	ref := scalar(t, s.R, `SELECT id FROM tracks WHERE import_rule = 'reference' LIMIT 1`)
	own := scalar(t, s.R, `SELECT id FROM tracks WHERE import_rule <> 'reference' LIMIT 1`)
	draft := scalar(t, s.R, `SELECT id FROM recipe_drafts LIMIT 1`)
	if ref == "" || own == "" || draft == "" {
		t.Fatalf("fixture lacks a reference (%q), a known take (%q) or a draft (%q)", ref, own, draft)
	}
	sha := strings.Repeat("a", 64)
	const now = "2026-11-21T10:00:00Z"
	ins := func(draftID, track, shaV string, lang any, words int, conf float64, style string, rationale any) error {
		_, err := s.W.ExecContext(ctx, `INSERT INTO draft_recoveries(draft_id, track_id, analysis_sha256, language, words, confidence, style_source, rationale, created_by, created_at)
			VALUES (?,?,?,?,?,?,?,?,'human:alice',?)`, draftID, track, shaV, lang, words, conf, style, rationale, now)
		return err
	}
	if err := ins(draft, own, sha, "it", 10, 0.5, "caller", nil); err == nil || !strings.Contains(err.Error(), "not a reference track") {
		t.Errorf("a known take was accepted: %v", err)
	}
	if err := ins(draft, "no-such-track", sha, "it", 10, 0.5, "caller", nil); err == nil || !strings.Contains(err.Error(), "not a reference track") {
		t.Errorf("an unknown track was accepted: %v", err)
	}
	for name, err := range map[string]error{
		"short sha":        ins(draft, ref, "abc", "it", 10, 0.5, "caller", nil),
		"language 1 char":  ins(draft, ref, sha, "i", 10, 0.5, "caller", nil),
		"language 4 chars": ins(draft, ref, sha, "ital", 10, 0.5, "caller", nil),
		"zero words":       ins(draft, ref, sha, "it", 0, 0.5, "caller", nil),
		"confidence > 1":   ins(draft, ref, sha, "it", 1, 1.01, "caller", nil),
		"confidence < 0":   ins(draft, ref, sha, "it", 1, -0.1, "caller", nil),
		"bad style source": ins(draft, ref, sha, "it", 1, 0.5, "guess", nil),
		"empty rationale":  ins(draft, ref, sha, "it", 1, 0.5, "caller", ""),
		"long rationale":   ins(draft, ref, sha, "it", 1, 0.5, "caller", strings.Repeat("r", 1001)),
		"unknown draft":    ins("no-such-draft", ref, sha, "it", 1, 0.5, "caller", nil),
	} {
		if err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if err := ins(draft, ref, sha, nil, 1, 0, "placeholder", strings.Repeat("r", 1000)); err != nil {
		t.Fatalf("a valid row (no language, the longest rationale): %v", err)
	}
	if err := ins(draft, ref, sha, "it", 1, 0.5, "caller", nil); err == nil {
		t.Error("a second recovery of the same draft was accepted")
	}
	if _, err := s.W.ExecContext(ctx, `UPDATE draft_recoveries SET words = 2`); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Errorf("update: %v, want immutable", err)
	}
	if _, err := s.W.ExecContext(ctx, `DELETE FROM draft_recoveries`); err == nil || !strings.Contains(err.Error(), "no physical delete") {
		t.Errorf("delete: %v, want no physical delete", err)
	}
	if n := scalar(t, s.R, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'draft_recoveries_track'`); n != "1" {
		t.Error("the track index is missing")
	}
}
