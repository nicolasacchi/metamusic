package morning

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/clock"
	"github.com/nicolasacchi/metamusic/internal/clock/clocktest"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestMain(m *testing.M) {
	store.UseMigratedTemplateForTests()
	os.Exit(m.Run())
}

// dawn is 07:00 on 2026-11-22 UTC: the moment the crate is due.
var dawn = time.Date(2026, 11, 22, 7, 0, 0, 0, time.UTC)

var (
	owner    = auth.Principal{Kind: auth.KindHuman, ID: "alice", Name: "alice", Role: auth.RoleOwner, User: "alice", Via: "local"}
	ownerDev = auth.Principal{Kind: auth.KindDevice, ID: "dev1", Name: "phone", Role: auth.RoleOwner, User: "alice", Via: "device"}
	editor   = auth.Principal{Kind: auth.KindHuman, ID: "bob", Name: "bob", Role: auth.RoleEditor, User: "bob", Via: "local"}
	listener = auth.Principal{Kind: auth.KindHuman, ID: "carol", Name: "carol", Role: auth.RoleListener, User: "carol", Via: "local"}
	agent    = auth.Principal{Kind: auth.KindAgent, ID: "ag1", Name: "ag1", Via: "token", Scopes: []auth.Scope{auth.ScopeAdmin}}
)

// stubBuilder is a SideBuilder that needs no media tools: it records calls, can block and can fail, and writes a stand-in file.
type stubBuilder struct {
	mu      sync.Mutex
	calls   []SideInput
	err     error
	gate    chan struct{} // when non-nil a build waits for it (or the context)
	data    *os.Root
	started chan struct{}
}

func (b *stubBuilder) BuildSide(ctx context.Context, in SideInput) (SideResult, error) {
	b.mu.Lock()
	b.calls = append(b.calls, in)
	b.mu.Unlock()
	if b.started != nil {
		select {
		case b.started <- struct{}{}:
		default:
		}
	}
	if b.gate != nil {
		select {
		case <-b.gate:
		case <-ctx.Done():
			return SideResult{}, &BuildError{Code: CodeInterrupted, Msg: "cancelled", Err: ctx.Err()}
		}
	}
	if b.err != nil {
		return SideResult{}, b.err
	}
	sha := fmt.Sprintf("%064x", 0xabc000+uint64(in.Side.Index)*7+uint64(len(b.calls))*1000+uint64(in.Side.Samples))
	rel := fmt.Sprintf("derived/morning/%s/%s.flac", sha[:2], sha)
	if b.data != nil {
		if err := b.data.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
			return SideResult{}, err
		}
		if err := b.data.WriteFile(rel, []byte("fake side"), 0o444); err != nil {
			return SideResult{}, err
		}
	}
	res := SideResult{SHA256: sha, RelPath: rel, Bytes: 9, Samples: in.Side.Samples}
	for _, c := range in.Side.Chapters {
		res.Chapters = append(res.Chapters, ChapterLoudness{N: c.N, LUFS: -16, TruePeak: -2})
	}
	return res, nil
}

type rig struct {
	t     *testing.T
	st    *store.Store
	svc   *Service
	clk   *clocktest.Manual
	data  *os.Root
	dir   string
	build *stubBuilder
	n     int
	ids   int
	night NightBatchFunc
}

func newRig(t *testing.T) *rig {
	return newRigAt(t, filepath.Join(t.TempDir(), "s.db"), t.TempDir(), nil)
}

