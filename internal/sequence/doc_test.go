package sequence_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/sequence"
	"github.com/nicolasacchi/metamusic/internal/sequencer"
)

func golden(t *testing.T, name string, got any) {
	t.Helper()
	b, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	p := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != string(b) {
		t.Errorf("%s differs from the golden (run with -update):\n%s", name, b)
	}
}

// TestSequence_Explanation_Golden pins mm.sequence/1 of the six-Song T set (arc, input t4 t6 t1 t5 t3 t2): the document the slot
// op stores. It holds ids, integers and codes only.
func TestSequence_Explanation_Golden(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.seedTSet()
	var refs []sequence.Ref
	for _, l := range shuffled {
		refs = append(refs, sequence.Ref{SongID: r.song(l), TakeID: strp(r.pickOf(r.song(l)))})
	}
	p, err := sequence.Prepare(context.Background(), r.st.R, refs, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Result.CostInput != 89847130000 || p.Result.CostOutput != 55198370000 {
		t.Errorf("costs = %d %d", p.Result.CostInput, p.Result.CostOutput)
	}
	// the stored form is exactly the JSON of the document
	var back sequence.Document
	if err := json.Unmarshal(p.Doc.JSON(), &back); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(back.Order, p.Doc.Order) || back.Def != "mm.sequence/1" {
		t.Errorf("round trip = %+v", back)
	}
	// ids in the golden are the fixture's fixed ids; the doc never holds a title
	for _, title := range []string{"Synthetic Song", "Synthetic Take"} {
		if strings.Contains(string(p.Doc.JSON()), title) {
			t.Errorf("the document holds the title %q", title)
		}
	}
	golden(t, "explanation_tset_arc.golden.json", p.Doc)
}

func strp(s string) *string { return &s }

func TestFormatCost(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		j    int64
		want string
	}{{0, "0.0000"}, {89847130000, "8.9847"}, {55198370000, "5.5198"}, {499_999, "0.0000"}, {500_000, "0.0001"}, {10_000_000_000, "1.0000"}, {123_456_789_012, "12.3457"}} {
		if got := sequence.FormatCost(c.j); got != c.want {
			t.Errorf("FormatCost(%d) = %s, want %s", c.j, got, c.want)
		}
	}
	for v, want := range map[int]string{386: "38.6", 0: "0.0", -25: "-2.5", -5: "-0.5", 1000: "100.0"} {
		if got := sequence.FormatPctX10(v); got != want {
			t.Errorf("FormatPctX10(%d) = %s, want %s", v, got, want)
		}
	}
}

// countingQ counts the statements a loader runs.
type countingQ struct {
	q       sequence.Querier
	queries []string
}

func (c *countingQ) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	c.queries = append(c.queries, query)
	return c.q.QueryContext(ctx, query, args...)
}

func (c *countingQ) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	c.queries = append(c.queries, query)
	return c.q.QueryRowContext(ctx, query, args...)
}

// TestLoadFeatures: one statement per table, the order of refs, the bases, and neutral Songs for missing rows.
func TestLoadFeatures(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.seedTSet()
	r.exec(`UPDATE take_features SET bpm_basis = 'declared', energy_basis = 'predicted' WHERE track_id = ?`, r.pickOf(r.song("S1")))
	cq := &countingQ{q: r.st.R}
	refs := []sequence.Ref{
		{SongID: r.song("S2"), TakeID: strp(r.pickOf(r.song("S2")))},
		{SongID: r.song("S1"), TakeID: strp(r.pickOf(r.song("S1")))},
		{SongID: r.song("S3")},                       // the Song's own (decoy) features
		{SongID: r.song("S7"), TakeID: strp("none")}, // a Take without a row
	}
	songs, bases, err := sequence.LoadFeatures(context.Background(), cq, refs)
	if err != nil {
		t.Fatal(err)
	}
	if len(cq.queries) != 2 {
		t.Errorf("%d statements, want 2 (one per table)", len(cq.queries))
	}
	want := []sequencer.Song{
		{ID: r.song("S2"), BPMx100: 9800, Energyx10000: 4500, Key: &sequencer.Camelot{Num: 8, Letter: 'A'}, Bases: sequencer.Bases{BPM: "measured", Energy: "measured", Key: "measured"}},
		{ID: r.song("S1"), BPMx100: 9600, Energyx10000: 3000, Key: &sequencer.Camelot{Num: 8, Letter: 'A'}, Bases: sequencer.Bases{BPM: "declared", Energy: "predicted", Key: "measured"}},
		{ID: r.song("S3"), BPMx100: 20000 - 10000, Energyx10000: 10000 - 5500, Key: &sequencer.Camelot{Num: 9, Letter: 'A'}, Bases: sequencer.Bases{BPM: "measured", Energy: "measured", Key: "measured"}},
		{ID: r.song("S7"), Energyx10000: sequencer.NoEnergy},
	}
	for i := range want {
		if songs[i].ID != want[i].ID || songs[i].BPMx100 != want[i].BPMx100 || songs[i].Energyx10000 != want[i].Energyx10000 || songs[i].Bases != want[i].Bases ||
			(songs[i].Key == nil) != (want[i].Key == nil) || (want[i].Key != nil && *songs[i].Key != *want[i].Key) {
			t.Errorf("song %d = %+v, want %+v", i, songs[i], want[i])
		}
	}
	if bases[r.song("S1")].Energy != "predicted" || len(bases) != 4 {
		t.Errorf("bases = %+v", bases)
	}
	// more than one chunk of ids still reads one statement per chunk and table
	cq.queries = nil
	if _, _, err := sequence.LoadFeatures(context.Background(), cq, nil); err != nil || len(cq.queries) != 0 {
		t.Errorf("no refs: %v, %d statements", err, len(cq.queries))
	}
}

// TestPrepare_Rules covers the checks Suggest and the slot op share.
func TestPrepare_Rules(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.seedTSet()
	ctx := context.Background()
	ref := func(l string) sequence.Ref { return sequence.Ref{SongID: r.song(l)} }
	if _, err := sequence.Prepare(ctx, r.st.R, []sequence.Ref{ref("S1")}, "", "", ""); err == nil {
		t.Error("one ref planned")
	}
	if _, err := sequence.Prepare(ctx, r.st.R, make([]sequence.Ref, 61), "", "", ""); err == nil {
		t.Error("61 refs planned")
	}
	p, err := sequence.Prepare(ctx, r.st.R, []sequence.Ref{ref("S1"), ref("S2")}, "", "", "")
	if err != nil || p.Strategy != "arc" || p.Doc.Strategy != "arc" {
		t.Errorf("default strategy: %+v %v", p, err)
	}
}
