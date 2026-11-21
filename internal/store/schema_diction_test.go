package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// dictionMigration is the position of 0021_diction.sql (M5b EARS-D1, C344.1); renumber it with the file when main moves.
const dictionMigration = 21

// TestMigration_Diction: a database at the previous schema upgrades with every existing table untouched, the two tables carry
// their checks, no mark or override can be physically deleted, a mark's identity is frozen, and user_version advances.
func TestMigration_Diction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	prev, p := openAt(t, dictionMigration-1)
	librarytest.FixtureM5(t, prev, librarytest.FixedNow)
	if n := scalar(t, prev.R, `SELECT COUNT(*) FROM sqlite_master WHERE name IN ('diction_marks','lexicon_overrides')`); n != "0" {
		t.Fatalf("schema %d already has the diction tables", dictionMigration-1)
	}
	before := scalar(t, prev.R, `SELECT COUNT(*) || ':' || COALESCE(MAX(id), '') FROM tracks`)
	prev.W.Close()
	s, err := store.Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, _ := s.UserVersion(ctx); v != store.MigrationCount() || v < dictionMigration {
		t.Fatalf("version = %d, migrations = %d", v, store.MigrationCount())
	}
	if got := scalar(t, s.R, `SELECT COUNT(*) || ':' || COALESCE(MAX(id), '') FROM tracks`); got != before {
		t.Errorf("tracks changed by the upgrade: %q -> %q", before, got)
	}
	track := scalar(t, s.R, `SELECT id FROM tracks LIMIT 1`)
	const now = "2026-11-21T10:00:00.000Z"
	mark := func(section, line any, source, mk string) error {
		_, err := s.W.ExecContext(ctx, `INSERT INTO diction_marks(track_id, section_idx, line_idx, source, mark, marked_by, updated_at) VALUES (?, ?, ?, ?, ?, 'human:alice', ?)`,
			track, section, line, source, mk, now)
		return err
	}
	if err := mark(0, 0, "human", "clear"); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"negative section": mark(-1, 0, "human", "clear"),
		"negative line":    mark(0, -1, "human", "clear"),
		"bad source":       mark(0, 1, "robot", "clear"),
		"bad mark":         mark(0, 1, "human", "loud"),
		"duplicate key":    mark(0, 0, "human", "unclear"),
	} {
		if err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if _, err := s.W.ExecContext(ctx, `INSERT INTO diction_marks(track_id, section_idx, line_idx, source, mark, marked_by, updated_at) VALUES ('no-such-track', 0, 0, 'human', 'clear', 'x', ?)`, now); err == nil {
		t.Error("a mark of an unknown track was accepted")
	}
	if _, err := s.W.ExecContext(ctx, `UPDATE diction_marks SET mark = 'unclear', updated_at = ? WHERE track_id = ?`, now, track); err != nil {
		t.Errorf("the mark and its time must be updatable: %v", err)
	}
	for _, col := range []string{"track_id = 'x'", "section_idx = 9", "line_idx = 9", "source = 'critic'"} {
		if _, err := s.W.ExecContext(ctx, `UPDATE diction_marks SET `+col+` WHERE track_id = ?`, track); err == nil || !strings.Contains(err.Error(), "frozen column") {
			t.Errorf("update of %s: %v, want frozen column", col, err)
		}
	}
	if _, err := s.W.ExecContext(ctx, `DELETE FROM diction_marks`); err == nil || !strings.Contains(err.Error(), "no physical delete") {
		t.Errorf("delete of a mark: %v", err)
	}

	over := func(lang, word, status, note any) error {
		_, err := s.W.ExecContext(ctx, `INSERT INTO lexicon_overrides(lang, word_fold, status, note, updated_by, updated_at) VALUES (?, ?, ?, ?, 'human:alice', ?)`,
			lang, word, status, note, now)
		return err
	}
	if err := over("it", "chiave", "ignored", "a name"); err != nil {
		t.Fatal(err)
	}
	if err := over("und", "mare", "active", nil); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"lang too short": over("i", "x", "active", nil),
		"lang too long":  over("ital", "x", "active", nil),
		"empty word":     over("it", "", "active", nil),
		"long word":      over("it", strings.Repeat("a", 65), "active", nil),
		"bad status":     over("it", "x", "banned", nil),
		"empty note":     over("it", "x", "active", ""),
		"long note":      over("it", "x", "active", strings.Repeat("n", 501)),
		"duplicate":      over("it", "chiave", "active", nil),
	} {
		if err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if _, err := s.W.ExecContext(ctx, `UPDATE lexicon_overrides SET status = 'active' WHERE word_fold = 'chiave'`); err != nil {
		t.Errorf("an override must be updatable: %v", err)
	}
	if _, err := s.W.ExecContext(ctx, `DELETE FROM lexicon_overrides`); err == nil || !strings.Contains(err.Error(), "no physical delete") {
		t.Errorf("delete of an override: %v", err)
	}
}
