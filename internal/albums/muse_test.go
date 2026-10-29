package albums_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/albums"
	"github.com/nicolasacchi/metamusic/internal/curation"
	"github.com/nicolasacchi/metamusic/internal/media/cover"
	"github.com/nicolasacchi/metamusic/internal/records"
)

// sqlDir is a MuseDirectory read straight from the muses table of the test database (the real adapter lives in
// internal/muses and internal/app; albums cannot import either). It counts every call.
type sqlDir struct {
	db                         *sql.DB
	ordinals, versions, labels atomic.Int64
	panicLabels                bool
}

func (d *sqlDir) Ordinals(ctx context.Context) (map[string]albums.MuseMeta, error) {
	d.ordinals.Add(1)
	rows, err := d.db.QueryContext(ctx, `SELECT id, ordinal, hidden_at IS NOT NULL FROM muses`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]albums.MuseMeta{}
	for rows.Next() {
		var id string
		var m albums.MuseMeta
		if err := rows.Scan(&id, &m.Ordinal, &m.Hidden); err != nil {
			return nil, err
		}
		out[id] = m
	}
	return out, rows.Err()
}

func (d *sqlDir) Version(ctx context.Context) (string, error) {
	d.versions.Add(1)
	var v string
	err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) || '|' || COALESCE(MAX(updated_at), '') FROM muses`).Scan(&v)
	return v, err
}

func (d *sqlDir) Labels(ctx context.Context, ids []string) (map[string]string, error) {
	d.labels.Add(1)
	if d.panicLabels {
		panic("the catalog asked the directory for Labels with the opt-in off")
	}
	out := map[string]string{}
	for _, id := range ids {
		var title string
		var artist sql.NullString
		if err := d.db.QueryRowContext(ctx, `SELECT title, artist FROM muses WHERE id = ?`, id).Scan(&title, &artist); err != nil {
			return nil, err
		}
		out[id] = title
		if artist.Valid {
			out[id] = title + " · " + artist.String
		}
	}
	return out, nil
}

// autoWorld is a FixtureL2 catalog with a SQL-backed directory.
func autoWorld(t *testing.T, panicLabels bool) (*world, *sqlDir) {
	t.Helper()
	w := newWorld(t, nil)
	d := &sqlDir{db: w.St.R, panicLabels: panicLabels}
	w.Svc = albums.New(w.R, records.New(w.R, nil, nil), d, w.Set, nil)
	return w, d
}

func (w *world) setFlag(t *testing.T, on bool) {
	t.Helper()
	w.exec(t, `INSERT INTO settings(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, curation.KeySubsonic, fmt.Sprint(on))
}

func (w *world) unpublish(t *testing.T, recordID string) {
	t.Helper()
	w.exec(t, `DROP TRIGGER IF EXISTS records_status_path`)
	w.exec(t, `UPDATE records SET status = 'draft', published_by = NULL, published_at = NULL WHERE id = ?`, recordID)
}

