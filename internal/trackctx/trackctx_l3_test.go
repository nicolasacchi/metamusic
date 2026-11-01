package trackctx_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

// fakeNA is a NextActionReader that returns a fixed list for every take, whatever limit it was asked for, so the assembly's own
// cap is what the tests exercise. It records the limit and the number of calls.
type fakeNA struct {
	out   []string
	err   error
	limit int
	calls int
}

func (f *fakeNA) ForTake(_ context.Context, _ auth.Principal, _ string, limit int) ([]string, error) {
	f.calls++
	f.limit = limit
	return f.out, f.err
}

const critique = "record your critique as a comment (add_comment) — you cannot hear: see perception.blind_spots"

// TestContext_NextActions_L3: the worked cases of C139.4. The Muse-aware strings go after the base rules and before the generation
// hint, at most min(2, 5 - len(base) - 1) of them, the critique stays last and the list never exceeds five.
func TestContext_NextActions_L3(t *testing.T) {
	t.Parallel()
	e := newEnvM3(t)
	// The base of each track is read first, so the cases state what they build on.
	base := func(track string) []string {
		t.Helper()
		e.b.UseNextActions(nil)
		return nextActions(t, e, generator, track)
	}
	rules := func(track string) []string { // the base rules only: no hint, no critique
		b := base(track)
		return slices.DeleteFunc(slices.Clone(b[:len(b)-1]), func(s string) bool { return s == hintText })
	}
	lunaBase, twinBase, shortBase := rules(lt.TrackLuna), rules(lt.TrackTwinA), rules(lt.TrackShort)
	if len(lunaBase) != 1 || len(twinBase) != 2 || len(shortBase) != 3 {
		t.Fatalf("bases: %v %v %v, want 1, 2 and 3 rules", lunaBase, twinBase, shortBase)
	}

	tests := []struct {
		name     string
		track    string
		out      []string
		wantMax  int // limit the reader must be asked for; 0 = not called
		want     []string
		wantHint bool
	}{
		{"base of one, a duel and an unjudged Muse: both kept, then the hint", lt.TrackLuna, []string{"duel", "unjudged"}, 2,
			append(slices.Clone(lunaBase), "duel", "unjudged", hintText, critique), true},
		{"base of one, three eligible: the two highest priority are kept, then the hint", lt.TrackLuna, []string{"a", "b", "c"}, 2,
			append(slices.Clone(lunaBase), "a", "b", hintText, critique), true},
		{"base of two, three eligible: two kept, the hint no longer fits", lt.TrackTwinA, []string{"a", "b", "c"}, 2,
			append(slices.Clone(twinBase), "a", "b", critique), false},
		{"base of three: one slot left", lt.TrackShort, []string{"a", "b", "c"}, 1,
			append(slices.Clone(shortBase), "a", critique), false},
		{"nothing eligible (below the floor or no Song): the hint stays", lt.TrackLuna, nil, 2,
			append(slices.Clone(lunaBase), hintText, critique), true},
	}
	for _, tc := range tests {
		r := &fakeNA{out: tc.out}
		e.b.UseNextActions(r)
		got := nextActions(t, e, generator, tc.track)
		if diff := cmp.Diff(tc.want, got); diff != "" {
			t.Errorf("%s (-want +got):\n%s", tc.name, diff)
		}
		if len(got) > 5 || got[len(got)-1] != critique || slices.Contains(got, hintText) != tc.wantHint {
			t.Errorf("%s: cap, critique or hint broken: %v", tc.name, got)
		}
		if r.limit != tc.wantMax {
			t.Errorf("%s: reader asked for %d, want %d", tc.name, r.limit, tc.wantMax)
		}
	}
}

