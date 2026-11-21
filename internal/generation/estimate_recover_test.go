package generation_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/generation"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/lineage"
)

// recoverDraft marks a fixture draft as recovered from the fixture reference (a draft_recoveries row).
func (r *rig) recoverDraft(draftID string) {
	r.t.Helper()
	r.exec(`INSERT INTO draft_recoveries(draft_id, track_id, analysis_sha256, language, words, confidence, style_source, created_by, created_at)
		VALUES (?, ?, ?, 'it', 12, 0.8, 'placeholder', 'human:alice', '2026-11-21T10:00:00Z')`, draftID, lt.TrackRefFlac, strings.Repeat("b", 64))
}

// TestFromDraft_RecoveredOperator: a recovered draft renders with operator recover and carries the reference in the take's params
// (the mutation params the ledger reads); the take is still planned as a rootless from_draft; every other draft keeps operator
// from_draft and a params document without the key.
func TestFromDraft_RecoveredOperator(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.recoverDraft(lt.Draft1)
	e := r.est(agent, generation.Item{Mode: lineage.ModeFromDraft, DraftIDs: []string{lt.Draft1, lt.Draft2}, NSeeds: 2})
	var ops []string
	for _, it := range e.Items {
		for _, tk := range it.Takes {
			ops = append(ops, tk.Operator)
		}
	}
	if got := strings.Join(ops, ","); got != "recover,recover,from_draft,from_draft" {
		t.Fatalf("operators = %s", got)
	}
	docs := r.items(e.ID)
	for i, d := range docs {
		_, has := d.Params["recovered_from"]
		switch {
		case i < 2 && (d.Params["recovered_from"] != lt.TrackRefFlac || d.Params["mode"] != lineage.ModeFromDraft || d.ParentTrackID != nil):
			t.Errorf("recovered take %d: params %v parent %v", i, d.Params, d.ParentTrackID)
		case i >= 2 && has:
			t.Errorf("plain take %d carries recovered_from: %v", i, d.Params)
		}
	}
	raw := r.str(`SELECT items_json FROM estimates WHERE id = ?`, e.ID)
	if n := strings.Count(raw, "recovered_from"); n != 2 {
		t.Errorf("items_json holds recovered_from %d times, want 2 (the recovered draft's takes only)", n)
	}
}

// TestFromDraft_RecoveredDraftParentRefused: a mother override on an item that renders a recovered draft is 422 INVALID_ITEM,
// rule recovered_draft_parent; the same override on an ordinary draft stays allowed.
func TestFromDraft_RecoveredDraftParentRefused(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.recoverDraft(lt.Draft1)
	mother := lt.TrackLuna
	err := r.estimateErr(generation.EstimateIn{Items: []generation.Item{{Mode: lineage.ModeFromDraft, DraftIDs: []string{lt.Draft1}, ParentTrackID: &mother}}})
	wantRule(t, err, http.StatusUnprocessableEntity, "INVALID_ITEM", "recovered_draft_parent")
	if e := r.est(agent, generation.Item{Mode: lineage.ModeFromDraft, DraftIDs: []string{lt.Draft2}, ParentTrackID: &mother}); len(e.Items) != 1 {
		t.Errorf("an ordinary draft with a mother: %+v", e.Items)
	}
}
