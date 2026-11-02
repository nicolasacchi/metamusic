package sequence_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/perftest"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/records"
	"github.com/nicolasacchi/metamusic/internal/sequence"
	"github.com/nicolasacchi/metamusic/internal/sequencer"
)

func wantProblem(t *testing.T, err error, status int, code string) *problem.Error {
	t.Helper()
	var pe *problem.Error
	if !errors.As(err, &pe) {
		t.Fatalf("error %v is not a *problem.Error", err)
	}
	if pe.Status != status || pe.Code != code {
		t.Fatalf("error = %d %s (%s), want %d %s", pe.Status, pe.Code, pe.Message, status, code)
	}
	return pe
}

func (r *rig) songIDs(labels ...string) []string {
	out := make([]string, len(labels))
	for i, l := range labels {
		out[i] = r.song(l)
	}
	return out
}

// TestSuggest_Sources: a Record plans by each slot's resolved Take, song_ids and a Crate by the Songs' subjects.
func TestSuggest_Sources(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("record: floating slots use the Pick take", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.seedTSet()
		rec := r.newRecord(shuffled...)
		s, err := r.svc.Suggest(ctx, sequence.SuggestInput{RecordID: rec.ID})
		if err != nil {
			t.Fatal(err)
		}
		if got := r.labels(s.Order); !slices.Equal(got, []string{"S1", "S2", "S3", "S4", "S5", "S6"}) {
			t.Errorf("order = %v", got)
		}
		if got := r.labels(s.InputOrder); !slices.Equal(got, shuffled) {
			t.Errorf("input order = %v", got)
		}
		if s.Def != "mm.sequence/1" || s.Strategy != "arc" || !s.Exact || s.Cost.Input != "8.9847" || s.Cost.Output != "5.5198" || s.Pending {
			t.Errorf("suggestion = %+v", s)
		}
		if s.Cost.ImprovementPct == nil || *s.Cost.ImprovementPct != "38.6" || len(s.Steps) != 6 || len(s.Digest) != 64 {
			t.Errorf("suggestion = %+v", s)
		}
	})

	t.Run("record: a pinned slot uses its pinned Take", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.seedTSet()
		pinned := r.fx.Takes["c2"]
		r.seedTake(pinned, feat{9000, 500, "8A"})
		rec := r.newRecord(shuffled...)
		slot := ""
		sl, err := r.recs.Slots(ctx, rec.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range sl {
			if x.SongID == r.song("S4") {
				slot = x.ID
			}
		}
		if _, err := r.recs.EditSlots(ctx, agent, rec.ID, rec.Revision, []records.SlotOp{{Op: "pin", SlotID: slot, TakeID: pinned}}); err != nil {
			t.Fatal(err)
		}
		s, err := r.svc.Suggest(ctx, sequence.SuggestInput{RecordID: rec.ID})
		if err != nil {
			t.Fatal(err)
		}
		if got := r.labels(s.Order); got[0] != "S4" {
			t.Errorf("order = %v, want S4 first (the pinned take is the quietest)", got)
		}
	})

	t.Run("record: an empty slot uses the Song's features, a Take without features is unknown", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.seedTSet()
		// S7 has no takes: its draft Song's own features count
		r.seedSong(r.song("S7"), "draft-subject", feat{9400, 1000, "8A"})
		// S6's Pick take loses its features: the slot is unknown, costed neutral
		r.exec(`DELETE FROM take_features WHERE track_id = ?`, r.pickOf(r.song("S6")))
		rec := r.newRecord("S7", "S1", "S2", "S6")
		s, err := r.svc.Suggest(ctx, sequence.SuggestInput{RecordID: rec.ID})
		if err != nil {
			t.Fatal(err)
		}
		missing := map[string][]string{}
		for _, st := range s.Steps {
			missing[r.labels([]string{st.Step.SongID})[0]] = st.Step.Missing
		}
		if len(missing["S7"]) != 0 || len(missing["S1"]) != 0 {
			t.Errorf("missing = %v", missing)
		}
		if got := missing["S6"]; !slices.Equal(got, []string{"bpm", "key", "energy"}) {
			t.Errorf("S6 missing = %v", got)
		}
		if s.Coverage.Tempo != 75 {
			t.Errorf("coverage = %+v, want 75 %% tempo", s.Coverage)
		}
	})

	t.Run("song_ids: the Song subjects", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.seedTSet()
		s, err := r.svc.Suggest(ctx, sequence.SuggestInput{SongIDs: r.songIDs(shuffled...)})
		if err != nil {
			t.Fatal(err)
		}
		// song_features hold the decoy (tempo and energy mirrored), so the plan is another one than the take-based plan
		if got := r.labels(s.Order); slices.Equal(got, []string{"S1", "S2", "S3", "S4", "S5", "S6"}) {
			t.Errorf("order = %v: song_ids must read song_features, not the Pick take", got)
		}
		if len(s.Order) != 6 {
			t.Errorf("order = %v", s.Order)
		}
	})

	t.Run("crate_id: manual and smart Crates through the resolver", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.seedTSet()
		var asked []string
		r.svc.SetCrates(func(_ context.Context, id string, limit int) ([]string, int, error) {
			asked = append(asked, fmt.Sprintf("%s/%d", id, limit))
			switch id {
			case "manual":
				return r.songIDs("S1", "S2", "S3"), 3, nil
			case "smart":
				return r.songIDs("S4", "S5"), 2, nil
			case "big":
				return r.songIDs("S1", "S2"), 61, nil
			}
			return nil, 0, problem.E(404, "NOT_FOUND", "crate not found", nil)
		})
		for id, want := range map[string]int{"manual": 3, "smart": 2} {
			s, err := r.svc.Suggest(ctx, sequence.SuggestInput{CrateID: id})
			if err != nil || len(s.Order) != want {
				t.Fatalf("%s: %v %v", id, s.Order, err)
			}
		}
		_, err := r.svc.Suggest(ctx, sequence.SuggestInput{CrateID: "big"})
		pe := wantProblem(t, err, 422, "TOO_MANY_SONGS")
		if pe.Details["count"] != 61 || pe.Details["limit"] != 60 {
			t.Errorf("details = %v", pe.Details)
		}
		_, err = r.svc.Suggest(ctx, sequence.SuggestInput{CrateID: "missing"})
		wantProblem(t, err, 404, "NOT_FOUND")
		if !slices.Contains(asked, "manual/61") {
			t.Errorf("resolver calls = %v", asked)
		}
	})
}

