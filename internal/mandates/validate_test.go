package mandates

import (
	"errors"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

func TestMandateScope_Validate_Table(t *testing.T) {
	t.Parallel()
	env := Env{BatchCap: 5_000_000,
		Adopted: func(e string) (bool, error) { return e == "adopted_engine", nil },
		RootsOK: func(ids []string) (string, error) {
			for _, id := range ids {
				if id != "r1" && id != "r2" {
					return id, nil
				}
			}
			return "", nil
		}}
	ids := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = string(rune('a'+i%26)) + string(rune('a'+i/26))
		}
		return out
	}
	rows := []struct {
		name   string
		mut    func(*Scope)
		budget money.MicroUSD
		rule   string // "" = valid
	}{
		{"good", func(*Scope) {}, 1_000_000, ""},
		{"style_edit only", func(s *Scope) { s.Modes = []string{"style_edit"} }, 1_000_000, ""},
		{"modes empty", func(s *Scope) { s.Modes = nil }, 1_000_000, "modes_empty"},
		{"mode unknown", func(s *Scope) { s.Modes = []string{"lyric_edit"} }, 1_000_000, "mode_unknown"},
		{"mode duplicate", func(s *Scope) { s.Modes = []string{"new_seed", "new_seed"} }, 1_000_000, "modes_duplicate"},
		{"engines empty", func(s *Scope) { s.Engines = nil }, 1_000_000, "engines_empty"},
		{"engine duplicate", func(s *Scope) { s.Engines = []string{"yue2", "yue2"} }, 1_000_000, "engines_duplicate"},
		{"engine not adopted", func(s *Scope) { s.Engines = []string{"acestep15"} }, 1_000_000, "engine_not_adopted"},
		{"engine unknown", func(s *Scope) { s.Engines = []string{"nope"} }, 1_000_000, "engine_not_adopted"},
		{"engine adopted", func(s *Scope) { s.Engines = []string{"yue2", "adopted_engine"} }, 1_000_000, ""},
		{"stars 0", func(s *Scope) { s.MinStars = 0 }, 1_000_000, "min_stars_range"},
		{"stars 6", func(s *Scope) { s.MinStars = 6 }, 1_000_000, "min_stars_range"},
		{"stars 1", func(s *Scope) { s.MinStars = 1 }, 1_000_000, ""},
		{"stars 5", func(s *Scope) { s.MinStars = 5 }, 1_000_000, ""},
		{"roots empty list", func(s *Scope) { s.RootTrackIDs = []string{} }, 1_000_000, "root_track_ids_range"},
		{"roots 201", func(s *Scope) { s.RootTrackIDs = ids(201) }, 1_000_000, "root_track_ids_range"},
		{"roots duplicate", func(s *Scope) { s.RootTrackIDs = []string{"r1", "r1"} }, 1_000_000, "root_track_ids_duplicate"},
		{"root missing", func(s *Scope) { s.RootTrackIDs = []string{"r1", "zz"} }, 1_000_000, "root_not_found"},
		{"roots ok", func(s *Scope) { s.RootTrackIDs = []string{"r1", "r2"} }, 1_000_000, ""},
		{"lineage 0", func(s *Scope) { s.MaxTakesPerLineage = 0 }, 1_000_000, "max_takes_per_lineage_range"},
		{"lineage 51", func(s *Scope) { s.MaxTakesPerLineage = 51 }, 1_000_000, "max_takes_per_lineage_range"},
		{"lineage 50", func(s *Scope) { s.MaxTakesPerLineage = 50 }, 1_000_000, ""},
		{"batch takes 0", func(s *Scope) { s.MaxTakesPerBatch = 0 }, 1_000_000, "max_takes_per_batch_range"},
		{"batch takes 13", func(s *Scope) { s.MaxTakesPerBatch = 13 }, 1_000_000, "max_takes_per_batch_range"},
		{"batch takes 12", func(s *Scope) { s.MaxTakesPerBatch = 12 }, 1_000_000, ""},
		{"ceiling 0", func(s *Scope) { s.MaxBatchCeilingMicroUSD = 0 }, 1_000_000, "max_batch_ceiling_range"},
		{"ceiling negative", func(s *Scope) { s.MaxBatchCeilingMicroUSD = -1 }, 1_000_000, "max_batch_ceiling_range"},
		{"ceiling above budget", func(s *Scope) { s.MaxBatchCeilingMicroUSD = 1_000_001 }, 1_000_000, "ceiling_above_budget"},
		{"ceiling equals budget", func(s *Scope) { s.MaxBatchCeilingMicroUSD = 1_000_000 }, 1_000_000, ""},
		{"ceiling above cap", func(s *Scope) { s.MaxBatchCeilingMicroUSD = 5_000_001 }, 10_000_000, "ceiling_above_batch_cap"},
		{"ceiling equals cap", func(s *Scope) { s.MaxBatchCeilingMicroUSD = 5_000_000 }, 10_000_000, ""},
		{"per day 0", func(s *Scope) { s.MaxBatchesPerDay = 0 }, 1_000_000, "max_batches_per_day_range"},
		{"per day 5", func(s *Scope) { s.MaxBatchesPerDay = 5 }, 1_000_000, "max_batches_per_day_range"},
		{"per day 4", func(s *Scope) { s.MaxBatchesPerDay = 4 }, 1_000_000, ""},
	}
	if len(rows) < 20 {
		t.Fatalf("%d rows", len(rows))
	}
	for _, r := range rows {
		s := goodScope()
		r.mut(&s)
		err := ValidateScope(s, r.budget, env)
		if r.rule == "" {
			if err != nil {
				t.Errorf("%s: %v", r.name, err)
			}
			continue
		}
		var pe *problem.Error
		if !errors.As(err, &pe) || pe.Code != "MANDATE_SCOPE_INVALID" || pe.Status != 422 || pe.Details["rule"] != r.rule || pe.Details["field"] == "" {
			t.Errorf("%s: err %v, want rule %s", r.name, err, r.rule)
		}
	}
	// a failing environment is an error, not a rule
	boom := errors.New("boom")
	s := goodScope()
	s.Engines = []string{"x"}
	if err := ValidateScope(s, 1_000_000, Env{Adopted: func(string) (bool, error) { return false, boom }}); !errors.Is(err, boom) {
		t.Errorf("adoption error: %v", err)
	}
	s = goodScope()
	s.RootTrackIDs = []string{"r1"}
	if err := ValidateScope(s, 1_000_000, Env{RootsOK: func([]string) (string, error) { return "", boom }}); !errors.Is(err, boom) {
		t.Errorf("roots error: %v", err)
	}
}

func TestParseScope_Strict(t *testing.T) {
	t.Parallel()
	good, _ := goodScope().JSON()
	got, err := ParseScope(good)
	if err != nil || got.MaxBatchCeilingMicroUSD != 943_587 || got.RootTrackIDs != nil || !got.TierB {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := ParseScope(`{"modes":[],"surprise":1}`); err == nil {
		t.Fatal("an unknown key was accepted")
	}
	if _, err := ParseScope(`[`); err == nil {
		t.Fatal("bad json")
	}
	var empty Scope
	j, _ := empty.JSON()
	if j == "" || !strings.Contains(j, `"modes":[]`) || !strings.Contains(j, `"engines":[]`) {
		t.Fatalf("empty slices must not be null: %s", j)
	}
}
