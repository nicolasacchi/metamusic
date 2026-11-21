package criticbench

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestSettle(t *testing.T) {
	t.Parallel()
	sc := func(state string) *Score { return &Score{State: state} }
	for _, tc := range []struct {
		name string
		c    Critic
		want string
	}{
		{"label critic uses lines", Critic{Kind: KindLabels, Scores: Scores{Lines: sc(StateContrary), Ratings: sc(StateTrusted)}}, StateContrary},
		{"ratings decide", Critic{Scores: Scores{Ratings: sc(StateTrusted), Duels: sc(StateContrary)}}, StateTrusted},
		{"ratings insufficient, duels decide", Critic{Scores: Scores{Ratings: sc(StateInsufficient), Duels: sc(StateTrusted)}}, StateTrusted},
		{"ratings alone", Critic{Scores: Scores{Ratings: sc(StateInsufficient)}}, StateInsufficient},
		{"duels alone", Critic{Scores: Scores{Duels: sc(StateContrary)}}, StateContrary},
		{"nothing", Critic{}, StateInsufficient},
	} {
		tc.c.settle()
		if tc.c.State != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, tc.c.State, tc.want)
		}
	}
}

func TestParseAt(t *testing.T) {
	t.Parallel()
	want := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	for _, s := range []string{"2026-11-21T10:00:00.000Z", "2026-11-21T10:00:00Z"} {
		if got := parseAt(s); !got.Equal(want) {
			t.Errorf("%s -> %v", s, got)
		}
	}
	if !parseAt("garbage").IsZero() {
		t.Error("garbage must parse to the zero time")
	}
}

func TestNewest_Bounds(t *testing.T) {
	t.Parallel()
	p := []Pair{{A: "a", B: "b"}, {A: "c", B: "d"}}
	if got := newest(p, -1); len(got) != 0 {
		t.Errorf("negative window kept %d", len(got))
	}
	if got := Newest(p, 5); len(got) != 2 {
		t.Errorf("kept %d", len(got))
	}
}

func TestLabels_DisagreementsCapped(t *testing.T) {
	t.Parallel()
	pred, human := map[string]bool{}, map[string]bool{}
	for i := 0; i < 15; i++ {
		k := fmt.Sprintf("l%02d", i)
		pred[k], human[k] = true, false
	}
	if s := Labels(pred, human); len(s.Disagreements) != 10 || s.Agree != 0 {
		t.Errorf("%+v", s)
	}
	if s := Labels(nil, nil); s.Coverage != 0 || s.State != StateInsufficient {
		t.Errorf("%+v", s)
	}
}

func TestNew_DefaultClock(t *testing.T) {
	t.Parallel()
	if s := New(Options{}); s.o.Now == nil {
		t.Fatal("no default clock")
	}
}

// failAfter lets n queries through to the real database and then fails: every read error path of the loaders.
type failAfter struct {
	db   *sql.DB
	left int
}

var errBoom = errors.New("boom")

func (f *failAfter) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	if f.left <= 0 {
		return nil, errBoom
	}
	f.left--
	return f.db.QueryContext(ctx, q, args...)
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "mm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestLoaders_ReadErrors(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	ctx := context.Background()
	ref := &Reference{}
	for left := 0; left < 2; left++ {
		if _, err := loadReference(ctx, &failAfter{st.R, left}); !errors.Is(err, errBoom) {
			t.Errorf("loadReference after %d queries: %v", left, err)
		}
		if _, err := (agents{}).Critics(ctx, &failAfter{st.R, left}, ref); !errors.Is(err, errBoom) {
			t.Errorf("agents after %d queries: %v", left, err)
		}
	}
	if _, err := (tierA{}).Critics(ctx, &failAfter{st.R, 0}, ref); !errors.Is(err, errBoom) {
		t.Errorf("tierA: %v", err)
	}
}

type errSource struct {
	criticsErr, versionErr, linesErr error
}

func (s errSource) Critics(context.Context, Reader, *Reference) ([]Critic, error) {
	return nil, s.criticsErr
}
func (s errSource) Version(context.Context) (string, error) { return "", s.versionErr }
func (s errSource) Lines(context.Context) (int, error)      { return 0, s.linesErr }

func TestBench_SourceErrors(t *testing.T) {
	t.Parallel()
	for name, src := range map[string]errSource{
		"critics": {criticsErr: errBoom}, "version": {versionErr: errBoom}, "lines": {linesErr: errBoom},
	} {
		svc := New(Options{Store: openStore(t)})
		svc.Use(src)
		if _, err := svc.Bench(context.Background(), Query{}); !errors.Is(err, errBoom) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestBench_ClosedStore(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	svc := New(Options{Store: st})
	_ = st.Close()
	if _, err := svc.Bench(context.Background(), Query{}); err == nil {
		t.Error("a closed store must fail")
	}
}