// TestContext_NextActionsCap_StillFive: with all four base rules the list is full (four plus the critique), the reader is not
// even asked, and the existing result is unchanged.
func TestContext_NextActionsCap_StillFive(t *testing.T) {
	t.Parallel()
	e := newEnvM3(t)
	e.exec(t, `INSERT INTO comments(id, track_id, body, kind, state, source, principal_kind, principal_id, principal_name, created_at, updated_at)
		VALUES('c-fix-l3', ?, 'fix the intro', 'fix', 'open', 'api', 'human', 'human:fixture', 'fixture', '2026-10-06T10:00:00Z', '2026-10-06T10:00:00Z')`, lt.TrackShort)
	e.b.UseNextActions(nil)
	want := nextActions(t, e, generator, lt.TrackShort)
	r := &fakeNA{out: []string{"a", "b", "c"}}
	e.b.UseNextActions(r)
	got := nextActions(t, e, generator, lt.TrackShort)
	if len(got) != 5 || r.calls != 0 || !slices.Equal(got, want) {
		t.Errorf("full base: %v (reader called %d times), want %v untouched", got, r.calls, want)
	}
	// and a reader that over-delivers never pushes the list past five
	e.b.UseNextActions(&fakeNA{out: []string{"a", "b", "c", "d", "e", "f"}})
	for _, tr := range []string{lt.TrackLuna, lt.TrackCitta, lt.TrackTwinA, lt.TrackShort} {
		if got := nextActions(t, e, generator, tr); len(got) > 5 || got[len(got)-1] != critique {
			t.Errorf("%s: %v", tr, got)
		}
	}
}

// TestContext_NextActions_FloorKeepsHint: the case that motivated the floor. The reader applies the priority floor, so a take whose
// only eligible rule is below it gets nothing from the reader and keeps the generation hint.
func TestContext_NextActions_FloorKeepsHint(t *testing.T) {
	t.Parallel()
	e := newEnvM3(t)
	e.b.UseNextActions(nil)
	base := nextActions(t, e, generator, lt.TrackLuna)
	e.b.UseNextActions(&fakeNA{out: []string{}})
	if got := nextActions(t, e, generator, lt.TrackLuna); !slices.Equal(got, base) || !slices.Contains(got, hintText) {
		t.Errorf("empty reader: %v, want %v with the hint", got, base)
	}
	// a failing reader is the same
	e.b.UseNextActions(&fakeNA{err: errors.New("boom")})
	if got := nextActions(t, e, generator, lt.TrackLuna); !slices.Equal(got, base) {
		t.Errorf("failing reader: %v, want %v", got, base)
	}
}

// TestContext_NextActions_UnchangedWithoutSong: a reader with nothing to say (a take with no Song or Muse) leaves every bundle
// byte-identical to the one built without a reader.
func TestContext_NextActions_UnchangedWithoutSong(t *testing.T) {
	t.Parallel()
	e := newEnvM3(t)
	for _, p := range []auth.Principal{agent, generator} {
		for _, tr := range []string{lt.TrackLuna, lt.TrackCitta, lt.TrackTwinA, lt.TrackTwinB, lt.TrackShort, lt.TrackJobChild, lt.TrackRefFlac, lt.TrackLossy} {
			e.b.UseNextActions(nil)
			b0, err := e.b.Build(context.Background(), p, tr, nil)
			if err != nil {
				t.Fatal(err)
			}
			e.b.UseNextActions(&fakeNA{})
			b1, err := e.b.Build(context.Background(), p, tr, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(marshal(t, b0), marshal(t, b1)) {
				t.Errorf("%s/%s: bundle changed by an empty reader", p.Name, tr)
			}
		}
	}
}

// TestContext_Goldens_PreL3Unchanged: with an empty reader installed the shipped golden bundles are still exact.
func TestContext_Goldens_PreL3Unchanged(t *testing.T) {
	t.Parallel()
	e := newEnvM3(t)
	e.b.UseNextActions(&fakeNA{})
	b, err := e.b.Build(context.Background(), generator, lt.TrackJobChild, nil)
	if err != nil {
		t.Fatal(err)
	}
	goldenBundle(t, "context_job.golden.json", marshal(t, b))
	if na := b.NextActions; len(na) < 2 || !strings.HasPrefix(na[len(na)-1], "record your critique") {
		t.Errorf("next_actions %v", na)
	}
}
