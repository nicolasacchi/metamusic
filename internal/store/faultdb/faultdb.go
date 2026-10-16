// Package faultdb is a test-only helper: a store.Store whose N-th database call (statement, transaction begin or commit,
// or row fetch) fails with ErrInjected. Sweeping N over an operation drives every error branch of the operation
// without a mock. It must never be linked into the binary (cmd/metamusic/nohelpers_test.go lists it).
//
// All runs of one sweep share ONE SQLite connection (both Store pools are the same *sql.DB with a single connection):
// the schema is parsed once, and the state is reset between runs by rolling back an outer transaction in which every
// transaction of the operation is a savepoint. An operation that needs a second connection at the same time (a reader
// iterating while a writer writes) would block; the sweep gives every run a deadline and fails the test on it.
package faultdb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
	_ "modernc.org/sqlite" // registers driver "sqlite"
)

// ErrInjected is the error every injected failure returns.
var ErrInjected = errors.New("faultdb: injected failure")

// counter counts database calls and fails the failAt-th one (1-based; 0 never fails).
type counter struct {
	failAt int64
	record bool
	calls  atomic.Int64
	mu     sync.Mutex
	sites  []string
}

func (c *counter) hit(site string) error {
	n := c.calls.Add(1)
	if c.record {
		c.mu.Lock()
		c.sites = append(c.sites, site)
		c.mu.Unlock()
	}
	if c.failAt > 0 && n == c.failAt {
		return ErrInjected
	}
	return nil
}

func dsn(path string) string {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(NORMAL)")
	return "file:" + (&url.URL{Path: path}).EscapedPath() + "?" + q.Encode()
}

// harness owns the one raw connection and the counter of the run in flight.
type harness struct {
	raw driver.Conn
	cur atomic.Pointer[counter]
}

func (h *harness) execRaw(ctx context.Context, q string) error {
	e, ok := h.raw.(driver.ExecerContext)
	if !ok {
		return errors.New("faultdb: driver connection cannot Exec")
	}
	_, err := e.ExecContext(ctx, q, nil)
	return err
}

// reset rolls the outer transaction back and starts the next one: the database is back at the template.
func (h *harness) reset(ctx context.Context, c *counter) error {
	_ = h.execRaw(ctx, "ROLLBACK") // fails when no transaction is open, which is fine
	if err := h.execRaw(ctx, "BEGIN"); err != nil {
		return err
	}
	h.cur.Store(c)
	return nil
}

type connector struct {
	h   *harness
	drv driver.Driver
}

func (k *connector) Connect(context.Context) (driver.Conn, error) { return &conn{h: k.h}, nil }
func (k *connector) Driver() driver.Driver                        { return k.drv }

type conn struct{ h *harness }

func (cn *conn) c() *counter { return cn.h.cur.Load() }

func (cn *conn) Prepare(q string) (driver.Stmt, error) {
	return cn.PrepareContext(context.Background(), q)
}

// Close keeps the shared raw connection open: the sweep closes it.
func (cn *conn) Close() error { return nil }

func (cn *conn) Begin() (driver.Tx, error) {
	return cn.BeginTx(context.Background(), driver.TxOptions{})
}

// BeginTx opens a savepoint inside the outer transaction (SQLite has no nested BEGIN).
func (cn *conn) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	if err := cn.c().hit("begin"); err != nil {
		return nil, err
	}
	if err := cn.h.execRaw(ctx, "SAVEPOINT fault_sp"); err != nil {
		return nil, err
	}
	return &txn{h: cn.h}, nil
}

func (cn *conn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if err := cn.c().hit("exec:" + q); err != nil {
		return nil, err
	}
	return cn.h.raw.(driver.ExecerContext).ExecContext(ctx, q, args)
}

func (cn *conn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if err := cn.c().hit("query:" + q); err != nil {
		return nil, err
	}
	rows, err := cn.h.raw.(driver.QueryerContext).QueryContext(ctx, q, args)
	if err != nil {
		return nil, err
	}
	return &rowsW{Rows: rows, c: cn.c(), q: q}, nil
}