// newRigAt opens (or reopens) a rig over a database file and data directory; share a clock to model two processes.
func newRigAt(t *testing.T, dbPath, dir string, clk *clocktest.Manual) *rig {
	t.Helper()
	st, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	var n int
	_ = st.R.QueryRow(`SELECT COUNT(*) FROM tracks`).Scan(&n)
	newDB := n == 0
	if newDB {
		librarytest.Fixture(t, st, dawn)
	}
	data, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { data.Close() })
	if clk == nil {
		clk = clocktest.NewManual(dawn)
	}
	r := &rig{t: t, st: st, clk: clk, data: data, dir: dir, build: &stubBuilder{data: data}}
	r.svc = New(ServiceOptions{Store: st, Data: data, Builder: r.build, Signer: serve.NewSigner([]byte("k"), clk.Now), Clock: clk, Loc: time.UTC,
		NewID: func() string { r.ids++; return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xc000+r.ids) },
		NightBatch: func(ctx context.Context, day string) (string, bool, bool, error) {
			if r.night == nil {
				return "", false, false, nil
			}
			return r.night(ctx, day)
		}})
	t.Cleanup(r.svc.Close)
	if newDB {
		r.setting("anchors", 0) // the fixture has a final take; tests that want anchors turn them back on
	}
	return r
}

func (r *rig) exec(q string, args ...any) {
	r.t.Helper()
	if _, err := r.st.W.Exec(q, args...); err != nil {
		r.t.Fatalf("%s: %v", q, err)
	}
}

func (r *rig) scalar(q string, args ...any) string {
	r.t.Helper()
	var v any
	if err := r.st.R.QueryRow(q, args...).Scan(&v); err != nil {
		r.t.Fatal(err)
	}
	return fmt.Sprint(v)
}

// take adds a job-made take (its own lineage root) created age ago, with a measured loudness, and returns its id.
func (r *rig) take(title string, age time.Duration) string {
	r.t.Helper()
	r.n++
	id := fmt.Sprintf("01900000-0000-7000-8000-%012x", 0x9000+r.n)
	r.exec(`INSERT INTO tracks(id,slug,title,recipe_id,parent_track_id,root_track_id,generation,status,status_set_by,origin,duration_s,sample_rate,bits_per_sample,
		channels,total_samples,master_path,master_sha256,master_bytes,source_sha256,frames_sha256,audio_md5,truncated_abc,truncated_semantic,bpm_declared,import_rule,
		import_run_id,recorded_at,created_at)
		SELECT ?,?,?,recipe_id,NULL,?,0,'draft','job','job',200,48000,24,2,9600000,?,?,1,source_sha256,?,audio_md5,0,0,NULL,'job',NULL,?,?
		FROM tracks WHERE id = ?`, id, fmt.Sprintf("t-%d", r.n), title, id, fmt.Sprintf("masters/%d.flac", r.n), fmt.Sprintf("%064x", 0x1000+r.n),
		fmt.Sprintf("%064x", 0x2000+r.n), store.FormatTime(dawn.Add(-age)), store.FormatTime(dawn.Add(-age)), librarytest.TrackLuna)
	r.exec(`INSERT INTO analyses(track_id,analyzer,version,tier,metrics_json,created_at) VALUES(?,'loudness','v1','0',?,?)`, id,
		`{"integrated_lufs":-14.2,"true_peak_dbtp":-1.4}`, store.FormatTime(dawn))
	return id
}

func (r *rig) rate(id string, stars int) {
	r.t.Helper()
	r.exec(`INSERT INTO ratings(track_id,source,rater,stars,principal_kind,principal_id,principal_name,created_at,updated_at)
		VALUES(?,'human','owner',?,'human','human:alice','alice',?,?)`, id, stars, store.FormatTime(dawn), store.FormatTime(dawn))
}

func (r *rig) setting(col string, v any) {
	r.t.Helper()
	r.exec(`UPDATE morning_crate_settings SET `+col+` = ?`, v)
}

func (r *rig) built(c Crate) Crate {
	r.t.Helper()
	r.svc.Wait()
	out, err := r.svc.Get(context.Background(), owner, c.ID)
	if err != nil {
		r.t.Fatal(err)
	}
	return out
}

// realClockForTest is the wall clock; under synctest it is the bubble's virtual one.
type realClockForTest struct{}

func (realClockForTest) Now() time.Time                       { return time.Now() }
func (realClockForTest) NewTimer(d time.Duration) clock.Timer { return realTimerT{time.NewTimer(d)} }

type realTimerT struct{ t *time.Timer }

func (r realTimerT) C() <-chan time.Time { return r.t.C }
func (r realTimerT) Stop() bool          { return r.t.Stop() }

var auth0 = auth.Principal{}
