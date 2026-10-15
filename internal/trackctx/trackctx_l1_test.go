package trackctx_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/trackctx"
)

// fakeLibrary answers the sections of one take; any other take has no Song yet.
type fakeLibrary struct {
	take string
	s    trackctx.LibrarySections
	err  error
}

func (f fakeLibrary) ForTrack(_ context.Context, id string) (trackctx.LibrarySections, error) {
	if id != f.take {
		return trackctx.LibrarySections{}, nil
	}
	return f.s, f.err
}

func sections(brief string, siblings int) trackctx.LibrarySections {
	sib := make([]map[string]any, siblings)
	for i := range sib {
		sib[i] = map[string]any{"id": fmt.Sprintf("sib-%02d", i), "title": strings.Repeat("Synthetic Title ", 8), "stars": nil, "status": "draft", "is_pick": false}
	}
	return trackctx.LibrarySections{
		Song: map[string]any{"id": "song-1", "title": "Synthetic Song", "angle": "variant-a", "state": "rendered", "takes": siblings + 1,
			"pick": map[string]any{"take_id": "t", "source": "ladder", "is_this": false}, "open_proposals": 0, "pending": false, "siblings": sib},
		Muse:   map[string]any{"id": "muse-1", "kind": "brief", "title": "Synthetic Brief", "artist": nil, "brief": brief},
		Sounds: map[string]any{"taxonomy_version": 1, "facets": map[string]any{"genre_family": []string{"reggae"}}, "tempo": nil},
	}
}

func TestContext_L1Sections(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.b.UseLibrary(fakeLibrary{take: lt.TrackLuna, s: sections("a brief", 2)})
	b, err := e.b.Build(context.Background(), agent, lt.TrackLuna, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := marshal(t, b)
	top := keys(t, raw)
	if i := slices.Index(top, "track"); i < 0 || !slices.Equal(top[i:i+4], []string{"track", "song", "muse", "sounds"}) {
		t.Errorf("order after track: %v", top)
	}
	if diff := cmp.Diff([]string{"id", "title", "angle", "state", "takes", "pick", "open_proposals", "pending", "siblings"}, keys(t, section(t, raw, "song"))); diff != "" {
		t.Errorf("song keys:\n%s", diff)
	}
	if diff := cmp.Diff([]string{"id", "kind", "title", "artist", "brief"}, keys(t, section(t, raw, "muse"))); diff != "" {
		t.Errorf("muse keys:\n%s", diff)
	}
	if diff := cmp.Diff([]string{"taxonomy_version", "facets", "tempo"}, keys(t, section(t, raw, "sounds"))); diff != "" {
		t.Errorf("sounds keys:\n%s", diff)
	}
	var env struct {
		Song struct {
			Siblings []map[string]any `json:"siblings"`
		} `json:"song"`
		Muse struct{ Brief string } `json:"muse"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || len(env.Song.Siblings) != 2 || env.Muse.Brief != "a brief" {
		t.Errorf("content: %+v %v", env, err)
	}

	// a take without a Song (queue pending): three nulls
	b, err = e.b.Build(context.Background(), agent, lt.TrackCitta, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw = marshal(t, b)
	for _, k := range []string{"song", "muse", "sounds"} {
		if string(section(t, raw, k)) != "null" {
			t.Errorf("%s = %s, want null", k, section(t, raw, k))
		}
	}
	// a reader error is not swallowed
	e.b.UseLibrary(fakeLibrary{take: lt.TrackLuna, err: context.DeadlineExceeded})
	if _, err := e.b.Build(context.Background(), agent, lt.TrackLuna, nil); err == nil {
		t.Error("a reader error was swallowed")
	}
}

func TestContext_L1Trim(t *testing.T) {
	t.Parallel()
	brief := strings.Repeat("b", 500)
	mk := func(sib int, brief string) *trackctx.Bundle {
		s := sections(brief, sib)
		return &trackctx.Bundle{Format: "mm.context/1", Song: s.Song, Muse: s.Muse, Sounds: s.Sounds}
	}
	margin := 80 // the names of the cuts are part of what _budget.chars counts
	full := mk(12, brief)
	at5, at0, brief200 := trackctx.Measure(mk(5, brief)), trackctx.Measure(mk(0, brief)), trackctx.Measure(mk(0, brief[:200]))
	if !(trackctx.Measure(full) > at5+margin && at5 > at0+margin && at0 > brief200+margin) {
		t.Fatalf("the cases do not separate: %d %d %d %d", trackctx.Measure(full), at5, at0, brief200)
	}
	for _, c := range []struct {
		name     string
		limit    int
		siblings int
		brief    int
		trunc    []string
	}{
		{"fits", trackctx.Measure(full) + margin, 12, 500, []string{}},
		{"siblings 12 to 5", at5 + margin, 5, 500, []string{"song.siblings"}},
		{"siblings to 0", at0 + margin, 0, 500, []string{"song.siblings"}},
		{"brief to 200", brief200 + margin, 0, 200, []string{"song.siblings", "muse.brief"}},
	} {
		b := mk(12, brief)
		chars, trunc := trackctx.Fit(b, c.limit, nil)
		if chars > c.limit {
			t.Errorf("%s: %d chars over limit %d", c.name, chars, c.limit)
		}
		if n := len(b.Song["siblings"].([]map[string]any)); n != c.siblings {
			t.Errorf("%s: %d siblings, want %d", c.name, n, c.siblings)
		}
		if n := len(b.Muse["brief"].(string)); n != c.brief {
			t.Errorf("%s: brief %d, want %d", c.name, n, c.brief)
		}
		if diff := cmp.Diff(c.trunc, trunc); diff != "" {
			t.Errorf("%s: truncated (-want +got):\n%s", c.name, diff)
		}
	}
	// the Song itself, its Pick and the Muse title survive every cut
	b := mk(12, brief)
	trackctx.Fit(b, 1, nil)
	if b.Song["id"] != "song-1" || b.Song["pick"] == nil || b.Muse["title"] != "Synthetic Brief" {
		t.Errorf("trimming removed identity: %v %v", b.Song, b.Muse)
	}
}

func TestContext_NoLibraryReader(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	b, err := e.b.Build(context.Background(), agent, lt.TrackLuna, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := marshal(t, b)
	for _, k := range []string{"song", "muse", "sounds"} {
		if got := string(section(t, raw, k)); got != "null" {
			t.Errorf("%s = %s without a reader, want null", k, got)
		}
	}
	if b.Song != nil || b.Muse != nil || b.Sounds != nil {
		t.Error("sections set without a reader")
	}
}
