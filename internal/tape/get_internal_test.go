package tape

import (
	"context"
	"testing"
)

// TestCarriesDiscarded_FailsClosedWithoutStore: a tape that names takes cannot be checked without a Store, so the check errors
// (the tape is not served to a Listener); a tape that names none needs no Store and carries nothing discarded.
func TestCarriesDiscarded_FailsClosedWithoutStore(t *testing.T) {
	t.Parallel()
	s, ctx := New(Options{}), context.Background()
	if hidden, err := s.carriesDiscarded(ctx, Tape{Items: []ItemPlan{{TakeID: "a"}}}); err == nil || hidden {
		t.Errorf("items: hidden=%v err=%v", hidden, err)
	}
	if hidden, err := s.carriesDiscarded(ctx, Tape{Chapters: []Chapter{{TakeID: "a"}}}); err == nil || hidden {
		t.Errorf("chapters: hidden=%v err=%v", hidden, err)
	}
	if hidden, err := s.carriesDiscarded(ctx, Tape{}); err != nil || hidden {
		t.Errorf("empty: hidden=%v err=%v", hidden, err)
	}
}