func (cn *conn) PrepareContext(ctx context.Context, q string) (driver.Stmt, error) {
	if err := cn.c().hit("prepare:" + q); err != nil {
		return nil, err
	}
	return cn.h.raw.(driver.ConnPrepareContext).PrepareContext(ctx, q)
}

func (cn *conn) Ping(context.Context) error { return nil }

func (cn *conn) ResetSession(context.Context) error { return nil }

func (cn *conn) IsValid() bool { return true }

// readConn is the reader pool's connection of a split sweep: a second SQLite connection that sees only committed
// state (the outer transaction of the writer connection never commits, so it sees the template).
type readConn struct {
	h   *harness
	raw driver.Conn
}

type readConnector struct {
	h   *harness
	drv driver.Driver
	dsn string
}

func (k *readConnector) Connect(context.Context) (driver.Conn, error) {
	raw, err := k.drv.Open(k.dsn)
	if err != nil {
		return nil, err
	}
	return &readConn{h: k.h, raw: raw}, nil
}
func (k *readConnector) Driver() driver.Driver { return k.drv }

func (r *readConn) c() *counter { return r.h.cur.Load() }

func (r *readConn) Prepare(q string) (driver.Stmt, error) {
	return r.PrepareContext(context.Background(), q)
}
func (r *readConn) Close() error { return r.raw.Close() }
func (r *readConn) Begin() (driver.Tx, error) {
	return r.BeginTx(context.Background(), driver.TxOptions{})
}

func (r *readConn) BeginTx(ctx context.Context, o driver.TxOptions) (driver.Tx, error) {
	if err := r.c().hit("rbegin"); err != nil {
		return nil, err
	}
	tx, err := r.raw.(driver.ConnBeginTx).BeginTx(ctx, o)
	if err != nil {
		return nil, err
	}
	return &readTxn{Tx: tx, c: r.c()}, nil
}

func (r *readConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if err := r.c().hit("rexec:" + q); err != nil {
		return nil, err
	}
	return r.raw.(driver.ExecerContext).ExecContext(ctx, q, args)
}

func (r *readConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if err := r.c().hit("rquery:" + q); err != nil {
		return nil, err
	}
	rows, err := r.raw.(driver.QueryerContext).QueryContext(ctx, q, args)
	if err != nil {
		return nil, err
	}
	return &rowsW{Rows: rows, c: r.c(), q: "r" + q}, nil
}

func (r *readConn) PrepareContext(ctx context.Context, q string) (driver.Stmt, error) {
	if err := r.c().hit("rprepare:" + q); err != nil {
		return nil, err
	}
	return r.raw.(driver.ConnPrepareContext).PrepareContext(ctx, q)
}

func (r *readConn) ResetSession(context.Context) error { return nil }
func (r *readConn) IsValid() bool                      { return true }

type readTxn struct {
	driver.Tx
	c *counter
}

func (t *readTxn) Commit() error {
	if err := t.c.hit("rcommit"); err != nil {
		_ = t.Rollback()
		return err
	}
	return t.Tx.Commit()
}

type txn struct {
	h    *harness
	done bool
}

func (t *txn) Commit() error {
	if err := t.h.cur.Load().hit("commit"); err != nil {
		_ = t.Rollback()
		return err
	}
	t.done = true
	return t.h.execRaw(context.Background(), "RELEASE fault_sp")
}

func (t *txn) Rollback() error {
	if t.done {
		return nil
	}
	t.done = true
	if err := t.h.execRaw(context.Background(), "ROLLBACK TO fault_sp"); err != nil {
		return err
	}
	return t.h.execRaw(context.Background(), "RELEASE fault_sp")
}

type rowsW struct {
	driver.Rows
	c *counter
	q string
}

func (r *rowsW) Next(dest []driver.Value) error {
	if err := r.c.hit("next:" + r.q); err != nil {
		return err
	}
	return r.Rows.Next(dest)
}