// TestAlbums_MembershipTable_Auto: the muse and unfiled rows of C126.1 on FixtureL2 (Muse M1 holds S1..S3, M2 holds S4 and
// S5, S6 has no Muse, S7 is a takes-less draft; R1 publishes S2..S5).
func TestAlbums_MembershipTable_Auto(t *testing.T) {
	t.Parallel()
	type want = map[string][]string // album key (M1, M2, unf) to take labels
	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, w *world)
		want   want
	}{
		{name: "fixture: published Songs are out, the rest stay", want: want{"M1": {"s1a"}, "unf": {"s6a"}}},
		{name: "unpublished R1: every Pick of every Muse, in Song order",
			mutate: func(t *testing.T, w *world) { w.unpublish(t, w.Info.Records["R1"]) },
			want:   want{"M1": {"s1a", "a1", "b1"}, "M2": {"c1", "e1"}, "unf": {"s6a"}}},
		{name: "a hidden published Record does not take Songs out of the albums",
			mutate: func(t *testing.T, w *world) {
				w.exec(t, `UPDATE records SET hidden_at = '2026-10-28T10:00:00.000Z', hidden_by = 'human:fixture' WHERE id = ?`, w.Info.Records["R1"])
			},
			want: want{"M1": {"s1a", "a1", "b1"}, "M2": {"c1", "e1"}, "unf": {"s6a"}}},
		{name: "a draft Record takes nothing out",
			mutate: func(t *testing.T, w *world) {
				w.exec(t, `UPDATE record_slots SET removed_by = 'x', removed_at = '2026-10-28T10:00:00.000Z' WHERE record_id = ?`, w.Info.Records["R1"])
			},
			want: want{"M1": {"s1a", "a1", "b1"}, "M2": {"c1", "e1"}, "unf": {"s6a"}}},
		{name: "publishing R2 moves its Songs out of M1 and unf: the albums vanish",
			mutate: func(t *testing.T, w *world) { w.publish(t, w.Info.Records["R2"]) },
			want:   want{}},
		{name: "a hidden Muse has no album", mutate: func(t *testing.T, w *world) {
			w.exec(t, `UPDATE muses SET hidden_at = '2026-10-28T10:00:00.000Z', hidden_by = 'human:fixture' WHERE id = ?`, w.Info.Muses["M1"])
		}, want: want{"unf": {"s6a"}}},
		{name: "a hidden Song is excluded", mutate: func(t *testing.T, w *world) {
			w.exec(t, `UPDATE songs SET hidden_at = '2026-10-28T10:00:00.000Z', hidden_by = 'x' WHERE id IN (?, ?)`, w.Info.Songs["S1"], w.Info.Songs["S6"])
		}, want: want{}},
		{name: "a discarded Pick is never listed", mutate: func(t *testing.T, w *world) {
			w.exec(t, `UPDATE tracks SET status = 'discarded', status_set_by = 'x' WHERE id IN (?, ?)`, w.Info.Takes["s1a"], w.Info.Takes["s6a"])
		}, want: want{}},
		{name: "a Muse-less Song assigned to M2 leaves unf", mutate: func(t *testing.T, w *world) {
			w.exec(t, `UPDATE songs SET muse_id = ?, muse_source = 'manual' WHERE id = ?`, w.Info.Muses["M2"], w.Info.Songs["S6"])
		}, want: want{"M1": {"s1a"}, "M2": {"s6a"}}},
		{name: "a Song whose Muse is cleared joins unf in Song order", mutate: func(t *testing.T, w *world) {
			w.exec(t, `UPDATE songs SET muse_id = NULL, muse_source = NULL WHERE id = ?`, w.Info.Songs["S1"])
		}, want: want{"unf": {"s1a", "s6a"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w, _ := autoWorld(t, true)
			if tc.mutate != nil {
				tc.mutate(t, w)
			}
			ids := map[string]string{"M1": albums.PrefixMuse + w.Info.Muses["M1"], "M2": albums.PrefixMuse + w.Info.Muses["M2"], "unf": albums.IDUnfiled}
			got := list(t, w, albums.KindMuse, albums.KindUnfiled)
			listed := map[string]bool{}
			for _, a := range got {
				listed[a.ID] = true
			}
			for key, id := range ids {
				labels, has := tc.want[key]
				if listed[id] != has {
					t.Errorf("%s listed = %v, want %v (%+v)", key, listed[id], has, got)
				}
				if !has {
					if _, _, err := w.Svc.Get(context.Background(), id); !errors.Is(err, albums.ErrNotFound) {
						t.Errorf("Get(%s) = %v, want ErrNotFound", key, err)
					}
					continue
				}
				a, songs, err := w.Svc.Get(context.Background(), id)
				if err != nil {
					t.Fatal(err)
				}
				var wantIDs []string
				for _, l := range labels {
					wantIDs = append(wantIDs, w.Info.Takes[l])
				}
				if d := cmp.Diff(wantIDs, takeIDs(songs)); d != "" {
					t.Errorf("%s songs (-want +got):\n%s", key, d)
				}
				for i, s := range songs {
					if s.Position != i+1 || s.Disc != 1 || s.SlotID != nil {
						t.Errorf("%s song %d = %+v", key, i, s)
					}
				}
				if a.SongCount != len(songs) || a.RecordID != nil || (key == "unf") != (a.MuseID == nil) {
					t.Errorf("%s album = %+v", key, a)
				}
			}
		})
	}
}

