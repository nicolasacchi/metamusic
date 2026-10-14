package jobs

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/store"
)

var errInjected = errors.New("injected database failure")

// failCtl fails the K-th database call (statement, begin or commit) made while it is armed. Every pool of
// the store built over it shares one counter, so a test can fail each call of a runner tick in turn.
type failCtl struct {
	armed atomic.Bool
	n     atomic.Int64
	at    atomic.Int64 // 0 = count only
	fired atomic.Bool
}

func (c *failCtl) arm(at int64) {
	c.n.Store(0)
	c.at.Store(at)
	c.fired.Store(false)
	c.armed.Store(true)
}
func (c *failCtl) disarm()      { c.armed.Store(false) }
func (c *failCtl) calls() int64 { return c.n.Load() }

func (c *failCtl) hit() bool {
	if !c.armed.Load() {
		return false
	}
	n := c.n.Add(1)
	if at := c.at.Load(); at > 0 && n == at {
		c.fired.Store(true)
		return true
	}
	return false
}

type failConnector struct {
	drv driver.Driver
	dsn string
	ctl *failCtl
}

func (f *failConnector) Connect(context.Context) (driver.Conn, error) {
	c, err := f.drv.Open(f.dsn)
	if err != nil {
		return nil, err
	}
	return &failConn{Conn: c, ctl: f.ctl}, nil
}

func (f *failConnector) Driver() driver.Driver { return f.drv }

type failConn struct {
	driver.Conn
	ctl *failCtl
}

func (c *failConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if c.ctl.hit() {
		return nil, errInjected
	}
	if e, ok := c.Conn.(driver.ExecerContext); ok {
		return e.ExecContext(ctx, q, args)
	}
	return nil, driver.ErrSkip
}

func (c *failConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if c.ctl.hit() {
		return nil, errInjected
	}
	if e, ok := c.Conn.(driver.QueryerContext); ok {
		return e.QueryContext(ctx, q, args)
	}
	return nil, driver.ErrSkip
}

func (c *failConn) PrepareContext(ctx context.Context, q string) (driver.Stmt, error) {
	if p, ok := c.Conn.(driver.ConnPrepareContext); ok {
		return p.PrepareContext(ctx, q)
	}
	return c.Prepare(q)
}

func (c *failConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if c.ctl.hit() {
		return nil, errInjected
	}
	var tx driver.Tx
	var err error
	if b, ok := c.Conn.(driver.ConnBeginTx); ok {
		tx, err = b.BeginTx(ctx, opts)
	} else {
		tx, err = c.Begin() //nolint:staticcheck // drivers without ConnBeginTx
	}
	if err != nil {
		return nil, err
	}
	return &failTx{Tx: tx, ctl: c.ctl}, nil
}

func (c *failConn) Ping(ctx context.Context) error {
	if p, ok := c.Conn.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (c *failConn) ResetSession(ctx context.Context) error {
	if r, ok := c.Conn.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (c *failConn) IsValid() bool {
	if v, ok := c.Conn.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

func (c *failConn) CheckNamedValue(nv *driver.NamedValue) error {
	if ck, ok := c.Conn.(driver.NamedValueChecker); ok {
		return ck.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}

type failTx struct {
	driver.Tx
	ctl *failCtl
}

// Commit fails by rolling back, as a commit that did not take effect.
func (t *failTx) Commit() error {
	if t.ctl.hit() {
		_ = t.Rollback()
		return errInjected
	}
	return t.Tx.Commit()
}

// failingStore opens the database file of an existing store again through failing connectors.
func failingStore(tb testing.TB, path string, ctl *failCtl) *store.Store {
	tb.Helper()
	open := func(writer bool) *sql.DB {
		q := url.Values{}
		q.Add("_pragma", "journal_mode(WAL)")
		q.Add("_pragma", "busy_timeout(5000)")
		q.Add("_pragma", "foreign_keys(1)")
		q.Add("_pragma", "synchronous(NORMAL)")
		if writer {
			q.Set("_txlock", "immediate")
		}
		dsn := "file:" + (&url.URL{Path: path}).EscapedPath() + "?" + q.Encode()
		base, err := sql.Open("sqlite", dsn)
		if err != nil {
			tb.Fatal(err)
		}
		drv := base.Driver()
		base.Close()
		db := sql.OpenDB(&failConnector{drv: drv, dsn: dsn, ctl: ctl})
		if writer {
			db.SetMaxOpenConns(1)
		}
		tb.Cleanup(func() { db.Close() })
		return db
	}
	return &store.Store{W: open(true), R: open(false)}
}
