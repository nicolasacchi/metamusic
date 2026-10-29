package app

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"modernc.org/sqlite"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/curation"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/muses"
	"github.com/nicolasacchi/metamusic/internal/records"
	"github.com/nicolasacchi/metamusic/internal/recordview"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// A counting reader pool: every statement the reader pool runs bumps n (the statement-count tests of lane V2).
type countConnector struct {
	driver.Connector
	n *atomic.Int64
}

func (c countConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &countConn{Conn: conn, n: c.n}, nil
}

type countConn struct {
	driver.Conn
	n *atomic.Int64
}

func (c *countConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.n.Add(1)
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
}

func (c *countConn) PrepareContext(ctx context.Context, q string) (driver.Stmt, error) {
	c.n.Add(1) // a statement database/sql prepares first is still one statement
	return c.Conn.(driver.ConnPrepareContext).PrepareContext(ctx, q)
}

func (c *countConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	c.n.Add(1)
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, q, args)
}

func (c *countConn) BeginTx(ctx context.Context, o driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, o)
}

// countedStore opens a migrated store at a temp path and returns it with a second Store whose reader pool counts statements.
func countedStore(t *testing.T) (*store.Store, *store.Store, *atomic.Int64) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "count.db")
	st, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	n := new(atomic.Int64)
	base, err := sqlite.NewConnector("file:" + path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(countConnector{Connector: base, n: n})
	t.Cleanup(func() { _ = db.Close() })
	return st, &store.Store{W: st.W, R: db}, n
}

var fixedNow = func() time.Time { return librarytest.FixedNow }