// TestAlbums_AutoOrderAndShape: List orders record, muse by ordinal, unfiled; the kinds filter and the cover input hold.
func TestAlbums_AutoOrderAndShape(t *testing.T) {
	t.Parallel()
	w, _ := autoWorld(t, true)
	w.unpublish(t, w.Info.Records["R1"])
	w.publish(t, w.Info.Records["R2"]) // S1 and S6 leave, so M1 keeps S2, S3 and M2 keeps S4, S5
	got := list(t, w)
	var ids []string
	for _, a := range got {
		ids = append(ids, a.ID)
	}
	wantIDs := []string{albums.PrefixRecord + w.Info.Records["R2"], albums.PrefixMuse + w.Info.Muses["M1"], albums.PrefixMuse + w.Info.Muses["M2"]}
	if d := cmp.Diff(wantIDs, ids); d != "" {
		t.Fatalf("order (-want +got):\n%s", d)
	}
	if k := list(t, w, albums.KindMuse); len(k) != 2 || k[0].Kind != albums.KindMuse || k[1].SongCount != 2 {
		t.Errorf("muse kind = %+v", k)
	}
	if k := list(t, w, albums.KindUnfiled); len(k) != 0 {
		t.Errorf("unfiled = %+v", k)
	}
	m1 := got[1]
	if m1.Name != "Inspiration 001" || m1.SortName != "inspiration 001" || m1.MuseID == nil || *m1.MuseID != w.Info.Muses["M1"] || m1.RecordID != nil {
		t.Errorf("M1 = %+v", m1)
	}
	if m1.CoverInput.Kind != cover.KindAlbum || m1.CoverInput.ID != m1.ID || m1.CoverInput.Seed != 0 || len(m1.CoverInput.Parts) != 2 || m1.CoverInput.Parts[0].RootID == "" {
		t.Errorf("cover input = %+v", m1.CoverInput)
	}
	// unfiled is last
	w.unpublish(t, w.Info.Records["R2"])
	all := list(t, w)
	if last := all[len(all)-1]; last.ID != albums.IDUnfiled || last.Name != albums.NameUnfiled || last.SortName != "unfiled" || last.CoverInput.ID != albums.IDUnfiled {
		t.Errorf("last = %+v", last)
	}
}