func TestSuggest_Errors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := newRig(t)
	r.seedTSet()
	rec := r.newRecord(shuffled...)
	one := r.newRecord("S1")
	var many []string
	for i := 0; i < 61; i++ {
		many = append(many, fmt.Sprintf("song-%d", i))
	}
	for _, c := range []struct {
		name   string
		in     sequence.SuggestInput
		status int
		code   string
	}{
		{"no source", sequence.SuggestInput{}, 422, "SEQUENCE_SOURCE"},
		{"two sources", sequence.SuggestInput{RecordID: rec.ID, SongIDs: r.songIDs("S1", "S2")}, 422, "SEQUENCE_SOURCE"},
		{"unknown record", sequence.SuggestInput{RecordID: "nope"}, 404, "NOT_FOUND"},
		{"one slot", sequence.SuggestInput{RecordID: one.ID}, 422, "SEQUENCE_TOO_SMALL"},
		{"one song", sequence.SuggestInput{SongIDs: r.songIDs("S1")}, 422, "SEQUENCE_TOO_SMALL"},
		{"unknown song", sequence.SuggestInput{SongIDs: []string{r.song("S1"), "ghost"}}, 422, "UNKNOWN_SONG"},
		{"duplicate song", sequence.SuggestInput{SongIDs: []string{r.song("S1"), r.song("S1")}}, 422, "UNKNOWN_SONG"},
		{"too many songs", sequence.SuggestInput{SongIDs: many}, 422, "TOO_MANY_SONGS"},
		{"unknown strategy", sequence.SuggestInput{RecordID: rec.ID, Strategy: "zigzag"}, 422, "UNKNOWN_STRATEGY"},
		{"first not in set", sequence.SuggestInput{RecordID: rec.ID, First: r.song("S7")}, 422, "SEQUENCE_ENDPOINT_NOT_IN_SET"},
		{"last not in set", sequence.SuggestInput{SongIDs: r.songIDs("S1", "S2"), Last: r.song("S7")}, 422, "SEQUENCE_ENDPOINT_NOT_IN_SET"},
		{"same endpoints", sequence.SuggestInput{RecordID: rec.ID, First: r.song("S1"), Last: r.song("S1")}, 422, "SEQUENCE_ENDPOINTS_CONFLICT"},
		{"no features at all", sequence.SuggestInput{SongIDs: r.songIDs("S2", "S7")}, 0, ""},
		{"crate without a resolver", sequence.SuggestInput{CrateID: "c"}, 503, "UNAVAILABLE"},
	} {
		_, err := r.svc.Suggest(ctx, c.in)
		if c.code == "" {
			continue
		}
		t.Run(c.name, func(t *testing.T) { wantProblem(t, err, c.status, c.code) })
	}
	// no feature row for any slot: every Song unknown
	r.exec(`DELETE FROM take_features`)
	r.exec(`DELETE FROM song_features`)
	_, err := r.svc.Suggest(ctx, sequence.SuggestInput{RecordID: rec.ID})
	wantProblem(t, err, 422, "SEQUENCE_NO_FEATURES")
}

