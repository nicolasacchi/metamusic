package atlas_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/atlas"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// The method set of C141.4 that lane AD keeps.
var _ func(*atlas.Service, context.Context, atlas.AdherenceQuery) (atlas.Adherence, error) = (*atlas.Service).Adherence

func TestAdherence_Formula(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		req, got int
		want     int // units of 0.01 %
	}{
		{7200, 14400, 0},     // asked 72, got 144: 0.00 %
		{7200, 8800, 2222},   // 22.22 %
		{7200, 10000, 3055},  // 30.55 %: the half-request comparison wins
		{12000, 12600, 500},  // 5.00 %, within
		{12000, 12700, 583},  // 5.83 %, not within
		{7600, 3800, 0},      // asked 76, got 38
		{10000, 10000, 0},    // exact
		{9000, 18000 - 1, 0}, // 17999 vs 2*9000: 1/18000 truncates to 0
	} {
		if got := atlas.TempoErrorX100ForTest(c.req, c.got); got != c.want {
			t.Errorf("TempoErrorX100(%d, %d) = %d, want %d", c.req, c.got, got, c.want)
		}
	}
	for _, c := range []struct {
		in   []int
		want int
	}{{[]int{5}, 5}, {[]int{9, 1, 5}, 5}, {[]int{1, 2}, 2}, {[]int{1, 4, 100, 7}, 6}} {
		if got := atlas.MedianIntForTest(c.in); got != c.want {
			t.Errorf("median(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

type adhLib struct {
	st   *store.Store
	svc  *atlas.Service
	info librarytest.L3Info
}

func newAdhLib(t *testing.T) *adhLib {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	info := librarytest.FixtureL3(t, st, librarytest.FixedNow)
	librarytest.InsertL3FeatureRows(t, st, librarytest.FixedNow, info)
	return &adhLib{st: st, svc: atlas.New(st, nil), info: info}
}

func (l *adhLib) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := l.st.W.ExecContext(context.Background(), q, args...); err != nil {
		t.Fatalf("%v\n%s", err, q)
	}
}

// pair rewrites the requested/measured tempo (centi-BPM), octave flag and durations of a fixture Song.
func (l *adhLib) pair(t *testing.T, label string, req, meas, oct int, dur, exp any) {
	t.Helper()
	l.exec(t, `UPDATE song_features SET bpm_requested_x100 = ?, bpm_measured_x100 = ?, bpm_octave = ?, duration_s = ?, duration_expected_s = ? WHERE song_id = ?`,
		req, meas, oct, dur, exp, l.info.Songs[label])
}

// adhScene gives the nine take Songs of the L3 fixture a known requested-versus-got picture.
func adhScene(t *testing.T) *adhLib {
	l := newAdhLib(t)
	l.pair(t, "T1", 7200, 14400, 1, 200.0, 200.0)                                                                        // reggae, 0.00 %, octave
	l.pair(t, "T2", 7200, 8800, 0, 205.0, 200.0)                                                                         // reggae, 22.22 %
	l.pair(t, "T3", 7200, 10000, 0, 210.0, 200.0)                                                                        // pop, 30.55 %
	l.pair(t, "T4", 12000, 12600, 0, 210.0, nil)                                                                         // pop, 5.00 % (no expected duration)
	l.pair(t, "T5", 12000, 12700, 0, nil, nil)                                                                           // dance, 5.83 %
	l.pair(t, "T6", 7600, 3800, 1, 150.0, 200.0)                                                                         // funk-soul, octave
	l.exec(t, `UPDATE song_features SET bpm_measured_x100 = NULL, bpm_octave = 0 WHERE song_id = ?`, l.info.Songs["T7"]) // jazz, no measurement
	l.pair(t, "u1", 10000, 10000, 0, 190.0, 200.0)                                                                       // pop, 0.00 %, one take, no Ears row in life but a literal row here
	return l
}

func adhJSON(t *testing.T, a atlas.Adherence) any {
	t.Helper()
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestAdherence_ByFacet(t *testing.T) {
	t.Parallel()
	l := adhScene(t)
	a, err := l.svc.Adherence(context.Background(), atlas.AdherenceQuery{MinN: 2, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "adherence_by_facet.golden.json", adhJSON(t, a))
	// default min_n 3: of the families only pop (T3, T4, u1) has three Songs
	a, err = l.svc.Adherence(context.Background(), atlas.AdherenceQuery{Facet: "family"})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.ByFacet) != 1 || a.ByFacet[0].Value != "pop" || a.ByFacet[0].N != 3 {
		t.Errorf("by_facet at min_n 3 = %+v", a.ByFacet)
	}
	// facet and value narrow every part
	a, err = l.svc.Adherence(context.Background(), atlas.AdherenceQuery{Facet: "family", Value: "reggae", MinN: 1})
	if err != nil {
		t.Fatal(err)
	}
	if a.Coverage.Songs != 2 || a.Summary.N != 2 || len(a.ByFacet) != 1 || len(a.Items) != 2 {
		t.Errorf("reggae: %+v", a)
	}
	golden(t, "adherence_reggae.golden.json", adhJSON(t, a))
}

func TestAdherence_Filters(t *testing.T) {
	t.Parallel()
	l := adhScene(t)
	ctx := context.Background()
	f := func(v float64) *float64 { return &v }
	b := func(v bool) *bool { return &v }
	ids := func(a atlas.Adherence) string {
		var s []string
		for _, it := range a.Items {
			s = append(s, it.TakeID)
		}
		return strings.Join(s, ",")
	}
	all, err := l.svc.Adherence(ctx, atlas.AdherenceQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Items) != 8 {
		t.Fatalf("items = %d, want the 8 Songs with a pair", len(all.Items))
	}
	for i := 1; i < len(all.Items); i++ {
		p, c := all.Items[i-1], all.Items[i]
		if p.ErrorPct < c.ErrorPct || p.ErrorPct == c.ErrorPct && p.SongID > c.SongID {
			t.Errorf("items %d, %d out of order: %v %v", i-1, i, p, c)
		}
	}
	a, _ := l.svc.Adherence(ctx, atlas.AdherenceQuery{MinErrorPct: f(5.5)})
	if len(a.Items) != 3 || a.Summary.N != 8 {
		t.Errorf("min_error_pct 5.5: %d items, summary n %d (the summary ignores the item filters)", len(a.Items), a.Summary.N)
	}
	a, _ = l.svc.Adherence(ctx, atlas.AdherenceQuery{Octave: b(true)})
	if len(a.Items) != 2 {
		t.Errorf("octave true: %s", ids(a))
	}
	a, _ = l.svc.Adherence(ctx, atlas.AdherenceQuery{Octave: b(false)})
	if len(a.Items) != 6 {
		t.Errorf("octave false: %s", ids(a))
	}
	// keyset paging reproduces the full list
	var got []string
	cursor := ""
	for range 10 {
		a, err := l.svc.Adherence(ctx, atlas.AdherenceQuery{Limit: 3, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range a.Items {
			got = append(got, it.SongID)
		}
		if a.NextCursor == nil {
			break
		}
		cursor = *a.NextCursor
	}
	var want []string
	for _, it := range all.Items {
		want = append(want, it.SongID)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("paged %v, want %v", got, want)
	}
	// validation
	for _, q := range []atlas.AdherenceQuery{{Facet: "bogus"}, {Value: "pop"}, {Cursor: "!!"}} {
		_, err := l.svc.Adherence(ctx, q)
		var pe *problem.Error
		if !errors.As(err, &pe) || pe.Status != 422 {
			t.Errorf("%+v: err = %v, want 422", q, err)
		}
	}
}

func TestAdherence_OctaveIsCompareTempo(t *testing.T) {
	t.Parallel()
	l := adhScene(t)
	a, err := l.svc.Adherence(context.Background(), atlas.AdherenceQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range a.Items {
		var flag int
		if err := l.st.R.QueryRow(`SELECT bpm_octave FROM song_features WHERE song_id = ?`, it.SongID).Scan(&flag); err != nil {
			t.Fatal(err)
		}
		if it.Octave != (flag == 1) {
			t.Errorf("%s: octave %v, song_features.bpm_octave %d", it.SongID, it.Octave, flag)
		}
	}
	// asked 72, got 100 is not an octave although its error is below the plain error
	for _, it := range a.Items {
		if it.SongID == l.info.Songs["T3"] && (it.Octave || it.ErrorPct != 30.55) {
			t.Errorf("asked 72 got 100 = %+v", it)
		}
	}
}

func TestAdherence_UsesPickTake(t *testing.T) {
	t.Parallel()
	l := adhScene(t)
	// one row per Song, naming the take the Song is read from (its Pick take)
	a, err := l.svc.Adherence(context.Background(), atlas.AdherenceQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, it := range a.Items {
		if it.SongID == l.info.Songs["T1"] {
			n++
			if it.TakeID != l.info.Takes["T1"] {
				t.Errorf("take = %s, want the Pick take %s", it.TakeID, l.info.Takes["T1"])
			}
		}
	}
	if n != 1 {
		t.Errorf("T1 appears %d times", n)
	}
}

func TestAdherence_PopulationScope(t *testing.T) {
	t.Parallel()
	l := adhScene(t)
	a, _ := l.svc.Adherence(context.Background(), atlas.AdherenceQuery{})
	// the nine take Songs; the five draft Songs have a draft subject and are not units
	if a.Coverage.Songs != 9 || a.Coverage.WithPair != 8 {
		t.Errorf("coverage = %+v", a.Coverage)
	}
	// a retired Song leaves the population even while a stale take-subject row of it exists (a draft Song has no takes, so
	// it is the one a test can retire)
	d1 := l.info.Songs["d1"]
	l.exec(t, `UPDATE song_features SET subject_kind = 'take', bpm_requested_x100 = 9000, bpm_measured_x100 = 9100 WHERE song_id = ?`, d1)
	if a, _ = l.svc.Adherence(context.Background(), atlas.AdherenceQuery{}); a.Coverage.Songs != 10 {
		t.Fatalf("stale take row: %+v", a.Coverage)
	}
	l.exec(t, `UPDATE songs SET retired_at = ?, retired_into = ? WHERE id = ?`, "2026-10-01T00:00:00.000Z", l.info.Songs["T1"], d1)
	if a, _ = l.svc.Adherence(context.Background(), atlas.AdherenceQuery{}); a.Coverage.Songs != 9 {
		t.Errorf("after retiring: %+v", a.Coverage)
	}
}

func TestAdherence_DurationRatio(t *testing.T) {
	t.Parallel()
	l := adhScene(t)
	a, err := l.svc.Adherence(context.Background(), atlas.AdherenceQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]*float64{}
	r := func(v float64) *float64 { return &v }
	want[l.info.Songs["T1"]] = r(1)
	want[l.info.Songs["T2"]] = r(1.025)
	want[l.info.Songs["T3"]] = r(1.05)
	want[l.info.Songs["T4"]] = nil
	want[l.info.Songs["T5"]] = nil
	want[l.info.Songs["T6"]] = r(0.75)
	want[l.info.Songs["T8"]] = nil // no expected duration
	want[l.info.Songs["u1"]] = r(0.95)
	for _, it := range a.Items {
		w, ok := want[it.SongID]
		if !ok {
			t.Errorf("unexpected item %s", it.SongID)
			continue
		}
		if (w == nil) != (it.DurationRatio == nil) || w != nil && *w != *it.DurationRatio {
			t.Errorf("%s: duration_ratio = %v, want %v", it.SongID, it.DurationRatio, w)
		}
	}
	if a.Coverage.WithDuration != 5 { // T1, T2, T3, T6, u1
		t.Errorf("with_duration = %d", a.Coverage.WithDuration)
	}
	if strings.Join(a.DimsAvailable, ",") != "bpm,duration" {
		t.Errorf("dims = %v", a.DimsAvailable)
	}
}

func TestAdherence_NoPairs(t *testing.T) {
	t.Parallel()
	l := newAdhLib(t)
	l.exec(t, `UPDATE song_features SET bpm_measured_x100 = NULL, bpm_octave = 0, duration_s = NULL, duration_expected_s = NULL`)
	a, err := l.svc.Adherence(context.Background(), atlas.AdherenceQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.DimsAvailable) != 0 || len(a.Items) != 0 || len(a.ByFacet) != 0 || a.NextCursor != nil || a.Summary.N != 0 || a.Coverage.WithPair != 0 {
		t.Errorf("no pairs: %+v", a)
	}
	b, _ := json.Marshal(a)
	if !strings.Contains(string(b), `"dims_available":[]`) || !strings.Contains(string(b), `"items":[]`) || !strings.Contains(string(b), `"by_facet":[]`) {
		t.Errorf("empty lists must serialise as []: %s", b)
	}
}

func TestAdherence_NoMuseText(t *testing.T) {
	t.Parallel()
	l := adhScene(t)
	a, err := l.svc.Adherence(context.Background(), atlas.AdherenceQuery{MinN: 1, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(a)
	for _, s := range []string{librarytest.L3Muse3Title, librarytest.L3Muse4Title, librarytest.L3Muse5Title, librarytest.L3Muse3Artist, librarytest.L3Muse5Artist} {
		if strings.Contains(string(b), s) {
			t.Errorf("the answer carries Muse text %q", s)
		}
	}
}
