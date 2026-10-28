package curation_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
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

// TestService_ReadsSettings: absent keys read false, stored keys read as written, and Get carries who and when. Set and Stats are
// lane A's stubs.
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

// TestStubsA_FrozenMethodSet: the stubs answer errors.ErrUnsupported (the API maps it to 501). Lane A deletes stub_a.go and
// this test.
func TestStubsA_FrozenMethodSet(t *testing.T) {
	t.Parallel()
	s := curation.New(nil, nil)
	if _, err := s.Set(context.Background(), auth.Principal{Kind: auth.KindHuman}, curation.SettingsPatch{}); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Set: %v", err)
	}
	if _, err := s.Stats(context.Background()); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Stats: %v", err)
	}
	if len(curation.Keys) != 4 {
		t.Errorf("keys = %v", curation.Keys)
	}
}