// TestAlbums_NeutralNames: the name is the frozen ordinal, zero-padded to three digits and growing past 999; hidden Muses
// keep their number and nothing of the Muse leaks into the name.
func TestAlbums_NeutralNames(t *testing.T) {
	t.Parallel()
	for ord, want := range map[int]string{1: "Inspiration 001", 42: "Inspiration 042", 999: "Inspiration 999", 1000: "Inspiration 1000", 12345: "Inspiration 12345"} {
		if got := albums.NeutralName(ord); got != want {
			t.Errorf("NeutralName(%d) = %q, want %q", ord, got, want)
		}
	}
	w, _ := autoWorld(t, true)
	w.unpublish(t, w.Info.Records["R1"])
	w.exec(t, `UPDATE muses SET hidden_at = '2026-10-28T10:00:00.000Z', hidden_by = 'x' WHERE id = ?`, w.Info.Muses["M1"])
	var names []string
	for _, a := range list(t, w, albums.KindMuse) {
		names = append(names, a.Name)
	}
	if d := cmp.Diff([]string{"Inspiration 002"}, names); d != "" {
		t.Errorf("M2 keeps its own number when M1 is hidden (-want +got):\n%s", d)
	}
	// a new Muse takes the next number whatever was hidden
	w.exec(t, `INSERT INTO muses(id, kind, title, title_fold, key, source, created_by, created_at, updated_by, updated_at) VALUES ('mx', 'reference', 'Synthetic New', 'synthetic new', 'k-new', 'human', 'x', '2026-10-28T10:00:00.000Z', 'x', '2026-10-28T10:00:00.000Z')`)
	w.exec(t, `UPDATE songs SET muse_id = 'mx', muse_source = 'manual' WHERE id = ?`, w.Info.Songs["S6"])
	got := list(t, w, albums.KindMuse)
	if got[len(got)-1].ID != "mus-mx" || got[len(got)-1].Name != "Inspiration 003" {
		t.Errorf("new Muse album = %+v", got)
	}
	// 1000 and up through a directory that reports them
	w.Svc = albums.New(w.R, records.New(w.R, nil, nil), bigDir{w: w}, w.Set, nil)
	big := list(t, w, albums.KindMuse)
	if len(big) != 2 || big[0].Name != "Inspiration 999" || big[1].Name != "Inspiration 1000" {
		t.Errorf("big ordinals = %+v", big)
	}
}

// bigDir reports ordinals 999 and 1000 for M1 and M2.
type bigDir struct{ w *world }

func (b bigDir) Ordinals(context.Context) (map[string]albums.MuseMeta, error) {
	return map[string]albums.MuseMeta{b.w.Info.Muses["M1"]: {Ordinal: 999}, b.w.Info.Muses["M2"]: {Ordinal: 1000}}, nil
}
func (bigDir) Version(context.Context) (string, error) { return "v", nil }
func (bigDir) Labels(context.Context, []string) (map[string]string, error) {
	panic("Labels with the opt-in off")
}

