package sequence_test

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/records"
	"github.com/nicolasacchi/metamusic/internal/sequence"
	"github.com/nicolasacchi/metamusic/internal/store"
)

var update = flag.Bool("update", false, "rewrite golden files")

var agent = auth.Principal{Kind: auth.KindAgent, ID: "ag1", Name: "synthetic-agent", Via: "token"}

// feat is one synthetic feature triple: tempo in centi-BPM, energy in 1/10000 and a Camelot key.
type feat struct {
	bpm, energy int
	key         string
}

// tSetFeat maps the Songs S1..S6 of the Records fixture onto the six-Song set of the sequencer fixture (t1..t6).
var tSetFeat = map[string]feat{
	"S1": {9600, 3000, "8A"}, "S2": {9800, 4500, "8A"}, "S3": {10000, 5500, "9A"},
	"S4": {10400, 7000, "9A"}, "S5": {10800, 9000, "9B"}, "S6": {10200, 4000, "8B"},
}

// shuffled is the input order of the T-set Record: t4, t6, t1, t5, t3, t2 (C137.6).
var shuffled = []string{"S4", "S6", "S1", "S5", "S3", "S2"}

type image struct {
	once sync.Once
	db   []byte
	fx   librarytest.L2Info
}

var l2Image, scaleImage image

const scaleSongs = 64

func (im *image) build(t *testing.T, scale bool) {
	im.once.Do(func() {
		dir, err := os.MkdirTemp("", "mm-sequence-image-")
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

type rig struct {
	t    *testing.T
	st   *store.Store
	svc  *sequence.Service
	recs *records.Service
	fx   librarytest.L2Info
}

func newRig(t *testing.T) *rig { return newRigWith(t, false) }

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
	newID := func() string { n++; return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xe30000+n) }
	r := &rig{t: t, st: st, svc: sequence.New(st), recs: records.New(st, now, newID), fx: im.fx}
	r.exec(`DELETE FROM provenance_dirty`) // the fixture leaves its queue undrained; a test that wants a pending key writes one
	return r
}

func (r *rig) exec(q string, args ...any) {
	r.t.Helper()
	if _, err := r.st.W.Exec(q, args...); err != nil {
		r.t.Fatalf("%s: %v", q, err)
	}
}

func (r *rig) song(label string) string { return r.fx.Songs[label] }

func (r *rig) pickOf(songID string) string {
	var t *string
	_ = r.st.R.QueryRow(`SELECT take_id FROM song_picks WHERE song_id = ?`, songID).Scan(&t)
	if t == nil {
		return ""
	}
	return *t
}

func keyParts(k string) (int, string) {
	var n int
	var l string
	if _, err := fmt.Sscanf(k, "%d%1s", &n, &l); err != nil {
		panic(err)
	}
	return n, l
}

const digest64 = "0000000000000000000000000000000000000000000000000000000000000000"

func (r *rig) seedTake(trackID string, f feat) {
	r.t.Helper()
	n, l := keyParts(f.key)
	r.exec(`DELETE FROM take_features WHERE track_id = ?`, trackID)
	r.exec(`INSERT INTO take_features(track_id, bpm_x100, bpm_basis, energy_x10000, energy_basis, key_num, key_letter, key_basis, digest, feature_def, computed_at)
		VALUES(?,?, 'measured', ?, 'measured', ?, ?, 'measured', ?, 'mm-features/1', '2026-10-15T12:00:00.000Z')`, trackID, f.bpm, f.energy, n, l, digest64)
}

func (r *rig) seedSong(songID, subjectID string, f feat) {
	r.t.Helper()
	n, l := keyParts(f.key)
	r.exec(`DELETE FROM song_features WHERE song_id = ?`, songID)
	r.exec(`INSERT INTO song_features(song_id, subject_kind, subject_id, bpm_x100, bpm_basis, energy_x10000, energy_basis, key_num, key_letter, key_basis, digest, feature_def, computed_at)
		VALUES(?, 'take', ?, ?, 'measured', ?, 'measured', ?, ?, 'measured', ?, 'mm-features/1', '2026-10-15T12:00:00.000Z')`, songID, subjectID, f.bpm, f.energy, n, l, digest64)
}

// decoy is the reversed triple a take-based plan must not read from song_features.
func decoy(f feat) feat { return feat{bpm: 20000 - f.bpm, energy: 10000 - f.energy, key: f.key} }

// seedTSet gives S1..S6 the T-set features on their Pick takes and the decoy on the Songs themselves.
func (r *rig) seedTSet() {
	r.t.Helper()
	for label, f := range tSetFeat {
		song, pick := r.song(label), r.pickOf(r.song(label))
		if pick == "" {
			r.t.Fatalf("%s has no Pick take", label)
		}
		r.seedTake(pick, f)
		r.seedSong(song, pick, decoy(f))
	}
}

func (r *rig) newRecord(labels ...string) records.Record {
	r.t.Helper()
	ids := make([]string, len(labels))
	for i, l := range labels {
		ids[i] = r.song(l)
	}
	rec, err := r.recs.Create(context.Background(), agent, records.NewRecord{Title: "Synthetic Record Under Test", SongIDs: ids})
	if err != nil {
		r.t.Fatal(err)
	}
	return rec
}

func (r *rig) labels(songIDs []string) []string {
	byID := map[string]string{}
	for l, id := range r.fx.Songs {
		byID[id] = l
	}
	out := make([]string, len(songIDs))
	for i, id := range songIDs {
		out[i] = byID[id]
	}
	return out
}