// TestSuggest_Pending: a queued track key of a slot Song marks the answer pending and adds the warning.
func TestSuggest_Pending(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.seedTSet()
	rec := r.newRecord(shuffled...)
	s, err := r.svc.Suggest(context.Background(), sequence.SuggestInput{RecordID: rec.ID})
	if err != nil || s.Pending {
		t.Fatalf("pending before: %v %v", s.Pending, err)
	}
	r.exec(`INSERT INTO provenance_dirty(kind, id, at) VALUES('track', ?, '2026-10-15T12:00:00.000Z')`, r.pickOf(r.song("S3")))
	s, err = r.svc.Suggest(context.Background(), sequence.SuggestInput{RecordID: rec.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !s.Pending || s.Warnings[len(s.Warnings)-1].Code != "pending_features" {
		t.Errorf("pending = %v, warnings = %+v", s.Pending, s.Warnings)
	}
	// a draft key of a Song counts the same way
	r.exec(`DELETE FROM provenance_dirty`)
	r.exec(`INSERT INTO provenance_dirty(kind, id, at) VALUES('draft', (SELECT draft_id FROM songs WHERE id = ?), '2026-10-15T12:00:00.000Z')`, r.song("S7"))
	s, err = r.svc.Suggest(context.Background(), sequence.SuggestInput{SongIDs: r.songIDs("S1", "S7")})
	if err != nil || !s.Pending {
		t.Errorf("draft key: pending = %v, %v", s.Pending, err)
	}
}

// TestSuggest_TitlesInjected_NoMuseText: the steps carry the Song and Take titles read now; no Muse text anywhere.
func TestSuggest_TitlesInjected_NoMuseText(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.seedTSet()
	rec := r.newRecord(shuffled...)
	s, err := r.svc.Suggest(context.Background(), sequence.SuggestInput{RecordID: rec.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range s.Steps {
		var title, take string
		if err := r.st.R.QueryRow(`SELECT title FROM songs WHERE id = ?`, st.Step.SongID).Scan(&title); err != nil {
			t.Fatal(err)
		}
		if err := r.st.R.QueryRow(`SELECT title FROM tracks WHERE id = ?`, r.pickOf(st.Step.SongID)).Scan(&take); err != nil {
			t.Fatal(err)
		}
		if st.Title != title || st.TakeTitle != take || title == "" {
			t.Errorf("step %d title %q take %q, want %q %q", st.Step.Position, st.Title, st.TakeTitle, title, take)
		}
		if strings.Contains(st.Text, title) {
			t.Errorf("text %q holds the title", st.Text)
		}
	}
	b, _ := json.Marshal(s)
	for _, canary := range []string{"Synthetic Curation Muse", "Synthetic Curation Artist"} {
		if strings.Contains(string(b), canary) {
			t.Errorf("Muse text %q in the suggestion", canary)
		}
	}
}

// TestSuggest_GETNeverWrites: Suggest runs in a read transaction; no table of the Record or the queue changes.
func TestSuggest_GETNeverWrites(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.seedTSet()
	rec := r.newRecord(shuffled...)
	r.exec(`CREATE TABLE writes(t TEXT)`)
	for _, tbl := range []string{"records", "record_slots", "record_changes", "record_sequences", "provenance_dirty", "take_features", "song_features"} {
		for _, ev := range []string{"INSERT", "UPDATE", "DELETE"} {
			r.exec(fmt.Sprintf(`CREATE TRIGGER w_%s_%s AFTER %s ON %s BEGIN INSERT INTO writes VALUES('%s %s'); END`, tbl, ev, ev, tbl, ev, tbl))
		}
	}
	for _, in := range []sequence.SuggestInput{{RecordID: rec.ID}, {SongIDs: r.songIDs("S1", "S2")}, {RecordID: rec.ID, Strategy: "steady", First: r.song("S1")}} {
		if _, err := r.svc.Suggest(context.Background(), in); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := r.st.R.QueryRow(`SELECT COUNT(*) FROM writes`).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d writes during Suggest (%v)", n, err)
	}
}

// TestSuggest_EqualsApply: the preview and the slot op agree on order, cost and digest for the same state.
func TestSuggest_EqualsApply(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, strategy := range []string{"arc", "ramp_up", "steady"} {
		r := newRig(t)
		r.seedTSet()
		rec := r.newRecord(shuffled...)
		s, err := r.svc.Suggest(ctx, sequence.SuggestInput{RecordID: rec.ID, Strategy: sequencer.Strategy(strategy)})
		if err != nil {
			t.Fatal(err)
		}
		res, err := r.recs.EditSlots(ctx, agent, rec.ID, rec.Revision, []records.SlotOp{{Op: "sequence", Strategy: strategy}})
		if err != nil {
			t.Fatal(err)
		}
		var order, doc string
		var in, out int64
		var digest string
		if err := r.st.R.QueryRow(`SELECT order_after_json, explanation_json, cost_input, cost_output, digest FROM record_sequences WHERE id = ?`, res.Sequences[0].ID).
			Scan(&order, &doc, &in, &out, &digest); err != nil {
			t.Fatal(err)
		}
		var stored struct {
			Order []string `json:"order"`
			Cost  struct{ Input, Output string }
			Steps []struct {
				SongID string `json:"song_id"`
				Text   string `json:"text"`
			}
		}
		if err := json.Unmarshal([]byte(doc), &stored); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(stored.Order, s.Order) || stored.Cost.Input != s.Cost.Input || stored.Cost.Output != s.Cost.Output || digest != s.Digest {
			t.Errorf("%s: stored %v %v %s != suggested %v %v %s", strategy, stored.Order, stored.Cost, digest, s.Order, s.Cost, s.Digest)
		}
		if in == 0 || out == 0 || sequence.FormatCost(in) != s.Cost.Input || sequence.FormatCost(out) != s.Cost.Output {
			t.Errorf("%s: cost columns %d %d vs %v", strategy, in, out, s.Cost)
		}
		for i, st := range s.Steps {
			if stored.Steps[i].SongID != st.Step.SongID || stored.Steps[i].Text != st.Text {
				t.Errorf("%s: step %d differs", strategy, i+1)
			}
		}
	}
}

// TestPerf_Suggest: a 60-slot Record end to end.
func TestPerf_Suggest(t *testing.T) {
	if testing.CoverMode() != "" {
		t.Skip("coverage instrumentation distorts the wall-clock budget")
	}
	budget := perftest.Budget(t, 150*time.Millisecond)
	r := newRigWith(t, true)
	var labels []string
	for i := 1; i <= 60; i++ {
		l := fmt.Sprintf("S%d", i)
		labels = append(labels, l)
		r.seedTake(r.pickOf(r.song(l)), feat{bpm: 8000 + i*137%4000, energy: i * 997 % 10000, key: fmt.Sprintf("%d%s", i%12+1, []string{"A", "B"}[i%2])})
	}
	rec := r.newRecord(labels...)
	var worst time.Duration
	for i := 0; i < 5; i++ {
		start := time.Now()
		if _, err := r.svc.Suggest(context.Background(), sequence.SuggestInput{RecordID: rec.ID}); err != nil {
			t.Fatal(err)
		}
		worst = max(worst, time.Since(start))
	}
	if worst > budget {
		t.Errorf("worst Suggest of 60 slots = %v > %v", worst, budget)
	}
}
