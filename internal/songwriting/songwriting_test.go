package songwriting_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/songwriting"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestMain(m *testing.M) {
	store.UseMigratedTemplateForTests()
	os.Exit(m.Run())
}

func newSvc(t *testing.T) (*songwriting.Service, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return songwriting.New(st), st
}

func ip(v int) *int { return &v }

func TestGet_Defaults(t *testing.T) {
	s, _ := newSvc(t)
	got, err := s.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := songwriting.Targets{MinDurationS: 180, TargetMinDurationS: 240, TargetMaxDurationS: 300}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("defaults (-want +got):\n%s", diff)
	}
}

func TestSet_PartialPatchPersists(t *testing.T) {
	s, st := newSvc(t)
	ctx := context.Background()
	got, err := s.Set(ctx, songwriting.Patch{MinDurationS: ip(200), TargetMaxDurationS: ip(330)})
	if err != nil {
		t.Fatal(err)
	}
	want := songwriting.Targets{MinDurationS: 200, TargetMinDurationS: 240, TargetMaxDurationS: 330}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("set result (-want +got):\n%s", diff)
	}
	// stored in the settings table under the documented keys; unset keys stay absent (defaults apply)
	var v string
	if err := st.R.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='songwriting.min_duration_s'`).Scan(&v); err != nil || v != "200" {
		t.Fatalf("min key: %q %v", v, err)
	}
	if err := st.R.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='songwriting.target_max_duration_s'`).Scan(&v); err != nil || v != "330" {
		t.Fatalf("target max key: %q %v", v, err)
	}
	again, err := s.Get(ctx)
	if err != nil || again != want {
		t.Fatalf("get after set: %+v %v", again, err)
	}
}

func TestSet_Validation(t *testing.T) {
	cases := []struct {
		name string
		p    songwriting.Patch
	}{
		{"empty patch", songwriting.Patch{}},
		{"min below floor", songwriting.Patch{MinDurationS: ip(10)}},
		{"max above ceiling", songwriting.Patch{TargetMaxDurationS: ip(601)}},
		{"min above target min", songwriting.Patch{MinDurationS: ip(250)}},
		{"target min above target max", songwriting.Patch{TargetMinDurationS: ip(310)}},
		{"target max below target min", songwriting.Patch{TargetMaxDurationS: ip(200)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, _ := newSvc(t)
			if _, err := s.Set(context.Background(), c.p); !errors.Is(err, songwriting.ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
			got, _ := s.Get(context.Background())
			if got != (songwriting.Targets{MinDurationS: 180, TargetMinDurationS: 240, TargetMaxDurationS: 300}) {
				t.Fatalf("a refused patch changed the settings: %+v", got)
			}
		})
	}
}

func TestGet_CorruptStoredValueFallsBackToDefaults(t *testing.T) {
	s, st := newSvc(t)
	ctx := context.Background()
	if _, err := st.W.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES('songwriting.min_duration_s','banana')`); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx)
	if err != nil || got.MinDurationS != 180 {
		t.Fatalf("got %+v %v", got, err)
	}
}
