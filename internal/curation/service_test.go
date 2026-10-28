package curation_test

import (
	"context"
	"github.com/danielgtaylor/huma/v2"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/curation"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestMain(m *testing.M) {
	store.UseMigratedTemplateForTests()
	os.Exit(m.Run())
}

func open(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestService_ReadsSettings: absent keys read false, stored keys read as written, and Get carries who and when.
func TestService_ReadsSettings(t *testing.T) {
	t.Parallel()
	st := open(t)
	s := curation.New(st, time.Now)
	ctx := context.Background()
	if got, err := s.Get(ctx); err != nil || got != (curation.Settings{}) {
		t.Fatalf("empty store: %+v, %v", got, err)
	}
	for _, kv := range [][2]string{{curation.KeyExport, "true"}, {curation.KeySubsonic, "false"}, {curation.KeyUpdatedAt, "2026-10-28T10:00:00.000Z"}, {curation.KeyUpdatedBy, "human:alice"}} {
		if _, err := st.W.Exec(`INSERT INTO settings(key, value) VALUES(?, ?)`, kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Get(ctx)
	if err != nil || !got.ExportMuseNames || got.SubsonicMuseNames || got.UpdatedAt != "2026-10-28T10:00:00.000Z" || got.UpdatedBy != "human:alice" {
		t.Errorf("Get = %+v, %v", got, err)
	}
	if v, err := s.ExportMuseNames(ctx); err != nil || !v {
		t.Errorf("ExportMuseNames = %v, %v", v, err)
	}
	if v, err := s.SubsonicMuseNames(ctx); err != nil || v {
		t.Errorf("SubsonicMuseNames = %v, %v", v, err)
	}
}

// TestKeys_Frozen pins the four settings keys (C118, C127.1).
func TestKeys_Frozen(t *testing.T) {
	t.Parallel()
	if len(curation.Keys) != 4 || curation.KeyExport != "curation.export_muse_names" || curation.KeySubsonic != "curation.subsonic_muse_names" ||
		curation.KeyUpdatedAt != "curation.updated_at" || curation.KeyUpdatedBy != "curation.updated_by" {
		t.Errorf("keys = %v", curation.Keys)
	}
}

// TestService_ErrorsSurface: a closed store is an error from every reader and from Set and Stats, never a false flag.
func TestService_ErrorsSurface(t *testing.T) {
	t.Parallel()
	st := open(t)
	s := curation.New(st, nil)
	_ = st.Close()
	ctx := context.Background()
	if _, err := s.Get(ctx); err == nil {
		t.Error("Get")
	}
	if _, err := s.ExportMuseNames(ctx); err == nil {
		t.Error("ExportMuseNames")
	}
	if _, err := s.SubsonicMuseNames(ctx); err == nil {
		t.Error("SubsonicMuseNames")
	}
	if _, err := s.Stats(ctx); err == nil {
		t.Error("Stats")
	}
}

// TestStats_Schema: the block is nullable inline, since Huma refuses nullable on an object $ref.
func TestStats_Schema(t *testing.T) {
	t.Parallel()
	reg := huma.NewMapRegistry("#/components/schemas/", huma.DefaultSchemaNamer)
	sc := curation.Stats{}.Schema(reg)
	if !sc.Nullable || sc.Type != "object" || sc.Properties["review"] == nil {
		t.Errorf("schema = %+v", sc)
	}
}
