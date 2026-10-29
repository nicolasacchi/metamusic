package trackctx_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/trackctx"
)

// curationSection builds the section a reader would return: n Records and n Crates.
func curationSection(records, crates int) map[string]any {
	rs, cs := make([]map[string]any, records), make([]map[string]any, crates)
	for i := range rs {
		rs[i] = map[string]any{"id": fmt.Sprintf("rec-%02d", i), "title": strings.Repeat("Synthetic Record ", 6), "status": "published", "position": i + 1, "mode": "floating"}
	}
	for i := range cs {
		cs[i] = map[string]any{"id": fmt.Sprintf("crate-%02d", i), "name": strings.Repeat("Synthetic Crate ", 6)}
	}
	return map[string]any{"records": rs, "crates": cs}
}

type fixedCuration struct{ out map[string]any }

func (f fixedCuration) ForTrack(context.Context, string) (map[string]any, error) { return f.out, nil }

// TestContext_L2Section: the section follows sounds, lists records then crates in that order, and is absent (not null) when the
// reader has nothing for the take.
func TestContext_L2Section(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.b.UseLibrary(fakeLibrary{take: lt.TrackLuna, s: sections("a brief", 2)})
	e.b.UseCuration(fixedCuration{curationSection(2, 1)})
	b, err := e.b.Build(context.Background(), agent, lt.TrackLuna, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := marshal(t, b)
	top := keys(t, raw)
	if i := slices.Index(top, "sounds"); i < 0 || top[i+1] != "curation" || top[i+2] != "recipe" {
		t.Errorf("order around curation: %v", top)
	}
	if diff := cmp.Diff([]string{"records", "crates"}, keys(t, section(t, raw, "curation"))); diff != "" {
		t.Errorf("curation keys:\n%s", diff)
	}
	e.b.UseCuration(fixedCuration{nil})
	b, err = e.b.Build(context.Background(), agent, lt.TrackLuna, nil)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(keys(t, marshal(t, b)), "curation") || b.Curation != nil {
		t.Error("an empty section must be absent")
	}
}

// TestContext_NoCurationReader: without a reader the bundle has no curation key and its bytes are what they were before L2.
func TestContext_NoCurationReader(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	b, err := e.b.Build(context.Background(), agent, lt.TrackLuna, nil)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(keys(t, marshal(t, b)), "curation") || b.Curation != nil {
		t.Error("a bundle without a reader carries a curation section")
	}
}

// TestContext_L2Trim: Fit trims curation.crates, then curation.records (6 -> 3 -> 0), before the siblings of the song, and names
// every cut in _budget.truncated.
func TestContext_L2Trim(t *testing.T) {
	t.Parallel()
	mk := func(records, crates int) *trackctx.Bundle {
		s := sections("a brief", 12)
		return &trackctx.Bundle{Format: "mm.context/1", Song: s.Song, Muse: s.Muse, Sounds: s.Sounds, Curation: curationSection(records, crates)}
	}
	margin := 80
	m66, m36, m06, m03, m00 := trackctx.Measure(mk(6, 6)), trackctx.Measure(mk(6, 3)), trackctx.Measure(mk(6, 0)), trackctx.Measure(mk(3, 0)), trackctx.Measure(mk(0, 0))
	if !(m66 > m36+margin && m36 > m06+margin && m06 > m03+margin && m03 > m00+margin) {
		t.Fatalf("the cases do not separate: %d %d %d %d %d", m66, m36, m06, m03, m00)
	}
	for _, c := range []struct {
		name            string
		limit           int
		records, crates int
		trunc           []string
	}{
		{"fits", m66 + margin, 6, 6, []string{}},
		{"crates to 3", m36 + margin, 6, 3, []string{"curation.crates"}},
		{"crates to 0", m06 + margin, 6, 0, []string{"curation.crates"}},
		{"records to 3", m03 + margin, 3, 0, []string{"curation.crates", "curation.records"}},
		{"records to 0", m00 + margin, 0, 0, []string{"curation.crates", "curation.records"}},
	} {
		b := mk(6, 6)
		chars, trunc := trackctx.Fit(b, c.limit, nil)
		if chars > c.limit {
			t.Errorf("%s: %d chars over limit %d", c.name, chars, c.limit)
		}
		if n := len(b.Curation["records"].([]map[string]any)); n != c.records {
			t.Errorf("%s: %d records, want %d", c.name, n, c.records)
		}
		if n := len(b.Curation["crates"].([]map[string]any)); n != c.crates {
			t.Errorf("%s: %d crates, want %d", c.name, n, c.crates)
		}
		if n := len(b.Song["siblings"].([]map[string]any)); n != 12 {
			t.Errorf("%s: siblings were trimmed before the curation lists: %d", c.name, n)
		}
		if diff := cmp.Diff(c.trunc, trunc); diff != "" {
			t.Errorf("%s: truncated (-want +got):\n%s", c.name, diff)
		}
	}
}
