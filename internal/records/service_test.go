package records_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/records"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// The principals of the C120.1 table.
var (
	human  = auth.Principal{Kind: auth.KindHuman, ID: "op", Name: "op", Via: "local"}
	agent  = auth.Principal{Kind: auth.KindAgent, ID: "ag1", Name: "synthetic-agent", Via: "token"}
	device = auth.Principal{Kind: auth.KindDevice, ID: "dev1", Name: "synthetic-phone", Via: "device"}
)

// rig is a store with the L2 fixture (or the scale fixture) and a Service on a one-second-per-call clock.
type rig struct {
	t   *testing.T
	st  *store.Store
	svc *records.Service
	fx  librarytest.L2Info
}

func newRig(t *testing.T) *rig { return newRigWith(t, false) }

// image is a fixture database built once per process (and per shape); every rig starts from a copy of it.
type image struct {
	once sync.Once
	db   []byte
	fx   librarytest.L2Info
}

var l2Image, scaleImage image

func (im *image) build(t *testing.T, scale bool) {
	im.once.Do(func() {
		dir, err := os.MkdirTemp("", "mm-records-image-")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(dir)
		p := filepath.Join(dir, "i.db")
		st, err := store.Open(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		if scale {
			im.fx = librarytest.FixtureL2Scale(t, st, librarytest.FixedNow, scaleSongs, 0, 0)
		} else {
			im.fx = librarytest.FixtureL2(t, st, librarytest.FixedNow)
		}
		if _, err := st.W.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			t.Fatal(err)
		}
		st.Close()
		if im.db, err = os.ReadFile(p); err != nil {
			t.Fatal(err)
		}
	})
	if im.db == nil {
		t.Fatal("the fixture image could not be built")
	}
}

// scaleSongs is the size of the scale fixture (Songs S1..S64).
const scaleSongs = 64

// newRigWith builds the L2 fixture, or with scale the store of scaleSongs Songs and no Records.
func newRigWith(t *testing.T, scale bool) *rig {
	t.Helper()
	im := &l2Image
	if scale {
		im = &scaleImage
	}
	im.build(t, scale)
	p := filepath.Join(t.TempDir(), "r.db")
	if err := os.WriteFile(p, im.db, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	clock, n := librarytest.FixedNow.Add(time.Hour), 0
	now := func() time.Time { clock = clock.Add(time.Second); return clock }
	newID := func() string { n++; return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xe10000+n) }
	return &rig{t: t, st: st, svc: records.New(st, now, newID), fx: im.fx}
}

func (r *rig) exec(q string, args ...any) {
	r.t.Helper()
	if _, err := r.st.W.Exec(q, args...); err != nil {
		r.t.Fatalf("%s: %v", q, err)
	}
}

func (r *rig) song(label string) string { return r.fx.Songs[label] }

// newRecord creates a draft Record by p over the Songs of the given labels.
func (r *rig) newRecord(p auth.Principal, labels ...string) records.Record {
	r.t.Helper()
	ids := make([]string, len(labels))
	for i, l := range labels {
		ids[i] = r.song(l)
	}
	rec, err := r.svc.Create(context.Background(), p, records.NewRecord{Title: "Synthetic Record Under Test", SongIDs: ids})
	if err != nil {
		r.t.Fatalf("create: %v", err)
	}
	return rec
}

func (r *rig) slots(id string) []records.Slot {
	r.t.Helper()
	s, err := r.svc.Slots(context.Background(), id)
	if err != nil {
		r.t.Fatal(err)
	}
	return s
}

func (r *rig) slotIDs(id string) []string {
	var out []string
	for _, s := range r.slots(id) {
		out = append(out, s.ID)
	}
	return out
}

func (r *rig) get(id string) records.Record {
	r.t.Helper()
	rec, err := r.svc.Get(context.Background(), id)
	if err != nil {
		r.t.Fatal(err)
	}
	return rec
}

// changes reads the changelog straight from the table, oldest first.
func (r *rig) changes(id string) []records.Change {
	r.t.Helper()
	rows, err := r.st.R.Query(`SELECT id, seq, at, kind, slot_id, position, from_take_id, to_take_id, reason, pick_event_id, evidence_json, detail_json, actor, actor_kind, actor_name
		FROM record_changes WHERE record_id = ? ORDER BY seq`, id)
	if err != nil {
		r.t.Fatal(err)
	}
	defer rows.Close()
	var out []records.Change
	for rows.Next() {
		var c records.Change
		var ev, det sql.NullString
		if err := rows.Scan(&c.ID, &c.Seq, &c.At, &c.Kind, &c.SlotID, &c.Position, &c.FromTakeID, &c.ToTakeID, &c.Reason, &c.PickEventID, &ev, &det, &c.Actor, &c.ActorKind, &c.ActorName); err != nil {
			r.t.Fatal(err)
		}
		if ev.Valid {
			c.Evidence = []byte(ev.String)
		}
		if det.Valid {
			c.Detail = []byte(det.String)
		}
		out = append(out, c)
	}
	return out
}

func kinds(cs []records.Change) []string {
	out := []string{}
	for _, c := range cs {
		out = append(out, c.Kind)
	}
	return out
}

// problemOf unwraps a service error to its status, code and details.
func problemOf(t *testing.T, err error) *problem.Error {
	t.Helper()
	var pe *problem.Error
	if !errors.As(err, &pe) {
		t.Fatalf("error %v is not a *problem.Error", err)
	}
	return pe
}

func wantProblem(t *testing.T, err error, status int, code string) *problem.Error {
	t.Helper()
	if err == nil {
		t.Fatalf("no error, want %d %s", status, code)
	}
	pe := problemOf(t, err)
	if pe.Status != status || pe.Code != code {
		t.Fatalf("error = %d %s (%s), want %d %s", pe.Status, pe.Code, pe.Message, status, code)
	}
	return pe
}

func edit(t *testing.T, r *rig, p auth.Principal, rec records.Record, ops ...records.SlotOp) (records.EditResult, error) {
	t.Helper()
	return r.svc.EditSlots(context.Background(), p, rec.ID, rec.Revision, ops)
}

func mustEdit(t *testing.T, r *rig, p auth.Principal, rec records.Record, ops ...records.SlotOp) records.EditResult {
	t.Helper()
	res, err := edit(t, r, p, rec, ops...)
	if err != nil {
		t.Fatalf("EditSlots: %v", err)
	}
	return res
}

func same(t *testing.T, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

func count(t *testing.T, r *rig, q string, args ...any) int {
	t.Helper()
	var n int
	if err := r.st.R.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
