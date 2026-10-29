package albums_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"path/filepath"
	"sync/atomic"
	"testing"

	"modernc.org/sqlite"

	"github.com/nicolasacchi/metamusic/internal/albums"
	"github.com/nicolasacchi/metamusic/internal/curation"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/records"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// countingDriver counts the statements a connection prepares (every query database/sql runs prepares one).
type countingDriver struct {
	inner *sqlite.Driver
	dsn   string
	n     *atomic.Int64
}

func (d countingDriver) Connect(context.Context) (driver.Conn, error) {
	c, err := d.inner.Open(d.dsn)
	if err != nil {
		return nil, err
	}
	return &countingConn{Conn: c, n: d.n}, nil
}
func (d countingDriver) Driver() driver.Driver { return d.inner }

type countingConn struct {
	driver.Conn
	n *atomic.Int64
}

func (c *countingConn) Prepare(q string) (driver.Stmt, error) {
	c.n.Add(1)
	return c.Conn.Prepare(q)
}

func (c *countingConn) BeginTx(ctx context.Context, o driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, o)
}

// world is a catalog over a FixtureL2 database; reads go through a counting pool, writes through the store's writer.
type world struct {
	St   *store.Store
	R    *store.Store // the counting reader pool paired with the writer
	Info librarytest.L2Info
	Svc  *albums.Service
	Set  *curation.Service
	N    *atomic.Int64
}

// museSpy is a MuseDirectory that fails the test on any call but Version, which it counts.
type museSpy struct {
	t        *testing.T
	versions atomic.Int64
	version  atomic.Value
}

func (m *museSpy) Ordinals(context.Context) (map[string]albums.MuseMeta, error) {
	m.t.Error("Token or the record kind asked the directory for Ordinals")
	return nil, nil
}
func (m *museSpy) Labels(context.Context, []string) (map[string]string, error) {
	m.t.Error("the directory was asked for Labels")
	return nil, nil
}
func (m *museSpy) Version(context.Context) (string, error) {
	m.versions.Add(1)
	v, _ := m.version.Load().(string)
	return v, nil
}

func newWorld(t *testing.T, dir albums.MuseDirectory) *world {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.db")
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	w := &world{St: st, N: new(atomic.Int64)}
	w.Info = librarytest.FixtureL2(t, st, librarytest.FixedNow)
	counted := sql.OpenDB(countingDriver{inner: &sqlite.Driver{}, dsn: "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", n: w.N})
	counted.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = counted.Close() })
	rst := &store.Store{W: st.W, R: counted}
	w.R = rst
	w.Set = curation.New(rst, nil)
	w.Svc = albums.New(rst, records.New(rst, nil, nil), dir, w.Set, nil)
	return w
}

// exec runs a write against the writer pool.
func (w *world) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := w.St.W.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// publish publishes a draft Record the way the Records service will (R-a), keeping the trigger rules.
func (w *world) publish(t *testing.T, recordID string) {
	t.Helper()
	w.exec(t, `UPDATE records SET status = 'published', published_by = 'human:fixture', published_at = ?, first_published_at = COALESCE(first_published_at, ?), updated_at = ? WHERE id = ?`,
		"2026-10-28T09:00:00.000Z", "2026-10-28T09:00:00.000Z", "2026-10-28T09:00:00.000Z", recordID)
}