// scale builds a library of nSongs Songs (each with two Takes and a Pick history, three Muses) and a Record of all of them
// with loudness analyses of 300 one-second values for every Take.
func scale(t *testing.T, nSongs int) (cst *store.Store, n *atomic.Int64, recordID string, songIDs, takeIDs []string) {
	t.Helper()
	ctx := context.Background()
	st, cst, n := countedStore(t)
	info := librarytest.FixtureL2Scale(t, st, librarytest.FixedNow, nSongs, 3, 0)
	for i := 1; i <= nSongs; i++ {
		songIDs = append(songIDs, info.Songs[fmt.Sprintf("S%d", i)])
	}
	rows, err := st.R.QueryContext(ctx, `SELECT id FROM tracks ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		takeIDs = append(takeIDs, id)
	}
	_ = rows.Close()
	series := strings.TrimSuffix(strings.Repeat("-14.0,", 300), ",")
	for i, id := range takeIDs {
		if _, err := st.W.Exec(`INSERT INTO analyses(track_id, analyzer, version, tier, metrics_json, created_at) VALUES(?, 'loudness', 'mm-loudness/1 ffmpeg test', 'A', ?, ?)`,
			id, fmt.Sprintf(`{"integrated_lufs":%d.5,"lra_lu":5,"lra_low_lufs":-18,"lra_high_lufs":-12,"true_peak_dbtp":-2,"sample_peak_dbfs":-2.5,"short_term_max_lufs":-10,"short_term_1hz":[%s]}`, -14-i%5, series),
			"2026-10-06T10:00:00.000Z"); err != nil {
			t.Fatal(err)
		}
	}
	recs := records.New(st, fixedNow, func() string { return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xe00000+int(n.Add(1))) })
	human := auth.Principal{Kind: auth.KindHuman, ID: "fixture", Name: "fixture", Via: "local"}
	rec, err := recs.Create(ctx, human, records.NewRecord{Title: "Synthetic Count Record", SongIDs: songIDs})
	if err != nil {
		t.Fatal(err)
	}
	n.Store(0)
	return cst, n, rec.ID, songIDs, takeIDs
}

// TestBatchReaders_OneStatement: each batch reader runs exactly one statement for 1 id and for 60 ids.
func TestBatchReaders_OneStatement(t *testing.T) {
	t.Parallel()
	cst, n, _, songIDs, takeIDs := scale(t, 60)
	rd := l2Readers{st: cst}
	ctx := context.Background()
	for _, k := range []int{1, 60} {
		cases := map[string]func() error{
			"SessionsOf": func() error { _, err := rd.SessionsOf(ctx, songIDs[:k]); return err },
			"FamiliesOf": func() error { _, err := rd.FamiliesOf(ctx, songIDs[:k]); return err },
			"Briefs":     func() error { _, err := rd.Briefs(ctx, songIDs[:k]); return err },
			"Current":    func() error { _, err := rd.Current(ctx, songIDs[:k]); return err },
			"Loudness":   func() error { _, err := rd.Loudness(ctx, takeIDs[:k]); return err },
		}
		for name, run := range cases {
			n.Store(0)
			if err := run(); err != nil {
				t.Fatalf("%s(%d): %v", name, k, err)
			}
			if got := n.Load(); got != 1 {
				t.Errorf("%s with %d ids ran %d statements, want 1", name, k, got)
			}
		}
	}
	// the readers answer what they were asked
	b, _ := rd.Briefs(ctx, songIDs[:3])
	if len(b) != 3 || b[songIDs[0]].RootTrackID == "" || b[songIDs[0]].State == "" {
		t.Errorf("briefs: %+v", b)
	}
	l, _ := rd.Loudness(ctx, takeIDs[:2])
	if len(l) != 2 || !l[takeIDs[0]].Measure.OK || len(l[takeIDs[0]].ShortTerm1Hz) != 300 {
		t.Errorf("loudness: %+v", l)
	}
}

// TestRecordView_NoN1: Get with every include runs the same number of statements at 6 and at 60 slots, at most maxStatements.
func TestRecordView_NoN1(t *testing.T) {
	t.Parallel()
	// Record 1, Resolved 3, songs 1, picks 1, takes 1, stars 1, loudness 1, pending 1, plus the includes: changes 2 (records.Changes checks the Record exists), Muses,
	// sessions, sounds and costs (liner) 4, the export settings (preview) 1, tapes (none wired here) = 17. The plan listed 12; it
	// left out the Pick rows, the Take titles, the stars, the costs and the pending probe.
	const maxStatements = 17
	counts := map[int]int64{}
	for _, slots := range []int{6, 60} {
		cst, n, id, _, _ := scale(t, slots)
		rd := l2Readers{st: cst}
		lib := library.New(cst, nil, fixedNow)
		svc := recordview.New(recordview.Options{Store: cst, Records: records.New(cst, fixedNow, nil), Muses: muses.New(cst, fixedNow, nil), Sessions: rd,
			Sounds: rd, Songs: rd, Picks: rd, Ears: rd, Curation: curation.New(cst, fixedNow), Library: lib})
		human := auth.Principal{Kind: auth.KindHuman, ID: "fixture", Name: "fixture", Via: "local"}
		v, err := svc.Get(context.Background(), human, id, recordview.GetOptions{Include: recordview.Includes, ChangesLimit: 50})
		if err != nil {
			t.Fatal(err)
		}
		if len(v.Slots) != slots || v.Liner == nil || v.Energy == nil || v.Preview == nil || len(v.Changes) == 0 {
			t.Fatalf("%d slots: view incomplete: slots=%d liner=%v energy=%v preview=%v changes=%d", slots, len(v.Slots), v.Liner != nil, v.Energy != nil, v.Preview != nil, len(v.Changes))
		}
		counts[slots] = n.Load()
	}
	if counts[6] != counts[60] {
		t.Errorf("statements at 6 slots = %d, at 60 = %d: the view must not depend on the slot count", counts[6], counts[60])
	}
	if counts[60] > maxStatements {
		t.Errorf("statements per Get = %d, want at most %d", counts[60], maxStatements)
	}
	t.Logf("statements per Get with every include: %d", counts[60])
}
