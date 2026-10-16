package muses_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/muses"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestMuses_EnsureReferenceTx(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	n := 0
	newID := func() string { n++; return fmt.Sprintf("01900000-0000-7000-8000-%012x", n) }
	ensure := func(title string, artist *string) (string, bool, error) {
		tx, err := st.W.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		id, created, err := muses.EnsureReferenceTx(ctx, tx, now, newID, title, artist, "system:provenance")
		if err != nil {
			return "", false, err
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		return id, created, nil
	}
	a := "Ärtist"
	id1, created, err := ensure("Mixed  Title", &a)
	if err != nil || !created {
		t.Fatalf("first call: %v %v", created, err)
	}
	b := "artist"
	id2, created, err := ensure("mixed title", &b)
	if err != nil || created || id2 != id1 {
		t.Errorf("the folded variant must return the same Muse: %s %v %v", id2, created, err)
	}
	id3, created, err := ensure("Mixed Title", nil)
	if err != nil || !created || id3 == id1 {
		t.Errorf("no artist is not the same Muse as an artist: %s %v %v", id3, created, err)
	}
	if _, _, err := ensure(strings.Repeat("x", 201), nil); err == nil {
		t.Error("a title over 200 runes must be refused")
	} else {
		var pe *problem.Error
		if !errors.As(err, &pe) || pe.Status != 422 || strings.Contains(pe.Message, "xxx") {
			t.Errorf("the error must be a Muse-text-free 422: %v", err)
		}
	}
	var source, createdBy, fold, title string
	if err := st.R.QueryRow(`SELECT source, created_by, title_fold, title FROM muses WHERE id = ?`, id1).Scan(&source, &createdBy, &fold, &title); err != nil {
		t.Fatal(err)
	}
	if source != "recipe_refs" || createdBy != "system:provenance" || fold != "mixed title" || title != "Mixed Title" {
		t.Errorf("row = %s %s %q %q", source, createdBy, fold, title)
	}
	var hits int
	if err := st.R.QueryRow(`SELECT COUNT(*) FROM muses_fts WHERE muses_fts MATCH 'mixed'`).Scan(&hits); err != nil || hits != 2 {
		t.Errorf("FTS finds %d Muses (%v), want 2", hits, err)
	}
	f, err := muses.VerifyMuses(ctx, st.R)
	if err != nil || len(f.KeyMismatch) != 0 || len(f.DanglingSongs) != 0 {
		t.Errorf("VerifyMuses = %+v %v", f, err)
	}
}