// TestAlbums_MuseLabelsNotCalledWhenOff: with the flag off the directory's Labels (which panics) is never reached, by List,
// Get or Token; a failing settings read does not fall through to Labels either.
func TestAlbums_MuseLabelsNotCalledWhenOff(t *testing.T) {
	t.Parallel()
	w, d := autoWorld(t, true)
	w.unpublish(t, w.Info.Records["R1"])
	ctx := context.Background()
	list(t, w)
	if _, _, err := w.Svc.Get(ctx, albums.PrefixMuse+w.Info.Muses["M1"]); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Svc.Token(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := w.Svc.MuseAlbum(ctx, w.Info.Muses["M1"]); err != nil || !ok {
		t.Fatalf("MuseAlbum = %v, %v", ok, err)
	}
	w.setFlag(t, false)
	list(t, w)
	if d.labels.Load() != 0 || d.ordinals.Load() == 0 {
		t.Errorf("labels %d ordinals %d", d.labels.Load(), d.ordinals.Load())
	}
	// no settings at all behaves as off
	w.Svc = albums.New(w.R, records.New(w.R, nil, nil), d, nil, nil)
	list(t, w)
	if d.labels.Load() != 0 {
		t.Error("nil settings must mean off")
	}
}

// TestAlbums_OptInNames: only with subsonic_muse_names on, a muse album is named by the Muse (title, or title · artist for a
// reference Muse); one Labels call serves the whole list, and unfiled keeps its name.
func TestAlbums_OptInNames(t *testing.T) {
	t.Parallel()
	w, d := autoWorld(t, false)
	w.unpublish(t, w.Info.Records["R1"])
	w.setFlag(t, true)
	ctx := context.Background()
	got := list(t, w)
	names := map[string]string{}
	for _, a := range got {
		names[a.ID] = a.Name
	}
	m1, m2 := albums.PrefixMuse+w.Info.Muses["M1"], albums.PrefixMuse+w.Info.Muses["M2"]
	var m1Artist sql.NullString
	var m1Title string
	if err := w.St.R.QueryRow(`SELECT title, artist FROM muses WHERE id = ?`, w.Info.Muses["M1"]).Scan(&m1Title, &m1Artist); err != nil {
		t.Fatal(err)
	}
	want1 := m1Title
	if m1Artist.Valid {
		want1 += " · " + m1Artist.String
	}
	if names[m1] != want1 || !strings.Contains(names[m1], "Synthetic") || !strings.Contains(names[m2], "Synthetic") || names[albums.IDUnfiled] != "Unfiled" {
		t.Errorf("names = %v", names)
	}
	if d.labels.Load() != 1 {
		t.Errorf("Labels called %d times for one List, want 1", d.labels.Load())
	}
	a, _, err := w.Svc.Get(ctx, m1)
	if err != nil || a.Name != want1 {
		t.Errorf("Get name = %q, %v", a.Name, err)
	}
	if a.SortName != strings.ToLower(strings.Join(strings.Fields(want1), " ")) && a.SortName == "" {
		t.Errorf("sort name %q", a.SortName)
	}
	// a brief Muse (no artist) is named by its title alone
	w.exec(t, `UPDATE muses SET artist = NULL WHERE id = ?`, w.Info.Muses["M1"])
	if a, _, _ := w.Svc.Get(ctx, m1); a.Name != m1Title {
		t.Errorf("no artist: %q", a.Name)
	}
	// a Muse with no label falls back to the neutral name
	w.Svc = albums.New(w.R, records.New(w.R, nil, nil), emptyLabels{d}, w.Set, nil)
	if a, _, _ := w.Svc.Get(ctx, m1); a.Name != "Inspiration 001" {
		t.Errorf("missing label: %q", a.Name)
	}
	// the neutral name for get_muse.catalog never depends on the flag
	ma, ok, err := w.Svc.MuseAlbum(ctx, w.Info.Muses["M1"])
	if err != nil || !ok || ma.ID != m1 || ma.NeutralName != "Inspiration 001" {
		t.Errorf("MuseAlbum = %+v %v %v", ma, ok, err)
	}
}

type emptyLabels struct{ *sqlDir }

func (emptyLabels) Labels(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}

// TestAlbums_MuseAlbum: the catalog reference of get_muse: present while the Muse has an album, absent when it is empty or
// hidden or unknown, and without a directory.
func TestAlbums_MuseAlbum(t *testing.T) {
	t.Parallel()
	w, _ := autoWorld(t, true)
	ctx := context.Background()
	if _, ok, _ := w.Svc.MuseAlbum(ctx, w.Info.Muses["M2"]); ok {
		t.Error("M2 has no Song outside R1")
	}
	if _, ok, _ := w.Svc.MuseAlbum(ctx, "nope"); ok {
		t.Error("unknown Muse")
	}
	w.unpublish(t, w.Info.Records["R1"])
	if _, ok, err := w.Svc.MuseAlbum(ctx, w.Info.Muses["M2"]); !ok || err != nil {
		t.Errorf("M2 = %v, %v", ok, err)
	}
	w.exec(t, `UPDATE muses SET hidden_at = '2026-10-28T10:00:00.000Z', hidden_by = 'x' WHERE id = ?`, w.Info.Muses["M2"])
	if _, ok, _ := w.Svc.MuseAlbum(ctx, w.Info.Muses["M2"]); ok {
		t.Error("hidden Muse")
	}
	nodir := newWorld(t, nil)
	nodir.unpublish(t, nodir.Info.Records["R1"])
	if _, ok, err := nodir.Svc.MuseAlbum(ctx, nodir.Info.Muses["M1"]); ok || err != nil {
		t.Errorf("no directory: %v %v", ok, err)
	}
	var nilSvc *albums.Service
	if _, ok, err := nilSvc.MuseAlbum(ctx, "x"); ok || err != nil {
		t.Errorf("nil service: %v %v", ok, err)
	}
}

// TestAlbums_AutoErrors: a failing directory, a failing reader and a failing settings read surface as errors.
func TestAlbums_AutoErrors(t *testing.T) {
	t.Parallel()
	w, d := autoWorld(t, false)
	ctx := context.Background()
	w.Svc = albums.New(w.R, records.New(w.R, nil, nil), failingDir{}, w.Set, nil)
	if _, err := w.Svc.List(ctx, albums.KindMuse); !errors.Is(err, errBoom) {
		t.Errorf("List: %v", err)
	}
	if _, _, err := w.Svc.Get(ctx, albums.PrefixMuse+w.Info.Muses["M1"]); !errors.Is(err, errBoom) {
		t.Errorf("Get: %v", err)
	}
	if _, _, err := w.Svc.MuseAlbum(ctx, w.Info.Muses["M1"]); !errors.Is(err, errBoom) {
		t.Errorf("MuseAlbum: %v", err)
	}
	w.unpublish(t, w.Info.Records["R1"])
	w.Svc = albums.New(w.R, records.New(w.R, nil, nil), d, failingSettings{}, nil)
	if _, err := w.Svc.List(ctx, albums.KindMuse); err == nil {
		t.Error("a failing settings read must fail List")
	}
	w.Svc = albums.New(w.R, records.New(w.R, nil, nil), labelsFail{d}, w.Set, nil)
	w.setFlag(t, true)
	if _, err := w.Svc.List(ctx, albums.KindMuse); !errors.Is(err, errBoom) {
		t.Errorf("Labels failure: %v", err)
	}
}

type labelsFail struct{ *sqlDir }

func (labelsFail) Labels(context.Context, []string) (map[string]string, error) { return nil, errBoom }

// TestAlbums_AutoStatementCount: the whole catalog, auto kinds and opt-in names included, stays within six statements.
func TestAlbums_AutoStatementCount(t *testing.T) {
	t.Parallel()
	w, _ := autoWorld(t, false)
	w.unpublish(t, w.Info.Records["R1"])
	w.setFlag(t, true)
	before := w.N.Load()
	list(t, w)
	if got := w.N.Load() - before; got > 6 || got < 1 {
		t.Errorf("List: %d statements through the catalog reader, want 1..6", got)
	}
}

// TestAlbums_TokenMuseChanges: a Muse's membership and names move the token with the real directory behaviour.
func TestAlbums_TokenMuseChanges(t *testing.T) {
	t.Parallel()
	w, d := autoWorld(t, true)
	ctx := context.Background()
	base, err := w.Svc.Token(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		name string
		do   func()
	}{
		{"a new Muse", func() {
			w.exec(t, `INSERT INTO muses(id, kind, title, title_fold, key, source, created_by, created_at, updated_by, updated_at) VALUES ('mx', 'reference', 'Synthetic New', 'synthetic new', 'k-new', 'human', 'x', '2026-10-28T10:00:00.000Z', 'x', '2026-10-28T10:00:00.000Z')`)
		}},
		{"a hidden Muse", func() {
			w.exec(t, `UPDATE muses SET hidden_at = '2026-10-28T11:00:00.000Z', hidden_by = 'x', updated_at = '2026-10-28T11:00:00.000Z' WHERE id = ?`, w.Info.Muses["M1"])
		}},
		{"a Muse rename", func() {
			w.exec(t, `UPDATE muses SET title = 'Synthetic Renamed', updated_at = '2026-10-28T12:00:00.000Z' WHERE id = ?`, w.Info.Muses["M2"])
		}},
		{"a publish", func() { w.publish(t, w.Info.Records["R2"]) }},
	} {
		step.do()
		got, err := w.Svc.Token(ctx)
		if err != nil || got == base {
			t.Errorf("%s: token %q (%v) did not move from %q", step.name, got, err, base)
		}
		base = got
	}
	if d.ordinals.Load() != 0 || d.labels.Load() != 0 {
		t.Errorf("Token called Ordinals %d / Labels %d", d.ordinals.Load(), d.labels.Load())
	}
}