// Snapshot checkpoints st and copies its database file into a new temp file; the copy is the template of Sweep.
func Snapshot(tb testing.TB, st *store.Store, path string) string {
	tb.Helper()
	if _, err := st.W.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		tb.Fatal(err)
	}
	b, err := os.ReadFile(path) //nolint:gosec // the test passes its own temp database
	if err != nil {
		tb.Fatal(err)
	}
	out := filepath.Join(tb.TempDir(), "template.db")
	if err := os.WriteFile(out, b, 0o600); err != nil { //nolint:gosec // a file in the test's own temp dir
		tb.Fatal(err)
	}
	return out
}

// perSite is how many calls of each distinct call site (statement text and kind) a sweep fails: the first
// occurrences reach every error branch of the code around the site, later ones repeat the same code in loops.
const perSite = 5

// runTimeout bounds one run: an operation that needs a second connection would block on the single one.
const runTimeout = 60 * time.Second

// Sweep runs op against the template database: first unharmed, recording the sequence of database calls, then once
// per selected call with that call failing (the first perSite calls of every distinct call site, so the number of
// runs follows the code, not the data). Every run starts from the template. It returns how many failing runs
// reported an error. op reports the error it received; swallowed errors are allowed (best-effort writes) and simply
// not counted.
func Sweep(tb testing.TB, template string, op func(ctx context.Context, st *store.Store) error) (errored int) {
	tb.Helper()
	return sweep(tb, template, false, op)
}

// SweepSplit is Sweep for an operation that holds a read transaction while it reads through another pool (it would
// block on a single connection): the reader pool gets its own connection, which sees the template, not the writes of
// the run in flight.
func SweepSplit(tb testing.TB, template string, op func(ctx context.Context, st *store.Store) error) (errored int) {
	tb.Helper()
	return sweep(tb, template, true, op)
}

func sweep(tb testing.TB, template string, split bool, op func(ctx context.Context, st *store.Store) error) (errored int) {
	tb.Helper()
	b, err := os.ReadFile(template) //nolint:gosec // the template is a Snapshot of the test
	if err != nil {
		tb.Fatal(err)
	}
	path := filepath.Join(tb.TempDir(), "sweep.db")
	if err := os.WriteFile(path, b, 0o600); err != nil { //nolint:gosec // a file in the test's own temp dir
		tb.Fatal(err)
	}
	base, err := sql.Open("sqlite", "")
	if err != nil {
		tb.Fatal(err)
	}
	drv := base.Driver()
	_ = base.Close()
	raw, err := drv.Open(dsn(path))
	if err != nil {
		tb.Fatal(err)
	}
	h := &harness{raw: raw}
	h.cur.Store(&counter{})
	db := sql.OpenDB(&connector{h: h, drv: drv})
	db.SetMaxOpenConns(1)
	st := &store.Store{W: db, R: db}
	if split {
		r := sql.OpenDB(&readConnector{h: h, drv: drv, dsn: dsn(path)})
		st.R = r
		tb.Cleanup(func() { _ = r.Close() })
	}
	tb.Cleanup(func() { _ = db.Close(); _ = raw.Close() })

	run := func(c *counter) error {
		ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
		defer cancel()
		if err := h.reset(ctx, c); err != nil {
			tb.Fatalf("faultdb: reset: %v", err)
		}
		err := op(ctx, st)
		if errors.Is(err, context.DeadlineExceeded) {
			tb.Fatalf("faultdb: the operation blocked on the single connection (it needs two at once): %v", err)
		}
		return err
	}
	probe := &counter{record: true}
	if err := run(probe); err != nil {
		tb.Fatalf("faultdb: the unharmed run failed: %v", err)
	}
	seen := map[string]int{}
	var plan []int64
	for i, site := range probe.sites {
		if seen[site]++; seen[site] <= perSite {
			plan = append(plan, int64(i+1))
		}
	}
	for _, n := range plan {
		if run(&counter{failAt: n}) != nil {
			errored++
		}
	}
	tb.Logf("faultdb: %d calls, %d failing runs, %d reported an error", len(probe.sites), len(plan), errored)
	return errored
}
