package recordview_test

import (
	"context"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/recordview"
	"github.com/nicolasacchi/metamusic/internal/tape"
)

// TestPublishPreview_EqualsSnapshot: every chapter name the publish preview shows is exactly the name the tape file will carry
// for that item (tape.ChapterName over the snapshot item): the side prefix and the lossy suffix are in both, in one spelling.
func TestPublishPreview_EqualsSnapshot(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	id := idOf(t, r, "records", r1Title)
	if _, err := r.Store.W.Exec(`UPDATE record_slots SET side = 'A' WHERE record_id = ? AND removed_at IS NULL`, id); err != nil {
		t.Fatal(err)
	}
	v, err := r.Services.RecordView.Get(ctx, human, id, recordview.GetOptions{Include: []string{recordview.IncludePreview}})
	if err != nil {
		t.Fatal(err)
	}
	s, err := r.Services.RecordView.Snapshot(ctx, id, tape.SnapOptions{IncludeLiner: true})
	if err != nil {
		t.Fatal(err)
	}
	if v.Preview == nil || len(v.Preview.Chapters) != len(s.Items) || len(s.Items) == 0 {
		t.Fatalf("preview %+v vs %d snapshot items", v.Preview, len(s.Items))
	}
	var sided, lossy int
	for i, it := range s.Items {
		want := tape.ChapterName(it.Title, it.Side, it.Lossy)
		if got := v.Preview.Chapters[i].Name; got != want {
			t.Errorf("chapter %d: preview %q, file %q", i+1, got, want)
		}
		if it.Side == "A" && strings.HasPrefix(want, "A"+tape.SideSeparator) {
			sided++
		}
		if it.Lossy && strings.HasSuffix(want, tape.LossySuffix) {
			lossy++
		}
	}
	if sided != len(s.Items) || lossy == 0 {
		t.Errorf("%d of %d chapters carry the side prefix, %d lossy suffixes", sided, len(s.Items), lossy)
	}
}
