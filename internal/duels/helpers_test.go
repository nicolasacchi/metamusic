package duels_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/duels"
	"github.com/nicolasacchi/metamusic/internal/duels/duelstest"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/clip"
	"github.com/nicolasacchi/metamusic/internal/media/clip/cliptest"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestMain(m *testing.M) {
	store.UseMigratedTemplateForTests()
	os.Exit(m.Run())
}

var (
	human  = auth.Principal{Kind: auth.KindHuman, ID: "fixture", Name: "fixture", Via: "proxy"}
	device = auth.Principal{Kind: auth.KindDevice, ID: lt.DevicePaired, Name: "phone", Via: "device"}
	agent  = auth.Principal{Kind: auth.KindAgent, ID: "fixture-agent", Name: "critic", Via: "token"}
	agent2 = auth.Principal{Kind: auth.KindAgent, ID: "other-agent", Name: "critic2", Via: "token"}
)

// env is one isolated service over the librarytest + duelstest fixture.
type env struct {
	st   *store.Store
	lib  *library.Service
	rend *spyRenderer
	svc  *duels.Service
	fx   duelstest.Duels
	now  time.Time
	nid  atomic.Uint64
	opts duels.Options
}

// spyRenderer wraps cliptest.Renderer and records the specs it was asked for.
type spyRenderer struct {
	*cliptest.Renderer
	mu      sync.Mutex
	ensured []clip.Spec
}

func (s *spyRenderer) Ensure(ctx context.Context, sp clip.Spec) (clip.Object, error) {
	s.mu.Lock()
	s.ensured = append(s.ensured, sp)
	s.mu.Unlock()
	return s.Renderer.Ensure(ctx, sp)
}

func (s *spyRenderer) specs() []clip.Spec {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]clip.Spec(nil), s.ensured...)
}

func newEnv(t *testing.T, mut ...func(*duels.Options)) *env {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e := &env{st: st, now: lt.FixedNow, rend: &spyRenderer{Renderer: cliptest.NewRenderer()}}
	fx := lt.Fixture(t, st, e.now)
	e.fx = duelstest.Fixture(t, st, "", fx, e.now, nil)
	e.lib = library.New(st, nil, func() time.Time { return e.now })
	e.opts = duels.Options{
		Store: st, Library: e.lib, Clips: e.rend, Signer: serve.NewSigner([]byte("0123456789abcdef0123456789abcdef"), func() time.Time { return e.now }),
		Now:   func() time.Time { return e.now },
		NewID: func() string { return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0x20000+e.nid.Add(1)) },
		Rand:  func() bool { return false },
	}
	for _, m := range mut {
		m(&e.opts)
	}
	e.svc = duels.New(e.opts)
	return e
}

func (e *env) create(t *testing.T, p auth.Principal, a, b string, blind bool) duels.Duel {
	t.Helper()
	d, err := e.svc.Create(context.Background(), p, duels.CreateInput{ATrackID: a, BTrackID: b, Blind: &blind})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return d
}

// slots returns the stored slot tracks of a duel.
func (e *env) slots(t *testing.T, id string) (a, b string) {
	t.Helper()
	if err := e.st.R.QueryRow(`SELECT slot_a_track_id, slot_b_track_id FROM duels WHERE id = ?`, id).Scan(&a, &b); err != nil {
		t.Fatal(err)
	}
	return a, b
}

// win judges so that track wins.
func (e *env) win(t *testing.T, p auth.Principal, id, track string) duels.SubmitResult {
	t.Helper()
	a, _ := e.slots(t, id)
	v := "b"
	if a == track {
		v = "a"
	}
	return e.judge(t, p, id, v)
}

func (e *env) judge(t *testing.T, p auth.Principal, id, verdict string) duels.SubmitResult {
	t.Helper()
	r, err := e.svc.Submit(context.Background(), p, id, duels.VerdictInput{Verdict: verdict})
	if err != nil {
		t.Fatalf("submit %s: %v", verdict, err)
	}
	return r
}

func (e *env) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.st.R.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func sp[T any](v T) *T { return &v }
