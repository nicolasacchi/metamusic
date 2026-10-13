package ears_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/ears/earstest"
	"github.com/nicolasacchi/metamusic/internal/ladder"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestMain(m *testing.M) {
	store.UseMigratedTemplateForTests()
	os.Exit(m.Run())
}

// rig is one isolated library: fixture rows, fixture media files, a fake Tier A and fake judgments.
type rig struct {
	t    *testing.T
	st   *store.Store
	root *os.Root
	dir  string
	lib  *library.Service
	svc  *ears.Service
	tier *earstest.TierA
	judg *earstest.Judgments
	fx   lt.Fixtures
	ears earstest.Ears
	now  time.Time
	opts ears.Options
}

type rigOpt func(*rig, *ears.Options)

// withTierARows adds earstest's Tier A rows (Citta clipped, Luna looping; both complete).
func withTierARows() rigOpt {
	return func(r *rig, _ *ears.Options) { r.ears = earstest.Fixture(r.t, r.st, r.root, r.fx, r.now) }
}

func newRig(t *testing.T, opts ...rigOpt) *rig {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	lt.WriteFiles(t, dir)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	now := lt.FixedNow
	r := &rig{t: t, st: st, root: root, dir: dir, now: now, tier: earstest.NewTierA(),
		judg: &earstest.Judgments{Pairs: map[[2]string]ladder.PairStats{}, Strength: map[string]ladder.Strength{}, Nu: 0.5}}
	r.fx = lt.Fixture(t, st, now)
	r.lib = library.New(st, root, func() time.Time { return now })
	o := ears.Options{Store: st, Library: r.lib, Data: root, DataDir: dir, Analyzer: r.tier, Judgments: r.judg,
		Signer: serve.NewSigner([]byte("test-secret-key-0123456789abcdef"), func() time.Time { return now }),
		Now:    func() time.Time { return now }}
	for _, f := range opts {
		f(r, &o)
	}
	r.opts = o
	r.svc = ears.New(o)
	return r
}

func (r *rig) exec(q string, a ...any) {
	r.t.Helper()
	if _, err := r.st.W.ExecContext(context.Background(), q, a...); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) count(q string, a ...any) (n int) {
	r.t.Helper()
	if err := r.st.R.QueryRowContext(context.Background(), q, a...).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

func (r *rig) str(q string, a ...any) (s string) {
	r.t.Helper()
	if err := r.st.R.QueryRowContext(context.Background(), q, a...).Scan(&s); err != nil {
		r.t.Fatal(err)
	}
	return s
}

// dropTriggers lets a test edit rows that are immutable in production.
func (r *rig) dropTriggers(names ...string) {
	r.t.Helper()
	for _, n := range names {
		r.exec(`DROP TRIGGER ` + n)
	}
}
