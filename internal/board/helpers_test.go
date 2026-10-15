package board_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"sync/atomic"
	"testing"
	"time"

	"modernc.org/sqlite"

	"github.com/nicolasacchi/metamusic/internal/board"
	"github.com/nicolasacchi/metamusic/internal/songs/songstest"
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

// countedBoard builds a board.Service over a counting connection pool on w's database.
func countedBoard(t *testing.T, w *songstest.World) (*board.Service, *atomic.Int64) {
	t.Helper()
	var n atomic.Int64
	db := sql.OpenDB(countingDriver{inner: &sqlite.Driver{}, dsn: "file:" + w.Path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", n: &n})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return board.New(&store.Store{R: db, W: db}, func() time.Time { return w.Now }), &n
}
