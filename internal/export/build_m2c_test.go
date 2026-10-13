package export_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/export"
)

// UnknownRecipeTracks makes fakeSource an export.Source: no reference tracks.
func (f *fakeSource) UnknownRecipeTracks(context.Context, export.TrackFilter) ([]string, error) {
	return nil, nil
}

// refSource adds reference tracks to a fakeSource.
type refSource struct {
	*fakeSource
	unknown []string
}

func (r refSource) UnknownRecipeTracks(context.Context, export.TrackFilter) ([]string, error) {
	return r.unknown, nil
}

var tracksSel = export.Selection{Kind: "tracks"}

func TestExport_EmptyErrorKeepsNote(t *testing.T) {
	t.Parallel()
	src := refSource{fakeSource: &fakeSource{}, unknown: []string{"ref-1"}}
	_, err := export.Build(context.Background(), src, tracksSel)
	if !errors.Is(err, export.ErrEmpty) {
		t.Fatalf("err = %v", err)
	}
	var ee *export.EmptyError
	if !errors.As(err, &ee) || len(ee.Notes) != 1 || ee.Notes[0].Code != "recipe_unknown_excluded" {
		t.Fatalf("notes %+v", ee)
	}
	// no reference tracks: the plain sentinel
	_, err = export.Build(context.Background(), &fakeSource{}, tracksSel)
	if !errors.Is(err, export.ErrEmpty) || errors.As(err, &ee) {
		t.Errorf("plain empty: %v", err)
	}
}

func TestExport_UnknownNote(t *testing.T) {
	t.Parallel()
	known := &fakeSource{recipes: []export.RecipeRow{
		{RecipeID: "r1", Hash: "h1", RequestID: "syn-a", Request: req("syn-a", 1), FirstRecordedAt: "2026-01-01T00:00:00Z"},
		{RecipeID: "r2", Hash: "h2", RequestID: "syn-b", Request: req("syn-b", 2), FirstRecordedAt: "2026-01-02T00:00:00Z"},
	}}
	res, err := export.Build(context.Background(), refSource{known, []string{"ref-1"}}, tracksSel)
	if err != nil {
		t.Fatal(err)
	}
	var note *export.Note
	for i := range res.Warnings {
		if res.Warnings[i].Code == "recipe_unknown_excluded" {
			note = &res.Warnings[i]
		}
	}
	if note == nil || note.Details["count"] != 1 || !cmp.Equal(note.Details["track_ids"], []string{"ref-1"}) {
		t.Fatalf("warnings %+v", res.Warnings)
	}
	if len(res.Rows) != 2 {
		t.Errorf("rows %d", len(res.Rows))
	}
	// none excluded: no note
	res, err = export.Build(context.Background(), known, tracksSel)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range res.Warnings {
		if w.Code == "recipe_unknown_excluded" {
			t.Errorf("unexpected note %+v", w)
		}
	}
	// the ids are capped at 50, the count is not
	many := make([]string, 120)
	for i := range many {
		many[i] = fmt.Sprintf("ref-%03d", i)
	}
	res, err = export.Build(context.Background(), refSource{known, many}, tracksSel)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range res.Warnings {
		if w.Code == "recipe_unknown_excluded" && (w.Details["count"] != 120 || len(w.Details["track_ids"].([]string)) != 50) {
			t.Errorf("cap: %+v", w.Details)
		}
	}
}

func TestExport_SourceSelectionHasNoUnknownNote(t *testing.T) {
	t.Parallel()
	src := refSource{fileSource(string(rawLine(req("syn-1", 1))) + "\n"), []string{"ref-1"}}
	res, err := export.Build(context.Background(), src, source)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range res.Warnings {
		if w.Code == "recipe_unknown_excluded" {
			t.Errorf("source selections never carry the note: %+v", w)
		}
	}
}
