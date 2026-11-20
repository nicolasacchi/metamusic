package generation_test

import (
	"encoding/json"
	"strings"
	"testing"

	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

// TestInsertBatch_DefaultsByteIdentical: a take planned without G1 fields leaves them out of the estimate document (so every
// existing estimate is byte-identical) and create inserts kind 'generate', needs '[]' and spec '{}'.
func TestInsertBatch_DefaultsByteIdentical(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	e := r.est(human, newSeedItem(lt.TrackLuna, 2))
	items := r.str(`SELECT items_json FROM estimates WHERE id = ?`, e.ID)
	for _, key := range []string{`"kind"`, `"needs"`, `"spec"`} {
		if strings.Contains(items, key) {
			t.Errorf("a plain estimate carries %s: %.200s", key, items)
		}
	}
	c := r.mustCreate(human, e, spend.ViaHumanCreate)
	if n := r.count(`SELECT COUNT(*) FROM jobs WHERE batch_id = ? AND kind = 'generate' AND needs_json = '[]' AND spec_json = '{}'`, c.BatchID); n != 2 {
		t.Errorf("%d of 2 jobs carry the defaults", n)
	}
	for _, j := range c.Jobs {
		if j.Kind != "generate" {
			t.Errorf("job view kind = %q", j.Kind)
		}
	}
}

// TestInsertBatch_WritesKindNeedsSpec: an estimate take that names a kind, needs and a spec has them written canonically to the
// job (C323.4); a bad kind, unsorted needs or a spec that does not parse strictly fails create without a batch.
func TestInsertBatch_WritesKindNeedsSpec(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, patch string
		wantKind    string
		wantNeeds   string
		wantSpec    string
		fails       bool
	}{
		{"plan", `{"kind":"plan","needs":["plan","v2"],"spec":{"plan":{"seconds_per_seed":30,"seeds":[3,4]}}}`, "plan", `["plan","v2"]`, `{"plan":{"seconds_per_seed":30,"seeds":[3,4]}}`, false},
		{"needs only", `{"needs":["ears","v2"]}`, "generate", `["ears","v2"]`, `{}`, false},
		{"unknown kind", `{"kind":"render"}`, "", "", "", true},
		{"unsorted needs", `{"needs":["v2","ears"]}`, "", "", "", true},
		{"duplicate needs", `{"needs":["v2","v2"]}`, "", "", "", true},
		{"spec with an unknown field", `{"spec":{"bogus":1}}`, "", "", "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			batches := r.count(`SELECT COUNT(*) FROM batches`)
			e := r.est(human, newSeedItem(lt.TrackLuna, 1))
			r.patchTake(e.ID, c.patch)
			cr, err := r.create(human, e, spend.ViaHumanCreate)
			if c.fails {
				if err == nil {
					t.Fatal("create accepted a bad estimate take")
				}
				if n := r.count(`SELECT COUNT(*) FROM batches`); n != batches {
					t.Errorf("%d batches left behind", n)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := r.str(`SELECT kind || '|' || needs_json || '|' || spec_json FROM jobs WHERE batch_id = ?`, cr.BatchID)
			if want := c.wantKind + "|" + c.wantNeeds + "|" + c.wantSpec; got != want {
				t.Errorf("job = %s, want %s", got, want)
			}
		})
	}
}

// patchTake merges the keys of patch (a JSON object) into the first take of the estimate's items_json; the document is frozen by a
// trigger in production, so the test drops it first.
func (r *rig) patchTake(estimate, patch string) {
	r.t.Helper()
	var items []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(r.str(`SELECT items_json FROM estimates WHERE id = ?`, estimate)), &items); err != nil {
		r.t.Fatal(err)
	}
	var over map[string]json.RawMessage
	if err := json.Unmarshal([]byte(patch), &over); err != nil {
		r.t.Fatal(err)
	}
	for k, v := range over {
		items[0][k] = v
	}
	b, err := json.Marshal(items)
	if err != nil {
		r.t.Fatal(err)
	}
	r.exec(`DROP TRIGGER IF EXISTS estimates_frozen`)
	r.exec(`UPDATE estimates SET items_json = ? WHERE id = ?`, string(b), estimate)
}
