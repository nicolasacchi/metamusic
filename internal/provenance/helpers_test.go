package provenance_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/provenance"
	"github.com/nicolasacchi/metamusic/internal/store"
)

type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type world struct {
	t    *testing.T
	st   *store.Store
	path string
	sc   *librarytest.Scene
	prov *provenance.Service
	log  *logBuf
	n    int
}

// newWorld opens an empty library and builds a Reconciler over it; mod may adjust the options (seams, steps, owner).
func newWorld(t *testing.T, mod ...func(*provenance.Options)) *world {
	t.Helper()
	path := filepath.Join(t.TempDir(), "w.db")
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	w := &world{t: t, st: st, path: path, sc: librarytest.NewScene(t, st, librarytest.FixedNow), log: &logBuf{}}
	w.prov = w.service(st, mod...)
	return w
}

// service builds one more Reconciler over st (two settlers on one database file use two of them).
func (w *world) service(st *store.Store, mod ...func(*provenance.Options)) *provenance.Service {
	o := provenance.Options{
		Store: st, Now: func() time.Time { return librarytest.FixedNow },
		NewID: func() string { w.n++; return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xe00000+w.n) },
		Log:   slog.New(slog.NewJSONHandler(w.log, nil)),
	}
	for _, m := range mod {
		m(&o)
	}
	return provenance.New(o)
}

func (w *world) settle() provenance.Report {
	w.t.Helper()
	rep, err := w.prov.Settle(context.Background())
	if err != nil {
		w.t.Fatalf("settle: %v", err)
	}
	return rep
}

func (w *world) verifyEmpty() {
	w.t.Helper()
	an, err := w.prov.Verify(context.Background())
	if err != nil {
		w.t.Fatal(err)
	}
	if len(an) != 0 {
		w.t.Errorf("Verify: %+v", an)
	}
}

func (w *world) count(q string, args ...any) int {
	w.t.Helper()
	var n int
	if err := w.st.R.QueryRow(q, args...).Scan(&n); err != nil {
		w.t.Fatalf("%s: %v", q, err)
	}
	return n
}

func (w *world) str(q string, args ...any) string {
	w.t.Helper()
	var s sql.NullString
	if err := w.st.R.QueryRow(q, args...).Scan(&s); err != nil && err != sql.ErrNoRows {
		w.t.Fatalf("%s: %v", q, err)
	}
	return s.String
}

func (w *world) exec(q string, args ...any) {
	w.t.Helper()
	if _, err := w.st.W.Exec(q, args...); err != nil {
		w.t.Fatalf("%s: %v", q, err)
	}
}

// l1Tables are the tables whose content the idempotence hash covers.
var l1Tables = []struct{ name, order string }{
	{"muses", "id"}, {"songs", "id"}, {"song_roots", "root_track_id"}, {"sessions", "id"}, {"session_members", "session_id, member_kind, member_id"},
	{"song_picks", "song_id"}, {"pick_events", "id"}, {"pick_proposals", "id"}, {"take_sounds", "track_id, facet, value, origin"},
	{"song_sounds", "song_id, facet, value"}, {"song_unmapped_terms", "song_id, term"},
}

// contentHash hashes every L1 table except the timestamp columns that legitimately move (computed_at, at).
func (w *world) contentHash() string {
	w.t.Helper()
	h := sha256.New()
	for _, tb := range l1Tables {
		rows, err := w.st.R.Query(`SELECT * FROM ` + tb.name + ` ORDER BY ` + tb.order)
		if err != nil {
			w.t.Fatal(err)
		}
		cols, _ := rows.Columns()
		fmt.Fprintf(h, "[%s]", tb.name)
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				w.t.Fatal(err)
			}
			for i, c := range cols {
				if c == "computed_at" || c == "at" {
					continue
				}
				fmt.Fprintf(h, "%s=%v;", c, vals[i])
			}
		}
		rows.Close()
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (w *world) dropTrigger(name string) {
	w.t.Helper()
	w.exec(`DROP TRIGGER ` + name)
}

func hasCode(an []provenance.Anomaly, code string) bool {
	for _, a := range an {
		if a.Code == code {
			return true
		}
	}
	return false
}

func codes(an []provenance.Anomaly) string {
	var out []string
	for _, a := range an {
		out = append(out, a.Code)
	}
	return strings.Join(out, ",")
}
